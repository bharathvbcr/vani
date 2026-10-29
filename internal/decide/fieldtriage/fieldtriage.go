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
	Essay    = "essay"
	Fact     = "applicant_fact"
	WorkAuth = "work_authorization"
	Consent  = "legal_consent"
	SelfID   = "self_identification"
	Other    = "other"
	KindText = "text"
	KindLong = "textarea"
	// KindChoice is a field whose answer is one of its Options (a select, a
	// radio group). The options never go to a decision model.
	KindChoice = "choice"
	contextFm  = "Field type: %s\nQuestion label (untrusted text copied from a job site):\n%s"
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
	// MaxChars is the control's maxlength in characters, when the form sets one.
	// Zero means none. It is a number the extension reads off the control, never
	// page text, and it is not sent to the decision model.
	MaxChars int `json:"max_chars,omitempty"`
	// Options are the answers a KindChoice field offers, exactly as the page
	// shows them (an answer is one of these strings, byte for byte). They are page
	// text: framed as untrusted in a prompt, and never sent to a triage model.
	Options []string `json:"options,omitempty"`
}

// MaxMaxChars bounds Field.MaxChars: a maxlength above it is a placeholder for
// "unlimited" that the extension should send as none.
const MaxMaxChars = 100_000

// Bounds on a choice field's options.
const (
	MinOptions     = 2
	MaxOptions     = 50
	MaxOptionRunes = 200
)

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
	case f.Kind != KindText && f.Kind != KindLong && f.Kind != KindChoice:
		return fmt.Errorf("fieldtriage: field %s kind %q is not text, textarea or choice", f.ID, f.Kind)
	case f.MaxChars < 0 || f.MaxChars > MaxMaxChars:
		return fmt.Errorf("fieldtriage: field %s max_chars %d outside [0, %d]", f.ID, f.MaxChars, MaxMaxChars)
	}
	if f.Kind != KindChoice {
		if len(f.Options) != 0 {
			return fmt.Errorf("fieldtriage: field %s kind %q takes no options", f.ID, f.Kind)
		}
		return nil
	}
	if f.MaxChars != 0 {
		return fmt.Errorf("fieldtriage: choice field %s takes no max_chars", f.ID)
	}
	if n := len(f.Options); n < MinOptions || n > MaxOptions {
		return fmt.Errorf("fieldtriage: choice field %s has %d options, want %d to %d", f.ID, n, MinOptions, MaxOptions)
	}
	seen := make(map[string]bool, len(f.Options))
	for _, o := range f.Options {
		switch {
		case !utf8.ValidString(o):
			return fmt.Errorf("fieldtriage: choice field %s has an option that is not UTF-8", f.ID)
		case strings.TrimSpace(o) == "" || utf8.RuneCountInString(o) > MaxOptionRunes:
			return fmt.Errorf("fieldtriage: choice field %s has an option that is empty or over %d characters", f.ID, MaxOptionRunes)
		case seen[o]:
			return fmt.Errorf("fieldtriage: choice field %s repeats an option", f.ID)
		}
		seen[o] = true
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

// Guard words. A label that matches one of these is not sent to the decision
// model and is never drafted: the applicant answers it. The patterns are a
// first line, not a proof: they read a form field's label (not a job
// description) after folding lookalike characters, and a sensitive label they
// miss is still stopped after the model reads it (Sensitive, and suggest.Run,
// which drafts only an essay reading of a label no pattern flags).
var (
	workAuthRE = regexp.MustCompile(`(?i)\b(visa|sponsor(ship)?|work(ing)? ?authori[sz]ation|authori[sz]ed to work|right to work|work rights|citizens?|citizenship|nationality|nationals|national of|immigration|work permit|green card|permanent residen(t|ts|ce|cy)|(eligible|eligibility|legally entitled) to work|legally (entitled|permitted|allowed|authori[sz]ed|eligible|able)|u\.? ?s\.? ?persons?|employment (authori[sz]ation|eligibility)|h-?1b|h-?4|l-?1|tn status|f[- ]?1 (visa|student|status))\b|\b(?-i:OPT|CPT)\b`)
	selfIDRE   = regexp.MustCompile(`(?i)\b(gender|sex|sexual|sexuality|(fe)?male|race|racial|ethnic(ity|al)?|hispanic|latin[oax]|veteran|military|armed forces|uniformed services|disability|disabilit(y|ies)|disabled|handicap(ped)?|medical (condition|history)|health (condition|status)|sexual orientation|lgbt\w*|queer|gay|lesbian|bisexual|non-?binary|cisgender|transgender|african[- ]american|caucasian|native american|alaska(n)? native|american indian|pacific islander|native hawaiian|indigenous|asian|age|religio(n|us)|pronouns?|criminal|convict(ed|ion|ions)?|marital|marital status)\b|\b(over|under|above|below|older than|younger than|at least|age) (16|18|21|40|55|62|65)\b|\b(16|18|21|40|55|62|65) ?(\+|or (older|over|above|younger|under|more)|years old|years of age)|(^|[,/(]|\bor) ?(black|white)\b ?($|[,/)]|\bor\b)`)
	consentRE  = regexp.MustCompile(`(?i)\b(agree|agreement|arbitrat\w*|consent|acknowledg\w*|certify|terms (and|&) conditions|privacy (notice|policy)|ai policy)\b`)
	// broaderRE is what only the post-model defence checks: subjects no model
	// reading should turn into a draft, but that are not pinned a category.
	broaderRE = regexp.MustCompile(`(?i)\b(date of birth|birth ?date|dob|national origin|pregnan\w*|felony|arrest(ed|s)?|social security|ssn|passport)\b`)
)

// Sensitive returns the category a label is pinned to by Guard, SelfID for a
// label only the broader pattern flags, or "" when no pattern matches. A
// field Sensitive flags is never drafted, whatever a model read it as.
func Sensitive(label string) string {
	forms := folds(label)
	if g := guardForms(forms); g != "" {
		return g
	}
	for _, form := range forms {
		if broaderRE.MatchString(form) {
			return SelfID
		}
	}
	return ""
}

// Guard returns the category a label is pinned to without asking a model,
// or "" when the model may be asked.
func Guard(label string) string { return guardForms(folds(label)) }

func guardForms(forms []string) string {
	for _, form := range forms {
		if mixedScript(form) {
			return SelfID
		}
	}
	for _, form := range forms {
		switch {
		case workAuthRE.MatchString(form):
			return WorkAuth
		case selfIDRE.MatchString(form):
			return SelfID
		case consentRE.MatchString(form):
			return Consent
		}
	}
	return ""
}
