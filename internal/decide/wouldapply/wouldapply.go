// Package wouldapply defines the career.would_apply question: a language
// model's guess at the owner's own audit answer, "would I apply to this?"
// (yes / maybe / no), with the one factor that most decided it.
//
// It is an auditor's question, not a triage question. It is asked only by
// `vani auditor`, its answers are kept in their own log (eval.AuditorFile), and
// nothing in the pipeline, the ranking or the drop policy reads them (Rule 7):
// they are compared with the owner's labels to measure whether the model can
// stand in for the owner at all. jdtriage's shared fixture is untouched, so no
// decision model is retrained for it.
//
// What leaves the machine is exactly what a full-digest kb_match question
// sends (kbmatch.Question renders the context): the target summary, the bounded
// knowledge-base digest built through eligibleBank, and the posting, all inside
// the backend's untrusted-content frame. Only the task, the prompt and the
// slots differ.
package wouldapply

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const (
	Task = "career.would_apply"
	// Slot is the answer; ReasonSlot the factor that most decided it.
	Slot       = "would_apply"
	ReasonSlot = "reason"
)

// Answers are the options of Slot, in the owner's audit vocabulary
// (eval.WouldApplyOptions), best first.
var Answers = [...]string{"yes", "maybe", "no"}

// Reasons are the options of ReasonSlot.
var Reasons = [...]string{"level", "domain", "location", "employer_type", "other"}

// Prompt is the question, outside the untrusted frame. It names no person and
// asks nothing about the job seeker beyond the target roles and the digest.
const Prompt = `Below, in this order, are the job seeker's target roles, a digest of the job seeker's experience, and a job posting.
Answer as the job seeker would: given this posting, would they apply?
- yes: the role fits their target roles and experience well enough that they would apply.
- maybe: they would consider it; some of it fits and some does not.
- no: they would not apply.
Judge the duties, the level and the setting the posting describes, not the company's industry, not its boilerplate (benefits, equal-opportunity text), and not words the posting merely shares with the experience. Assume nothing about the job seeker beyond what is shown.
For the reason slot, pick the one factor that most decided the answer:
- level: the seniority or experience the role asks for.
- domain: the kind of work and its field.
- location: where or how the work is done.
- employer_type: the kind of employer.
- other: anything else.
Answer "none" for a slot if the posting is too thin to tell.`

// Score orders an answer for agreement measures: yes 2, maybe 1, no 0. ok is
// false for anything else.
func Score(answer string) (float64, bool) {
	switch answer {
	case "yes":
		return 2, true
	case "maybe":
		return 1, true
	case "no":
		return 0, true
	}
	return 0, false
}

// Fixture identifies the question's definition: the task, the slots, the
// prompt and the ordered options. A stored answer made under another
// definition is a different question.
func Fixture() string {
	h := sha256.New()
	parts := []string{Task, Prompt, Slot}
	parts = append(parts, Answers[:]...)
	parts = append(parts, ReasonSlot)
	parts = append(parts, Reasons[:]...)
	for _, s := range parts {
		fmt.Fprintf(h, "%d:", len(s))
		io.WriteString(h, s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Question renders the question for p: kbmatch's full-digest context (target
// summary, digest, then the posting, cut to jdtriage.MaxContextBytes), asked
// with this package's prompt and slots. truncated says whether anything was cut.
func Question(p posting.Posting, summary string, d kbmatch.Digest) (q decide.Question, truncated bool) {
	q, truncated = kbmatch.Question(p, summary, d, nil)
	q.Task = Task
	q.Prompt = Prompt
	q.Slots = []decide.Slot{
		{Name: Slot, Options: append([]string(nil), Answers[:]...)},
		{Name: ReasonSlot, Options: append([]string(nil), Reasons[:]...)},
	}
	return q, truncated
}
