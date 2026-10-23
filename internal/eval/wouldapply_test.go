package eval

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wa(key, verdict, source string, p float64) Entry {
	return Entry{Key: postingKey(key), Labels: map[string]string{WouldApply: verdict}, Source: source, PSelect: p}
}

func TestWouldApplyRoundTripOnTheOneLabelStore(t *testing.T) {
	s, labelDir, _ := newStore(t)
	hash, err := s.PutText("a description")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{Key: "gh:a:1", Labels: map[string]string{WouldApply: "yes"}, Source: "decile:10", PSelect: 0.25, TextSHA256: hash},
		{Key: "gh:a:2", Labels: map[string]string{WouldApply: "maybe"}, Source: "dropped:title:role_fit", PSelect: 1},
		{Key: "gh:a:3", Labels: map[string]string{WouldApply: "no"}, Source: "decile:1", PSelect: 0.0125},
		{Key: "gh:a:4", Labels: map[string]string{WouldApply: Skip}, Source: "decile:2", PSelect: 0.5},
		{Key: "gh:a:5", Labels: map[string]string{WouldApply: "yes"}, Source: SourceManual},
		// A would_apply answer may sit beside the triage slots in one entry.
		{Key: "gh:a:6", Labels: map[string]string{WouldApply: "no", "work_mode": "remote"}, Source: "decile:3", PSelect: 0.1},
	} {
		if err := s.Put(e); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
	}
	got, bad, err := s.Labels()
	if err != nil || bad != 0 || len(got) != 6 {
		t.Fatalf("err=%v bad=%d n=%d", err, bad, len(got))
	}
	g := got["gh:a:1"]
	if g.Labels[WouldApply] != "yes" || g.Source != "decile:10" || g.PSelect != 0.25 || g.TextSHA256 != hash {
		t.Errorf("round trip lost a field: %+v", g)
	}
	raw, _ := os.ReadFile(filepath.Join(labelDir, LabelsFile))
	if strings.Count(string(raw), "\n") != 6 {
		t.Errorf("one append-only file, one line per label: %q", raw)
	}
	// Only the hash of the text is stored beside the label, never the text.
	if strings.Contains(string(raw), "a description") {
		t.Error("description text reached the label file")
	}
	if text, err := s.Text(hash); err != nil || text != "a description" {
		t.Errorf("hash-only text not retrievable: %q %v", text, err)
	}
}

func TestWouldApplyRejectsBadRows(t *testing.T) {
	s, labelDir, _ := newStore(t)
	bad := map[string]Entry{
		"unknown verdict":        wa("k", "apply", "decile:1", 0.5),
		"empty verdict":          wa("k", "", "decile:1", 0.5),
		"no source":              wa("k", "yes", "", 0.5),
		"no p_select":            wa("k", "yes", "decile:1", 0),
		"p_select over 1":        wa("k", "yes", "decile:1", 1.01),
		"p_select negative":      wa("k", "yes", "decile:1", -0.1),
		"p_select NaN":           wa("k", "yes", "decile:1", math.NaN()),
		"p_select Inf":           wa("k", "yes", "decile:1", math.Inf(1)),
		"source with a newline":  wa("k", "yes", "decile:1\nx", 0.5),
		"source with a space":    wa("k", "yes", "a b", 0.5),
		"source too long":        wa("k", "yes", strings.Repeat("a", 65), 0.5),
		"manual with a p_select": wa("k", "yes", SourceManual, 0.5),
		"source on a triage row": {Key: "k", Labels: map[string]string{"work_mode": "remote"}, Source: "decile:1", PSelect: 0.5},
		"p_select on triage row": {Key: "k", Labels: map[string]string{"work_mode": "remote"}, PSelect: 0.5},
	}
	for name, e := range bad {
		if err := s.Put(e); err == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
	}
	if _, err := os.Stat(filepath.Join(labelDir, LabelsFile)); !os.IsNotExist(err) {
		t.Errorf("a refused label touched the file: %v", err)
	}
	// A hand-edited line that breaks the rules is counted bad on read, not merged.
	line := `{"v":1,"key":"gh:x:1","labels":{"would_apply":"yes"},"source":"decile:1","p_select":7}` + "\n" +
		`{"v":1,"key":"gh:x:2","labels":{"would_apply":"yes"},"source":"decile:1","p_select":0.5}` + "\n"
	if err := os.WriteFile(filepath.Join(labelDir, LabelsFile), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	got, nbad, err := s.Labels()
	if err != nil || nbad != 1 || len(got) != 1 || got["gh:x:2"].PSelect != 0.5 {
		t.Fatalf("err=%v bad=%d got=%+v", err, nbad, got)
	}
}
