package applyform

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func yesNo(label string) Question { return Question{Label: label, Type: YesNo, Required: true} }

func choice(label string, opts ...string) Question {
	return Question{Label: label, Type: Choice, Required: true, Options: opts, OptionsTotal: len(opts)}
}

func gate(t *testing.T, q Question) (posting.FormGate, bool) {
	t.Helper()
	f := Summarize([]Question{q}, false)
	if f == nil {
		t.Fatal("not read")
	}
	if len(f.Gates) == 0 {
		return posting.FormGate{}, false
	}
	return f.Gates[0], true
}

func TestSummarizeNothingIsNotRead(t *testing.T) {
	if f := Summarize(nil, false); f != nil {
		t.Fatalf("%+v", f)
	}
	if f := Summarize([]Question{}, true); f != nil {
		t.Fatalf("%+v", f)
	}
}

func TestSummarizeBasicsAreLeftOutAndAFormOfBasicsIsRead(t *testing.T) {
	f := Summarize([]Question{
		{Label: "Name", Type: Other, Required: true, Basic: true},
		{Label: "Resume", Type: File, Required: true, Basic: true},
	}, false)
	if f == nil || f.Questions != 0 || f.Required != 0 || len(f.Uploads) != 0 || f.V != posting.ApplyFormVersion {
		t.Fatalf("%+v", f)
	}
}

func TestSponsorshipYesNoQuestions(t *testing.T) {
	cases := []struct {
		label string
		flag  string
	}{
		{"Will you now or in the future require sponsorship for employment visa status?", ""},
		{"Do you require visa sponsorship to work in the UK?", ""},
		{"Will you need sponsorship?", ""},
		{"Are you legally authorized to work in the United States without sponsorship?", posting.FlagAuthorizedYesNo},
		{"Are you authorised to work in Canada without requiring visa sponsorship?", posting.FlagAuthorizedYesNo},
		{"Can you work here and not require sponsorship now or in the future?", posting.FlagAuthorizedYesNo},
		{"Do you not need a work permit?", posting.FlagAuthorizedYesNo},
	}
	for _, c := range cases {
		g, ok := gate(t, yesNo(c.label))
		if !ok || g.Kind != posting.GateSponsorship || !g.YesNo || g.Flag != c.flag {
			t.Errorf("%q: %+v ok=%v, want flag %q", c.label, g, ok, c.flag)
		}
	}
}

func TestSponsorshipChoiceQuestions(t *testing.T) {
	cases := []struct {
		name  string
		label string
		opts  []string
		flag  string
	}{
		{"offers need", "Will you require sponsorship?", []string{"Yes", "No"}, ""},
		{"yes, in the future", "Will you require sponsorship?", []string{"No", "Yes, now", "Yes, in the future"}, ""},
		{"status list with a need", "Visa status", []string{"Citizen", "I will need sponsorship"}, ""},
		{"status list without", "Work authorization and visa status", []string{"US citizen", "Green card holder"}, posting.FlagNoSponsorAnswer},
		{"negated need does not count", "Will you require visa sponsorship?", []string{"I do not require sponsorship", "No sponsorship needed"}, posting.FlagNoSponsorAnswer},
		{"authorized form yes/no", "Are you authorized to work without sponsorship?", []string{"Yes", "No"}, posting.FlagAuthorizedYesNo},
		{"authorized form richer", "Are you authorized to work without sponsorship?", []string{"Yes", "No, I need a visa", "Not sure"}, ""},
		{"yesterday is not yes", "Will you require sponsorship?", []string{"Yesterday", "Nope"}, posting.FlagNoSponsorAnswer},
		{"no options is no claim", "Will you require sponsorship?", nil, ""},
	}
	for _, c := range cases {
		g, ok := gate(t, choice(c.label, c.opts...))
		if !ok || g.Kind != posting.GateSponsorship || g.Flag != c.flag {
			t.Errorf("%s: %+v ok=%v, want flag %q", c.name, g, ok, c.flag)
		}
	}
}

func TestWorkAuthorizationAndEligibility(t *testing.T) {
	for _, label := range []string{
		"Are you living in the country where this role is based and eligible to work there?",
		"Are you legally authorized to work in the United States?",
		"Do you have the right to work in the UK?",
		"Are you legally entitled to work in Germany?",
		"Do you have work authorization in Canada?",
	} {
		g, ok := gate(t, yesNo(label))
		if !ok || g.Kind != posting.GateWorkAuth || g.Flag != posting.FlagEligibilityYesNo {
			t.Errorf("%q: %+v ok=%v", label, g, ok)
		}
	}
	g, ok := gate(t, choice("Are you authorized to work in the US?", "Yes", "No"))
	if !ok || g.Flag != posting.FlagEligibilityYesNo || len(g.Options) != 2 {
		t.Fatalf("%+v", g)
	}
	g, ok = gate(t, choice("What is your work authorization?", "Citizen", "Visa holder"))
	if !ok || g.Kind != posting.GateWorkAuth || g.Flag != "" {
		t.Fatalf("%+v", g)
	}
}

func TestPlaceListIsAGateOnlyWhenShort(t *testing.T) {
	g, ok := gate(t, choice("Which country are you based in?", "Portugal", "Spain", "Poland"))
	if !ok || g.Kind != posting.GatePlace || g.Flag != posting.FlagPlaceList || len(g.Options) != 3 {
		t.Fatalf("%+v", g)
	}
	var all []string
	for i := 0; i < 200; i++ {
		all = append(all, fmt.Sprintf("Country %d", i))
	}
	if g, ok := gate(t, choice("Which country are you based in?", all...)); ok {
		t.Fatalf("a list of every country restricts nobody: %+v", g)
	}
	edge := all[:maxPlaceOptions]
	if g, ok := gate(t, choice("Which state do you live in?", edge...)); !ok || g.Flag != posting.FlagPlaceList || len(g.Options) != posting.MaxFormOptions || g.OptionsTotal != maxPlaceOptions {
		t.Fatalf("at the bound: %+v ok=%v", g, ok)
	}
	if _, ok := gate(t, choice("Which state do you live in?", all[:maxPlaceOptions+1]...)); ok {
		t.Fatal("one past the bound is not a gate")
	}
	// A one-option or empty list is not "one of these places"; a yes/no on
	// location words is not a place list.
	if _, ok := gate(t, choice("Which country are you based in?", "Spain")); ok {
		t.Fatal("one option")
	}
	if _, ok := gate(t, yesNo("Are you located in Spain?")); ok {
		t.Fatal("yes/no place question")
	}
}

func TestExperienceAndDegree(t *testing.T) {
	g, ok := gate(t, choice("How many years of experience do you have with Kubernetes?", "0-2", "3-5", "6+"))
	if !ok || g.Kind != posting.GateExperience || g.Flag != "" || len(g.Options) != 3 {
		t.Fatalf("%+v", g)
	}
	g, ok = gate(t, yesNo("Do you have 5+ years of experience in sales?"))
	if !ok || g.Kind != posting.GateExperience || !g.YesNo {
		t.Fatalf("%+v", g)
	}
	g, ok = gate(t, yesNo("Do you hold a Bachelor's degree?"))
	if !ok || g.Kind != posting.GateDegree {
		t.Fatalf("%+v", g)
	}
	g, ok = gate(t, choice("What is your highest level of education?", "High school", "Bachelor's", "PhD"))
	if !ok || g.Kind != posting.GateDegree || len(g.Options) != 3 {
		t.Fatalf("%+v", g)
	}
}

func TestOnlyRequiredQuestionsGate(t *testing.T) {
	q := yesNo("Will you require sponsorship?")
	q.Required = false
	f := Summarize([]Question{q}, false)
	if f == nil || f.Questions != 1 || f.Required != 0 || len(f.Gates) != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestUploadsEssaysAndTheOtherTypes(t *testing.T) {
	f := Summarize([]Question{
		{Label: "Cover letter", Type: File, Required: true},
		{Label: "Portfolio", Type: File},
		{Label: "Why us?", Type: LongText, Required: true},
		{Label: "Anything else?", Type: LongText},
		{Label: "Start date", Type: Other, Required: true},
	}, true)
	if f == nil || f.Questions != 5 || f.Required != 3 || f.Essays != 1 || len(f.Uploads) != 1 || f.Uploads[0] != "Cover letter" || !f.Incomplete {
		t.Fatalf("%+v", f)
	}
}

func TestLabelsAreCleanedAndBounded(t *testing.T) {
	long := strings.Repeat("ü", 1000)
	g, ok := gate(t, yesNo("  Require‮ sponsorship\x00?\n\t"+long))
	if !ok {
		t.Fatal("no gate")
	}
	if strings.ContainsAny(g.Text, "‮\x00\n\t") || utf8.RuneCountInString(g.Text) != posting.MaxFormLabelRunes || !utf8.ValidString(g.Text) {
		t.Fatalf("%q (%d runes)", g.Text, utf8.RuneCountInString(g.Text))
	}
	if !strings.HasPrefix(g.Text, "Require sponsorship ? ü") {
		t.Fatalf("%q", g.Text)
	}
	if got := clean("a\xffb", 10); got != "a�b" {
		t.Fatalf("invalid UTF-8: %q", got)
	}
	if got := clean("​‍", 10); got != "" {
		t.Fatalf("format characters only: %q", got)
	}
	if got := clean("abc", 3); got != "abc" {
		t.Fatalf("exactly at the limit: %q", got)
	}
	if got := clean("abcd", 3); got != "ab…" {
		t.Fatalf("one past the limit: %q", got)
	}
}

func TestOptionsAreBounded(t *testing.T) {
	var opts []string
	for i := 0; i < 100; i++ {
		opts = append(opts, strings.Repeat("o", 300))
	}
	q := choice("How many years of experience?", opts...)
	q.OptionsTotal = 5000
	g, ok := gate(t, q)
	if !ok || len(g.Options) != posting.MaxFormOptions || g.OptionsTotal != 5000 {
		t.Fatalf("%d %d", len(g.Options), g.OptionsTotal)
	}
	for _, o := range g.Options {
		if utf8.RuneCountInString(o) != posting.MaxFormOptionRunes {
			t.Fatalf("option of %d runes", utf8.RuneCountInString(o))
		}
	}
}

func TestDuplicateQuestionsAreEachCounted(t *testing.T) {
	q := yesNo("Are you eligible to work in the US?")
	f := Summarize([]Question{q, q}, false)
	if f == nil || f.Questions != 2 || len(f.Gates) != 2 {
		t.Fatalf("%+v", f)
	}
}
