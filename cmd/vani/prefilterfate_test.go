package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// An excluded posting the exclusions no longer match has one of three fates at
// the next run, and `vani prefilter` names each: it stays dropped (its board is
// no longer watched), it waits for its board's listing, or it comes back at
// once (no full listing to wait for).
func TestPrefilterNamesEachReopenFate(t *testing.T) {
	weak, err := posting.FallbackKey("Acme", "Platform Engineer", "Remote")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(k posting.Key) ledger.Record {
		return ledger.Record{Key: k, Status: posting.Dropped,
			Posting:     posting.Posting{Key: k, Company: "Acme", Title: "Platform Engineer"},
			Annotations: map[string]string{pipeline.AnnExcluded: "excluded: title matches \"platform\""}}
	}
	recs := []ledger.Record{mk("gh:watched:1"), mk("gh:gone:1"), mk(weak)}
	sc := pipeline.ReopenScope{Watched: pipeline.WatchedBy([]sources.Board{{ATS: posting.Greenhouse, Token: "watched", Company: "Acme"}})}
	sc.Defer = sc.Watched
	rep := prefilterOf(recs, pipeline.DropPolicy{OpenRouter: "d1-ultra"}, nil, sc)
	if rep.ReopenWaits != 1 || rep.ReopenStays != 1 || rep.ReopenNow != 1 || len(rep.Reopen) != 3 {
		t.Fatalf("waits %d stays %d now %d of %d", rep.ReopenWaits, rep.ReopenStays, rep.ReopenNow, len(rep.Reopen))
	}
	want := map[posting.Key]string{"gh:watched:1": fateWaits, "gh:gone:1": fateStays, weak: fateNow}
	for _, r := range rep.Reopen {
		if r.Fate != want[r.Key] {
			t.Errorf("%s: fate %q, want %q", r.Key, r.Fate, want[r.Key])
		}
	}
	var b strings.Builder
	writePrefilter(&b, rep, len(recs), true)
	for _, w := range []string{
		"3 excluded posting(s) no longer match: 1 would wait, still dropped, until their board lists them again, 1 would come back to live at once",
		"1 would stay dropped (their board is no longer watched)",
		"gh:gone:1\tPlatform Engineer\tstays dropped: its board is no longer watched",
		"gh:watched:1\tPlatform Engineer\twaits, dropped, until its board lists it",
	} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, b.String())
		}
	}
	if strings.Contains(b.String(), "let back") {
		t.Errorf("the report still says let back:\n%s", b.String())
	}
}
