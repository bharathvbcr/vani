package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// motivation_notes reach the drafter's model prompt. The owner's own words file
// is not applied (drafts keep project names), but the built-in list is: contact
// details, handles and links, and work-authorization wording never go to a
// model. The notes below are synthetic.
var badMotivationNotes = map[string]string{
	"email":           "Reach me at someone@example.test about this",
	"handle":          "I post as @someone about tools",
	"linkedin":        "I care about tools; see my linkedin page",
	"github":          "See my github for the tools I care about",
	"visa":            "I need a visa to work here",
	"h-1b":            "I would need an H-1B transfer",
	"opt":             "I am on OPT right now",
	"sponsorship":     "I will need sponsorship",
	"citizen":         "I am not a citizen of this country",
	"nonprofit":       "I want a nonprofit that is cap-exempt",
	"zero-width":      "I need a vi\u200bsa",
	"fullwidth":       "I am on \uff2f\uff30\uff34",
	"cap exempt":      "I prefer cap exempt employers",
	"green card":      "I am waiting on a green card",
	"all caps":        "SPONSORSHIP REQUIRED",
	"work auth words": "my work authorization is limited",
}

func TestConfigMotivationRefusesBuiltInTermsAndKeepsTheFile(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range badMotivationNotes {
		t.Run(name, func(t *testing.T) {
			setNotesInput(t, in)
			o, _ := mustCode(t, 1, "config", "motivation", "--set")
			r := notesOut(t, o)
			if r.Error == "" || r.Notes != "" || !strings.Contains(r.Error, "motivation_notes") {
				t.Fatalf("reply = %+v, want an error naming motivation_notes", r)
			}
			if strings.Contains(strings.ToLower(r.Error), strings.ToLower(in)) {
				t.Errorf("the error quotes the note: %q", r.Error)
			}
			after, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Error("a refused note changed the config file")
			}
		})
	}
}

func TestConfigMotivationAcceptsAPlainNote(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, in := range []string{
		"I care about tools that help researchers.",
		"I want my work to make AI systems safer to deploy; the Modal project taught me that.", // project names stay
		"I optimize latency for a living",                                                      // 'opt' only as a whole word
	} {
		setNotesInput(t, in)
		o, _ := mustCode(t, 0, "config", "motivation", "--set")
		if r := notesOut(t, o); r.Notes != in || r.Error != "" {
			t.Errorf("%q: reply = %+v", in, r)
		}
	}
}

// A note that got into the file by hand (or before this check) is refused when
// the config loads, so no command that reads the config runs on it, and the
// drafter never builds a prompt from it. Reading the note back (the Settings
// screen) still works, so it can be fixed.
func TestLoadRefusesBuiltInTermsInANoteAlreadyInTheFile(t *testing.T) {
	for name, in := range badMotivationNotes {
		t.Run(name, func(t *testing.T) {
			cfgPath, _ := setup(t)
			mustCode(t, 0, "init")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MotivationNotes = in
			writeConfig(t, cfgPath, cfg)

			e := &env{configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
			if _, err := e.loadConfig(); err == nil || !strings.Contains(err.Error(), "motivation_notes") || strings.Contains(strings.ToLower(err.Error()), strings.ToLower(in)) {
				t.Fatalf("loadConfig: %v; want a refusal naming motivation_notes and not quoting it", err)
			}
			if d, _, _ := e.drafterWith(t.Context(), cfg, nil, 0); d != nil {
				t.Error("a drafter was built from a refused note")
			}
			// the Settings read still returns it, so it can be edited.
			o, _ := mustCode(t, 0, "config", "motivation")
			if r := notesOut(t, o); r.Notes != in || r.Error != "" {
				t.Errorf("read = %+v, want the note back", r)
			}
			// and a clean --set fixes it.
			setNotesInput(t, "I care about tools that help researchers.")
			mustCode(t, 0, "config", "motivation", "--set")
			if _, err := e.loadConfig(); err != nil {
				t.Errorf("after the fix: %v", err)
			}
		})
	}
}
