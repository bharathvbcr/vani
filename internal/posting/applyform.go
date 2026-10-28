package posting

import "reflect"

// ApplyFormVersion is the schema of ApplyForm. A reader that meets another
// version treats the form as not read (ApplyForm.Usable).
const ApplyFormVersion = 1

// Bounds on what an ApplyForm carries, so one hostile or huge form cannot grow
// a ledger line.
const (
	MaxFormGates       = 12
	MaxFormOptions     = 16
	MaxFormUploads     = 4
	MaxFormLabelRunes  = 200
	MaxFormOptionRunes = 80
)

// The kinds of question that can knock a candidate out (FormGate.Kind).
const (
	GateSponsorship = "sponsorship"
	GateWorkAuth    = "work_authorization"
	GatePlace       = "place"
	GateExperience  = "experience"
	GateDegree      = "degree"
)

// What is worth a person's attention about a gate (FormGate.Flag).
const (
	// FlagNoSponsorAnswer: a list of answers, none of which says the candidate
	// would need sponsorship.
	FlagNoSponsorAnswer = "no_sponsorship_answer"
	// FlagAuthorizedYesNo: only yes/no on "authorized to work without
	// sponsorship", so a No is the one way to say sponsorship is needed.
	FlagAuthorizedYesNo = "authorized_yes_no"
	// FlagEligibilityYesNo: a yes/no on the right to work or on living where the
	// role is based.
	FlagEligibilityYesNo = "eligibility_yes_no"
	// FlagPlaceList: a required pick from a short list of places, so the
	// candidate must be in one of them.
	FlagPlaceList = "place_list"
)

// ApplyForm is what an application form's public structure says about who can
// apply: the questions that can knock a candidate out and how long the form
// is. It is read from the form definition, never from the posting text, and it
// carries no answers or personal data.
//
// A nil *ApplyForm on a Posting means the form was not read (no source reads
// it, the read failed or was skipped): never "no knock-outs". A read form with
// no Gates, Essays or Uploads is the form that has none.
//
// The question texts are the employer's, untrusted, and already cut to the
// Max* bounds.
type ApplyForm struct {
	V int `json:"v"`
	// Questions counts every question on the form except the applicant basics
	// (name, e-mail, resume); Required counts those that must be answered.
	Questions int `json:"questions"`
	Required  int `json:"required"`
	// Gates are the required questions that can knock a candidate out, in form
	// order, at most MaxFormGates.
	Gates []FormGate `json:"gates,omitempty"`
	// Essays counts required long-text questions. The form does not publish a
	// length limit for them.
	Essays int `json:"essays,omitempty"`
	// Uploads are the labels of required file uploads other than the resume (a
	// cover letter, a transcript), at most MaxFormUploads.
	Uploads []string `json:"uploads,omitempty"`
	// Omitted counts gates and uploads left out past the bounds, so a capped
	// list is never read as the whole form.
	Omitted int `json:"omitted,omitempty"`
	// Incomplete says some entries could not be read or the form was larger
	// than the reader scans: the counts are a floor.
	Incomplete bool `json:"incomplete,omitempty"`
}

// FormGate is one required question that can knock a candidate out.
type FormGate struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	// YesNo is true for a Boolean question, whose answers are Yes and No.
	YesNo bool `json:"yes_no,omitempty"`
	// Options are the offered answers (at most MaxFormOptions) and
	// OptionsTotal how many there are when that is more.
	Options      []string `json:"options,omitempty"`
	OptionsTotal int      `json:"options_total,omitempty"`
	Flag         string   `json:"flag,omitempty"`
}

// Usable reports whether f is a form this build can read: present and of the
// known version.
func (f *ApplyForm) Usable() bool { return f != nil && f.V == ApplyFormVersion }

// Equal reports whether two summaries say the same; two nil ones do.
func (f *ApplyForm) Equal(o *ApplyForm) bool {
	if f == nil || o == nil {
		return f == o
	}
	return reflect.DeepEqual(*f, *o)
}

// Flagged reports whether any gate carries a flag.
func (f *ApplyForm) Flagged() bool {
	if f == nil {
		return false
	}
	for _, g := range f.Gates {
		if g.Flag != "" {
			return true
		}
	}
	return false
}
