package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// scaleSnapshot is n kept postings across 40 companies, each with a ~6 KiB
// description: the size of a large real ledger.
func scaleSnapshot(n int) ledger.Snapshot {
	para := "We build LLM inference in Go and Rust on Kubernetes, with Python tooling, CUDA kernels and observability. " +
		"You will own latency, cost and reliability of model serving, work with PyTorch and TensorRT, and mentor others. "
	desc := strings.Repeat(para, 28)
	var snap ledger.Snapshot
	for i := range n {
		k := posting.Key(fmt.Sprintf("gh:co%02d:%05d", i%40, i))
		snap.Records = append(snap.Records, ledger.Record{
			Key: k, Status: posting.Digested, FirstSeen: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			Posting:     posting.Posting{Key: k, Company: fmt.Sprintf("Company %02d", i%40), Title: "Inference Engineer", Description: desc},
			Annotations: map[string]string{"triage.role_fit": "partial_fit", ledger.AnnMissing: "Kubernetes (weak)"},
		})
	}
	return snap
}

// scaleInsight is what newInsight builds, minus the config: the ranking
// context with target-keyword hits, so the pass includes the score.
func scaleInsight(v *jobinsight.Vocab, snap ledger.Snapshot) *insight {
	in := &insight{vocab: v, target: v.Terms("Inference in Go and Rust."), now: jobsT0, halfLife: jobinsight.DefaultScoreHalfLifeDays}
	in.learn(snap, nil)
	in.rc = jobinsight.NewRankContext(snap.Records, nil, jobsT0)
	in.rc.Target, in.rc.Hits, in.rc.HalfLifeDays = true, in.targetHits, in.halfLife
	return in
}

// The whole insight pass for one `jobs list` (learn every posting's keywords,
// then select, enrich and sort) stays interactive at 5,000 postings: the Mac
// app runs it on every list, mark and note.
func TestJobsInsightScalesTo5000Postings(t *testing.T) {
	if testing.Short() {
		t.Skip("scale check")
	}
	snap := scaleSnapshot(5000)
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	in := scaleInsight(v, snap)
	o := jobsOpts{Opts: jobview.Opts{View: "all", Sort: "priority", HalfLife: jobinsight.DefaultScoreHalfLifeDays}}
	sel, _ := jobview.Select(snap, nil, &o.Opts, in, listKeywords)
	jobview.Sort(sel, o.View, o.Sort)
	took := time.Since(start)
	if len(sel) != 5000 {
		t.Fatalf("selected %d", len(sel))
	}
	t.Logf("5000 postings: %s", took)
	// Generous bound for -race and a loaded machine; the app's call timeout is 30 s.
	if took > 10*time.Second {
		t.Errorf("insight pass took %s for 5000 postings", took)
	}
}

func BenchmarkJobsInsight5000(b *testing.B) {
	snap := scaleSnapshot(5000)
	v, err := jobinsight.Default()
	if err != nil {
		b.Fatal(err)
	}
	o := jobsOpts{Opts: jobview.Opts{View: "all", Sort: "priority", HalfLife: jobinsight.DefaultScoreHalfLifeDays}}
	for b.Loop() {
		in := scaleInsight(v, snap)
		sel, _ := jobview.Select(snap, nil, &o.Opts, in, listKeywords)
		jobview.Sort(sel, o.View, o.Sort)
	}
}
