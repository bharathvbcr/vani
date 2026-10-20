package main

import (
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// tieFixture is two postings of equal priority and different relevance, found
// by search over Score itself, so the test does not pin Score's constants.
func tieFixture(t *testing.T) (hi, lo jobinsight.Result) {
	t.Helper()
	type cand struct {
		res jobinsight.Result
	}
	var all []jobinsight.Result
	for _, f := range []string{"strong_fit", "partial_fit", "adjacent"} {
		for age := 0; age < 40; age++ {
			all = append(all, jobinsight.Score(jobinsight.Inputs{
				RoleFit:  jobinsight.Slot{Value: f, Answered: true, P: 0.9, HasP: true},
				Coverage: 60, HasCoverage: true, AgeKnown: true, AgeDays: age,
			}))
		}
	}
	for _, a := range all {
		for _, b := range all {
			if a.Score == b.Score && a.Relevance > b.Relevance {
				return a, b
			}
		}
	}
	t.Fatal("no fixture with equal priority and different relevance")
	return
}

func orderBy(by string, jobs []jobJSON) []string {
	sel := make([]selected, len(jobs))
	for i, j := range jobs {
		sel[i] = selected{job: j}
	}
	sortJobs(sel, "new", by)
	var out []string
	for _, s := range sel {
		out = append(out, string(s.job.Key))
	}
	return out
}

// The default and priority sorts break an equal priority exactly as the digest
// does (jobinsight.ComparePriority): by relevance, then key. Rank, which counts
// coverage a second time, is not a tie-break.
func TestSortJobsTieBreakIsTheDigests(t *testing.T) {
	hi, lo := tieFixture(t)
	ranked := func(v float64) *float64 { return &v }
	// Keys and ranks both oppose the relevance order.
	jobs := []jobJSON{
		{Key: "gh:a:a", Priority: lo.Score, Relevance: lo.Relevance, Rank: ranked(99)},
		{Key: "gh:a:z", Priority: hi.Score, Relevance: hi.Relevance, Rank: ranked(1)},
	}
	want := []string{"gh:a:z", "gh:a:a"}
	for _, by := range []string{"default", "priority"} {
		if got := orderBy(by, jobs); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v (relevance breaks the tie)", by, got, want)
		}
	}
	// The same fixture through the digest's comparator.
	cmp := jobinsight.ComparePriority(hi, lo, "gh:a:z", "gh:a:a")
	if cmp >= 0 {
		t.Errorf("ComparePriority puts the lower relevance first: %d", cmp)
	}

	// A full tie falls to the key, whatever the rank, the fit or when it was seen.
	tie := []jobJSON{
		{Key: "gh:a:b", Priority: 50, Relevance: 60, Rank: ranked(10)},
		{Key: "gh:a:a", Priority: 50, Relevance: 60, Rank: ranked(1)},
		{Key: "gh:a:c", Priority: 50, Relevance: 60},
	}
	for _, by := range []string{"default", "priority"} {
		if got := orderBy(by, tie); !slices.Equal(got, []string{"gh:a:a", "gh:a:b", "gh:a:c"}) {
			t.Errorf("%s: full tie order %v, want by key", by, got)
		}
	}
	// The relevance sort is the mirror: relevance first, then priority.
	rel := []jobJSON{
		{Key: "gh:a:a", Priority: 10, Relevance: 80},
		{Key: "gh:a:b", Priority: 90, Relevance: 80},
	}
	if got := orderBy("relevance", rel); !slices.Equal(got, []string{"gh:a:b", "gh:a:a"}) {
		t.Errorf("relevance sort: %v", got)
	}
}

func TestSortJobsNaNAndEmpty(t *testing.T) {
	if got := orderBy("default", nil); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
	nan := jobJSON{Key: "gh:a:b", Priority: nanF(), Relevance: nanF()}
	ok := jobJSON{Key: "gh:a:a", Priority: nanF(), Relevance: nanF()}
	if got := orderBy("priority", []jobJSON{nan, ok}); !slices.Equal(got, []string{"gh:a:a", "gh:a:b"}) {
		t.Errorf("NaN scores must order by key: %v", got)
	}
}

func nanF() float64 { var z float64; return z / z }

// The jobs list shapes its default order for reading as the digest does: three
// roles of one company lead at most, and exact company+title duplicates are one
// row carrying a similar count.
func TestJobsListDiversity(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	var seeds []scoreSeed
	for i, title := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		seeds = append(seeds, scoreSeed{key: fmt.Sprintf("gh:big:%d", i), company: "BigCo", title: title + " Engineer", fit: "strong_fit", cov: "90", posted: day, seenAgo: day})
	}
	seeds = append(seeds,
		scoreSeed{key: "gh:big:dup", company: "BigCo", title: "alpha  engineer!", fit: "strong_fit", cov: "90", posted: day, seenAgo: day},
		scoreSeed{key: "gh:s1:0", company: "Small One", title: "Role One", fit: "partial_fit", cov: "50", posted: day, seenAgo: day},
		scoreSeed{key: "gh:s2:0", company: "Small Two", title: "Role Two", fit: "partial_fit", cov: "50", posted: day, seenAgo: day},
	)
	seedScores(t, state, seeds...)
	l := listJSON(t)
	if len(l.Jobs) != 7 {
		t.Fatalf("%d rows, want 7 (one duplicate folded): %s", len(l.Jobs), keys(l))
	}
	big := 0
	for _, j := range l.Jobs[:5] {
		if j.Company == "BigCo" {
			big++
		}
	}
	if big > 3 {
		t.Errorf("%d BigCo rows in the first five: %s", big, keys(l))
	}
	folded := 0
	for _, j := range l.Jobs {
		folded += j.Similar
	}
	if folded != 1 {
		t.Errorf("similar counts sum to %d, want 1", folded)
	}
	if l.Total != 7 {
		t.Errorf("total %d, want 7", l.Total)
	}
	// Marked views are the owner's own lists, in the order they marked: untouched.
	if saved := listJSON(t, "--view", "saved"); len(saved.Jobs) != 0 {
		t.Errorf("saved view %s", keys(saved))
	}
}

// The daily run's advance order is built from the config: title signals, drop
// rules and age-neutral employers all reach it.
func TestAdvanceOrderCarriesTheConfig(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	cfg.TitleSignals = config.TitleSignals{Include: []string{"Inference"}}
	cfg.DropRules = []config.DropRule{{Slot: "sponsorship", Values: []string{"no_sponsorship"}}}
	cfg.AgeNeutralCompanies = []string{"Acme"}
	opt := newWired(cfg, state, io.Discard).advanceOrder()
	if opt == nil {
		t.Fatal("no order option with semantic matching on")
	}
	set := settingsOf([]orderOption{opt})
	if set.titleRank == nil || len(set.rules) != 1 || !set.ageNeutral["acme"] {
		t.Errorf("settings %+v", set)
	}
}

// advance orders by the configured title signals as well as the semantic
// score, through the same RankContext the digest and the list use.
func TestOrderAdvanceUsesRankConfig(t *testing.T) {
	cfg := config.Config{TitleSignals: config.TitleSignals{Include: []string{"Inference"}, Exclude: []string{"Sales"}}}
	_, titleRank := rankRules(cfg)
	recs := []ledger.Record{
		rec("ab:a:1", crawl1, days(1), "Sales Engineer"),
		rec("ab:a:2", crawl1, days(1), "Inference Engineer"),
		rec("ab:a:3", crawl1, days(1), "Software Engineer"),
	}
	for i := range recs {
		recs[i].Posting.Company = "Acme"
		recs[i].Posting.Key = posting.Key(recs[i].Key)
	}
	semantic := func(ledger.Record) (float64, bool) { return 0.5, true }
	todo := slices.Clone(recs)
	orderAdvance(todo, recs, nil, withSemantic(semantic), withRank(nil, titleRank))
	if got, want := keysOf(todo), []string{"ab:a:2", "ab:a:3", "ab:a:1"}; !slices.Equal(got, want) {
		t.Errorf("with title signals %v, want %v (Score's title part and excluded-title gate)", got, want)
	}
	// Without the config the three tie on Score, so ledger order stands.
	todo = slices.Clone(recs)
	orderAdvance(todo, recs, nil, withSemantic(semantic))
	if got, want := keysOf(todo), []string{"ab:a:1", "ab:a:2", "ab:a:3"}; !slices.Equal(got, want) {
		t.Errorf("without it %v, want ledger order %v", got, want)
	}
	// Nil rules and a nil title rank are no-ops, not a panic.
	todo = slices.Clone(recs)
	orderAdvance(todo, recs, nil, withSemantic(semantic), withRank(nil, nil))
	if got := keysOf(todo); len(got) != 3 {
		t.Errorf("order %v", got)
	}
}
