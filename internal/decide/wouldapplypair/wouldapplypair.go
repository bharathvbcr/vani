// Package wouldapplypair defines the career.would_apply_pair question: given two
// job postings, which would the job seeker rather apply to. It is the pairwise
// auditor's question. Its answer is a silver label (a model's opinion kept
// beside the owner's own labels, never in them) and, like every decision
// answer, only annotates (Rule 7): nothing here writes a label, ranks or drops.
//
// Why a pair: the role_fit question says "fit" a few percent of the time, so a
// pointwise verdict has almost no spread to tell the owner's taste apart. A
// forced comparison between two postings of one stratum has a 50% base rate by
// construction, so it needs no calibration to the owner's rate.
//
// What the model reads besides the two postings: the owner's non-identifying
// target summary (jdtriage.NormalizeSummary's rules) and a frozen set of the
// owner's own labelled job titles with their companies, split into the ones the
// owner would apply to and the ones the owner would not. Titles and companies
// are posting data, not personal data. Nothing in the question names the owner,
// and the owner's contact details, work authorization and notes never reach it.
//
// Everything that varies sits in the question's Context, inside the backend's
// untrusted-content frame (llmbackend): the exemplar titles and both postings
// are scraped text and carry no instructions. The prompt itself is fixed.
// The options are "a", "b" and "neither" (both clearly unwanted); the backend's
// own "none" is the abstention (the postings are too thin to tell).
package wouldapplypair

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const (
	Task = "career.would_apply_pair"
	Slot = "would_apply_pair"

	// The options. A and B are the postings as presented; Neither says both
	// are clearly ones the job seeker would not apply to.
	OptA       = "a"
	OptB       = "b"
	OptNeither = "neither"

	// MaxExemplarsPerClass bounds each of the yes and no title lists.
	MaxExemplarsPerClass = 30
	// MaxExemplarTitleBytes and MaxExemplarCompanyBytes bound one exemplar line,
	// so the lists have a worst case whatever the script of the text.
	MaxExemplarTitleBytes   = 110
	MaxExemplarCompanyBytes = 40
	// MaxLocationRunes bounds a posting's location in a question.
	MaxLocationRunes = 120
	// MaxBodyBytes bounds one posting's description. Each posting has its own
	// budget: a huge A can never cut B off.
	MaxBodyBytes = 4096
)

// Options are the slot's options, in the order the question lists them.
var Options = [...]string{OptA, OptB, OptNeither}

// Prompt is the question, outside the untrusted frame.
const Prompt = `Below, in this order, are the job seeker's target roles, titles of jobs the job seeker has said they would apply to, titles of jobs the job seeker has said they would not apply to, and two job postings, A and B.
Decide which of the two postings the job seeker would rather apply to. Judge the work the posting describes and how it compares with the job seeker's target roles and with the titles they would and would not apply to. Do not judge by length, polish or the company's fame. The order of A and B carries no meaning.
Pick one option for the slot:
- a: the job seeker would rather apply to posting A.
- b: the job seeker would rather apply to posting B.
- neither: the job seeker would not apply to either one.
Answer "none" if the postings are too thin to tell.`

// Exemplar is one of the owner's labelled postings as the question shows it:
// a title and its company, nothing else.
type Exemplar struct {
	Title   string `json:"title"`
	Company string `json:"company"`
}

// Exemplars are the owner's labelled titles, split by the owner's verdict.
// Maybes are not in either list.
type Exemplars struct {
	Yes []Exemplar `json:"yes"`
	No  []Exemplar `json:"no"`
}

// Validate refuses a set the question cannot be built from: an empty class (a
// model shown only what the owner likes, or only what they dislike, has no
// contrast to read), or more than MaxExemplarsPerClass in one.
func (x Exemplars) Validate() error {
	for _, c := range []struct {
		name string
		l    []Exemplar
	}{{"yes", x.Yes}, {"no", x.No}} {
		if len(c.l) == 0 {
			return fmt.Errorf("wouldapply: no %q exemplars", c.name)
		}
		if len(c.l) > MaxExemplarsPerClass {
			return fmt.Errorf("wouldapply: %d %q exemplars, max %d", len(c.l), c.name, MaxExemplarsPerClass)
		}
		for _, e := range c.l {
			if strings.TrimSpace(e.Title) == "" {
				return fmt.Errorf("wouldapply: a %q exemplar has no title", c.name)
			}
		}
	}
	return nil
}

// Question renders the question for the pair (a, b), a being shown first.
// The context stays within jdtriage.MaxContextBytes: the exemplar lines, titles
// and locations are cut to fixed bounds, each posting's body to the lesser of
// MaxBodyBytes and half of what is left, and truncated says whether anything
// was cut. summary must be normalized (jdtriage.NormalizeSummary). The pair's
// keys are the record id, in presentation order.
func Question(a, b posting.Posting, summary string, ex Exemplars) (q decide.Question, truncated bool, err error) {
	if err := ex.Validate(); err != nil {
		return decide.Question{}, false, err
	}
	if strings.TrimSpace(summary) == "" {
		return decide.Question{}, false, errors.New("wouldapply: no target summary")
	}
	if a.Key == "" || a.Key == b.Key {
		return decide.Question{}, false, errors.New("wouldapply: a pair needs two distinct postings")
	}
	var head strings.Builder
	head.WriteString("JOB SEEKER'S TARGET ROLES:\n")
	head.WriteString(summary)
	for _, c := range []struct {
		heading string
		l       []Exemplar
	}{
		{"\n\nJOBS THE JOB SEEKER WOULD APPLY TO:\n", ex.Yes},
		{"\nJOBS THE JOB SEEKER WOULD NOT APPLY TO:\n", ex.No},
	} {
		head.WriteString(c.heading)
		for _, e := range c.l {
			head.WriteString("- ")
			head.WriteString(cutBytes(oneLine(e.Title), MaxExemplarTitleBytes))
			if co := cutBytes(oneLine(e.Company), MaxExemplarCompanyBytes); co != "" {
				head.WriteString(" (" + co + ")")
			}
			head.WriteByte('\n')
		}
	}
	headers := [2]string{}
	for i, p := range [2]posting.Posting{a, b} {
		title, cutTitle := cutRuneCount(oneLine(p.Title), draft.MaxTitleRunes)
		location, cutLocation := cutRuneCount(oneLine(p.Location), MaxLocationRunes)
		truncated = truncated || cutTitle || cutLocation
		headers[i] = fmt.Sprintf("\nJOB POSTING %s:\nTitle: %s\nLocation: %s\n\n", strings.ToUpper(Options[i]), title, location)
	}
	room := max(jdtriage.MaxContextBytes-head.Len()-len(headers[0])-len(headers[1])-1, 0)
	each := min(MaxBodyBytes, room/2)
	bodies := [2]string{}
	for i, p := range [2]posting.Posting{a, b} {
		bodies[i] = p.Description
		if len(bodies[i]) > each {
			bodies[i], truncated = cutBytes(bodies[i], each), true
		}
	}
	rendered := head.String() + headers[0] + bodies[0] + "\n" + headers[1] + bodies[1]
	if len(rendered) > jdtriage.MaxContextBytes { // the exemplars and summary alone overran
		rendered, truncated = cutBytes(rendered, jdtriage.MaxContextBytes), true
	}
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  []byte(rendered),
		Slots:    []decide.Slot{{Name: Slot, Options: append([]string(nil), Options[:]...)}},
		Facts:    map[string]string{jdtriage.FactContextTruncated: fmt.Sprint(truncated)},
		RecordID: string(a.Key) + "|" + string(b.Key),
	}, truncated, nil
}

// Fixture identifies the question's definition, the role the shared fixture's
// hash plays for jd_triage: the task, the slot, the prompt and the ordered
// options. A change to any of them is a new question.
func Fixture() string {
	h := sha256.New()
	for _, s := range append([]string{Task, Slot, Prompt}, Options[:]...) {
		writeField(h, s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PromptHash fingerprints everything a question sends apart from the postings:
// the system prompt and the user message of a question for an empty pair (the
// frame, the headings, the options, the target summary and the exemplar
// lists), and the summary once more. Two runs with the same hash asked the same
// question of whichever postings.
func PromptHash(summary string, ex Exemplars) (string, error) {
	q, _, err := Question(posting.Posting{Key: "a"}, posting.Posting{Key: "b"}, summary, ex)
	if err != nil {
		return "", err
	}
	sys, user := llmbackend.Messages(q)
	h := sha256.New()
	for _, s := range []string{sys, user, summary} {
		writeField(h, s)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RenderSHA256 is the hash of the exact Context bytes of a question, the
// binding of a stored answer to what the model was sent.
func RenderSHA256(q decide.Question) string { return posting.RawSHA256(string(q.Context)) }

// Choice is what a model's answer came to.
type Choice struct {
	// Value is OptA, OptB or OptNeither when Reading is Answered, else "".
	Value string
	// Reading is the slot's reading (answered, abstained, failed, ...), kept as
	// the backend gave it; Detail is its detail code, never model text.
	Reading decide.Reading
	Detail  string
}

// Read takes the slot's reading out of a. A slot that is missing, or answered
// with a value that is not one of the options, is Failed: an answered Choice
// always holds a listed option.
func Read(a decide.Answer) Choice {
	rd, ok := a.Slots[Slot]
	switch {
	case !ok:
		return Choice{Reading: decide.Failed, Detail: "missing_slot"}
	case rd.Reading != decide.Answered:
		return Choice{Reading: rd.Reading, Detail: rd.Detail}
	}
	for _, o := range Options {
		if rd.Value == o {
			return Choice{Value: o, Reading: decide.Answered}
		}
	}
	return Choice{Reading: decide.Failed, Detail: "invalid_value"}
}

// Usable reports whether c names an option.
func (c Choice) Usable() bool { return c.Reading == decide.Answered && c.Value != "" }

func writeField(w io.Writer, s string) { fmt.Fprintf(w, "%d:%s\x00", len(s), s) }

// oneLine collapses every run of whitespace, so a title cannot start a new
// line of the question.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cutRuneCount cuts s to at most n runes and reports whether it cut.
func cutRuneCount(s string, n int) (string, bool) {
	if len(s) <= n || utf8.RuneCountInString(s) <= n {
		return s, false
	}
	i := 0
	for range n {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i], true
}

// cutBytes cuts s to at most n bytes on a rune boundary (found even when s is
// not valid UTF-8, by backing up at most to byte 0).
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
