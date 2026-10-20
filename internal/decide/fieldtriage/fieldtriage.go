// Package fieldtriage defines the career.form_field question: what kind of
// answer one job-application form field asks for. The decision model sees the
// field's label and type only, never a value, a saved answer or anything about
// the applicant. The reading only annotates; the engine (Plan) decides what
// happens to the field, and only an "essay" reading ever leads to a draft.
//
// Lappi is not trained on this task, so it is asked of decision-model backends
// (the jev client) and the LLM backend only.
package fieldtriage

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/decide"
)

const (
	Task = "career.form_field"
	// Slot is the question's one slot.
	Slot = "kind"

	// MaxLabelRunes caps a field label; a longer one is refused, not cut.
	MaxLabelRunes = 1000
)

// The options. Only Essay leads to a draft.
const (
	Essay     = "essay"
	Fact      = "applicant_fact"
	WorkAuth  = "work_authorization"
	Consent   = "legal_consent"
	SelfID    = "self_identification"
	Other     = "other"
	KindText  = "text"
	KindLong  = "textarea"
	contextFm = "Field type: %s\nQuestion label (untrusted text copied from a job site):\n%s"
)

// Option is one legal answer with the description a decision model judges it by.
type Option struct {
	Value       string
	Description string
}

// Options is the slot's table, in a fixed order.
var Options = []Option{
	{Essay, "An open free-text answer about motivation, interest or fit that is written from the job posting and the applicant's experience, such as why they want this job or company, a cover letter, or anything else they want to share."},
	{Fact, "A fact or preference about the applicant only they know: name, contact details, links or profile URLs, address or location, start date, deadlines, relocation, office attendance, salary, how they heard about the job, name pronunciation, or prior interviews."},
	{WorkAuth, "Work authorization, visa, sponsorship, citizenship or right to work."},
	{Consent, "An agreement, acknowledgement, consent, certification or policy the applicant must read and accept themselves, such as an arbitration agreement or an AI-use policy."},
	{SelfID, "Voluntary demographic self-identification: gender, race or ethnicity, veteran status, disability or sexual orientation."},
	{Other, "None of the above, or the label is unclear."},
}

// Prompt is the fixed question text.
const Prompt = "What kind of answer does this job-application form field ask for? Judge only from the field label and type."

// Field is one form field as the extension reports it: a stable id, the
// label shown on the page and the control type.
type Field struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// Validate checks f's bounds. It trims the label in place.
func (f *Field) Validate() error {
	f.Label = strings.Join(strings.Fields(f.Label), " ")
	switch {
	case f.ID == "" || len(f.ID) > 64:
		return errors.New("fieldtriage: field id must be 1..64 bytes")
	case f.Label == "":
		return fmt.Errorf("fieldtriage: field %s has no label", f.ID)
	case !utf8.ValidString(f.Label):
		return fmt.Errorf("fieldtriage: field %s label is not UTF-8", f.ID)
	case utf8.RuneCountInString(f.Label) > MaxLabelRunes:
		return fmt.Errorf("fieldtriage: field %s label is over %d characters", f.ID, MaxLabelRunes)
	case f.Kind != KindText && f.Kind != KindLong:
		return fmt.Errorf("fieldtriage: field %s kind %q is not text or textarea", f.ID, f.Kind)
	}
	return nil
}

// Descriptions returns the table as the slot -> option -> description map
// jev.Config takes.
func Descriptions() map[string]map[string]string {
	m := make(map[string]string, len(Options))
	for _, o := range Options {
		m[o.Value] = o.Description
	}
	return map[string]map[string]string{Slot: m}
}

func values() []string {
	out := make([]string, len(Options))
	for i, o := range Options {
		out[i] = o.Value
	}
	return out
}

// Question builds the question for f, which must have passed Validate.
func Question(f Field) decide.Question {
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  []byte(fmt.Sprintf(contextFm, f.Kind, f.Label)),
		Slots:    []decide.Slot{{Name: Slot, Options: values()}},
		RecordID: f.ID,
	}
}

// Guard words. A label that matches one of these is never asked of a model
// and never drafted, whatever a model would say: work authorization and
// self-identification must not enter a prompt, and consent is the
// applicant's own act.
var (
	workAuthRE = regexp.MustCompile(`(?i)\b(visa|sponsor(ship)?|work(ing)? authori[sz]ation|authori[sz]ed to work|right to work|citizenship|immigration|work permit|green card)\b`)
	selfIDRE   = regexp.MustCompile(`(?i)\b(gender|race|ethnicity|hispanic|latin[oax]|veteran|disability|disabled|sexual orientation|transgender)\b`)
	consentRE  = regexp.MustCompile(`(?i)\b(agree|agreement|arbitrat\w*|consent|acknowledg\w*|certify|terms (and|&) conditions|privacy (notice|policy)|ai policy)\b`)
)

// Guard returns the category a label is pinned to without asking a model,
// or "" when the model may be asked.
func Guard(label string) string {
	switch {
	case workAuthRE.MatchString(label):
		return WorkAuth
	case selfIDRE.MatchString(label):
		return SelfID
	case consentRE.MatchString(label):
		return Consent
	}
	return ""
}
