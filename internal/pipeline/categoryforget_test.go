package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// reopenEvent forgets the category an earlier triage derived, with the rest of
// the triage: jobview prefers `category` over triage.category, so a stale one
// would outlive the reading it came from.
func TestReopenForgetsTheCategory(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "d")
	ann := map[string]string{ledger.AnnCategory: "backend", "triage.category": "backend", "triage.category.reading": "unavailable", ledger.AnnLCA: "badge"}
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Unsure, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	if keys, err := RetriageUnread(ctx, l, t0.Add(time.Hour), false); err != nil || len(keys) != 1 {
		t.Fatalf("retriage = %v, %v", keys, err)
	}
	got := rec(t, l, p.Key)
	if v, ok := got.Annotations[ledger.AnnCategory]; ok {
		t.Errorf("category %q survived the reopen", v)
	}
	if got.Annotations[ledger.AnnLCA] != "badge" {
		t.Error("an unrelated annotation was forgotten")
	}
}

// A record with no annotations at all reopens cleanly: nothing to forget.
func TestReopenEventWithNoAnnotations(t *testing.T) {
	ev := reopenEvent(ledger.Record{Key: "gh:a:1"}, t0, "r")
	if ev.Status != posting.Live {
		t.Fatalf("status %s", ev.Status)
	}
	if v, ok := ev.Annotations[ledger.AnnCategory]; !ok || v != "" {
		t.Errorf("category not in the forget set: %v", ev.Annotations)
	}
}

// A Live record still carrying a category (a reopen by an older build) and
// re-triaged by an answer with no category ends with none; an answer that does
// set one replaces it.
func TestLiveTriageClearsAStaleCategory(t *testing.T) {
	noCat := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	r, l, k := runnerWith(t, noCat, nil)
	r.SkipScoring = true
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0.Add(time.Second), Key: k, Status: posting.Live,
		Annotations: map[string]string{ledger.AnnCategory: "stale"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if v, ok := rec(t, l, k).Annotations[ledger.AnnCategory]; ok {
		t.Errorf("stale category %q survived a triage that set none", v)
	}

	withCat := decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"category": jev("data", 0.99)}}
	r2, l2, k2 := runnerWith(t, withCat, nil)
	r2.SkipScoring = true
	if err := l2.Append(ctx, ledger.Event{V: 1, At: t0.Add(time.Second), Key: k2, Status: posting.Live,
		Annotations: map[string]string{ledger.AnnCategory: "stale"}}); err != nil {
		t.Fatal(err)
	}
	if err := r2.Advance(ctx, k2); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l2, k2).Annotations[ledger.AnnCategory]; got != "data" {
		t.Errorf("category = %q, want data", got)
	}
}
