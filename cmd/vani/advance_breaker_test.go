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

// A stage that fails every posting (DevPrism down, the explainer refused) is
// an outage: the run stops after maxConsecutiveFailed in a row and fails, so
// no digest is published over it. It used to return nil and publish.
func TestAdvanceStopsOnAStageFailureStreak(t *testing.T) {
	var warn strings.Builder
	calls := 0
	rep, err := advanceAll(context.Background(), liveRecs(300), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		calls++
		return fmt.Errorf("pipeline: score %s: devprism down", k)
	}, func(f string, a ...any) { fmt.Fprintf(&warn, f+"\n", a...) })
	if !errors.Is(err, errAdvanceFailing) || !strings.Contains(err.Error(), fmt.Sprintf("%d postings in a row failed a stage", maxConsecutiveFailed)) {
		t.Fatalf("err = %v", err)
	}
	if calls != maxConsecutiveFailed || rep.Stopped != stopFailStreak || rep.Failed != maxConsecutiveFailed || rep.Deferred != 300-maxConsecutiveFailed {
		t.Fatalf("calls %d, report %+v", calls, rep)
	}
	// The collapsed count is still printed before the run stops.
	if !strings.Contains(warn.String(), fmt.Sprintf("%d more posting(s) failed the same way as gh:acme:000", maxConsecutiveFailed-1)) {
		t.Fatalf("no summary count:\n%s", warn.String())
	}
}

// A success or an unread posting breaks a failure streak.
func TestAdvanceFailureStreakResets(t *testing.T) {
	for name, every := range map[string]error{"success": nil, "unread": unread("x")} {
		i := 0
		rep, err := advanceAll(context.Background(), liveRecs(100), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
			i++
			if i%maxConsecutiveFailed == 0 {
				return every
			}
			return errors.New("pipeline: score: devprism down")
		}, func(string, ...any) {})
		if name == "success" && (err != nil || rep.Stopped != "") {
			t.Errorf("%s: err %v, report %+v", name, err, rep)
		}
		// Only unread and failed postings: nothing advanced, so it still fails,
		// but only after working through all of them.
		if name == "unread" && (!errors.Is(err, errAdvanceFailing) || rep.Stopped != stopNoneAdvanced || rep.Attempted != 100) {
			t.Errorf("%s: err %v, report %+v", name, err, rep)
		}
	}
}

// Postings dropped by title asked no stage, so they do not hide a stage that
// failed every posting it was asked about (title_signals.read).
func TestAdvanceTitleDropsDoNotHideAFailingStage(t *testing.T) {
	recs := liveRecs(10)
	byTitle := func(r ledger.Record) bool { return r.Key < recs[5].Key }
	rep, err := advanceAll(context.Background(), recs, nil, config.DefaultAdvancePerRun, byTitle, func(_ context.Context, k posting.Key) error {
		if k < recs[5].Key {
			return nil // dropped by title, no stage asked
		}
		return fmt.Errorf("pipeline: score %s: devprism down", k)
	}, func(string, ...any) {})
	if !errors.Is(err, errAdvanceFailing) || rep.Stopped != stopNoneAdvanced || rep.Failed != 5 || rep.Attempted != 10 {
		t.Fatalf("err %v, report %+v", err, rep)
	}
	// Title drops alone are a working run.
	rep, err = advanceAll(context.Background(), recs[:5], nil, config.DefaultAdvancePerRun, byTitle, func(context.Context, posting.Key) error { return nil }, func(string, ...any) {})
	if err != nil || rep.Stopped != "" {
		t.Fatalf("title drops only: err %v, report %+v", err, rep)
	}
}

// Fewer postings than either streak, all failed or unread: still a failure,
// and not reported as a triage outage.
func TestAdvanceFailsWhenNothingAdvanced(t *testing.T) {
	i := 0
	rep, err := advanceAll(context.Background(), liveRecs(6), nil, config.DefaultAdvancePerRun, nil, func(_ context.Context, k posting.Key) error {
		i++
		if i%2 == 0 {
			return unread(k)
		}
		return fmt.Errorf("pipeline: explain %s: refused", k)
	}, func(string, ...any) {})
	if !errors.Is(err, errAdvanceFailing) || errors.Is(err, pipeline.ErrTriageUnavailable) || !strings.Contains(err.Error(), "3 failed a stage and 3 were unread, of 6") {
		t.Fatalf("err = %v", err)
	}
	if rep.Stopped != stopNoneAdvanced || rep.Failed != 3 || rep.Unread != 3 {
		t.Fatalf("report %+v", rep)
	}
}
