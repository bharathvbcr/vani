package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// rankfit --silver-prior end to end: the auditor's answers about postings the
// owner never judged fit the prior, its answers about judged ones are left
// out, the gold-only fit on the same split is reported beside it, and the
// file is written only when the prior's fit passes the gold gate.
func TestRankfitSilverPriorKeepsGoldOutAndTheGateInPlace(t *testing.T) {
	e := psSetup(t, 150, 400, nil)
	f := &psFake{}
	f.use(t)
	// Most gold comes before the freeze: it trains the weight fit (which needs
	// 150 judgments) but is never held out. Thirty labels after the freeze are
	// the held-out set, and the planner pairs each with stratum-mates; the rest
	// of its pairs fall on postings nobody judged, which the prior learns from.
	early := jobsT0.Add(-48 * time.Hour)
	e.label(t, "w", "yes", 0, 60, early)
	e.label(t, "u", "no", 0, 120, early)
	e.freeze(t)
	e.setCap(t, "50")
	late := jobsT0.Add(24 * time.Hour)
	e.label(t, "w", "yes", 60, 70, late)
	e.label(t, "u", "no", 120, 140, late)
	for _, seed := range []string{"11", "12", "13", "14", "15", "16"} {
		mustCode(t, 0, "teacher", "--task", "would_apply_pair", "--budget-usd", "40", "--pairs", "500", "--concurrency", "1", "--seed", seed)
	}

	code, o, se := do(t, "rankfit", "--part", "weights", "--silver-prior", "--json")
	if code != 0 && code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, o, se)
	}
	var r rankfitReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	sp := r.SilverPrior
	if sp == nil {
		t.Fatalf("no silver prior in the reply: %s", o)
	}
	t.Logf("prior: %d pairs used, %d excluded, %d absent, %d items; error %q", sp.Prior.Pairs, sp.Prior.Excluded, sp.Absent, sp.Prior.Items, r.Error)
	if sp.Prior.Excluded == 0 {
		t.Error("no answer about an owner-judged posting was left out; the planner pairs every post-freeze label")
	}
	if sp.Prior.Pairs < 100 {
		t.Fatalf("the fixture gave %d usable answers, under the prior's 100 (error %q, written %v)", sp.Prior.Pairs, r.Error, r.Written)
	}
	if sp.GoldOnly == nil || r.Report == nil || r.Report.Weights == nil {
		t.Fatalf("both fits should be reported: gold-only %v, weights %v", sp.GoldOnly, r.Report)
	}
	res := r.Report.Weights
	if sp.GoldOnly.HeldOutItems != res.HeldOutItems || sp.GoldOnly.HeldOutPairs != res.HeldOutPairs {
		t.Errorf("the two fits were not graded on one split: %d/%d vs %d/%d", sp.GoldOnly.HeldOutItems, sp.GoldOnly.HeldOutPairs, res.HeldOutItems, res.HeldOutPairs)
	}
	if r.Written != res.Beats() {
		t.Errorf("written %v but Beats %v", r.Written, res.Beats())
	}
}

func TestRankfitSilverPriorIsForTheWeightFit(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 2, "rankfit", "--part", "platt", "--silver-prior")
}

// Without auditor answers there is no prior: an error that names it, and
// nothing written, never a fit that quietly fell back to the hand weights.
func TestRankfitSilverPriorWithoutAnswersRefuses(t *testing.T) {
	e := psSetup(t, 90, 120, nil)
	early := jobsT0.Add(-48 * time.Hour)
	e.label(t, "w", "yes", 0, 60, early)
	e.label(t, "u", "no", 0, 100, early)
	e.freeze(t)
	_, se := mustCode(t, 1, "rankfit", "--part", "weights", "--silver-prior")
	if !strings.Contains(se, "silver-prior") {
		t.Errorf("refusal: %s", se)
	}
}
