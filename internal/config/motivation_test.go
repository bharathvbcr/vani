package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/draft"
)

func TestMotivationNotesBounds(t *testing.T) {
	cases := []struct {
		name  string
		notes string
		want  string // "" means valid
	}{
		{"absent", "", ""},
		{"at the cap", strings.Repeat("x", draft.MaxMotivationNotesBytes), ""},
		{"over the cap", strings.Repeat("x", draft.MaxMotivationNotesBytes+1), "motivation_notes is 2049 bytes"},
		// Measured after whitespace collapse, as target_summary is.
		{"padding collapses under the cap", "\n\n  " + strings.Repeat("ab ", draft.MaxMotivationNotesBytes/3) + "\n\t\n", ""},
		{"many words over the cap", strings.Repeat("word ", draft.MaxMotivationNotesBytes/4), "motivation_notes is"},
		{"control character", "I care about\x00 open research", "motivation_notes contains a control character"},
		{"escape character", "I care about \x1b[2J open research", "motivation_notes contains a control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			c.MotivationNotes = tc.notes
			err := c.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("got %v, want valid", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestMotivationNotesRoundTripAndOmitWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := valid()
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "motivation_notes") {
		t.Error("an unset note was written: every save would add the key to existing configs")
	}
	c.MotivationNotes = "I want my work to make AI systems safer to deploy."
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.MotivationNotes != c.MotivationNotes {
		t.Errorf("motivation_notes = %q, want %q", got.MotivationNotes, c.MotivationNotes)
	}
	var raw map[string]any
	data, _ = os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil || raw["motivation_notes"] != c.MotivationNotes {
		t.Errorf("file key motivation_notes = %v (%v)", raw["motivation_notes"], err)
	}
}

func TestOversizedMotivationNotesRefusedOnLoad(t *testing.T) {
	c := valid()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["motivation_notes"] = strings.Repeat("y", draft.MaxMotivationNotesBytes+1)
	data, _ = json.Marshal(m)
	if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "motivation_notes") {
		t.Fatalf("Parse = %v, want a motivation_notes refusal", err)
	}
}
