package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
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
	rep, err := advanceAll(context.Background(), liveRecs(300), nil, config.DefaultAdvancePerRun, 1, everyOther, func(_ context.Context, k posting.Key) error {
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
	_, err = advanceAll(context.Background(), liveRecs(6), nil, config.DefaultAdvancePerRun, 1, everyOther, func(_ context.Context, k posting.Key) error {
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

// With postings advanced concurrently, a title drop is still decided per
// posting and still no backend reading: the breaker trips and a phase in which
// every asked posting was unread fails, as with one worker.
func TestAdvanceTitleDropsConcurrent(t *testing.T) {
	const workers = 4
	var asked atomic.Int64
	adv := func(_ context.Context, k posting.Key) error {
		if everyOther(ledger.Record{Key: k}) {
			return nil
		}
		asked.Add(1)
		return unread(k)
	}
	rep, err := advanceAll(context.Background(), liveRecs(300), nil, config.DefaultAdvancePerRun, workers, everyOther, adv, func(string, ...any) {})
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep.Stopped != stopUnreadStreak {
		t.Fatalf("err = %v, report %+v", err, rep)
	}
	if n := asked.Load(); n < maxConsecutiveUnread || n > maxConsecutiveUnread+workers-1 || int(n) != rep.Unread {
		t.Fatalf("asked %d, report %+v", n, rep)
	}

	var warn strings.Builder
	asked.Store(0)
	rep, err = advanceAll(context.Background(), liveRecs(6), nil, config.DefaultAdvancePerRun, workers, everyOther, adv, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if !errors.Is(err, pipeline.ErrTriageUnavailable) || rep.Stopped != stopNoneRead || !strings.Contains(err.Error(), "3 of 6") {
		t.Fatalf("err = %v, report %+v", err, rep)
	}
	if !strings.Contains(warn.String(), "3 posting(s) dropped by title before triage") {
		t.Fatalf("warnings:\n%s", warn.String())
	}
}

// Title drops alone, or with working backends, are a normal run, and they
// count toward the cap (advance_per_run) as the owner decided.
func TestAdvanceTitleDropsCountTowardTheCap(t *testing.T) {
	calls := 0
	rep, err := advanceAll(context.Background(), liveRecs(10), nil, 4, 1, everyOther, func(context.Context, posting.Key) error { calls++; return nil }, func(string, ...any) {})
	if err != nil || calls != 4 || rep.Attempted != 4 || rep.Deferred != 6 {
		t.Fatalf("err %v calls %d report %+v", err, calls, rep)
	}
	all := func(ledger.Record) bool { return true }
	if _, err := advanceAll(context.Background(), liveRecs(5), nil, 10, 1, all, func(context.Context, posting.Key) error { return nil }, func(string, ...any) {}); err != nil {
		t.Fatalf("all title drops: %v", err)
	}
	// A title drop that fails to record is an ordinary failure, not a drop;
	// when every posting fails that way nothing advanced and the run fails
	// (stop none_advanced) rather than publish over a full ledger.
	rep, err = advanceAll(context.Background(), liveRecs(2), nil, 10, 1, all, func(context.Context, posting.Key) error { return errors.New("ledger full") }, func(string, ...any) {})
	if !errors.Is(err, errAdvanceFailing) || rep.Failed != 2 || rep.Stopped != stopNoneAdvanced {
		t.Fatalf("err %v report %+v", err, rep)
	}
}

// The Workday source's title prefilter is the runner's title drop, and what
// it may reuse is every stored posting under the board, whatever its status.
func TestWorkdayHooksMatchTheRunnerAndTheBoard(t *testing.T) {
	if titleDrops(pipeline.DropPolicy{}) != nil {
		t.Fatal("a policy without title reads has a title prefilter")
	}
	cfg := config.Config{
		DropRules:    []config.DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}},
		TitleSignals: config.TitleSignals{Read: []config.TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"Manager"}}}},
	}
	drops := titleDrops(dropPolicy(cfg, "d1-ultra"))
	if !drops("Engineering Manager") || drops("Software Engineer") {
		t.Fatal("title prefilter disagrees with the runner")
	}

	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, c := range []struct {
		key posting.Key
		st  posting.Status
	}{
		{"wd:nvidia:site:jr1", posting.Live},
		{"wd:nvidia:site:jr2", posting.Dead},
		{"wd:nvidia:site2:jr3", posting.Live}, // another site of the tenant
		{"gh:nvidia:x", posting.Live},
	} {
		p := posting.Posting{Key: c.key, ATS: c.key.ATS(), Company: "N", Title: "t", Description: "jd"}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: c.key, Status: c.st, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	w := &wired{led: l}
	got := w.storedFor(sources.Board{ATS: posting.Workday, Token: "NVIDIA/Site"})
	var keys []string
	for _, p := range got {
		keys = append(keys, string(p.Key))
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "wd:nvidia:site:jr1,wd:nvidia:site:jr2" {
		t.Fatalf("stored for the board: %v", keys)
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
