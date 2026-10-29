package draft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

// Answer answers a page's fact and choice questions ("Are you willing to
// relocate?", "Which of these languages have you shipped in?") from the seeker's
// knowledge base in ONE tool-less turn, through the same provider, budget,
// timeout and stop-reason handling as a draft. It shares the draft's evidence
// selection (selectQuestion), its untrusted frame and its grounding guard; what
// it adds is a strict JSON reply read field by field.
//
// What the model sees: the question labels and their options (untrusted page
// text, inside the frame), the posting excerpt (untrusted), the seeker's target
// summary and the evidence bullets, each with an id it must cite. A question the
// evidence does not answer is "none". What it never sees: the seeker's name,
// contact details or work authorization (the evidence has none: see the package
// comment), and no question Vani never drafts: Validate refuses a request that
// holds one (fieldtriage.Sensitive) before anything is built.

// Bounds and codes of the batched answers.
const (
	// MaxAnswerQuestions is how many questions one turn answers.
	MaxAnswerQuestions = 15
	// MaxFactWords bounds a fact answer in words; maxFactRunes bounds it in runes
	// (a word can be long).
	MaxFactWords = 30
	maxFactRunes = 400
	// maxAnswerPromptRunes bounds the labels and options sent in one turn; a
	// question past it is not sent and comes back CodeTooLarge.
	maxAnswerPromptRunes = 24_000
	// MaxAnswerReplyRunes bounds the reply read.
	MaxAnswerReplyRunes = 16_000

	// CodeBadReply: the reply for this question was not in the form asked for (a
	// missing, repeated or malformed entry, an extra key, text where a choice was
	// asked, or a choice that is not one of the options).
	CodeBadReply = "bad_reply"
	// CodeNoEvidence: the knowledge base does not answer this question.
	CodeNoEvidence = "no_evidence"
	// CodeTooLarge: the question's options did not fit the turn's bound.
	CodeTooLarge = "too_large"
)

// NoEvidenceReason is the reason a question the knowledge base does not answer
// is left for the applicant.
const NoEvidenceReason = "Your knowledge base does not answer this."

// Question is one fact or choice question: Options empty is a short text answer,
// otherwise the answer is exactly one option.
type Question struct {
	Label   string
	Options []string
}

// AnswerRequest is one page's questions and the posting they belong to.
type AnswerRequest struct {
	Job       Job
	Questions []Question
}

// Answer is the outcome for one question. Code is empty when it was answered:
// Text holds a fact answer, Choice the option chosen. Otherwise Code says why not
// (CodeNoEvidence, CodeUngrounded with Kind, CodeBadReply, CodeTooLarge) and
// Reason says it for a person.
type Answer struct {
	Text          string
	Choice        string
	EvidenceItems int
	Code          string
	Kind          string
	Reason        string
}

// AnswerResult is one batch: Answers has one entry per question, in order.
// Turns is the provider turns spent (0 when nothing was asked).
type AnswerResult struct {
	Answers []Answer
	Model   string
	Turns   int
}

// Validate checks the request and normalizes the labels in place. A question
// Vani never drafts is refused whole, before any prompt is built.
func (r *AnswerRequest) Validate() error {
	if n := len(r.Questions); n == 0 || n > MaxAnswerQuestions {
		return fail(CodeBadRequest, "%d questions, want 1 to %d", n, MaxAnswerQuestions)
	}
	if runes(r.Job.Company) > MaxCompanyRunes || runes(r.Job.Title) > MaxTitleRunes {
		return fail(CodeBadRequest, "the company or job title is too long")
	}
	for i := range r.Questions {
		q := &r.Questions[i]
		q.Label = strings.Join(strings.Fields(q.Label), " ")
		switch {
		case q.Label == "" || !utf8.ValidString(q.Label):
			return fail(CodeBadRequest, "question %d has no usable label", i+1)
		case runes(q.Label) > MaxQuestionRunes:
			return fail(CodeBadRequest, "question %d is over %d characters", i+1, MaxQuestionRunes)
		case len(q.Options) == 1 || len(q.Options) > fieldtriage.MaxOptions:
			return fail(CodeBadRequest, "question %d has %d options", i+1, len(q.Options))
		}
		if c := Pinned(q.Label, q.Options); c != "" {
			return &Error{Code: CodeNotDrafted, Category: c, Err: fmt.Errorf("draft: question %d is read as %s, which Vani never answers; answer it yourself", i+1, c)}
		}
		seen := map[string]bool{}
		for _, o := range q.Options {
			if !utf8.ValidString(o) || strings.TrimSpace(o) == "" || runes(o) > fieldtriage.MaxOptionRunes || seen[o] {
				return fail(CodeBadRequest, "question %d has an empty, repeated, oversized or non-UTF-8 option", i+1)
			}
			seen[o] = true
		}
	}
	r.Job.Description = clip(r.Job.Description, MaxPostingRunes)
	return nil
}

const answerSystemPrompt = `You answer short fact and multiple-choice questions on a job application for a job seeker, as the seeker, from the seeker's evidence only.
` + evidenceRules + `
Answer a question only when the evidence states the answer. If it does not, or you would have to guess (a preference, a number of years the evidence does not give, a yes or no it does not settle), answer none: leaving a question to the seeker is always right, a guess never is.
A question without options gets a plain-text answer of at most 30 words. A question with options gets exactly one of its options, copied character for character.
Cite the ids of the evidence bullets that back each answer ("e1").
Reply with JSON only, no prose and no code fence: one object keyed by question id ("q1", "q2", ...) with one entry per question, each exactly one of {"answer": "<text>", "evidence": ["e1"]}, {"choice": "<option>", "evidence": ["e1"]} or {"none": true}. No other keys.
` + untrustedRule

// maxAnswerTokens is the visible-reply budget: each entry is short.
func maxAnswerTokens(questions int) int { return questions*150 + 300 }

// Answer asks the model for every question in r in one turn and reads the reply
// strictly. A failure of the turn itself (a spent budget, the timeout, a
// provider error, a refusal, a reply cut off) is the returned *Error; a reply
// that came back is read field by field and never fails the others.
func (d *Drafter) Answer(ctx context.Context, r AnswerRequest) (AnswerResult, error) {
	if err := r.Validate(); err != nil {
		return AnswerResult{}, err
	}
	res := AnswerResult{Answers: make([]Answer, len(r.Questions)), Model: d.cfg.Model}
	var sent []int // indexes into r.Questions, in order
	used := 0
	for i, q := range r.Questions {
		cost := runes(q.Label) + 16
		for _, o := range q.Options {
			cost += runes(o) + 4
		}
		if used+cost > maxAnswerPromptRunes {
			res.Answers[i] = Answer{Code: CodeTooLarge, Reason: "This question's options were too long to send with the page's other questions; answer it yourself."}
			continue
		}
		used += cost
		sent = append(sent, i)
	}
	if len(sent) == 0 {
		return res, nil
	}

	post := excerpt(r.Job.Description, MaxDescriptionRunes)
	var query strings.Builder
	for _, i := range sent {
		query.WriteString(r.Questions[i].Label + " " + strings.Join(r.Questions[i].Options, " ") + " ")
	}
	ev := d.selectQuestion(Request{Question: query.String(), Job: r.Job}, post)
	items := 0
	for _, e := range ev {
		items += len(e.Bullets)
	}
	if items == 0 {
		// Nothing in the knowledge base touches these questions: no turn is spent.
		for _, i := range sent {
			res.Answers[i] = Answer{Code: CodeNoEvidence, Reason: NoEvidenceReason}
		}
		return res, nil
	}

	ctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	qs := make([]Question, len(sent))
	for k, i := range sent {
		qs[k] = r.Questions[i]
	}
	user := llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: answerMessage(r.Job, qs, post, ev, d.cfg.TargetSummary)}}}
	req := llm.Request{Model: d.cfg.Model, System: answerSystemPrompt, Messages: []llm.Message{user}, MaxTokens: maxAnswerTokens(len(sent))}
	shape(d.p, &req)
	text, err := d.rawTurn(ctx, req)
	res.Turns = 1
	if err != nil {
		return AnswerResult{}, withTurns(err, 1)
	}

	seeker := []string{d.cfg.TargetSummary}
	for _, e := range ev {
		seeker = append(seeker, e.Title, e.Org, e.Dates)
		seeker = append(seeker, e.Bullets...)
	}
	shown := []string{r.Job.Company, r.Job.Title, post.text()}
	for _, q := range qs {
		shown = append(shown, q.Label)
		shown = append(shown, q.Options...)
	}
	g := newGrounding(seeker, shown)
	entries, err := decodeAnswers(text, len(sent))
	for k, i := range sent {
		switch {
		case err != nil || entries[k].bad:
			res.Answers[i] = Answer{Code: CodeBadReply, Reason: "The model's reply for this field was not in the expected form; answer it yourself."}
		default:
			res.Answers[i] = readAnswer(qs[k], entries[k], items, g)
		}
	}
	return res, nil
}

// answerMessage renders the turn. The questions, their options and the posting
// are page text and sit inside the frame as one unmarked piece; everything
// outside it is the seeker's own or ours (the question ids are generated here:
// a field id from the page never reaches the prompt).
func answerMessage(job Job, qs []Question, post postingText, ev []Evidence, target string) string {
	var b strings.Builder
	if t := strings.TrimSpace(target); t != "" {
		fmt.Fprintf(&b, "Target roles (written by the seeker): %s\n\n", t)
	}
	fmt.Fprintf(&b, "Answer questions q1 to q%d. A text answer is at most %d words.\n\n", len(qs), MaxFactWords)
	b.WriteString("The seeker's evidence (trusted, from their own knowledge base; cite a bullet by its id):\n")
	n := 0
	for _, e := range ev {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(strings.Join(nonEmpty(e.Title, e.Org, e.Dates), ", ")))
		for _, bl := range e.Bullets {
			n++
			fmt.Fprintf(&b, "  - [e%d] %s\n", n, bl)
		}
	}
	var in strings.Builder
	for k, q := range qs {
		fmt.Fprintf(&in, "q%d: %s\n", k+1, q.Label)
		if len(q.Options) > 0 {
			var opts bytes.Buffer
			enc := json.NewEncoder(&opts)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(q.Options) // []string always marshals
			fmt.Fprintf(&in, "  options: %s", opts.String())
		}
	}
	if job.Company != "" {
		fmt.Fprintf(&in, "Company: %s\n", job.Company)
	}
	if job.Title != "" {
		fmt.Fprintf(&in, "Job title: %s\n", job.Title)
	}
	writePosting(&in, post)
	b.WriteString("\n" + llmbackend.BeginMarker + "\n")
	b.WriteString(llmbackend.Unmark(in.String()))
	b.WriteString("\n" + llmbackend.EndMarker + "\n")
	return b.String()
}

// answerEntry is one question's entry of the reply, as decoded.
type answerEntry struct {
	bad      bool // missing, repeated, malformed, or with a key it should not have
	answer   *string
	choice   *string
	none     bool
	evidence []string
}

// decodeAnswers reads the reply: one JSON object, keyed "q1".."qN", each key at
// most once, each value an object of the answer, choice, none and evidence keys
// at most once each. Anything about the reply as a whole (not an object, trailing
// data, a key that is not a question id, over the bound) is an error and fails
// every question; a problem inside one entry marks only that entry bad, and a
// question with no entry is bad.
func decodeAnswers(text string, n int) ([]answerEntry, error) {
	text = llmbackend.StripFence(text)
	if runes(text) > MaxAnswerReplyRunes {
		return nil, errors.New("reply too large")
	}
	out := make([]answerEntry, n)
	seen := make([]bool, n)
	dec := json.NewDecoder(strings.NewReader(text))
	err := walkObject(dec, func(key string, raw json.RawMessage) error {
		k, ok := strings.CutPrefix(key, "q")
		idx, cerr := strconv.Atoi(k)
		if !ok || cerr != nil || strconv.Itoa(idx) != k || idx < 1 || idx > n {
			return fmt.Errorf("unknown question id %q", clip(key, 20))
		}
		if seen[idx-1] {
			out[idx-1] = answerEntry{bad: true}
			return nil
		}
		seen[idx-1] = true
		out[idx-1] = decodeEntry(raw)
		return nil
	})
	if err == nil {
		if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		return nil, err
	}
	for i, ok := range seen {
		if !ok {
			out[i] = answerEntry{bad: true}
		}
	}
	return out, nil
}

// walkObject reads one JSON object from dec, calling fn for each member in order
// (a repeated key is called again: the caller decides).
func walkObject(dec *json.Decoder, fn func(key string, raw json.RawMessage) error) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("not an object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := kt.(string)
		if !ok {
			return errors.New("not an object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if err := fn(key, raw); err != nil {
			return err
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return errors.New("not an object")
	}
	return nil
}

func decodeEntry(raw json.RawMessage) answerEntry {
	var e answerEntry
	var seen [4]bool
	dec := json.NewDecoder(bytes.NewReader(raw))
	err := walkObject(dec, func(key string, v json.RawMessage) error {
		var slot int
		switch key {
		case "answer":
			slot = 0
		case "choice":
			slot = 1
		case "none":
			slot = 2
		case "evidence":
			slot = 3
		default:
			return fmt.Errorf("unknown key %q", clip(key, 20))
		}
		if seen[slot] {
			return errors.New("repeated key")
		}
		seen[slot] = true
		switch slot {
		case 0, 1:
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			if slot == 0 {
				e.answer = &s
			} else {
				e.choice = &s
			}
		case 2:
			if err := json.Unmarshal(v, &e.none); err != nil {
				return err
			}
			if !e.none {
				return errors.New("none must be true")
			}
		default:
			return json.Unmarshal(v, &e.evidence)
		}
		return nil
	})
	if _, terr := dec.Token(); err != nil || !errors.Is(terr, io.EOF) {
		return answerEntry{bad: true}
	}
	return e
}

// readAnswer turns one decoded entry into the question's Answer: it must be
// exactly one of answer, choice or none, of the kind the question asks for; an
// answer cites evidence that was shown; and what it says passes the grounding
// guard. Nothing here repairs: an answer that fails is left to the applicant.
// affirmativeRE reads a chosen option that says yes to the question it answers.
var affirmativeRE = regexp.MustCompile(`^(yes|y|true|i do|i have|i am|i can|i hold|i agree|agree|correct)\b`)

// affirmative reports whether choice answers yes.
func affirmative(choice string) bool {
	return affirmativeRE.MatchString(strings.ToLower(strings.TrimSpace(choice)))
}

func readAnswer(q Question, e answerEntry, items int, g grounding) Answer {
	bad := Answer{Code: CodeBadReply, Reason: "The model's reply for this field was not in the expected form; answer it yourself."}
	kinds := 0
	for _, set := range []bool{e.answer != nil, e.choice != nil, e.none} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return bad
	}
	if e.none {
		return Answer{Code: CodeNoEvidence, Reason: NoEvidenceReason}
	}
	if (e.answer != nil) != (len(q.Options) == 0) {
		return bad // a text where a choice was asked, or the reverse
	}
	cited := map[int]bool{}
	for _, id := range e.evidence {
		k, ok := strings.CutPrefix(id, "e")
		n, err := strconv.Atoi(k)
		if !ok || err != nil || strconv.Itoa(n) != k || n < 1 || n > items {
			return bad
		}
		cited[n] = true
	}
	var a Answer
	if e.answer != nil {
		text := strings.Join(strings.Fields(*e.answer), " ")
		if text == "" || len(strings.Fields(text)) > MaxFactWords || runes(text) > maxFactRunes {
			return bad
		}
		a.Text = text
	} else {
		var found bool
		for _, o := range q.Options {
			found = found || o == *e.choice
		}
		if !found {
			return bad
		}
		a.Choice = *e.choice
	}
	if len(cited) == 0 {
		return Answer{Code: CodeUngrounded, Reason: "The answer cites nothing in your knowledge base; answer it yourself."}
	}
	// "Yes" to "Do you hold a PhD?" or "Do you have 5+ years of Go?" claims what the question says: the question's
	// figures, years and credentials must be backed too, as if the answer had written them.
	claimed := a.Text + " " + a.Choice
	if affirmative(a.Choice) {
		claimed += " " + q.Label
	}
	if cs := g.claims(claimed); len(cs) > 0 {
		ue := ungroundedError(cs)
		return Answer{Code: CodeUngrounded, Kind: ue.Kind, Reason: ue.Error()}
	}
	a.EvidenceItems = len(cited)
	return a
}
