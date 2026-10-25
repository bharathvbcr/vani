package pipeline

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The backfill stage runs once, after the advance phase and before the run is
// stopped, and only when the phase ended without an error.
func TestRunBackfillStageRunsAfterAdvance(t *testing.T) {
	r := &recorder{plan: AdvancePlan{Limit: 5, Workers: 1}}
	st := r.stages()
	st.Backfill = func(context.Context) { r.log = append(r.log, "backfill") }
	if err := Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "drain", "boards", "neardups", "logos", "plan", "warm", "deps", "advance gh:a:1", "advance gh:a:2", "advanced", "backfill", "stop"}
	if !slices.Equal(r.log, want) {
		t.Fatalf("order\n got %v\nwant %v", r.log, want)
	}
}

func TestRunBackfillStageIsSkippedWhenTheRunDidNotGoWell(t *testing.T) {
	ran := func(r *recorder) bool { return slices.Contains(r.log, "backfill") }
	withBackfill := func(r *recorder) RunStages {
		st := r.stages()
		st.Backfill = func(context.Context) { r.log = append(r.log, "backfill") }
		return st
	}

	// An advance phase that failed (every posting failed a stage).
	r := &recorder{plan: AdvancePlan{Limit: 5, Workers: 1}, advance: func(context.Context, posting.Key) error { return errors.New("boom") }}
	if err := Run(context.Background(), withBackfill(r)); err == nil || ran(r) {
		t.Fatalf("a failed advance still backfilled: err=%v log=%v", err, r.log)
	}
	// --list-only: the advance phase is not started, and neither is the backfill.
	r = &recorder{plan: AdvancePlan{Limit: 5, Workers: 1, Skip: true}}
	if err := Run(context.Background(), withBackfill(r)); err != nil || ran(r) {
		t.Fatalf("a list-only run backfilled: err=%v log=%v", err, r.log)
	}
	// No board could be read: the run ended before the advance phase.
	r = &recorder{counts: BoardCounts{Configured: 1}, plan: AdvancePlan{Limit: 5, Workers: 1}}
	if err := Run(context.Background(), withBackfill(r)); !errors.Is(err, ErrNoBoardRead) || ran(r) {
		t.Fatalf("err=%v log=%v", err, r.log)
	}
	// Cancelled during the advance phase.
	ctx, cancel := context.WithCancel(context.Background())
	r = &recorder{plan: AdvancePlan{Limit: 5, Workers: 1}, advance: func(context.Context, posting.Key) error { cancel(); return nil }}
	_ = Run(ctx, withBackfill(r))
	if ran(r) {
		t.Fatalf("a cancelled run backfilled: %v", r.log)
	}
	// A run with no backfill stage is as it was.
	r = &recorder{plan: AdvancePlan{Limit: 5, Workers: 1}}
	if err := Run(context.Background(), r.stages()); err != nil || ran(r) {
		t.Fatal(err, r.log)
	}
}
