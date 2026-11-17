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

// storedByPrefix keeps to the board's prefix, returns a posting that reads back
// whole, leaves one that does not out for every adapter but Workday's, and keeps
// it there with an empty description (which Options.Unusable reads), so the
// adapter's guards still see it as stored.
func TestW1dStoredByPrefix(t *testing.T) {
	gh := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	ghl, ghOK, _ := w1dLostLedger(t, gh, posting.GreenhouseKey)
	got := storedByPrefix(ghl, ghl.All(), gh)
	if len(got) != 1 || got[0].Key != ghOK || got[0].Description == "" {
		t.Fatalf("greenhouse: storedByPrefix = %+v, want only the posting whose text reads back", got)
	}

	wd := sources.Board{ATS: posting.Workday, Token: "acme/site", Company: "Acme"}
	wdKey := func(token, id string) (posting.Key, error) {
		tenant, site, _ := strings.Cut(token, "/")
		return posting.WorkdayKey(tenant, site, id)
	}
	wdl, wdOK, wdLost := w1dLostLedger(t, wd, wdKey)
	got = storedByPrefix(wdl, wdl.All(), wd)
	if len(got) != 2 {
		t.Fatalf("workday: storedByPrefix = %d postings, want both of the board's (one unreadable)", len(got))
	}
	for _, p := range got {
		switch p.Key {
		case wdOK:
			if workdayUnusable(p) {
				t.Fatalf("workday: the readable posting is marked unusable: %+v", p)
			}
		case wdLost:
			if !workdayUnusable(p) {
				t.Fatalf("workday: the unreadable posting carries text %q", p.Description)
			}
		default:
			t.Fatalf("workday: storedByPrefix returned %s, which is not under the board's prefix", p.Key)
		}
	}
}

// No records, and a prefix no record has, give nothing, not a panic.
func TestW1dStoredByPrefixEmpty(t *testing.T) {
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	if got := storedByPrefix(nil, nil, b); len(got) != 0 {
		t.Fatalf("no records: %+v", got)
	}
	l, _, _ := w1dLostLedger(t, b, posting.GreenhouseKey)
	if got := storedByPrefix(l, l.All(), sources.Board{ATS: posting.Greenhouse, Token: "nobody", Company: "N"}); len(got) != 0 {
		t.Fatalf("unknown prefix: %+v", got)
	}
}
