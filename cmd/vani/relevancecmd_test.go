package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

func TestRelevanceWithNoLabelsSaysNoData(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	o, _ := mustCode(t, 0, "relevance")
	if !strings.Contains(o, "no data") {
		t.Errorf("zero labels:\n%s", o)
	}
	for _, bad := range []string{"0%", "NaN", "| New"} {
		if strings.Contains(o, bad) {
			t.Errorf("zero labels printed %q:\n%s", bad, o)
		}
	}
	// No ledger either: still no data, not an error (nothing is judged, so the
	// ledger is not read).
	setup(t)
	mustCode(t, 0, "init")
	if o, _ := mustCode(t, 0, "relevance"); !strings.Contains(o, "no data") {
		t.Errorf("empty state:\n%s", o)
	}
}

func TestRelevanceReportsLabelsFromAnAudit(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 6, 6)
	old := stdin
	t.Cleanup(func() { stdin = old })
	// 15 drawn: answer yes, no, maybe, skip in turn.
	stdin = strings.NewReader(strings.Repeat("1\n3\n2\ns\n", 4))
	mustCode(t, 0, "audit-sample", "--seed", "w1")

	o, _ := mustCode(t, 0, "relevance", "-k", "5,10")
	for _, frag := range []string{
		"# Vani relevance", "would_apply labels: 15", "## Precision@k by ordering",
		"| New | 5 |", "| Top-open | 10 |", "## Wanted by score decile",
		"## Drop recall per rule", "dropped:title:other", "dropped:model:other",
		"## Backend reliability", "| d1-ultra |", "Brier",
	} {
		if !strings.Contains(o, frag) {
			t.Errorf("report lacks %q:\n%s", frag, o)
		}
	}
	if strings.Contains(o, "| New | 20 |") {
		t.Errorf("-k was not honoured:\n%s", o)
	}
}

func TestRelevanceRefusesUnreadableLabelsAndReportsMalformed(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	dir := filepath.Join(state, "eval", "labels")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A torn line: counted and shown, the figures still print.
	good := `{"v":1,"key":"gh:k:001","labels":{"would_apply":"yes"},"source":"decile:5","p_select":0.5}` + "\n" + `{"v":1,"key":"gh:k:0`
	if err := os.WriteFile(filepath.Join(dir, eval.LabelsFile), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "relevance")
	if !strings.Contains(o, "label lines skipped as unreadable: 1") || !strings.Contains(o, "would_apply labels: 1") {
		t.Errorf("torn line:\n%s", o)
	}
	// An unreadable store prints no figures at all.
	if err := os.Remove(filepath.Join(dir, eval.LabelsFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, eval.LabelsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	so, se := mustCode(t, 1, "relevance")
	if so != "" || !strings.Contains(se, "unreadable") {
		t.Errorf("stdout %q stderr %q", so, se)
	}
	mustCode(t, 2, "relevance", "-k", "0")
	mustCode(t, 2, "relevance", "-k", "5,x")
	mustCode(t, 2, "relevance", "extra")
}
