package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// recorder builds RunStages that log their own name, so a test reads the order.
type recorder struct {
	log      []string
	counts   BoardCounts
	boardErr error
	nearErr  error
	plan     AdvancePlan
	advance  func(context.Context, posting.Key) error
	reports  []AdvanceReport
}

func (r *recorder) stages() RunStages {
	mark := func(s string) { r.log = append(r.log, s) }
	return RunStages{
		Start: func(context.Context) (func(), error) { mark("start"); return func() { mark("stop") }, nil },
		Drain: func(context.Context) { mark("drain") },
		Boards: func(context.Context) (BoardCounts, error) {
			mark("boards")
			return r.counts, r.boardErr
		},
		NearDups: func(context.Context) error { mark("neardups"); return r.nearErr },
		Logos:    func(context.Context) { mark("logos") },
		Plan:     func() AdvancePlan { mark("plan"); return r.plan },
		Warm:     func(context.Context) { mark("warm") },
		Deps: func() AdvanceDeps {
			mark("deps")
			recs := []ledger.Record{{Key: "gh:a:1", Status: posting.Live}, {Key: "gh:a:2", Status: posting.Live}}
			return AdvanceDeps{Records: recs, Advance: func(ctx context.Context, k posting.Key) error {
				mark("advance " + string(k))
				if r.advance != nil {
					return r.advance(ctx, k)
				}
				return nil
			}}
		},
		Advanced: func(rep AdvanceReport) { mark("advanced"); r.reports = append(r.reports, rep) },
	}
}

func TestRunStageOrder(t *testing.T) {
	r := &recorder{counts: BoardCounts{Configured: 1, Read: 1}, plan: AdvancePlan{Limit: 5, Workers: 1}}
	if err := Run(context.Background(), r.stages()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "drain", "boards", "neardups", "logos", "plan", "warm", "deps", "advance gh:a:1", "advance gh:a:2", "advanced", "stop"}
	if !slices.Equal(r.log, want) {
		t.Fatalf("order\n got %v\nwant %v", r.log, want)
	}
	if len(r.reports) != 1 || r.reports[0].Attempted != 2 {
		t.Fatalf("reports %+v", r.reports)
	}
}

// --list-only ends the run after the listing: nothing is warmed, advanced or
// reported, and the stop still runs.
func TestRunSkipEndsAfterTheListing(t *testing.T) {
	var warned []string
	r := &recorder{plan: AdvancePlan{Limit: 5, Workers: 1, Skip: true, Warn: func(f string, a ...any) { warned = append(warned, f) }}}
	if err := Run(context.Background(), r.stages()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "drain", "boards", "neardups", "logos", "plan", "stop"}
	if !slices.Equal(r.log, want) || len(r.reports) != 0 {
		t.Fatalf("order %v, reports %v", r.log, r.reports)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "advance phase skipped") {
		t.Fatalf("warnings %v", warned)
	}
}

// Boards configured and none read fails the run before near-duplicates and
// advance; no boards configured is not that failure.
func TestRunNoBoardRead(t *testing.T) {
	r := &recorder{counts: BoardCounts{Configured: 2}, plan: AdvancePlan{Limit: 5, Workers: 1}}
	err := Run(context.Background(), r.stages())
	if !errors.Is(err, ErrNoBoardRead) || err.Error() != "no board could be read" {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"start", "drain", "boards", "stop"}; !slices.Equal(r.log, want) {
		t.Fatalf("order %v, want %v", r.log, want)
	}
	r = &recorder{plan: AdvancePlan{Limit: 5, Workers: 1}}
	if err := Run(context.Background(), r.stages()); err != nil {
		t.Fatalf("zero configured boards: %v", err)
	}
}

func TestRunStageErrorsStopTheRun(t *testing.T) {
	boom := errors.New("boom")
	r := &recorder{boardErr: boom, plan: AdvancePlan{Limit: 5, Workers: 1}}
	if err := Run(context.Background(), r.stages()); !errors.Is(err, boom) || slices.Contains(r.log, "neardups") {
		t.Fatalf("board error: %v %v", err, r.log)
	}
	r = &recorder{nearErr: boom, plan: AdvancePlan{Limit: 5, Workers: 1}}
	if err := Run(context.Background(), r.stages()); !errors.Is(err, boom) || slices.Contains(r.log, "plan") || r.log[len(r.log)-1] != "stop" {
		t.Fatalf("near-dup error: %v %v", err, r.log)
	}
}

// A cancelled run stops before the advance plan is read.
func TestRunCancelledBeforeAdvance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &recorder{plan: AdvancePlan{Limit: 5, Workers: 1}}
	st := r.stages()
	st.Logos = func(context.Context) { cancel() }
	if err := Run(ctx, st); !errors.Is(err, context.Canceled) || slices.Contains(r.log, "plan") {
		t.Fatalf("%v %v", err, r.log)
	}
}

// A refused advance phase still hands its (empty) report to the run and fails it.
func TestRunAdvanceRefusalReachesAdvanced(t *testing.T) {
	r := &recorder{plan: AdvancePlan{Limit: 0, Workers: 1}}
	err := Run(context.Background(), r.stages())
	if err == nil || !strings.Contains(err.Error(), "advance limit 0 must be above 0") || len(r.reports) != 1 {
		t.Fatalf("%v %v", err, r.reports)
	}
}

func TestRunRequiresItsStages(t *testing.T) {
	for name, drop := range map[string]func(*RunStages){
		"start":    func(s *RunStages) { s.Start = nil },
		"boards":   func(s *RunStages) { s.Boards = nil },
		"neardups": func(s *RunStages) { s.NearDups = nil },
		"plan":     func(s *RunStages) { s.Plan = nil },
		"deps":     func(s *RunStages) { s.Deps = nil },
	} {
		r := &recorder{}
		st := r.stages()
		drop(&st)
		if err := Run(context.Background(), st); !errors.Is(err, ErrStageMissing) || len(r.log) != 0 {
			t.Errorf("%s: err %v, ran %v", name, err, r.log)
		}
	}
	// Optional stages may be absent.
	r := &recorder{plan: AdvancePlan{Limit: 1, Workers: 1}}
	st := r.stages()
	st.Drain, st.Logos, st.Warm, st.Advanced = nil, nil, nil, nil
	if err := Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
}

// A failing Start never reaches the other stages, and no stop is owed.
func TestRunStartFailure(t *testing.T) {
	boom := errors.New("no backend")
	r := &recorder{}
	st := r.stages()
	st.Start = func(context.Context) (func(), error) { return nil, boom }
	if err := Run(context.Background(), st); !errors.Is(err, boom) || len(r.log) != 0 {
		t.Fatalf("%v %v", err, r.log)
	}
}

func TestTitleDropperOnlyDropsLivePostings(t *testing.T) {
	if (DropPolicy{}).TitleDrops() != nil || (DropPolicy{}).TitleDropper() != nil {
		t.Fatal("a policy that reads no titles must give nil")
	}
}
