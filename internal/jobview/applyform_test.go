package jobview

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func abPosting(f *posting.ApplyForm) posting.Posting {
	return posting.Posting{Key: "ab:acme:1", Form: f}
}

func TestApplyFormNote(t *testing.T) {
	cases := []struct {
		name string
		p    posting.Posting
		want string
	}{
		{"ashby not read", abPosting(nil), "Application form not read."},
		{"ashby newer version", abPosting(&posting.ApplyForm{V: 9}), "Application form not read."},
		{"other ATS says nothing", posting.Posting{Key: "gh:acme:1"}, ""},
		{"other ATS with a form still reads it", posting.Posting{Key: "gh:acme:1", Form: &posting.ApplyForm{V: 1, Questions: 3, Required: 3}}, "Application form: 3 of 3 questions required."},
		{"only basics", abPosting(&posting.ApplyForm{V: 1}), "Application form: no questions beyond name, e-mail and resume."},
		{"essays and uploads", abPosting(&posting.ApplyForm{V: 1, Questions: 5, Required: 4, Essays: 3, Uploads: []string{"Cover letter", "Transcript"}}),
			"Application form: 4 of 5 questions required; 3 required essays; required uploads beyond the resume: Cover letter, Transcript."},
		{"asked not flagged", abPosting(&posting.ApplyForm{V: 1, Questions: 4, Required: 4, Gates: []posting.FormGate{
			{Kind: posting.GateSponsorship, YesNo: true},
			{Kind: posting.GateWorkAuth, Options: []string{"Citizen"}},
			{Kind: posting.GateExperience}, {Kind: posting.GateExperience}, {Kind: posting.GateDegree},
		}}), "Application form: 4 of 4 questions required; asks about sponsorship; asks about work authorization; " +
			"2 required years-of-experience questions with fixed answers; 1 required degree question with fixed answers."},
		{"no answer", abPosting(&posting.ApplyForm{V: 1, Questions: 1, Required: 1, Gates: []posting.FormGate{
			{Kind: posting.GateSponsorship, Flag: posting.FlagNoSponsorAnswer}}}),
			"Application form: 1 of 1 questions required; sponsorship: no answer offered says you would need it."},
		{"eligibility", abPosting(&posting.ApplyForm{V: 1, Questions: 1, Required: 1, Gates: []posting.FormGate{
			{Kind: posting.GateWorkAuth, Flag: posting.FlagEligibilityYesNo}}}),
			"Application form: 1 of 1 questions required; work authorization: a required yes/no on your right to work there."},
		{"places with more", abPosting(&posting.ApplyForm{V: 1, Questions: 1, Required: 1, Gates: []posting.FormGate{
			{Kind: posting.GatePlace, Flag: posting.FlagPlaceList, Options: []string{"Spain", "Portugal"}, OptionsTotal: 30}}}),
			"Application form: 1 of 1 questions required; you must be in one of: Spain, Portugal and 28 more."},
		{"partial", abPosting(&posting.ApplyForm{V: 1, Questions: 1, Required: 1, Omitted: 4, Incomplete: true}),
			"Application form: 1 of 1 questions required; 4 more not listed; form only partly read."},
	}
	for _, c := range cases {
		if got := ApplyFormNote(c.p); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestApplyFormNoteIsPlainAndBounded(t *testing.T) {
	var opts []string
	for i := 0; i < posting.MaxFormOptions; i++ {
		opts = append(opts, strings.Repeat("ü", posting.MaxFormOptionRunes))
	}
	f := &posting.ApplyForm{V: 1, Questions: 1, Required: 1, Gates: []posting.FormGate{
		{Kind: posting.GatePlace, Flag: posting.FlagPlaceList, Options: opts, OptionsTotal: 40},
		{Kind: posting.GatePlace, Flag: posting.FlagPlaceList, Options: []string{"A\x1b[31m\nB"}},
	}}
	got := ApplyFormNote(abPosting(f))
	if utf8.RuneCountInString(got) > NoteRunes+1 || strings.ContainsAny(got, "\x1b\n") {
		t.Fatalf("%d runes: %q", utf8.RuneCountInString(got), got)
	}
}

func TestApplyFormFlags(t *testing.T) {
	f := &posting.ApplyForm{V: 1, Gates: []posting.FormGate{
		{Kind: posting.GateSponsorship, Flag: posting.FlagAuthorizedYesNo},
		{Kind: posting.GateSponsorship, Flag: posting.FlagNoSponsorAnswer},
		{Kind: posting.GateWorkAuth, Flag: posting.FlagEligibilityYesNo},
		{Kind: posting.GatePlace, Flag: posting.FlagPlaceList},
		{Kind: posting.GateExperience},
	}}
	if got := strings.Join(ApplyFormFlags(f), ","); got != "sponsorship,work-auth,places" {
		t.Fatalf("%q", got)
	}
	if ApplyFormFlags(nil) != nil || ApplyFormFlags(&posting.ApplyForm{V: 9, Gates: f.Gates}) != nil || ApplyFormFlags(&posting.ApplyForm{V: 1}) != nil {
		t.Fatal("an unread, newer or unflagged form has no flags")
	}
}

func TestSponsorshipLineSitsBesideTriageAndNeverReplacesIt(t *testing.T) {
	flagged := &posting.ApplyForm{V: 1, Gates: []posting.FormGate{{Kind: posting.GateSponsorship, Flag: posting.FlagAuthorizedYesNo}}}
	asked := &posting.ApplyForm{V: 1, Gates: []posting.FormGate{{Kind: posting.GateSponsorship, YesNo: true}}}
	cases := []struct {
		name string
		j    Job
		want string
	}{
		{"flagged beside a reading", Job{Key: "ab:a:1", ApplyForm: flagged, Triage: map[string]string{"sponsorship": "sponsors_visa"}},
			"Sponsorship: triage reads Sponsors visas; only a yes/no on being authorized without it."},
		{"asked beside a reading", Job{Key: "ab:a:1", ApplyForm: asked, Triage: map[string]string{"sponsorship": "no_sponsorship"}},
			"Sponsorship: triage reads No sponsorship; the form asks."},
		{"no triage reading", Job{Key: "ab:a:1", ApplyForm: asked}, "Sponsorship: triage reads nothing; the form asks."},
		{"unknown reading shows as written", Job{Key: "ab:a:1", ApplyForm: asked, Triage: map[string]string{"sponsorship": "weird"}},
			"Sponsorship: triage reads weird; the form asks."},
		{"form does not ask", Job{Key: "ab:a:1", ApplyForm: &posting.ApplyForm{V: 1}}, "Sponsorship: triage reads nothing; the form does not ask."},
		{"form not read", Job{Key: "ab:a:1"}, "Sponsorship: triage reads nothing; not read."},
		{"form of a newer version", Job{Key: "ab:a:1", ApplyForm: &posting.ApplyForm{V: 7}}, "Sponsorship: triage reads nothing; not read."},
		{"another ATS", Job{Key: "gh:a:1", ApplyForm: flagged}, ""},
	}
	for _, c := range cases {
		if got := SponsorshipLine(c.j); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
