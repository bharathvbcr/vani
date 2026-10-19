package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestTitleDropped(t *testing.T) {
	title := map[string]string{"triage.backend": "title"}
	for name, c := range map[string]struct {
		r    ledger.Record
		want bool
	}{
		"title drop":           {ledger.Record{Status: posting.Dropped, Annotations: title}, true},
		"model drop":           {ledger.Record{Status: posting.Dropped, Annotations: map[string]string{"triage.backend": "d1-ultra"}}, false},
		"title-read but live":  {ledger.Record{Status: posting.Live, Annotations: title}, false},
		"no annotations":       {ledger.Record{Status: posting.Dropped}, false},
		"merged model backend": {ledger.Record{Status: posting.Dropped, Annotations: map[string]string{"triage.backend": "d1-ultra+title"}}, false},
	} {
		if got := TitleDropped(c.r); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A posting dropped on its title comes back once the rules no longer drop
// it, with the title reading forgotten and its badge kept, and the next
// advance asks the backends. While the rules still drop it, it stays dropped.
func TestRetriageTitleDropsRoundTrip(t *testing.T) {
	r, ft, l, k := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("setup: %v %s", err, rec(t, l, k).Status)
	}

	// Same rules: nothing to return.
	keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), r.Policy, false)
	if err != nil || len(keys) != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("unchanged rules returned %v (%v)", keys, err)
	}

	// The owner stops dropping staff_plus: a dry run reports it and writes nothing.
	kept := r.Policy
	kept.Rules = []DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}}
	keys, err = RetriageTitleDrops(ctx, l, t0.Add(time.Hour), kept, true)
	if err != nil || len(keys) != 1 || keys[0] != k || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("dry run: %v (%v), status %s", keys, err, rec(t, l, k).Status)
	}
	keys, err = RetriageTitleDrops(ctx, l, t0.Add(time.Hour), kept, false)
	if err != nil || len(keys) != 1 {
		t.Fatalf("retriage: %v (%v)", keys, err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Live || got.Annotations["triage.backend"] != "" || got.Annotations["triage.seniority"] != "" ||
		got.Annotations[ledger.AnnLCA] == "" {
		t.Fatalf("after retriage: %s %v", got.Status, got.Annotations)
	}
	r.Policy = kept
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 1 || rec(t, l, k).Annotations["triage.backend"] != "d1-ultra" {
		t.Fatalf("re-advance: %d calls, %v", ft.calls, rec(t, l, k).Annotations)
	}
}

// Turning title readings off returns every title drop; a posting the model
// dropped is never returned, whatever the rules.
func TestRetriageTitleDropsOffAndModelDrops(t *testing.T) {
	r, _, l, k := titleRunner(t, "Engineering Manager", titleRules)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	p2 := ghPost(t, "2", "jd")
	p2.Title = "Software Engineer"
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p2}, t0); err != nil {
		t.Fatal(err)
	}
	r.Triager = &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"seniority": jev("manager", 0.99)}}}
	if err := r.Advance(ctx, p2.Key); err != nil || rec(t, l, p2.Key).Status != posting.Dropped {
		t.Fatalf("model drop setup: %v %s", err, rec(t, l, p2.Key).Status)
	}

	keys, err := RetriageTitleDrops(ctx, l, t0.Add(time.Hour), DropPolicy{Rules: titleRules}, false)
	if err != nil || len(keys) != 1 || keys[0] != k {
		t.Fatalf("returned %v (%v), want only the title drop", keys, err)
	}
	if rec(t, l, p2.Key).Status != posting.Dropped {
		t.Fatal("a model drop was returned")
	}
	// The unread retriage never touches a title drop either: it carries a reading.
	r2, _, l2, k2 := titleRunner(t, "Staff Engineer", titleRules)
	if err := r2.Advance(ctx, k2); err != nil {
		t.Fatal(err)
	}
	if keys, err := RetriageUnread(ctx, l2, t0.Add(time.Hour), false); err != nil || len(keys) != 0 {
		t.Fatalf("unread retriage returned %v (%v)", keys, err)
	}
}

func TestRetriageTitleDropsStopsOnCancel(t *testing.T) {
	r, _, l, k := titleRunner(t, "Staff Engineer", titleRules)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RetriageTitleDrops(c, l, t0, DropPolicy{}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("cancelled retriage wrote")
	}
}
