package main

import (
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

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
