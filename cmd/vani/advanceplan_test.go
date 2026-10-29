package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The advance plans of the callers differ in exactly the named fields below;
// this pins each difference, so a change to one is a decision, not an accident.
// The owner decided these inbox differences are deliberate, because a
// user-submitted posting runs in arrival order and the poll is a small
// between-runs pass: (1) Limit reads advance_per_run, never the run's tuned cap;
// (2) Skip is never set, so --list-only does not hold the inbox back; (4) Order
// is the title rank only, with no semantic knowledge-base order; (5) there is no
// warm before the advance. The one difference that was an accident, the missing
// funnel report (3), is fixed and pinned below.
func TestAdvancePlanDifferencesBetweenCallers(t *testing.T) {
	cfg := config.Config{AdvancePerRun: 40, TitleSignals: config.TitleSignals{Include: []string{"Inference"}}}
	w := &wired{cfg: cfg}
	w.tune(runOptions{Advance: 7, ListOnly: true})
	policy := pipeline.DropPolicy{}

	run := w.advancePlan(kindRun, policy)
	inbox := w.advancePlan(kindInbox, policy)
	resc := baseAdvancePlan(kindRescore, 3, 2, nil)

	if run.Limit != 7 || !run.Skip || !run.FillFunnel || run.Select != nil || run.QuietCap {
		t.Errorf("run: %+v", run)
	}
	// The inbox poll reads neither the tuned cap nor --list-only (deliberate,
	// see the table in runwire.go), but it does fill the funnel report.
	if inbox.Limit != 40 || inbox.Skip || !inbox.FillFunnel || inbox.Select != nil || inbox.QuietCap {
		t.Errorf("inbox: %+v", inbox)
	}
	if run.Workers != inbox.Workers || run.Workers != cfg.AdvanceWorkers() {
		t.Errorf("workers: run %d inbox %d", run.Workers, inbox.Workers)
	}
	if resc.Limit != 3 || resc.Workers != 2 || resc.Select == nil || !resc.QuietCap || resc.FillFunnel || resc.TitleDrop != nil {
		t.Errorf("rescore: %+v", resc)
	}

	// Run and inbox rank by the title signals; rescore does not rank.
	recs := titledRecs("Software Engineer", "Inference Engineer")
	for name, p := range map[string]pipeline.AdvancePlan{"run": run, "inbox": inbox} {
		todo := slices.Clone(recs)
		p.Order(todo, recs)
		if todo[0].Posting.Title != "Inference Engineer" {
			t.Errorf("%s: order %v", name, keysOf(todo))
		}
	}
}

// "Could not check" must not vanish for inbox postings: an inbox advance that
// kept a posting because a check could not run says so in the warnings, as the
// run does (pipeline.FillFunnelReport through the plan's Warn).
func TestInboxAdvanceReportsKeptBecauseACheckCouldNotRun(t *testing.T) {
	w, warn := openWired(t, config.Default())
	fo := &pipeline.FailOpen{}
	runner := &pipeline.Runner{Funnel: pipeline.Funnel{FailOpen: fo}}
	recs := liveRecs(2)
	rep, err := pipeline.Advance(context.Background(), w.advancePlan(kindInbox, pipeline.DropPolicy{}), pipeline.AdvanceDeps{
		Records: recs, Runner: runner,
		Advance: func(_ context.Context, _ posting.Key) error {
			fo.Note(pipeline.CauseEmptyDescription)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FailOpen.Total() != 2 {
		t.Errorf("fail-open counts %+v, want 2", rep.FailOpen)
	}
	if !strings.Contains(warn.String(), "2 posting(s) kept because a check could not run") {
		t.Errorf("warnings: %q", warn.String())
	}
}

// A policy that reads titles reaches the run and the inbox poll as TitleDrop,
// and only live postings are dropped by it.
func TestAdvancePlanTitleDropComesFromThePolicy(t *testing.T) {
	cfg := config.Config{
		AdvancePerRun: 5,
		DropRules:     []config.DropRule{{Slot: "seniority", Values: []string{"manager"}, MinOpenRouterProb: 0.9}},
		TitleSignals:  config.TitleSignals{Read: []config.TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"Manager"}}}},
	}
	w := &wired{cfg: cfg}
	if p := w.advancePlan(kindRun, dropPolicy(config.Config{}, "")); p.TitleDrop != nil {
		t.Error("no title reading must give no TitleDrop")
	}
	live := ledger.Record{Status: posting.Live, Posting: posting.Posting{Title: "Engineering Manager"}}
	scored := live
	scored.Status = posting.Scored
	for _, k := range []advanceKind{kindRun, kindInbox} {
		p := w.advancePlan(k, dropPolicy(cfg, "d1-ultra"))
		if p.TitleDrop == nil || !p.TitleDrop(live) || p.TitleDrop(scored) {
			t.Errorf("kind %d: TitleDrop does not follow the policy", k)
		}
	}
}
