package main

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// rankRules carries drop rules and rank penalties alike, in that order, and
// the title signals' Rank only when include or exclude signals are set; the
// digest options read the same, so `vani jobs` and the digest rank alike.
func TestRankRulesFromConfig(t *testing.T) {
	cfg := config.Default()
	if rules, rank := rankRules(cfg); len(rules) != 0 || rank != nil {
		t.Fatalf("an empty config ranks with %v rules and title rank %v", rules, rank != nil)
	}

	cfg.DropRules = []config.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	cfg.RankPenalties = []config.RankPenalty{{Slot: "seniority", Values: []string{"senior"}}}
	cfg.TitleSignals = config.TitleSignals{Read: []config.TitleRead{{Slot: "seniority", Value: "senior", Match: []string{"Senior"}}}}
	rules, rank := rankRules(cfg)
	want := []jobinsight.DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}}, {Slot: "seniority", Values: []string{"senior"}}}
	if !slices.EqualFunc(rules, want, func(a, b jobinsight.DropRule) bool { return a.Slot == b.Slot && slices.Equal(a.Values, b.Values) }) {
		t.Fatalf("rules = %+v, want %+v", rules, want)
	}
	if rank != nil {
		t.Fatal("title read entries alone set a title rank")
	}

	cfg.TitleSignals.Exclude = []string{"Sales"}
	if _, rank = rankRules(cfg); rank == nil || rank("Sales Engineer") != -1 || rank("Engineer") != 0 {
		t.Fatal("exclude signals not ranked")
	}

	o, _ := digestOptions(cfg, t.TempDir())
	if len(o.Rules) != 2 || o.TitleRank == nil || o.TitleRank("Sales Engineer") != -1 {
		t.Fatalf("digest options: %d rules, title rank %v", len(o.Rules), o.TitleRank != nil)
	}
}
