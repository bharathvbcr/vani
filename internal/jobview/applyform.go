package jobview

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The application-form summary a posting carries (posting.ApplyForm) as words.
// It is display only: nothing here, and no engine rule, drops or blocks a
// posting because of it. The sponsorship line shows the form beside triage's
// sponsorship reading and never replaces it.

// formUnread is what a posting whose form could not be read shows. It is said
// in words, never left blank: a form not read is not a form with no knock-outs.
const formUnread = "Application form not read."

// ApplyFormNote is the one line about a posting's application form: its size
// and the required questions that can knock a candidate out. A posting from an
// ATS whose form Vani cannot read has none ("").
func ApplyFormNote(p posting.Posting) string {
	f := p.Form
	if f == nil || !f.Usable() {
		if p.Key.ATS() == posting.Ashby {
			return formUnread
		}
		return ""
	}
	var parts []string
	if f.Questions == 0 {
		parts = append(parts, "no questions beyond name, e-mail and resume")
	} else {
		parts = append(parts, fmt.Sprintf("%d of %d questions required", f.Required, f.Questions))
	}
	asks := map[string]int{}
	for _, g := range f.Gates {
		if c := gateClause(g); c != "" {
			parts = append(parts, c)
		} else {
			asks[g.Kind]++
		}
	}
	if asks[posting.GateSponsorship] > 0 {
		parts = append(parts, "asks about sponsorship")
	}
	if asks[posting.GateWorkAuth] > 0 {
		parts = append(parts, "asks about work authorization")
	}
	if n := asks[posting.GateExperience]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d required years-of-experience %s with fixed answers", n, plural(n, "question")))
	}
	if n := asks[posting.GateDegree]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d required degree %s with fixed answers", n, plural(n, "question")))
	}
	if f.Essays > 0 {
		parts = append(parts, fmt.Sprintf("%d required %s", f.Essays, plural(f.Essays, "essay")))
	}
	if len(f.Uploads) > 0 {
		parts = append(parts, "required uploads beyond the resume: "+strings.Join(f.Uploads, ", "))
	}
	if f.Omitted > 0 {
		parts = append(parts, fmt.Sprintf("%d more not listed", f.Omitted))
	}
	if f.Incomplete {
		parts = append(parts, "form only partly read")
	}
	return CleanText("Application form: "+strings.Join(parts, "; ")+".", NoteRunes, false)
}

// ApplyFormFlags are the short words for the flagged gates of a posting's form,
// in form order without repeats: "sponsorship", "work-auth" and "places". They
// are what a list row can say in a few characters; nil for an unread form or
// one with no flagged gate.
func ApplyFormFlags(f *posting.ApplyForm) []string {
	if !f.Usable() {
		return nil
	}
	var out []string
	for _, g := range f.Gates {
		w := ""
		switch g.Flag {
		case posting.FlagNoSponsorAnswer, posting.FlagAuthorizedYesNo:
			w = "sponsorship"
		case posting.FlagEligibilityYesNo:
			w = "work-auth"
		case posting.FlagPlaceList:
			w = "places"
		}
		if w != "" && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// gateClause words a flagged gate, or "" for one that is only asked.
func gateClause(g posting.FormGate) string {
	switch g.Flag {
	case posting.FlagNoSponsorAnswer:
		return "sponsorship: no answer offered says you would need it"
	case posting.FlagAuthorizedYesNo:
		return "sponsorship: only a yes/no on being authorized without it"
	case posting.FlagEligibilityYesNo:
		return "work authorization: a required yes/no on your right to work there"
	case posting.FlagPlaceList:
		more := ""
		if g.OptionsTotal > len(g.Options) {
			more = fmt.Sprintf(" and %d more", g.OptionsTotal-len(g.Options))
		}
		return "you must be in one of: " + strings.Join(g.Options, ", ") + more
	}
	return ""
}

// SponsorshipLine puts the form's sponsorship question beside triage's
// sponsorship reading, for an Ashby posting, so the two are read together and
// neither overrides the other. It is "" for any other posting. j.ApplyForm must
// be set (`jobs show` sets it); an unset one reads as not read.
func SponsorshipLine(j Job) string {
	if j.Key.ATS() != posting.Ashby {
		return ""
	}
	triage := "nothing"
	if v := j.Triage["sponsorship"]; v != "" {
		triage = v
		if l := TriageLabel("sponsorship", v); l != "" {
			triage = l
		}
	}
	form := "not read"
	if f := j.ApplyForm; f.Usable() {
		form = "the form does not ask"
		for _, g := range f.Gates {
			if g.Kind != posting.GateSponsorship {
				continue
			}
			form = "the form asks"
			if c := gateClause(g); c != "" {
				form = strings.TrimPrefix(c, "sponsorship: ")
			}
			break
		}
	}
	return CleanText("Sponsorship: triage reads "+triage+"; "+form+".", NoteRunes, false)
}
