package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w4aDeferredLedger holds two scored postings whose note is waiting, one for
// each reason, one explained posting and one scored posting not yet gated.
func w4aDeferredLedger(t *testing.T) *memLedger {
	t.Helper()
	at := time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
	mk := func(key string, st posting.Status, ann map[string]string) *ledger.Record {
		return &ledger.Record{Key: posting.Key(key), Status: st, FirstSeen: at, LastEvent: at,
			Posting:     posting.Posting{Company: "Acme", Title: "Eng " + key, URL: "https://x.example/" + key},
			Annotations: ann}
	}
	m := &memLedger{recs: map[posting.Key]*ledger.Record{}}
	for _, r := range []*ledger.Record{
		mk("gh:a:unknown", posting.Scored, map[string]string{pipeline.AnnNotesDeferred: pipeline.DeferUnknownRelevance}),
		mk("gh:a:budget", posting.Scored, map[string]string{pipeline.AnnNotesDeferred: pipeline.DeferBudget}),
		mk("gh:a:ready", posting.Explained, map[string]string{"score.total": "70"}),
		mk("gh:a:fresh", posting.Scored, nil),
	} {
		m.recs[r.Key] = r
	}
	return m
}

// w4aListed is whether the digest lists the posting k, by its link.
func w4aListed(md, k string) bool { return strings.Contains(md, "<https://x.example/"+k+">") }

func w4aPublishDigest(t *testing.T, l ledger.Ledger) string {
	t.Helper()
	_, stateDir := setup(t)
	e := &env{stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	pub, err := e.publishDigest(context.Background(), &App{Ledger: l}, time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC), time.UTC, digestOptionsForTest())
	if err != nil || pub.path == "" {
		t.Fatalf("publish: %v, path %q", err, pub.path)
	}
	raw, err := os.ReadFile(pub.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A scored posting whose note is deferred is not in the digest until it is
// explained (the digest lists explained and unsure roles), so the footer counts
// the ones held back: otherwise a posting waiting on the notes budget or on an
// unknown relevance is not in the digest and is not said to exist. When the
// note is written the posting is explained and listed: the second half only
// checks the digest's side of that (it lists explained roles, and the footer
// stops counting the posting). That a later run does write the note, for each
// deferral reason, is internal/pipeline's TestW4aDeferredNoteIsExplainedOnALaterRun.
func TestW4aDigestCountsPostingsWithDeferredNotes(t *testing.T) {
	l := w4aDeferredLedger(t)
	md := w4aPublishDigest(t, l)
	for _, k := range []string{"gh:a:unknown", "gh:a:budget", "gh:a:fresh"} {
		if w4aListed(md, k) {
			t.Errorf("%s was digested before its note was written:\n%s", k, md)
		}
	}
	if !w4aListed(md, "gh:a:ready") {
		t.Fatalf("the explained posting is missing:\n%s", md)
	}
	if !strings.Contains(md, "Notes deferred: 2 scored posting(s)") {
		t.Fatalf("the footer does not count the 2 postings held back (a scored posting not yet gated is not deferred):\n%s", md)
	}

	// The later run: the budget is back, the note is written, the posting is explained.
	for _, k := range []posting.Key{"gh:a:unknown", "gh:a:budget"} {
		r := l.recs[k]
		r.Status, r.Annotations = posting.Explained, map[string]string{"score.total": "70", pipeline.AnnNotesDeferred: ""}
	}
	md = w4aPublishDigest(t, l)
	for _, k := range []string{"gh:a:unknown", "gh:a:budget"} {
		if !w4aListed(md, k) {
			t.Errorf("%s was not digested once explained:\n%s", k, md)
		}
	}
	if strings.Contains(md, "Notes deferred") {
		t.Errorf("the footer still counts postings whose note was written:\n%s", md)
	}
}

// A day with nothing held back says nothing about it.
func TestW4aDigestFooterOmitsZeroNotesDeferred(t *testing.T) {
	md, _ := digest.Render(nil, nil, time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC), digest.Counts{})
	if strings.Contains(md, "Notes deferred") {
		t.Fatalf("digest:\n%s", md)
	}
}
