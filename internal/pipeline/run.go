package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// TitleDrops reports whether the policy drops a posting by its title alone, or
// is nil when the policy reads no titles.
func (p DropPolicy) TitleDrops() func(string) bool {
	if p.ReadTitle == nil {
		return nil
	}
	return func(title string) bool {
		_, _, dropped := p.EvaluateTitle(title)
		return dropped
	}
}

// TitleDropper reports whether the runner would drop a record by its title
// alone (a live posting the policy's title reading drops), or is nil when the
// policy reads no titles. It is AdvancePlan.TitleDrop.
func (p DropPolicy) TitleDropper() func(ledger.Record) bool {
	drops := p.TitleDrops()
	if drops == nil {
		return nil
	}
	return func(r ledger.Record) bool {
		return r.Status == posting.Live && drops(r.Posting.Title)
	}
}

// BoardCounts is what the listing stage read: how many boards were configured
// and how many of them could be read.
type BoardCounts struct {
	Configured, Read int
}

// RunStages are the stages of one daily run. Run owns their order and the
// rules between them; the caller owns what each stage does (it wires the
// concrete egress client, sources, ledger and decision backends) and keeps
// whatever the stages hand to each other.
type RunStages struct {
	// Start assembles what the run needs to triage and returns what ends it.
	// It comes first: a run that cannot triage must not fetch boards for
	// nothing. Required.
	Start func(ctx context.Context) (stop func(), err error)
	// Drain folds what the browser extension spooled into the ledger Live
	// before the boards are read; the advance phase picks them up with the
	// rest. It reports its own trouble as warnings. Optional.
	Drain func(ctx context.Context)
	// Boards lists and reconciles every configured board and records what each
	// board did for the run record. A single bad board is a warning inside it,
	// and its postings are left untouched (an unread board must never count as
	// "every posting disappeared"). Required.
	Boards func(ctx context.Context) (BoardCounts, error)
	// NearDups runs the near-duplicate stage over the ledger. Required.
	NearDups func(ctx context.Context) error
	// Logos refreshes company icons for the Jobs list: bounded, warn-only.
	// Optional.
	Logos func(ctx context.Context)
	// Plan is the advance plan; Skip ends the run after the listing. Required.
	Plan func() AdvancePlan
	// Warm runs before the advance phase and never fails the run. Optional.
	Warm func(ctx context.Context)
	// Deps is what the advance phase runs against, read after Warm. Required.
	Deps func() AdvanceDeps
	// Advanced receives the advance report however the phase ended (a refused
	// or failed phase leaves a report too). Optional.
	Advanced func(AdvanceReport)
}

// ErrNoBoardRead means boards were configured and none could be read.
var ErrNoBoardRead = errors.New("no board could be read")

// Run is one daily pass: start, drain the spool, list and reconcile the boards,
// annotate near-duplicates, refresh logos, then advance postings. It fails as a
// whole only when no board could be read, the ledger fails or a stage that
// must run fails. The same Advance serves every other caller of the phase.
func Run(ctx context.Context, st RunStages) error {
	if st.Start == nil || st.Boards == nil || st.NearDups == nil || st.Plan == nil || st.Deps == nil {
		return fmt.Errorf("run: %w: a required stage is missing", ErrStageMissing)
	}
	stop, err := st.Start(ctx)
	if err != nil {
		return err
	}
	if stop != nil {
		defer stop()
	}
	if st.Drain != nil {
		st.Drain(ctx)
	}
	counts, err := st.Boards(ctx)
	if err != nil {
		return err
	}
	if counts.Configured > 0 && counts.Read == 0 {
		return ErrNoBoardRead
	}
	if err := st.NearDups(ctx); err != nil {
		return err
	}
	if st.Logos != nil {
		st.Logos(ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	plan := st.Plan()
	if plan.Skip {
		plan.warnf("advance phase skipped (--list-only): new postings stay live for the next run")
		return nil
	}
	if st.Warm != nil {
		st.Warm(ctx)
	}
	rep, err := Advance(ctx, plan, st.Deps())
	if st.Advanced != nil {
		st.Advanced(rep)
	}
	return err
}
