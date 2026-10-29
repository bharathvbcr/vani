package fieldtriage

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/decide"
)

func TestQuestionIsValidAndCarriesOnlyLabelAndKind(t *testing.T) {
	f := Field{ID: "q1", Label: "  Why do you want to\n work at Acme?  ", Kind: KindLong}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if f.Label != "Why do you want to work at Acme?" {
		t.Fatalf("label not normalized: %q", f.Label)
	}
	q := Question(f)
	if err := decide.ValidateQuestion(q); err != nil {
		t.Fatalf("question invalid: %v", err)
	}
	want := "Field type: textarea\nQuestion label (untrusted text copied from a job site):\nWhy do you want to work at Acme?"
	if string(q.Context) != want {
		t.Fatalf("context = %q", q.Context)
	}
	if q.Task != Task || len(q.Slots) != 1 || q.Slots[0].Name != Slot || q.RecordID != "q1" {
		t.Fatalf("question shape: %+v", q)
	}
}

func TestDescriptionsCoverEveryOption(t *testing.T) {
	d := Descriptions()[Slot]
	if len(d) != len(Options) {
		t.Fatalf("%d descriptions for %d options", len(d), len(Options))
	}
	for _, o := range Options {
		if strings.TrimSpace(d[o.Value]) == "" {
			t.Fatalf("option %s has no description", o.Value)
		}
	}
}

func TestValidateRefuses(t *testing.T) {
	for name, f := range map[string]Field{
		"empty id":     {ID: "", Label: "x", Kind: KindText},
		"long id":      {ID: strings.Repeat("a", 65), Label: "x", Kind: KindText},
		"blank label":  {ID: "a", Label: " \n\t ", Kind: KindText},
		"huge label":   {ID: "a", Label: strings.Repeat("é", MaxLabelRunes+1), Kind: KindText},
		"bad utf8":     {ID: "a", Label: "ok\xff", Kind: KindText},
		"choice field": {ID: "a", Label: "Yes or no?", Kind: "select"},
		"negative max": {ID: "a", Label: "x", Kind: KindText, MaxChars: -1},
		"huge max":     {ID: "a", Label: "x", Kind: KindText, MaxChars: MaxMaxChars + 1},
	} {
		if err := f.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMaxCharsIsAnOptionalBoundedNumberAndNeverReachesTheModel(t *testing.T) {
	for _, n := range []int{0, 1, 260, 1300, MaxMaxChars} {
		f := Field{ID: "a", Label: "Why us?", Kind: KindLong, MaxChars: n}
		if err := f.Validate(); err != nil {
			t.Errorf("max_chars %d refused: %v", n, err)
		}
		if got, want := string(Question(f).Context), "Field type: textarea\nQuestion label (untrusted text copied from a job site):\nWhy us?"; got != want {
			t.Errorf("max_chars %d changed what the model sees: %q", n, got)
		}
	}
}

func TestGuardPinsPrivateAndConsentLabels(t *testing.T) {
	cases := map[string]string{
		"Will you now or in the future require employment visa sponsorship?": WorkAuth,
		"Are you legally authorized to work in the US?":                      WorkAuth,
		"Gender":                           SelfID,
		"Are you Hispanic/Latino?":         SelfID,
		"Veteran Status":                   SelfID,
		"Agreement to Arbitrate":           Consent,
		"AI Policy for Application":        Consent,
		"I acknowledge the privacy notice": Consent,
		"Why Anthropic?":                   "",
		"Additional Information":           "",
		"LinkedIn Profile":                 "",
		"How do you pronounce your name?":  "",
	}
	for label, want := range cases {
		if got := Guard(label); got != want {
			t.Errorf("Guard(%q) = %q, want %q", label, got, want)
		}
	}
}
