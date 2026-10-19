package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// everyOther marks every second record as dropped by its title.
func everyOther(r ledger.Record) bool {
	var i int
	fmt.Sscanf(string(r.Key), "gh:acme:%d", &i)
	return i%2 == 1
}

// During an outage, title drops between unread postings are not backend
// readings: the unread breaker still trips, after the same number of unread
// postings, and the title drops still count toward the attempts.
func TestAdvanceTitleDropsDoNotHideAnOutage(t *testing.T) {
	asked := 0
	rep, err := advanceAll(context.Background(), liveRecs(300), nil, config.DefaultAdvancePerRun, everyOther, func(_ context.Context, k posting.Key) error {
		if everyOther(ledger.Record{Key: k}) {
			return nil // dropped by title, no backend asked
		}
		asked++
		return unread(k)
	}, func(string, ...any) {})
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || !strings.Contains(err.Error(), fmt.Sprintf("%d postings in a row", maxConsecutiveUnread)) {
		t.Fatalf("err = %v", err)
	}
	if asked != maxConsecutiveUnread || rep.Attempted != 2*maxConsecutiveUnread-1 {
		t.Fatalf("asked %d, attempted %d", asked, rep.Attempted)
	}

	// Below the breaker: every posting a backend was asked about was unread,
	// so the phase fails, whatever the title drops.
	var warn strings.Builder
	_, err = advanceAll(context.Background(), liveRecs(6), nil, config.DefaultAdvancePerRun, everyOther, func(_ context.Context, k posting.Key) error {
		if everyOther(ledger.Record{Key: k}) {
			return nil
		}
		return unread(k)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || !strings.Contains(err.Error(), "3 of 6") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(warn.String(), "3 posting(s) dropped by title before triage") {
		t.Fatalf("warnings:\n%s", warn.String())
	}
}

// Title drops alone, or with working backends, are a normal run, and they
// count toward the cap (advance_per_run) as the owner decided.
func TestAdvanceTitleDropsCountTowardTheCap(t *testing.T) {
	calls := 0
	rep, err := advanceAll(context.Background(), liveRecs(10), nil, 4, everyOther, func(context.Context, posting.Key) error { calls++; return nil }, func(string, ...any) {})
	if err != nil || calls != 4 || rep.Attempted != 4 || rep.Deferred != 6 {
		t.Fatalf("err %v calls %d report %+v", err, calls, rep)
	}
	all := func(ledger.Record) bool { return true }
	if _, err := advanceAll(context.Background(), liveRecs(5), nil, 10, all, func(context.Context, posting.Key) error { return nil }, func(string, ...any) {}); err != nil {
		t.Fatalf("all title drops: %v", err)
	}
	// A title drop that fails to record is an ordinary failure, not a drop;
	// when every posting fails that way nothing advanced and the run fails
	// (stop none_advanced) rather than publish over a full ledger.
	rep, err = advanceAll(context.Background(), liveRecs(2), nil, 10, all, func(context.Context, posting.Key) error { return errors.New("ledger full") }, func(string, ...any) {})
	if !errors.Is(err, errAdvanceFailing) || rep.Failed != 2 || rep.Stopped != stopNoneAdvanced {
		t.Fatalf("err %v report %+v", err, rep)
	}
}

func TestTitleDropperMatchesTheRunner(t *testing.T) {
	if titleDropper(pipeline.DropPolicy{}) != nil {
		t.Fatal("a policy without title reads has a dropper")
	}
	cfg := config.Config{
		DropRules:    []config.DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}},
		TitleSignals: config.TitleSignals{Read: []config.TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"Manager"}}}},
	}
	d := titleDropper(dropPolicy(cfg, "d1-ultra"))
	live := ledger.Record{Status: posting.Live, Posting: posting.Posting{Title: "Engineering Manager"}}
	if !d(live) {
		t.Fatal("live manager title not a title drop")
	}
	// Only Live postings are triaged; a later stage never drops by title.
	scored := live
	scored.Status = posting.Scored
	if d(scored) || d(ledger.Record{Status: posting.Live, Posting: posting.Posting{Title: "Software Engineer"}}) {
		t.Fatal("dropper reports a posting the runner would not drop by title")
	}
}
