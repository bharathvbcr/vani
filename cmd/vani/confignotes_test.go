package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/draft"
)

func notesOut(t *testing.T, stdout string) motivationNotesReply {
	t.Helper()
	var r motivationNotesReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

func setNotesInput(t *testing.T, s string) {
	t.Helper()
	old := notesInput
	notesInput = strings.NewReader(s)
	t.Cleanup(func() { notesInput = old })
}

func TestConfigMotivationReadsAndSets(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")

	o, _ := mustCode(t, 0, "config", "motivation")
	if r := notesOut(t, o); r.Notes != "" || r.MaxBytes != draft.MaxMotivationNotesBytes || r.Error != "" {
		t.Fatalf("empty read = %+v", r)
	}

	setNotesInput(t, "  I care about tools that help researchers.\n")
	o, _ = mustCode(t, 0, "config", "motivation", "--set")
	if r := notesOut(t, o); r.Notes != "I care about tools that help researchers." {
		t.Fatalf("set reply = %+v", r)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MotivationNotes != "I care about tools that help researchers." {
		t.Errorf("saved notes = %q", cfg.MotivationNotes)
	}
	o, _ = mustCode(t, 0, "config", "motivation")
	if r := notesOut(t, o); r.Notes != cfg.MotivationNotes {
		t.Errorf("read back = %+v", r)
	}

	// Empty input clears the note, and the key leaves the file.
	setNotesInput(t, " \n")
	mustCode(t, 0, "config", "motivation", "--set")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "motivation_notes") {
		t.Error("a cleared note left its key in the config")
	}
}

func TestConfigMotivationRefusesBadNotesAndKeepsTheFile(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]string{
		"over the cap":      strings.Repeat("x", draft.MaxMotivationNotesBytes+1),
		"control character": "I care \x00 about this",
		"over the read cap": strings.Repeat("y ", maxNotesInput),
	} {
		t.Run(name, func(t *testing.T) {
			setNotesInput(t, in)
			o, _ := mustCode(t, 1, "config", "motivation", "--set")
			if r := notesOut(t, o); r.Error == "" || r.Notes != "" {
				t.Errorf("reply = %+v, want an error", r)
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

func TestConfigMotivationReportsABrokenConfig(t *testing.T) {
	setup(t) // no init: the config file is missing
	o, _ := mustCode(t, 1, "config", "motivation")
	if r := notesOut(t, o); r.Error == "" {
		t.Errorf("reply = %+v, want an error", r)
	}
}
