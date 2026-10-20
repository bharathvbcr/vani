package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedAudit writes kept postings (ranks 1..kept, so priorities differ) and
// dropped ones: nTitle dropped by the title reading, nModel by a backend.
// The ledger is closed again: the audit commands read a snapshot.
func seedAudit(t *testing.T, stateDir string, kept, nTitle, nModel int) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	add := func(key string, st posting.Status, ann map[string]string) {
		p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: "Engineer " + key, Description: "Build things. " + key}
		e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: st, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < kept; i++ {
		add(fmt.Sprintf("gh:k:%03d", i), posting.Digested, map[string]string{ledger.AnnRank: fmt.Sprint(i + 1), "triage.role_fit": "strong_fit",
			"triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "0.9000"})
	}
	for i := 0; i < nTitle; i++ {
		add(fmt.Sprintf("gh:t:%03d", i), posting.Dropped, map[string]string{"triage.backend": "title", "triage.role_fit": "mismatch_domain"})
	}
	for i := 0; i < nModel; i++ {
		add(fmt.Sprintf("gh:m:%03d", i), posting.Dropped, map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain"})
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func listed(t *testing.T, args ...string) []string {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"audit-sample", "--list"}, args...)...)
	var out []string
	for _, ln := range strings.Split(o, "\n") {
		if strings.HasPrefix(ln, "decile:") || strings.HasPrefix(ln, "dropped:") {
			out = append(out, ln)
		}
	}
	return out
}

func TestAuditSampleListIsDeterministicBySeedAndStratified(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 60, 20, 20)

	a := listed(t, "--seed", "w1")
	b := listed(t, "--seed", "w1")
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("same seed differs:\n%v\n%v", a, b)
	}
	if c := listed(t, "--seed", "w2"); strings.Join(a, "\n") == strings.Join(c, "\n") {
		t.Errorf("a new seed drew the same postings")
	}
	if len(a) != 15 {
		t.Fatalf("%d picks, want 15: %v", len(a), a)
	}
	deciles, title, model := 0, 0, 0
	for _, ln := range a {
		switch {
		case strings.HasPrefix(ln, "decile:"):
			deciles++
		case strings.HasPrefix(ln, "dropped:title:"):
			title++
		case strings.HasPrefix(ln, "dropped:model:"):
			model++
		}
	}
	if deciles != 5 || title != 5 || model != 5 {
		t.Errorf("deciles %d title %d model %d, want 5/5/5: %v", deciles, title, model, a)
	}
	// Quotas are flags: a week of 2+4.
	if got := listed(t, "--seed", "w1", "--scored", "2", "--dropped", "4"); len(got) != 6 {
		t.Errorf("2+4 drew %d: %v", len(got), got)
	}
	// --list asks nothing and writes nothing.
	if _, err := os.Stat(filepath.Join(state, "eval", "labels", eval.LabelsFile)); !os.IsNotExist(err) {
		t.Errorf("--list wrote labels: %v", err)
	}
}

func TestAuditSampleSmallAndEmptyPools(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	// No ledger at all is an error, as for `jobs`: it is not an empty draw.
	if o, e := mustCode(t, 1, "audit-sample", "--list"); o != "" || !strings.Contains(e, "ledger") {
		t.Errorf("no ledger: stdout %q stderr %q", o, e)
	}
	// A ledger with nothing in either pool draws nothing, and says so.
	seedAudit(t, state, 0, 0, 0)
	if o, _ := mustCode(t, 0, "audit-sample", "--list"); !strings.Contains(o, "nothing to audit") {
		t.Errorf("empty pools:\n%s", o)
	}
	seedAudit(t, state, 3, 0, 1)
	got := listed(t, "--seed", "x")
	if len(got) != 4 {
		t.Errorf("3 kept + 1 dropped drew %d: %v", len(got), got)
	}
}

func TestAuditSampleAnswersRecordAndNeverRepeat(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 6, 6)

	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(strings.Repeat("1\n", 15)) // yes to all
	o, _ := mustCode(t, 0, "audit-sample", "--seed", "w1")
	if !strings.Contains(o, "audited 15 of 15 drawn") {
		t.Fatalf("session:\n%s", o)
	}
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	labels, bad, err := s.Labels()
	if err != nil || bad != 0 || len(labels) != 15 {
		t.Fatalf("labels %d bad %d err %v", len(labels), bad, err)
	}
	for k, e := range labels {
		if e.Labels[eval.WouldApply] != "yes" || e.Source == "" || !(e.PSelect > 0 && e.PSelect <= 1) || e.TextSHA256 == "" {
			t.Errorf("%s: %+v", k, e)
		}
	}
	// A second draw (same week) never re-draws a labelled posting.
	for _, ln := range listed(t, "--seed", "w1") {
		key := ln[strings.Index(ln, "gh:"):]
		key = strings.Fields(key)[0]
		if _, done := labels[posting.Key(key)]; done {
			t.Fatalf("re-drew labelled %s", key)
		}
	}
	// The one label store: `label` does not re-queue them either.
	snap, err := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := eval.Queue(snap.Records, s, 100, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range q {
		if _, done := labels[r.Key]; done {
			t.Errorf("label queue re-queued %s", r.Key)
		}
	}
	if len(q) != 42-15 {
		t.Errorf("label queue holds %d, want %d", len(q), 42-15)
	}
}

func TestAuditSampleRefusesUnreadableLabels(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 20, 5, 5)
	// A directory where labels.jsonl belongs cannot be read.
	dir := filepath.Join(state, "eval", "labels", eval.LabelsFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"audit-sample", "--list"}, {"audit-sample"}} {
		o, e := mustCode(t, 1, args...)
		if o != "" || !strings.Contains(e, "unreadable") {
			t.Errorf("%v: stdout %q stderr %q", args, o, e)
		}
	}
	// `label` refuses the same way (the behaviour audit-sample reuses).
	mustCode(t, 1, "label")
}

func TestAuditSampleUsageErrors(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	for _, args := range [][]string{
		{"audit-sample", "--scored", "-1"},
		{"audit-sample", "--dropped", "-1"},
		{"audit-sample", "--scored", "0", "--dropped", "0"},
		{"audit-sample", "--scored", "101"},
		{"audit-sample", "--seed", strings.Repeat("x", 300)},
		{"audit-sample", "extra"},
	} {
		mustCode(t, 2, args...)
	}
}
