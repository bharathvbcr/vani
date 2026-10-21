package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type getCounter struct {
	ledger.Ledger
	gets []posting.Key
}

func (g *getCounter) Get(k posting.Key) (ledger.Record, bool) {
	g.gets = append(g.gets, k)
	return g.Ledger.Get(k)
}

// advClosedLedger holds n closed (Dead) records after a reopen, so their text is
// evicted; key 0 is the one to mark.
func advClosedLedger(t *testing.T, n int) (dir string, keys []posting.Key) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "ledger")
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		key := posting.Key("gh:acme:" + string(rune('a'+i)))
		keys = append(keys, key)
		p := posting.Posting{Key: key, ATS: key.ATS(), Company: "Acme", Title: "Closed " + string(rune('a'+i)), Description: "the full job text number " + string(rune('a'+i))}
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
	return dir, keys
}

// Only marked closed records are read back from disk; unmarked closed ones are
// never hydrated.
func TestAdvWithMarkedTextReadsOnlyMarkedClosedRecords(t *testing.T) {
	dir, keys := advClosedLedger(t, 5)
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	g := &getCounter{Ledger: l}
	mk := map[posting.Key]marks.Mark{keys[0]: marks.Saved}
	out := withMarkedText(g, l.All(), mk)
	if len(g.gets) != 1 || g.gets[0] != keys[0] {
		t.Fatalf("%d disk reads %v, want exactly one for the marked key", len(g.gets), g.gets)
	}
	for _, r := range out {
		hydrated := r.Posting.Description != ""
		if hydrated != (r.Key == keys[0]) {
			t.Errorf("%s hydrated=%v", r.Key, hydrated)
		}
	}
}

// A marked closed record whose body cannot be read back is skipped by the warm,
// and the run does not fail.
func TestAdvMarkedClosedRecordWithUnavailableBodyDoesNotFailTheRun(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Semantic.Snapshot, cfg.KB.Bank = "snapshot", "bank"
	dir, keys := advClosedLedger(t, 2)
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// The file changes under the open ledger: the evicted text is gone.
	events := filepath.Join(dir, "events.jsonl")
	raw, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("the full job text"), []byte("THE FULL JOB TEXT"))
	if err := os.WriteFile(events, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, err := marks.SetMark(marks.Dir(stateDir), k, marks.Saved, jobsT0); err != nil {
			t.Fatal(err)
		}
	}
	var seen []ledger.Record
	old := warmSemanticRun
	warmSemanticRun = func(_ context.Context, _ config.Config, _ string, recs []ledger.Record, _ map[posting.Key]marks.Mark, _ int) semanticReport {
		seen = recs
		return semanticReport{Ran: true}
	}
	t.Cleanup(func() { warmSemanticRun = old })
	var stderr bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	app := &App{Ledger: l, RunOnce: func(context.Context) error { return nil }}
	rec := &runRecord{}
	if _, err := e.runAndPublish(context.Background(), app, cfg, runOptions{}, rec); err != nil {
		t.Fatalf("the run failed on an unreadable evicted body: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("warm saw %d records", len(seen))
	}
	if docs := warmDocs(seen, map[posting.Key]marks.Mark{keys[0]: marks.Saved, keys[1]: marks.Saved}); len(docs) != 0 {
		for _, r := range seen {
			t.Logf("%s evicted=%v desc=%q", r.Key, r.BodyEvicted, r.Posting.Description)
		}
		t.Fatalf("warmDocs would embed %d records whose text is unreadable", len(docs))
	}
	t.Logf("semantic report note: %+v; stderr: %q", rec.Semantic, stderr.String())
}
