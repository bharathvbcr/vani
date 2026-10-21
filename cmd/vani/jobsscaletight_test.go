package main

import (
	"runtime"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// allocsOf is the heap allocations f makes.
func allocsOf(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.Mallocs - before.Mallocs
}

// The 5x bound in TestJobsInsightScalesTo5000Postings is looser than the
// pass's own spread (4.000x +/- 0.0001x across GOMAXPROCS=1/8 and -race), so
// a quadratic term costing one allocation per 50 other postings per row (4.4x
// at 5,000) slips past it. This pins the linear pass to its measured ratio.
func TestJobsInsightScaleIsTightlyLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("scale check")
	}
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	insightPass(t, v, scaleSnapshot(50))
	_, small := insightPass(t, v, scaleSnapshot(1250))
	_, large := insightPass(t, v, scaleSnapshot(5000))
	if ratio := float64(large) / float64(small); ratio > 4.1 {
		t.Errorf("4x the postings made %.3fx the allocations (%d -> %d), want <= 4.1x", ratio, small, large)
	}
}

// A row's own pass (learn with a company key) tokenises only that company: with
// the company held at 30 postings, growing the ledger 4x adds only the one scan
// allocation per other record, not another extraction (~1,400 allocations) each.
func TestScopedLearnCostIsIndependentOfLedgerSize(t *testing.T) {
	if testing.Short() {
		t.Skip("scale check")
	}
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	cost := func(n int) uint64 {
		snap := scaleSnapshot(n)
		for i := range 30 {
			snap.Records[i].Posting.Company = "Target"
		}
		in := &insight{vocab: v, now: jobsT0, halfLife: jobinsight.DefaultScoreHalfLifeDays}
		return allocsOf(func() { in.learn(snap, nil, "target") })
	}
	cost(50)
	small, large := cost(1250), cost(5000)
	t.Logf("scoped learn, 30-posting company: %d allocs in a 1250 ledger, %d in a 5000 ledger", small, large)
	if extra := large - small; extra > 2*(5000-1250) {
		t.Errorf("a 4x ledger added %d allocations to one company's learn (%d -> %d)", extra, small, large)
	}
	var full uint64
	{
		snap := scaleSnapshot(1250)
		in := &insight{vocab: v, now: jobsT0, halfLife: jobinsight.DefaultScoreHalfLifeDays}
		full = allocsOf(func() { in.learn(snap, nil, "") })
	}
	if small*10 > full {
		t.Errorf("scoped learn (%d allocs) is not an order of magnitude under the full learn (%d)", small, full)
	}
}
