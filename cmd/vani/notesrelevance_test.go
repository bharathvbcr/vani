package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func relRec(id string, ann map[string]string) ledger.Record {
	k := posting.Key("gh:acme:" + id)
	return ledger.Record{Key: k, Status: posting.Scored, Annotations: ann,
		Posting: posting.Posting{Key: k, Company: "Acme", Title: "ML Engineer", Description: "Build inference systems in Go and Rust."}}
}

// The gate's relevance is jobinsight.Result.Relevance from the Jobs views' own
// ranking context, age-free. Coverage 0 is an input, not "unknown"; only a
// posting nothing is known about (RelevanceKnown 0) has no relevance.
func TestNotesRelevanceIsTheRankingsRelevance(t *testing.T) {
	cfg := config.Default()
	cfg.TargetSummary = "ML systems, inference, Go, Rust"
	recs := []ledger.Record{
		relRec("1", map[string]string{ledger.AnnRank: "80", ledger.AnnCoverage: "80", "triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered"}),
		relRec("2", map[string]string{ledger.AnnRank: "0", ledger.AnnCoverage: "0"}), // the suspected parse failure
		relRec("3", map[string]string{}),
	}
	rel := notesRelevance(cfg, t.TempDir(), func() []ledger.Record { return recs }, func(string, ...any) {})

	in, err := newInsightFor(cfg, nil, t.TempDir(), jobinsight.DefaultScoreHalfLifeDays, ledger.Snapshot{Records: recs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		want := jobinsight.Score(jobinsight.InputsFor(r, in.rc))
		got, ok := rel(r)
		if got != want.Relevance || ok != (want.RelevanceKnown > 0) {
			t.Errorf("%s: gate reads %v %v, ranking says %v known %v", r.Key, got, ok, want.Relevance, want.RelevanceKnown)
		}
	}
	if _, ok := rel(recs[1]); !ok {
		t.Error("coverage 0 alone made relevance unknown")
	}
	bare := notesRelevance(config.Default(), t.TempDir(), func() []ledger.Record { return recs }, func(string, ...any) {}) // no target summary
	if _, ok := bare(recs[2]); ok {
		t.Error("a posting nothing is known about has a relevance")
	}
	hi, _ := rel(recs[0])
	lo, _ := rel(recs[1])
	if hi <= lo {
		t.Errorf("strong fit with 80%% coverage (%v) must outrank coverage 0 (%v)", hi, lo)
	}
}

// Age must not move the gate: the same posting seen a year ago and today reads
// the same relevance.
func TestNotesRelevanceIgnoresAge(t *testing.T) {
	cfg := config.Default()
	a := relRec("1", map[string]string{ledger.AnnRank: "60", ledger.AnnCoverage: "60"})
	b := relRec("2", map[string]string{ledger.AnnRank: "60", ledger.AnnCoverage: "60"})
	a.FirstSeen, b.FirstSeen = now().AddDate(-1, 0, 0), now()
	a.Posting.PostedAt = a.FirstSeen
	rel := notesRelevance(cfg, t.TempDir(), func() []ledger.Record { return []ledger.Record{a, b} }, func(string, ...any) {})
	x, _ := rel(a)
	y, _ := rel(b)
	if x != y {
		t.Fatalf("relevance moved with age: %v vs %v", x, y)
	}
}
