package main

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// The repair marks the 0-of-0 records unknown, leaves a measured coverage and a
// genuine zero (missing listed) alone, calls DevPrism never, and a second pass
// writes nothing.
func TestRepairUnknownCoverageCommandPass(t *testing.T) {
	sc := &rescoreScorer{}
	l := rescoreLedger(t, []rescoreSeed{
		{"1", posting.Explained, "jd", "0"},  // 0 of 0
		{"2", posting.Scored, "jd", "0"},     // 0 of 0
		{"3", posting.Explained, "jd", "0"},  // genuine zero, missing below
		{"4", posting.Explained, "jd", "70"}, // measured
	})
	k3, _ := posting.GreenhouseKey("acme", "3")
	r := rescoreRunner(l, sc)
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: r.Now(), Key: k3, Status: posting.Explained,
		Annotations: map[string]string{ledger.AnnMissing: "cuda"}}); err != nil {
		t.Fatal(err)
	}
	if p := planRescore(l.All()); p.ZeroOfZero != 2 {
		t.Fatalf("plan %+v", p)
	}
	n, err := repairUnknownCoverage(context.Background(), l, r.RepairUnknownCoverage)
	if err != nil || n != 2 {
		t.Fatalf("repaired %d err %v", n, err)
	}
	recs := recordsByKey(t, l)
	for _, id := range []string{"1", "2"} {
		k, _ := posting.GreenhouseKey("acme", id)
		if recs[k].Annotations[pipeline.AnnCoverageUnknown] != pipeline.CoverageNoMustHaves || recs[k].Annotations[ledger.AnnCoverage] != "" {
			t.Fatalf("%s not marked: %v", id, recs[k].Annotations)
		}
	}
	if recs[k3].Annotations[ledger.AnnCoverage] != "0" || recs[k3].Annotations[pipeline.AnnCoverageUnknown] != "" {
		t.Fatalf("genuine zero touched: %v", recs[k3].Annotations)
	}
	k4, _ := posting.GreenhouseKey("acme", "4")
	if recs[k4].Annotations[ledger.AnnCoverage] != "70" {
		t.Fatalf("measured coverage touched: %v", recs[k4].Annotations)
	}
	if n, err := repairUnknownCoverage(context.Background(), l, r.RepairUnknownCoverage); err != nil || n != 0 {
		t.Fatalf("second pass repaired %d err %v", n, err)
	}
	if sc.calls.Load() != 0 {
		t.Fatal("repair called DevPrism")
	}
	var _ resume.Scorer = sc
}
