package pipeline

import (
	"strconv"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// fromRank is a stand-in relevance reader for the gate tests: the rank
// annotation, with zero or missing read as not computable.
func fromRank(r ledger.Record) (float64, bool) {
	v, err := strconv.ParseFloat(r.Annotations[ledger.AnnRank], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// The gate reads the injected relevance, never the coverage: a posting whose
// DevPrism coverage is exactly 0 still gets a note when its relevance clears
// the floor, and is explained (not deferred) when it does not.
func TestNotesGateKeysOnRelevanceNotCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rel    func(ledger.Record) (float64, bool)
		status posting.Status
		calls  int
		defer_ string
	}{
		{"relevance above floor despite coverage 0", func(ledger.Record) (float64, bool) { return 55, true }, posting.Explained, 1, ""},
		{"relevance below floor despite coverage 0", func(ledger.Record) (float64, bool) { return 20, true }, posting.Explained, 0, ""},
		{"relevance not computable", func(ledger.Record) (float64, bool) { return 0, false }, posting.Scored, 0, DeferUnknownRelevance},
		{"no reader", nil, posting.Scored, 0, DeferUnknownRelevance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &NotesGate{MinRelevance: 40, Relevance: tc.rel}
			r, ex, l, k := scoredRunner(t, 0, g) // coverage 0, the suspected parse-failure signature
			if err := r.Advance(ctx, k); err != nil {
				t.Fatal(err)
			}
			got := rec(t, l, k)
			if got.Status != tc.status || ex.calls != tc.calls || got.Annotations[AnnNotesDeferred] != tc.defer_ {
				t.Fatalf("status %s calls %d ann %v", got.Status, ex.calls, got.Annotations)
			}
		})
	}
}
