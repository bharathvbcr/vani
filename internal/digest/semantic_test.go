package digest

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Two roles that differ only in their cached knowledge-base similarity: the
// digest orders them by it when given a lookup, and by key (a tie) without one.
func TestDigestUsesSemanticLookup(t *testing.T) {
	ann := func() map[string]string {
		return map[string]string{
			ledger.AnnRank: "70", ledger.AnnCoverage: "70",
			"triage.role_fit": "partial_fit", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9",
		}
	}
	a := rec("gh:a:1", posting.Explained, "Role A", ann())
	b := rec("gh:a:2", posting.Explained, "Role B", ann())
	c := rec("gh:a:3", posting.Explained, "Role C (not cached)", ann())
	recs := []ledger.Record{a, b, c}

	_, plain := Render(recs, nil, day, Counts{})
	if len(plain) != 3 || plain[0] != "gh:a:1" || plain[1] != "gh:a:2" || plain[2] != "gh:a:3" {
		t.Fatalf("without a lookup the order is by key, got %v", plain)
	}

	scores := map[posting.Key]float64{"gh:a:1": 0.05, "gh:a:2": 0.95}
	o := Options{Semantic: func(r ledger.Record) (float64, bool) {
		s, ok := scores[r.Key]
		return s, ok
	}}
	_, keys := RenderWith(recs, nil, day, Counts{}, o)
	if len(keys) != 3 || keys[0] != "gh:a:2" {
		t.Fatalf("the high-similarity role should lead, got %v", keys)
	}
	// An uncached role takes the prior: it is neither the best nor forced to 0.
	pos := map[posting.Key]int{}
	for i, k := range keys {
		pos[k] = i
	}
	if !(pos["gh:a:3"] > pos["gh:a:2"]) {
		t.Errorf("uncached role outranked the high-similarity one: %v", keys)
	}
	if !(pos["gh:a:3"] < pos["gh:a:1"]) {
		t.Errorf("uncached role (prior) should beat a known near-zero similarity: %v", keys)
	}
}
