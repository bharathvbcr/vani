package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedLedger writes m's records into the real ledger under stateDir, one
// event per record, so commands that read the ledger file see them.
func seedLedger(t *testing.T, stateDir string, m *memLedger) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	recs := m.All()
	sort.Slice(recs, func(i, j int) bool { return recs[i].Key < recs[j].Key })
	for _, r := range recs {
		p := r.Posting
		p.Key = r.Key
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: r.LastEvent, Key: r.Key,
			Status: r.Status, Posting: &p, Annotations: r.Annotations}); err != nil {
			t.Fatal(err)
		}
	}
}

// The read-only commands work while another process holds the ledger, as
// the daemon does for its whole life. They used to open it exclusively and
// fail with "ledger: directory is locked by another process".
func TestReadOnlyCommandsRunWhileTheLedgerIsHeld(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedLedger(t, stateDir, fixture())
	held, err := ledger.Open(filepath.Join(stateDir, "ledger")) // the daemon
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	o, e := mustCode(t, 0, "status")
	if !strings.Contains(o, "postings: 4") || strings.Contains(e, "locked") {
		t.Errorf("status while held:\nstdout %s\nstderr %s", o, e)
	}
	if _, e := mustCode(t, 0, "eval"); strings.Contains(e, "locked") {
		t.Errorf("eval while held: %s", e)
	}
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("")
	if _, e := mustCode(t, 0, "label", "-n", "1"); strings.Contains(e, "locked") {
		t.Errorf("label while held: %s", e)
	}
	// The writer is untouched: it still appends.
	if err := held.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: fixture().recs["gh:a:1"].LastEvent,
		Key: "gh:a:1", Status: posting.Digested}); err != nil {
		t.Fatalf("the holder could not write after the readers: %v", err)
	}
}

// A ledger never written reads as empty, as opening it used to make it.
func TestReadOnlyCommandsOnAMissingLedger(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "postings: 0") {
		t.Errorf("status on no ledger: %s", o)
	}
}
