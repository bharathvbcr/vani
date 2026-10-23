package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func putSlotLabels(t *testing.T, state string, key posting.Key, labels map[string]string) {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(eval.Entry{Key: key, Labels: labels}); err != nil {
		t.Fatal(err)
	}
}

// The eval store keeps the latest line per key whole, so an audit answer for a
// posting that already has slot labels must carry them forward, not replace
// them with a would_apply-only map.
func TestAuditAnswerKeepsSlotLabels(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	putSlotLabels(t, state, "gh:k:001", map[string]string{"role_fit": "strong_fit", "seniority": "senior"})

	mustCode(t, 0, "audit", "answer", "--key", "gh:k:001", "--verdict", "yes", "--source", "decile:3", "--p-select", "0.5")
	e := readLabels(t, state)["gh:k:001"]
	if e.Labels[eval.WouldApply] != "yes" || e.Labels["role_fit"] != "strong_fit" || e.Labels["seniority"] != "senior" {
		t.Fatalf("labels after the audit answer: %v", e.Labels)
	}
	if e.Source != "decile:3" || e.PSelect != 0.5 {
		t.Errorf("source/p_select = %q/%v", e.Source, e.PSelect)
	}
	// A duplicate answer is refused and leaves every label alone.
	if _, se := mustCode(t, 1, "audit", "answer", "--key", "gh:k:001", "--verdict", "no", "--source", "decile:3", "--p-select", "0.5"); !strings.Contains(se, "already") {
		t.Fatalf("duplicate answer: %q", se)
	}
	if got := readLabels(t, state)["gh:k:001"].Labels; len(got) != 3 || got[eval.WouldApply] != "yes" || got["role_fit"] != "strong_fit" {
		t.Fatalf("a refused duplicate changed the labels: %v", got)
	}
}

// A previous entry with an empty (or absent) label map merges to just the
// answer: nil and empty behave alike.
func TestAuditAnswerWithNoPreviousLabels(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	mustCode(t, 0, "audit", "answer", "--key", "gh:k:002", "--verdict", "no", "--source", "decile:2", "--p-select", "0.25")
	if got := readLabels(t, state)["gh:k:002"].Labels; len(got) != 1 || got[eval.WouldApply] != "no" {
		t.Fatalf("labels %v", got)
	}
}
