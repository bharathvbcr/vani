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

// An adapter that reuses a stored posting hands it back as the listed row, so
// storedFor never returns a closed posting with an empty description in place
// of its text: one that reads back is returned whole, one that does not is left
// out and the adapter fetches it again.
func TestW1dStoredForNeverReturnsEmptyText(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	const readable, lost = "stored text that reads back", "stored text that is edited away"
	for _, c := range []struct{ id, text string }{{"1", readable}, {"2", lost}} {
		k, err := posting.GreenhouseKey("acme", c.id)
		if err != nil {
			t.Fatal(err)
		}
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Engineer " + c.id, Description: c.text}
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
	// Both Dropped events carry their posting, so the last line of each key holds
	// the text; editing the second key's text (same length) makes it unreadable.
	edited := strings.ReplaceAll(string(raw), lost, strings.Repeat("Z", len(lost)))
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	w := &wired{led: l}
	got := w.storedFor(sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"})
	if len(got) != 1 || got[0].Description != readable {
		t.Fatalf("storedFor = %+v, want only the posting whose text reads back", got)
	}
}
