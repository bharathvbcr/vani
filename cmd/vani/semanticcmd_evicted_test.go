package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// ft-0aa92138: the CLI reads the ledger with ledger.ReadSnapshot, which never
// evicts a body, so an owner-marked closed record reaches the warm with its
// description and needs no withMarkedText. These tests pin that: if the CLI ever
// moves to a resident ledger (File.All evicts), they fail until it hydrates
// marked records the way the daily run does.

// closedLedgerAt writes n closed (Dead) records into dir and returns their keys.
func closedLedgerAt(t *testing.T, dir string, n int) []posting.Key {
	t.Helper()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var keys []posting.Key
	for i := 0; i < n; i++ {
		key := posting.Key("gh:acme:" + string(rune('a'+i)))
		keys = append(keys, key)
		p := posting.Posting{Key: key, ATS: key.ATS(), Company: "Acme", Title: "Closed " + string(rune('a'+i)), Description: "Run k8s services number " + string(rune('a'+i))}
		for _, ev := range []ledger.Event{
			{V: 1, At: jobsT0, Key: key, Status: posting.Live, Posting: &p},
			{V: 1, At: jobsT0, Key: key, Status: posting.Dead},
		} {
			if err := l.Append(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return keys
}

// closedMarkedLedger writes n closed records under state and marks the first one.
func closedMarkedLedger(t *testing.T, state string, n int) []posting.Key {
	t.Helper()
	keys := closedLedgerAt(t, filepath.Join(state, "ledger"), n)
	if _, err := marks.SetMark(marks.Dir(state), keys[0], marks.Saved, jobsT0); err != nil {
		t.Fatal(err)
	}
	return keys
}

// The same ledger through File.All has every closed record evicted (the gap the
// daily run closes with withMarkedText); through the CLI's ReadSnapshot it does
// not, so `vani semantic warm` scores the marked closed record.
func TestSemanticWarmCLIWarmsMarkedClosedRecordWithItsDescription(t *testing.T) {
	_, state := semanticSetup(t)
	keys := closedMarkedLedger(t, state, 3)

	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	evicted := 0
	for _, r := range l.All() {
		if r.BodyEvicted && r.Posting.Description == "" {
			evicted++
		}
	}
	_ = l.Close()
	if evicted != 3 {
		t.Fatalf("precondition: %d closed records evicted in memory, want 3", evicted)
	}

	recs, err := (&env{stateDir: state, stderr: &strings.Builder{}}).ledgerRecords()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.BodyEvicted || r.Posting.Description == "" {
			t.Fatalf("CLI record %s evicted=%v desc=%q: ReadSnapshot must return every body", r.Key, r.BodyEvicted, r.Posting.Description)
		}
	}
	mk, err := marks.ReadMarks(marks.Dir(state))
	if err != nil {
		t.Fatal(err)
	}
	docs := warmDocs(recs, mk)
	if len(docs) != 1 || docs[0].Key != string(keys[0]) || docs[0].Description == "" {
		t.Fatalf("warm docs %+v: want only the marked closed record, with its description", docs)
	}

	out, errOut := mustCode(t, 0, "semantic", "warm")
	if !strings.Contains(out, "scored 1") {
		t.Fatalf("warm output %q (stderr %q): the marked closed record was not scored", out, errOut)
	}
	if strings.Contains(errOut, "unreadable") {
		t.Errorf("stderr %q reports an unreadable body on a readable ledger", errOut)
	}
}

// withMarkedText over the CLI's records reads nothing back from the ledger (no
// Get for any record, marked or not), nothing is unreadable, and unmarked closed
// records are never warmed.
func TestSemanticWarmCLIRecordsNeedNoHydration(t *testing.T) {
	_, state := semanticSetup(t)
	keys := closedMarkedLedger(t, state, 4)
	recs, err := (&env{stateDir: state, stderr: &strings.Builder{}}).ledgerRecords()
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	g := &getCounter{Ledger: l}
	mk := map[posting.Key]marks.Mark{keys[0]: marks.Saved}
	out := withMarkedText(g, recs, mk)
	if len(g.gets) != 0 {
		t.Fatalf("%d ledger reads %v for snapshot records, want none", len(g.gets), g.gets)
	}
	if n := unreadableMarked(out, mk); n != 0 {
		t.Fatalf("%d unreadable marked records in a snapshot", n)
	}
	if docs := warmDocs(out, nil); len(docs) != 0 {
		t.Fatalf("unmarked closed records warmed: %+v", docs)
	}
}
