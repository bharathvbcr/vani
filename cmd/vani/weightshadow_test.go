package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestWeightVerdictNeedsEveryModeAboveZero(t *testing.T) {
	ok := func(lo, hi float64) pairedAUC { return pairedAUC{OK: true, Lo: lo, Hi: hi} }
	off := pairedAUC{}
	cases := []struct {
		name string
		in   []pairedAUC
		want string
	}{
		{"nothing", nil, verdictInsufficient},
		{"primary mode not computable", []pairedAUC{off, ok(0.1, 0.2)}, verdictInsufficient},
		{"all above zero", []pairedAUC{ok(0.01, 0.2), ok(0.02, 0.3), ok(0.05, 0.1)}, verdictBetter},
		{"skipped modes do not count against", []pairedAUC{ok(0.01, 0.2), off, ok(0.05, 0.1)}, verdictBetter},
		{"one mode straddles zero", []pairedAUC{ok(0.01, 0.2), ok(-0.01, 0.3), ok(0.05, 0.1)}, verdictWithinNoise},
		{"interval touching zero is not above it", []pairedAUC{ok(0, 0.2)}, verdictWithinNoise},
		{"any mode below zero is worse", []pairedAUC{ok(0.01, 0.2), ok(-0.3, -0.01)}, verdictWorse},
		{"worse beats a straddle", []pairedAUC{ok(-0.1, 0.2), ok(-0.3, -0.01)}, verdictWorse},
	}
	for _, c := range cases {
		if got := verdictOf(c.in); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// Below the label floor a better verdict says so itself; nothing else changes.
	if got := withFloor(verdictBetter, minWeightShadowLabels-1); got != verdictBetterBelowFloor {
		t.Errorf("better under the floor: %s", got)
	}
	if got := withFloor(verdictBetter, minWeightShadowLabels); got != verdictBetter {
		t.Errorf("better at the floor: %s", got)
	}
	for _, v := range []string{verdictWorse, verdictWithinNoise, verdictInsufficient} {
		if got := withFloor(v, 0); got != v {
			t.Errorf("%s under the floor became %s", v, got)
		}
	}
}

func TestOpenJudgedKeepsOnlyOpenAnsweredPostings(t *testing.T) {
	recs := []ledger.Record{
		{Key: "a", Status: posting.Scored},
		{Key: "b", Status: posting.Dropped},
		{Key: "c", Status: posting.Scored},
		{Key: "d", Status: posting.Dead},
		{Key: "e", Status: posting.Unsure},
		{Key: "f", Status: posting.Scored},
		{Key: "g", Status: posting.Scored},
	}
	lab := func(v string) eval.Entry { return eval.Entry{Labels: map[string]string{eval.WouldApply: v}} }
	from := func(v, src string) eval.Entry { e := lab(v); e.Source = src; return e }
	done := map[posting.Key]eval.Entry{
		"a": lab("yes"), "b": lab("yes"), "c": lab("skip"), "d": lab("no"), "e": lab("maybe"),
		"zz": lab("yes"),                        // judged, but not in the ledger
		"f":  from("yes", eval.EngineeringKept), // picked on its title
		"g":  from("no", eval.CellYesNeighbour), // picked on its vectors
	}
	got := openJudged(done, recs)
	if len(got) != 2 {
		t.Fatalf("kept %v, want a and e", got)
	}
	for _, k := range []posting.Key{"a", "e"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s missing", k)
		}
	}
}

func TestPartCoverageCountsEvidenceNotPriors(t *testing.T) {
	w := jobinsight.DefaultWeights()
	fits := []jobinsight.Slot{
		{Value: "mismatch_domain", Answered: true, P: 0.95, HasP: true}, // gated: no share of the match
		{Value: "partial_fit", Answered: true, P: 0.6, HasP: true},
		{}, // not read
		{Value: "strong_fit", Answered: true, P: 0.9, HasP: true}, // dropped: out of the pool
	}
	i := 0
	in := func(cov float64, has, preferred bool) jobinsight.Inputs {
		x := jobinsight.Inputs{HasCoverage: has, Coverage: cov, TitleSignals: true, TitleRank: 2, PreferredEmployer: preferred, RoleFit: fits[i]}
		i++
		return x
	}
	recs := []ledger.Record{
		{Key: "a", Status: posting.Scored},
		{Key: "b", Status: posting.Scored},
		{Key: "c", Status: posting.Scored},
		{Key: "x", Status: posting.Dropped}, // not in the pool, however it reads
	}
	inputs := []jobinsight.Inputs{in(40, true, false), in(80, true, true), in(0, false, false), in(90, true, true)}
	c := partCoverageOf(inputs, recs, w)
	if c.Pool != 3 {
		t.Fatalf("pool %d, want 3 (the dropped posting is out)", c.Pool)
	}
	by := map[string]partCovered{}
	for _, p := range c.Parts {
		by[p.Name] = p
	}
	if p := by["coverage"]; p.Known != 2 || p.Distinct != 2 || p.Constant || !(p.Carried > 0) {
		t.Errorf("coverage: %+v", p)
	}
	// The mismatch is known but gated; the partial fit carries its confidence.
	if p := by["fit"]; p.Known != 2 || p.Gated != 1 || !(p.MeanConf > 0 && p.MeanConf <= 1) || !(p.Carried > 0) {
		t.Errorf("fit: %+v, want 2 known, 1 gated, a mean confidence from the partial fit", p)
	}
	if p := by["semantic"]; p.Known != 0 || p.Carried != 0 {
		t.Errorf("semantic is never known here: %+v", p)
	}
	// An unpreferred employer is known to the blend but says nothing.
	if p := by["employer"]; p.Known != 1 {
		t.Errorf("employer: %+v, want 1 known (the preferred one in the pool)", p)
	}
	if p := by["title"]; p.Known != 3 || !p.Constant {
		t.Errorf("title reads rank 2 everywhere: %+v, want constant", p)
	}
	if c.RelevanceKnownMedian <= 0 || c.RelevanceKnownP10 > c.RelevanceKnownMedian {
		t.Errorf("relevance known: median %v p10 %v", c.RelevanceKnownMedian, c.RelevanceKnownP10)
	}
	if md := c.Markdown(); !strings.Contains(md, "never known") || !strings.Contains(md, "constant: orders nothing") {
		t.Errorf("markdown misses the notes:\n%s", md)
	}
	if empty := partCoverageOf(nil, nil, w); empty.Pool != 0 || !strings.Contains(empty.Markdown(), "no open postings") {
		t.Errorf("empty pool: %+v", empty)
	}
}

// The shadow measures the age-free ranked score because the displayed
// Relevance keeps a fixed shrink: a MatchShrink variant moves one, not the
// other. If this ever fails, the measure choice in weightshadow.go is stale.
func TestMatchShrinkMovesTheRankedScoreNotRelevance(t *testing.T) {
	in := jobinsight.Inputs{HasCoverage: true, Coverage: 90, TitleSignals: true, TitleRank: 3}
	w := jobinsight.DefaultWeights()
	v := w
	for _, x := range weightVariants {
		if strings.HasPrefix(x.name, "V4") {
			x.set(&v)
		}
	}
	if v.MatchShrink == w.MatchShrink {
		t.Fatal("V4 did not change the shrink")
	}
	a, b := jobinsight.ScoreWith(in, w), jobinsight.ScoreWith(in, v)
	if a.Relevance != b.Relevance {
		t.Errorf("Relevance moved %v -> %v", a.Relevance, b.Relevance)
	}
	if ageFreeScore(a) == ageFreeScore(b) {
		t.Errorf("age-free score did not move: %v", ageFreeScore(a))
	}
}

func TestRelevanceReportsTheWeightShadow(t *testing.T) {
	auditedState(t)
	o, _ := mustCode(t, 0, "relevance", "--json")
	var r relevanceReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	s := r.WeightShadow
	if s == nil || s.Error != "" || len(s.Variants) != len(weightVariants) || s.Pool == 0 || s.Coverage == nil {
		t.Fatalf("weight shadow: %+v", s)
	}
	if s.MinLabels != minWeightShadowLabels || !(s.Alpha > 0 && s.Alpha < 0.05) {
		t.Errorf("rule: min %d alpha %v", s.MinLabels, s.Alpha)
	}
	for _, v := range s.Variants {
		if len(v.Paired) != len(shadowModes) || len(v.AUC) != len(shadowModes) || v.Verdict == "" {
			t.Errorf("%s: %+v", v.Name, v)
		}
	}
	text, _ := mustCode(t, 0, "relevance")
	for _, want := range []string{"## Weight shadow", "## Score part coverage", "Stated before the data", "verdict:"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q", want)
		}
	}
}
