// Package suggest answers "what can Vani put in these new form fields?" for one
// application page. Each free-text field is triaged by the decision model
// (fieldtriage: label and type only), and the engine rule here decides what
// follows: only an "essay" reading is drafted by the LLM, a fact or choice field
// is answered from the knowledge base in one batched turn (draft.Answer), and
// every other reading, an uncertain one included, and every label a sensitive
// pattern flags, leaves the field for the applicant. Drafts and answers are
// suggestions the extension fills for review, never as confident answers.
package suggest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

// Bounds. All are ceilings.
const (
	MaxFields         = 60
	DefaultDrafts     = 3
	MaxDrafts         = 5
	TriageConcurrency = 4
	DefaultTriage     = 45 * time.Second
	// MinDescriptionRunes is the shortest posting a draft is written from.
	MinDescriptionRunes = 40
)

// Statuses of one field.
const (
	Drafted = "drafted" // Draft holds a suggestion to review
	Manual  = "manual"  // the applicant answers it; Reason says why
	Unsure  = "unsure"  // the decision model gave no reading; kept for the applicant
	Failed  = "failed"  // the draft failed; Code says why
	Skipped = "skipped" // an essay over this page's draft cap
)

// Request is one page's new fields and the posting they belong to.
type Request struct {
	Job       draft.Job           `json:"job"`
	Fields    []fieldtriage.Field `json:"fields"`
	MaxDrafts int                 `json:"max_drafts,omitempty"`
}

// Suggestion is the outcome for one field.
type Suggestion struct {
	ID            string `json:"id"`
	Category      string `json:"category,omitempty"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	Backend       string `json:"backend,omitempty"`
	Draft         string `json:"draft,omitempty"`
	Model         string `json:"model,omitempty"`
	EvidenceItems int    `json:"evidence_items,omitempty"`
	Code          string `json:"code,omitempty"`
	// Kind says what a draft.CodeUngrounded refusal found unbacked (draft.Kind*).
	Kind string `json:"kind,omitempty"`
	// AnswerKind says what a drafted row is (AnswerEssay, AnswerFact, AnswerChoice);
	// it is omitted when nothing was drafted.
	AnswerKind string `json:"answer_kind,omitempty"`
	// Choice is the one option of the field's Options a drafted choice row picks;
	// Draft is empty on such a row.
	Choice string `json:"choice,omitempty"`

	// Not on the wire: what the outcome log counts (outcome.go). Batch is the
	// AnswerKind a field was sent to the batched turn as, whatever came of it.
	Batch    string `json:"-"`
	Turns    int    `json:"-"`
	Repaired bool   `json:"-"`
	DraftMS  int64  `json:"-"`
}

// What a drafted row is (Suggestion.AnswerKind).
const (
	AnswerEssay  = "essay"  // an essay drafted from the posting and the knowledge base
	AnswerFact   = "fact"   // a short text answer from the knowledge base
	AnswerChoice = "choice" // one of the field's options, chosen from the knowledge base
)

// MaxAnswers is how many fact and choice fields one page has answered: the rest
// are skipped (code CodeAnswerCap).
const MaxAnswers = draft.MaxAnswerQuestions

// AnsweredReason is the reason on a fact or choice row that was answered: the
// extension fills such a row yellow, as a suggestion to check.
const AnsweredReason = "Answered from your knowledge base, not saved: it is filled in yellow for you to check."

// Drafter writes one draft (draft.Drafter in production).
type Drafter interface {
	Draft(ctx context.Context, r draft.Request) (draft.Result, error)
}

// Answerer answers a page's fact and choice questions in one turn
// (draft.Drafter in production).
type Answerer interface {
	Answer(ctx context.Context, r draft.AnswerRequest) (draft.AnswerResult, error)
}

// Engine triages and drafts. D and Drafter are required. Answerer is optional:
// without it fact and choice fields are left for the applicant.
type Engine struct {
	D        decide.Decider
	Drafter  Drafter
	Answerer Answerer
	// Triage bounds the whole triage phase; default DefaultTriage.
	Triage time.Duration
	// Record, when set, is called once per completed Run with its counts-only
	// outcome (outcome.go). It must not block for long and must not fail the run.
	Record func(Outcome)
}

// ErrBadRequest marks a request the engine refuses before asking anything.
var ErrBadRequest = errors.New("suggest: bad request")

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBadRequest}, a...)...)
}

// Validate checks req's bounds and normalizes it in place.
func (req *Request) Validate() error {
	if len(req.Fields) == 0 {
		return bad("no fields")
	}
	if len(req.Fields) > MaxFields {
		return bad("%d fields, max %d", len(req.Fields), MaxFields)
	}
	seen := make(map[string]bool, len(req.Fields))
	for i := range req.Fields {
		f := &req.Fields[i]
		if err := f.Validate(); err != nil {
			return bad("%v", err)
		}
		if seen[f.ID] {
			return bad("field id %q is repeated", f.ID)
		}
		seen[f.ID] = true
	}
	switch {
	case req.MaxDrafts == 0:
		req.MaxDrafts = DefaultDrafts
	case req.MaxDrafts < 0 || req.MaxDrafts > MaxDrafts:
		return bad("max_drafts %d outside [1, %d]", req.MaxDrafts, MaxDrafts)
	}
	if utf8.RuneCountInString(req.Job.Company) > draft.MaxCompanyRunes || utf8.RuneCountInString(req.Job.Title) > draft.MaxTitleRunes {
		return bad("the company or job title is too long")
	}
	return nil
}

// manualReason is the reason for a field the decision model read as a
// category that is never drafted.
var manualReason = map[string]string{
	fieldtriage.Fact:     "A detail about you: answer it once and it is saved for the next form.",
	fieldtriage.WorkAuth: "Work authorization: answer it yourself. Vani never drafts it.",
	fieldtriage.Consent:  "An agreement or policy: read it and answer it yourself.",
	fieldtriage.SelfID:   "Voluntary self-identification: answer it yourself if you choose to.",
	fieldtriage.Other:    "Not a question Vani drafts.",
}

// guardedReason is the reason for a label the guard patterns pinned: unlike a
// model reading, these labels were not sent to the decision model at all.
func guardedReason(category string) string {
	if category == fieldtriage.WorkAuth {
		return "Work authorization: answer it yourself. The question was not sent to a model."
	}
	return manualReason[category]
}

// Run triages every field, then drafts the essays in page order up to the
// page's cap. It returns one Suggestion per field, in request order. It never
// drafts a field the decision model did not read as an essay.
func (e Engine) Run(ctx context.Context, req Request) ([]Suggestion, error) {
	if e.D == nil || e.Drafter == nil {
		return nil, errors.New("suggest: a decider and a drafter are required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	start := time.Now()
	out := e.triage(ctx, req.Fields)
	triaged := time.Since(start)

	hasPosting := utf8.RuneCountInString(req.Job.Description) >= MinDescriptionRunes
	// drafted counts drafts that came back: only those use up the page's cap.
	// attempts counts drafts asked for, so a provider that fails every one cannot
	// spend without bound: at most MaxDrafts+ExtraAttempts are tried per page.
	drafted, attempts, stop := 0, 0, ""
	var batch batchCounts
	if e.Record != nil {
		defer func() { e.Record(outcomeOf(OpSuggest, start, out, attempts, drafted, req.MaxDrafts, triaged, batch)) }()
	}
	var answerIdx []int // fact and choice fields for the one batched turn, in page order
	for i := range out {
		s := &out[i]
		if s.Status != "" {
			continue // decided in triage (guarded or unsure)
		}
		if f := req.Fields[i]; f.Kind == fieldtriage.KindChoice || s.Category == fieldtriage.Fact {
			// A choice is never an essay; a fact is never drafted from the posting.
			// Both are answered from the knowledge base in one batch, unless a
			// sensitive pattern pins the label (checked here, on top of triage: the
			// post-model defence also covers labels the guard does not pin) or the
			// category says it is not a question the knowledge base answers.
			switch c := draft.Pinned(f.Label, f.Options); {
			case c != "":
				s.Category, s.Status, s.Reason = c, Manual, manualReason[c]
			case e.Answerer == nil || (f.Kind == fieldtriage.KindChoice && !answerableChoice[s.Category]):
				s.Status, s.Reason = Manual, manualReason[s.Category]
				if s.Reason == "" { // an essay reading of a choice has no stock reason
					s.Reason = "Choose it yourself: Vani does not answer this one."
				}
			case len(answerIdx) >= MaxAnswers:
				s.Status, s.Code, s.Reason = Skipped, CodeAnswerCap, fmt.Sprintf("Only %d fact and choice questions are answered per page.", MaxAnswers)
			default:
				answerIdx = append(answerIdx, i)
			}
			continue
		}
		// The one place a draft is decided: only an essay reading of a label no
		// sensitive pattern flags (a pattern the guard missed still stops here).
		if s.Category != fieldtriage.Essay {
			s.Status, s.Reason = Manual, manualReason[s.Category]
			continue
		}
		if c := draft.NeverDrafted(req.Fields[i].Label); c != "" {
			s.Category, s.Status, s.Reason = c, Manual, manualReason[c]
			continue
		}
		words, short := WordsFor(req.Fields[i].MaxChars)
		switch {
		case short:
			s.Status, s.Reason = Manual, ShortAnswerReason
		case !hasPosting:
			s.Status, s.Reason = Manual, "No job description was found on this page to draft from."
		case stop != "":
			s.Status, s.Code, s.Reason = Failed, stop, "Not drafted: an earlier draft on this page hit the same limit."
			if stop == draft.CodeKB {
				s.Reason = "Not drafted: the knowledge base could not be read. Run vani draft to see why."
			}
		case drafted >= req.MaxDrafts:
			s.Status, s.Reason = Skipped, fmt.Sprintf("Only %d drafts are written per page. Use Draft on this one.", req.MaxDrafts)
		case attempts >= req.MaxDrafts+ExtraAttempts:
			s.Status, s.Code, s.Reason = Skipped, CodeAttempts, "Not drafted: earlier drafts on this page failed. Use Draft on this one."
		default:
			attempts++
			t0 := time.Now()
			res, err := e.Drafter.Draft(ctx, draft.Request{Question: req.Fields[i].Label, Job: req.Job, MaxWords: words})
			s.DraftMS = time.Since(t0).Milliseconds()
			if err != nil {
				code := draft.CodeProvider
				var de *draft.Error
				if errors.As(err, &de) {
					code = de.Code
				}
				s.Turns = draft.TurnsOf(err)
				if code == draft.CodeNotDrafted {
					// The drafter pinned a category the triage did not: keep it for the applicant.
					c := draft.CategoryOf(err)
					s.Category, s.Status, s.Reason = c, Manual, manualReason[c]
					continue
				}
				s.Status, s.Code, s.Kind, s.Reason = Failed, code, draft.KindOf(err), err.Error()
				// A spent budget, a knowledge base that did not load or a cancelled
				// request will refuse every later draft too.
				if code == draft.CodeBudget || code == draft.CodeKB || ctx.Err() != nil {
					stop = code
				}
				continue
			}
			text, ok := fit(res.Draft, req.Fields[i].MaxChars)
			if !ok {
				s.Status, s.Code, s.Reason = Failed, CodeTooLong, fmt.Sprintf("The draft did not fit this field's %d characters; use Draft on this one.", req.Fields[i].MaxChars)
				s.Turns = res.Turns
				continue
			}
			drafted++
			s.Status, s.Draft, s.Model, s.EvidenceItems, s.AnswerKind = Drafted, text, res.Model, res.EvidenceItems, AnswerEssay
			s.Turns, s.Repaired = res.Turns, res.Repaired
		}
	}
	if len(answerIdx) > 0 {
		batch = e.answerPage(ctx, req, out, answerIdx, stop)
	}
	return out, nil
}

// answerableChoice is the triage categories whose choice fields are answered
// from the knowledge base. Work authorization, self-identification and consent
// stay the applicant's.
var answerableChoice = map[string]bool{fieldtriage.Fact: true, fieldtriage.Essay: true, fieldtriage.Other: true}

// CodeAnswerCap marks a fact or choice field not answered because the page
// already had MaxAnswers.
const CodeAnswerCap = "answer_cap"

// batchCounts is what the page's one batched turn came to, for the outcome log.
type batchCounts struct {
	asked, answered, turns int
	ms                     int64
}

// answerPage answers the fields at idx (fact text and choice, none of them
// pinned) with one Answerer turn and writes each result into out. stop is the
// code an earlier draft on this page stopped on: nothing is spent then.
func (e Engine) answerPage(ctx context.Context, req Request, out []Suggestion, idx []int, stop string) batchCounts {
	var bc batchCounts
	for _, i := range idx {
		out[i].Batch = AnswerFact
		if req.Fields[i].Kind == fieldtriage.KindChoice {
			out[i].Batch = AnswerChoice
		}
	}
	if stop != "" {
		for _, i := range idx {
			s := &out[i]
			s.Status, s.Code, s.Reason = Failed, stop, "Not answered: an earlier draft on this page hit the same limit."
			if stop == draft.CodeKB {
				s.Reason = "Not answered: the knowledge base could not be read. Run vani draft to see why."
			}
		}
		return bc
	}
	qs := make([]draft.Question, len(idx))
	for k, i := range idx {
		qs[k] = draft.Question{Label: req.Fields[i].Label, Options: req.Fields[i].Options}
	}
	bc.asked = len(idx)
	t0 := time.Now()
	res, err := e.Answerer.Answer(ctx, draft.AnswerRequest{Job: req.Job, Questions: qs})
	bc.ms = time.Since(t0).Milliseconds()
	if err == nil && len(res.Answers) != len(idx) {
		err = &draft.Error{Code: draft.CodeBadReply, Err: errors.New("draft: the answers did not match the questions")}
	}
	if err != nil {
		code := draft.CodeProvider
		var de *draft.Error
		if errors.As(err, &de) {
			code = de.Code
		}
		bc.turns = draft.TurnsOf(err)
		for _, i := range idx {
			s := &out[i]
			s.Status, s.Code, s.Kind, s.Reason = Failed, code, draft.KindOf(err), err.Error()
		}
		return bc
	}
	bc.turns = res.Turns
	for k, i := range idx {
		s, f, a := &out[i], req.Fields[i], res.Answers[k]
		switch a.Code {
		case "":
			if !answerFits(f, a) {
				s.Status, s.Code, s.Reason = Failed, draft.CodeBadReply, "The model's reply for this field was not usable; answer it yourself."
				if a.Text != "" && f.MaxChars > 0 {
					s.Code, s.Reason = CodeTooLong, fmt.Sprintf("The answer did not fit this field's %d characters; answer it yourself.", f.MaxChars)
				}
				continue
			}
			bc.answered++
			s.Status, s.Reason, s.Model, s.EvidenceItems = Drafted, AnsweredReason, res.Model, a.EvidenceItems
			s.AnswerKind = s.Batch
			if f.Kind == fieldtriage.KindChoice {
				s.Choice = a.Choice
			} else {
				s.Draft = a.Text
			}
		case draft.CodeNoEvidence, draft.CodeUngrounded:
			s.Status, s.Code, s.Kind, s.Reason = Manual, a.Code, a.Kind, a.Reason
		default:
			s.Status, s.Code, s.Kind, s.Reason = Failed, a.Code, a.Kind, a.Reason
		}
	}
	return bc
}

// answerFits re-checks an answer against its field, whatever the Answerer is: a
// choice is exactly one of the field's options, a text answer is non-empty and
// within the control's max_chars.
func answerFits(f fieldtriage.Field, a draft.Answer) bool {
	if f.Kind == fieldtriage.KindChoice {
		return slices.Contains(f.Options, a.Choice) && a.Text == ""
	}
	return strings.TrimSpace(a.Text) != "" && a.Choice == "" && (f.MaxChars <= 0 || utf8.RuneCountInString(a.Text) <= f.MaxChars)
}

// ExtraAttempts is how many more drafts than the page's cap may be tried when
// some fail: the cap counts drafts that came back, this bounds the tries.
const ExtraAttempts = 2

// Codes suggest adds to draft's own.
const (
	// CodeAttempts marks an essay not tried because MaxDrafts+ExtraAttempts drafts
	// were already asked for on this page.
	CodeAttempts = "attempts_exhausted"
	// CodeTooLong marks a draft that came back longer than the field's max_chars
	// and had no sentence boundary to cut at.
	CodeTooLong = "too_long"
)

// ShortAnswerReason is the reason for a field whose max_chars cannot hold the
// shortest draft.
const ShortAnswerReason = "short-answer field"

// CharsPerWord converts a field's max_chars into the words asked for: English
// averages about 5.5 letters a word plus its space.
const CharsPerWord = 6.5

// WordsFor is the draft length for a field of maxChars: 0 when the field has no
// limit (the drafter's default), the characters over CharsPerWord clamped to
// [MinWords, DefaultWords] otherwise: a limit only ever shortens a draft, so a roomy
// box gets the same length as a field with none (the extension's maxWordsFor agrees). short is true when maxChars cannot hold
// MinWords, so the field is for the applicant.
func WordsFor(maxChars int) (words int, short bool) {
	if maxChars <= 0 {
		return 0, false
	}
	w := int(float64(maxChars) / CharsPerWord)
	if w < draft.MinWords {
		return 0, true
	}
	return min(w, draft.DefaultWords), false
}

// fit returns text cut to at most maxChars characters at a sentence end, or
// false when no sentence fits. maxChars 0 is no limit. The words asked for are an
// estimate, so a draft can still run over a form's maxlength, and a control cuts
// the overflow mid-sentence; a draft only ever loses whole trailing sentences.
func fit(text string, maxChars int) (string, bool) {
	rs := []rune(text)
	if maxChars <= 0 || len(rs) <= maxChars {
		return text, true
	}
	for i := maxChars - 1; i >= 0; i-- {
		if (rs[i] == '.' || rs[i] == '!' || rs[i] == '?') && (i+1 == len(rs) || rs[i+1] == ' ' || rs[i+1] == '\n') {
			if cut := strings.TrimSpace(string(rs[:i+1])); cut != "" {
				return cut, true
			}
		}
	}
	return "", false
}

// triage fills Category for every field, and Status for fields it settles
// itself: guarded labels (never asked) and unread ones (unsure).
func (e Engine) triage(ctx context.Context, fields []fieldtriage.Field) []Suggestion {
	timeout := e.Triage
	if timeout <= 0 {
		timeout = DefaultTriage
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := make([]Suggestion, len(fields))
	sem := make(chan struct{}, TriageConcurrency)
	var wg sync.WaitGroup
	for i, f := range fields {
		out[i].ID = f.ID
		// Pinned without asking, unless the only pin is the broad consent pattern on a
		// plain essay prompt (draft.NeverDrafted): that one the model reads.
		if g := fieldtriage.Guard(f.Label); g != "" && draft.NeverDrafted(f.Label) != "" {
			out[i].Category, out[i].Status, out[i].Reason = g, Manual, guardedReason(g)
			continue
		}
		wg.Add(1)
		go func(i int, f fieldtriage.Field) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i].Status, out[i].Reason = Unsure, "The decision model was not asked in time; answer it yourself."
				return
			}
			out[i] = read(ctx, e.D, f)
		}(i, f)
	}
	wg.Wait()
	return out
}

// read asks one field and maps the reading. Anything but an answered value
// is "unsure": an unread field is kept for the applicant, never drafted.
func read(ctx context.Context, d decide.Decider, f fieldtriage.Field) Suggestion {
	s := Suggestion{ID: f.ID}
	ans, err := d.Decide(ctx, fieldtriage.Question(f))
	if err != nil {
		s.Status, s.Reason = Unsure, "The decision model is unavailable; answer it yourself."
		return s
	}
	s.Backend = ans.Backend
	r, ok := ans.Slots[fieldtriage.Slot]
	if !ok || r.Reading != decide.Answered {
		reading := decide.NotAsked
		if ok {
			reading = r.Reading
		}
		s.Status, s.Reason = Unsure, fmt.Sprintf("The decision model gave no reading (%s); answer it yourself.", reading)
		return s
	}
	if _, known := manualReason[r.Value]; !known && r.Value != fieldtriage.Essay {
		s.Status, s.Reason = Unsure, "The decision model answered outside the options; answer it yourself."
		return s
	}
	s.Category = r.Value
	return s
}
