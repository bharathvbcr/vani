package main

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The advance plans of the callers differ in exactly the named fields below;
// this pins each difference (some are open questions for the owner, see the
// refactor report), so a change to one is a decision, not an accident.
func TestAdvancePlanDifferencesBetweenCallers(t *testing.T) {
	cfg := config.Config{AdvancePerRun: 40, TitleSignals: config.TitleSignals{Include: []string{"Inference"}}}
	w := &wired{cfg: cfg}
	w.tune(7, true)
	policy := pipeline.DropPolicy{}

	run := w.advancePlan(kindRun, policy)
	inbox := w.advancePlan(kindInbox, policy)
	resc := baseAdvancePlan(kindRescore, 3, 2, nil)

	if run.Limit != 7 || !run.Skip || !run.FillFunnel || run.Select != nil || run.QuietCap {
		t.Errorf("run: %+v", run)
	}
	// The inbox poll reads neither the tuned cap nor --list-only, and keeps no
	// funnel report.
	if inbox.Limit != 40 || inbox.Skip || inbox.FillFunnel || inbox.Select != nil || inbox.QuietCap {
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
