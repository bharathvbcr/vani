package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The footer's "Notes deferred" counts postings the digest is holding back for a
// note. A posting the owner hid in Jobs is never going to be listed, whatever
// note a later run writes, so it is not one the digest is holding back.
func TestW4aAdvNotesDeferredIgnoresHiddenRoles(t *testing.T) {
	l := w4aDeferredLedger(t) // gh:a:unknown and gh:a:budget are deferred
	_, stateDir := setup(t)
	if _, err := marks.SetMark(marks.Dir(stateDir), "gh:a:budget", marks.Hidden, time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// A closed posting that still carries the annotation is not scored.
	dead := *l.recs["gh:a:fresh"]
	dead.Key, dead.Status = "gh:a:dead", posting.Dead
	dead.Annotations = map[string]string{pipeline.AnnNotesDeferred: pipeline.DeferBudget}
	l.recs["gh:a:dead"] = &dead

	e := &env{stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	pub, err := e.publishDigest(context.Background(), &App{Ledger: l}, time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC), time.UTC, digestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pub.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Notes deferred: 1 scored posting(s)") {
		t.Fatalf("one visible deferred posting expected (the other is hidden, the third closed):\n%s", raw)
	}
}
