package main

import (
	"context"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The advance engine lives in internal/pipeline (Advance, AdvancePlan). The
// tests of its behaviour were written against the positional advanceAll and
// the cmd-local names; this file keeps them readable by adapting those names
// onto the pipeline's. It is test-only: no production code goes through it.

type advanceReport = pipeline.AdvanceReport

const (
	stopCap          = pipeline.StopCap
	stopUnreadStreak = pipeline.StopUnreadStreak
	stopNoneRead     = pipeline.StopNoneRead
	stopFailStreak   = pipeline.StopFailStreak
	stopNoneAdvanced = pipeline.StopNoneAdvanced
	stopCancelled    = pipeline.StopCancelled

	maxConsecutiveUnread = pipeline.MaxConsecutiveUnread
	maxConsecutiveFailed = pipeline.MaxConsecutiveFailed
	maxUnreadExamples    = pipeline.MaxUnreadExamples
	maxDistinctCauses    = pipeline.MaxDistinctCauses
)

var errAdvanceFailing = pipeline.ErrAdvanceFailing

func errorKey(msg string) string { return pipeline.ErrorKey(msg) }

func fillFunnelReport(rep *advanceReport, r *pipeline.Runner, warnf func(string, ...any)) {
	pipeline.FillFunnelReport(rep, r, warnf)
}

func prefilterDropper(p pipeline.DropPolicy) func(ledger.Record) bool { return p.PrefilterDropper() }

func titleDrops(p pipeline.DropPolicy) func(string) bool { return p.TitleDrops() }

// testHookCounted is AdvancePlan.OnCounted for advanceAll.
var testHookCounted func(key posting.Key, stopped bool)

// advanceAll runs pipeline.Advance with the plan its positional arguments and
// options describe.
func advanceAll(ctx context.Context, recs []ledger.Record, rank func(posting.Posting) int, limit, workers int, titleDrop func(ledger.Record) bool, advance func(context.Context, posting.Key) error, warnf func(string, ...any), ordering ...orderOption) (advanceReport, error) {
	plan := pipeline.AdvancePlan{
		Limit: limit, Workers: workers, TitleDrop: titleDrop, Warn: warnf, OnCounted: testHookCounted,
		Order: func(todo, all []ledger.Record) { orderAdvance(todo, all, rank, ordering...) },
	}
	return pipeline.Advance(ctx, plan, pipeline.AdvanceDeps{Records: recs, Advance: advance})
}
