package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedUnread writes n postings triaged while no backend could read them, and
// one with a real reading, which retriage leaves alone.
func seedUnread(t *testing.T, stateDir string, n int) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	add := func(id string, ann map[string]string) {
		p := posting.Posting{Key: posting.Key("gh:u:" + id), ATS: "gh", Company: "Acme", Title: "Engineer", Description: "Build things."}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Unsure, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		add(string(rune('a'+i)), map[string]string{"triage.role_fit.reading": "unavailable", "triage.backend": "d1-ultra"})
	}
	add("real", map[string]string{"triage.role_fit.reading": "answered", "triage.role_fit": "strong_fit"})
}

// retriage --dry-run reads the ledger without its lock, as rescore --dry-run
// does, and reports what the locked path would: it used to open the ledger
// exclusively and fail while the daemon held it.
func TestRetriageDryRunWhileTheLedgerIsHeld(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedUnread(t, stateDir, 3)

	free, _ := mustCode(t, 0, "retriage", "--dry-run") // no holder: same planner, same count
	if !strings.Contains(free, "3 posting(s) would return to live") {
		t.Fatalf("unheld dry run: %q", free)
	}
	held, err := ledger.Open(filepath.Join(stateDir, "ledger")) // the daemon
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	o, e := mustCode(t, 0, "retriage", "--dry-run")
	if o != free || strings.Contains(e, "locked") {
		t.Errorf("dry run while held:\nstdout %q (want %q)\nstderr %s", o, free, e)
	}
	if o, e := mustCode(t, 0, "retriage", "--dry-run", "--title-drops"); strings.Contains(e, "locked") || !strings.Contains(o, "posting(s) would return to live") {
		t.Errorf("--title-drops dry run while held:\nstdout %q\nstderr %s", o, e)
	}
	// Only the dry run is lock-free: the real thing still needs the ledger.
	if _, e := mustCode(t, 1, "retriage"); !strings.Contains(e, "locked") {
		t.Errorf("a real retriage while held: %s", e)
	}
	// The dry run changed nothing.
	if n := len(held.All()); n != 4 {
		t.Errorf("ledger holds %d records", n)
	}
	for _, r := range held.All() {
		if r.Status != posting.Unsure {
			t.Errorf("%s is %s after a dry run", r.Key, r.Status)
		}
	}
}

// A ledger never written, and an empty one, have nothing to return; a dry run
// says so and creates nothing.
func TestRetriageDryRunOnAMissingAndAnEmptyLedger(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	if o, _ := mustCode(t, 0, "retriage", "--dry-run"); !strings.Contains(o, "0 posting(s) would return to live") {
		t.Errorf("missing ledger: %q", o)
	}
	if _, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger")); err == nil {
		t.Error("a dry run created the ledger")
	}
	seedAudit(t, stateDir, 0, 0, 0)
	if o, _ := mustCode(t, 0, "retriage", "--dry-run"); !strings.Contains(o, "0 posting(s) would return to live") {
		t.Errorf("empty ledger: %q", o)
	}
}
