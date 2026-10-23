package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func putEntry(t *testing.T, state string, e eval.Entry) {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(e); err != nil {
		t.Fatal(err)
	}
}

func labelsFileLines(t *testing.T, state string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "eval", "labels", eval.LabelsFile))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A previous would_apply of "skip" or a manual one counts as answered: the
// audit refuses, whatever slot labels sit beside it, and writes nothing.
func TestAuditAnswerRefusesSkipAndManualPrevious(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	putEntry(t, state, eval.Entry{Key: "gh:k:001", Labels: map[string]string{eval.WouldApply: eval.Skip, "role_fit": "strong_fit"}, Source: "decile:1", PSelect: 0.5})
	putEntry(t, state, eval.Entry{Key: "gh:k:002", Labels: map[string]string{eval.WouldApply: "yes", "seniority": "senior"}, Source: eval.SourceManual})
	before := len(labelsFileLines(t, state))
	for _, k := range []string{"gh:k:001", "gh:k:002"} {
		if _, se := mustCode(t, 1, "audit", "answer", "--key", k, "--verdict", "no", "--source", "decile:3", "--p-select", "0.5"); !strings.Contains(se, "already") {
			t.Errorf("%s: %q", k, se)
		}
	}
	if n := len(labelsFileLines(t, state)); n != before {
		t.Errorf("a refused answer appended %d line(s)", n-before)
	}
	m := readLabels(t, state)["gh:k:002"]
	if m.Source != eval.SourceManual || m.PSelect != 0 || m.Labels["seniority"] != "senior" {
		t.Errorf("manual entry changed: %+v", m)
	}
}

// The merged entry takes the audit's stratum and p_select whole (a slot-only
// predecessor has none to mix), and the predecessor's line stays untouched in
// the append-only log, so the merge copies rather than edits.
func TestAuditAnswerSourceNotMixedAndHistoryIntact(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	putEntry(t, state, eval.Entry{Key: "gh:k:003", Labels: map[string]string{"role_fit": "adjacent", "seniority": "mid"}})
	first := labelsFileLines(t, state)[0]
	mustCode(t, 0, "audit", "answer", "--key", "gh:k:003", "--verdict", "maybe", "--source", "dropped:title:role_fit", "--p-select", "0.125")
	lines := labelsFileLines(t, state)
	if len(lines) != 2 || lines[0] != first {
		t.Fatalf("history rewritten: %q", lines)
	}
	e := readLabels(t, state)["gh:k:003"]
	if e.Source != "dropped:title:role_fit" || e.PSelect != 0.125 || len(e.Labels) != 3 || e.Labels["role_fit"] != "adjacent" {
		t.Errorf("merged: %+v", e)
	}
}

// The sampler never draws a key that has any label, so the draw path has no
// previous labels to merge: answering a pick writes the draw's stratum with
// would_apply alone, and a slot-labelled key is out of the draw.
func TestAuditAnswerDrawPathWritesWouldApplyAlone(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 3, 3)
	d, _ := drawJSON(t, "--seed", "w")
	if len(d.Picks) < 2 {
		t.Fatalf("picks %d", len(d.Picks))
	}
	k0, k1 := posting.Key(d.Picks[0]["key"].(string)), posting.Key(d.Picks[1]["key"].(string))
	putEntry(t, state, eval.Entry{Key: k1, Labels: map[string]string{"role_fit": "strong_fit"}})
	mustCode(t, 0, "audit", "answer", "--key", string(k0), "--verdict", "yes", "--seed", "w")
	if e := readLabels(t, state)[k0]; len(e.Labels) != 1 || e.Labels[eval.WouldApply] != "yes" || e.Source == "" || e.PSelect <= 0 {
		t.Errorf("draw-path answer: %+v", e)
	}
	if _, se := mustCode(t, 1, "audit", "answer", "--key", string(k1), "--verdict", "yes", "--seed", "w"); !strings.Contains(se, "not in the draw") {
		t.Errorf("slot-labelled key in the draw: %q", se)
	}
}
