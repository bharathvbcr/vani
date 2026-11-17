package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// w1dLostLedger holds, for the board b, one posting that reads back and one
// whose closed body is edited away on disk (same length, so the ledger opens
// but cannot read the body), plus a posting of a board whose token extends b's.
func w1dLostLedger(t *testing.T, b sources.Board, key func(token, id string) (posting.Key, error)) (l *ledger.File, readable, lost posting.Key) {
	t.Helper()
	dir := t.TempDir()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	const goodText, lostText = "stored text that reads back", "stored text that is edited away"
	for _, c := range []struct {
		token, id, text string
		out             *posting.Key
	}{{b.Token, "1", goodText, &readable}, {b.Token, "2", lostText, &lost}, {b.Token + "x", "3", "another board's text", new(posting.Key)}} {
		k, err := key(c.token, c.id)
		if err != nil {
			t.Fatal(err)
		}
		*c.out = k
		p := posting.Posting{Key: k, ATS: b.ATS, Company: "Acme", Title: "Engineer " + c.id, Description: c.text}
		for _, st := range []posting.Status{posting.Live, posting.Dropped} {
			if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: jobsT0, Key: k, Status: st, Posting: &p}); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(dir, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), lostText, strings.Repeat("Z", len(lostText)))), 0o600); err != nil {
		t.Fatal(err)
	}
	return l, readable, lost
}

// storedByPrefix keeps to the board's prefix and returns every posting the
// board holds, for every adapter: a posting that reads back whole with its text,
// one that does not with an empty description and its key in the unusable set,
// so the adapter's guards still see it as stored and its detail planner reads it
// again.
func TestW1dStoredByPrefix(t *testing.T) {
	gh := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	ghl, ghOK, ghLost := w1dLostLedger(t, gh, posting.GreenhouseKey)
	got, unusable := storedByPrefix(ghl, ghl.All(), gh)
	if len(got) != 2 || len(unusable) != 1 || unusable[0] != ghLost {
		t.Fatalf("greenhouse: storedByPrefix = %d postings, unusable %v; want both of the board's and only %s marked", len(got), unusable, ghLost)
	}
	for _, p := range got {
		if p.Key == ghOK && p.Description == "" || p.Key == ghLost && p.Description != "" || p.Key != ghOK && p.Key != ghLost {
			t.Fatalf("greenhouse: %s carries text %q", p.Key, p.Description)
		}
	}

	wd := sources.Board{ATS: posting.Workday, Token: "acme/site", Company: "Acme"}
	wdKey := func(token, id string) (posting.Key, error) {
		tenant, site, _ := strings.Cut(token, "/")
		return posting.WorkdayKey(tenant, site, id)
	}
	wdl, wdOK, wdLost := w1dLostLedger(t, wd, wdKey)
	got, unusable = storedByPrefix(wdl, wdl.All(), wd)
	if len(got) != 2 || len(unusable) != 1 || unusable[0] != wdLost {
		t.Fatalf("workday: storedByPrefix = %d postings, unusable %v; want both of the board's and only %s marked", len(got), unusable, wdLost)
	}
	for _, p := range got {
		if p.Key != wdOK && p.Key != wdLost {
			t.Fatalf("workday: storedByPrefix returned %s, which is not under the board's prefix", p.Key)
		}
	}
}

// A stored posting whose text is genuinely empty reads back whole, so it is not
// marked unusable: the mark comes from the ledger failing to read a body, never
// from the text being empty.
func TestW1dStoredByPrefixEmptyTextIsNotMarked(t *testing.T) {
	l, err := ledger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	k, err := posting.GreenhouseKey("acme", "1")
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Engineer"}
	for _, st := range []posting.Status{posting.Live, posting.Dropped} {
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: jobsT0, Key: k, Status: st, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	got, unusable := storedByPrefix(l, l.All(), b)
	if len(got) != 1 || got[0].Description != "" || len(unusable) != 0 {
		t.Fatalf("storedByPrefix = %+v, unusable %v: want the empty-text posting returned and unmarked", got, unusable)
	}
}

// No records, and a prefix no record has, give nothing, not a panic.
func TestW1dStoredByPrefixEmpty(t *testing.T) {
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	if got, un := storedByPrefix(nil, nil, b); len(got) != 0 || len(un) != 0 {
		t.Fatalf("no records: %+v", got)
	}
	l, _, _ := w1dLostLedger(t, b, posting.GreenhouseKey)
	if got, un := storedByPrefix(l, l.All(), sources.Board{ATS: posting.Greenhouse, Token: "nobody", Company: "N"}); len(got) != 0 || len(un) != 0 {
		t.Fatalf("unknown prefix: %+v", got)
	}
}
