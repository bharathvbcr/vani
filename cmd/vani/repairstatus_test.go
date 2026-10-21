package main

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The live ledger's 0-of-0 records are mostly Digested (the ranked, shown
// ones). The repair must reach every status that carries a stored coverage
// reading, the dry-run count must equal what the real pass writes, and a
// Dead record is left alone: the daily footer counts dead postings by last
// event day, so a repair event would add it to today's count.
func TestRepairUnknownCoverageAllStatuses(t *testing.T) {
	sc := &rescoreScorer{}
	l := rescoreLedger(t, []rescoreSeed{
		{"1", posting.Digested, "jd", "0"},
		{"2", posting.Scored, "jd", "0"},
		{"3", posting.Explained, "jd", "0"},
		{"4", posting.Duplicate, "jd", "0"},
		{"5", posting.Dead, "jd", "0"},
		{"6", posting.Digested, "jd", "70"},
		{"7", posting.Digested, "jd", ""},
	})
	k := func(id string) posting.Key {
		key, err := posting.GreenhouseKey("acme", id)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	r := rescoreRunner(l, sc)
	// 3 is a genuine zero: it lists what is missing.
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: r.Now(), Key: k("3"), Status: posting.Explained,
		Annotations: map[string]string{ledger.AnnMissing: "cuda"}}); err != nil {
		t.Fatal(err)
	}
	before := recordsByKey(t, l)
	dry := planRescore(l.All()).ZeroOfZero
	n, err := repairUnknownCoverage(context.Background(), l, r.RepairUnknownCoverage)
	if err != nil {
		t.Fatal(err)
	}
	if dry != 3 || n != dry {
		t.Fatalf("dry-run counted %d, repaired %d, want 3 and equal", dry, n)
	}
	after := recordsByKey(t, l)
	for _, id := range []string{"1", "2", "4"} {
		a, b := after[k(id)], before[k(id)]
		if a.Status != b.Status || a.Annotations[ledger.AnnCoverageUnknown] != ledger.CoverageNoMustHaves ||
			a.Annotations[ledger.AnnCoverage] != "" || a.Annotations[ledger.AnnRank] != "" || !a.FirstSeen.Equal(b.FirstSeen) {
			t.Fatalf("%s: status %s->%s ann %v", id, b.Status, a.Status, a.Annotations)
		}
	}
	for _, id := range []string{"3", "5", "6", "7"} {
		if !after[k(id)].LastEvent.Equal(before[k(id)].LastEvent) || after[k(id)].Annotations[ledger.AnnCoverageUnknown] != "" {
			t.Fatalf("%s must be untouched: %v", id, after[k(id)].Annotations)
		}
	}
	if n, err := repairUnknownCoverage(context.Background(), l, r.RepairUnknownCoverage); err != nil || n != 0 {
		t.Fatalf("second pass repaired %d err %v", n, err)
	}
	if got := planRescore(l.All()).ZeroOfZero; got != 0 {
		t.Fatalf("dry-run after repair counts %d", got)
	}
	// A repaired Digested record is not rescored and stays Digested.
	if pipeline.Unscored(after[k("1")]) || rescorable(after[k("1")]) {
		t.Fatal("repaired digested record became rescorable")
	}
}
