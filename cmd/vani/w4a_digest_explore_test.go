package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w4aExploreLedger holds one dropped posting, whose closed body the ledger
// evicts from All(). When lose is set its text is edited away on disk (same
// length), so the ledger opens but cannot read the body back.
func w4aExploreLedger(t *testing.T, lose bool) (*ledger.File, posting.Key) {
	t.Helper()
	dir := t.TempDir()
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	const text = "Own the inference stack end to end."
	k, err := posting.GreenhouseKey("acme", "9")
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Engineer", Description: text}
	for _, st := range []posting.Status{posting.Live, posting.Dropped} {
		if err := l.Append(context.Background(), ledger.Event{V: ledger.SchemaVersion, At: jobsT0, Key: k, Status: st, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	if lose {
		path := filepath.Join(dir, "events.jsonl")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), text, strings.Repeat("Z", len(text)))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return l, k
}

func w4aPublishExplore(t *testing.T, l *ledger.File, k posting.Key) (seen []string, md string) {
	t.Helper()
	if r := l.All(); len(r) != 1 || !r[0].BodyEvicted {
		t.Fatalf("the fixture's body is not evicted in All(): %+v", r)
	}
	_, stateDir := setup(t)
	e := &env{stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	opts := digest.Options{ExploreDropped: 1, KBMatch: func(r ledger.Record) (jobinsight.Slot, bool) {
		seen = append(seen, r.Posting.Description)
		return jobinsight.Slot{}, false
	}}
	pub, err := e.publishDigest(context.Background(), &App{Ledger: l}, time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC), time.UTC, opts)
	if err != nil || pub.path == "" {
		t.Fatalf("publish: %v, path %q", err, pub.path)
	}
	raw, err := os.ReadFile(pub.path)
	if err != nil {
		t.Fatal(err)
	}
	return seen, string(raw)
}

// The daily digest scores an evicted Explore pick on the text read back from
// the ledger, not on the empty description All() hands out.
func TestW4aPublishDigestExploreReadsEvictedTextBack(t *testing.T) {
	l, k := w4aExploreLedger(t, false)
	seen, md := w4aPublishExplore(t, l, k)
	if len(seen) == 0 {
		t.Fatalf("the pick was not scored at all:\n%s", md)
	}
	for _, text := range seen {
		if text != "Own the inference stack end to end." {
			t.Fatalf("scored on %q, want the stored text", text)
		}
	}
	if !strings.Contains(md, "key `"+string(k)+"`") || strings.Contains(md, "not scored") {
		t.Fatalf("digest:\n%s", md)
	}
}

// When the body cannot be read back the pick is listed as not scored.
func TestW4aPublishDigestExploreUnreadableTextIsNotScored(t *testing.T) {
	l, k := w4aExploreLedger(t, true)
	seen, md := w4aPublishExplore(t, l, k)
	if len(seen) != 0 {
		t.Fatalf("an unreadable pick was scored on %q", seen)
	}
	if !strings.Contains(md, "key `"+string(k)+"`") || !strings.Contains(md, "not scored: text not loaded") {
		t.Fatalf("digest:\n%s", md)
	}
}
