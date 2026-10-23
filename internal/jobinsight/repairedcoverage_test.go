package jobinsight

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A repaired 0-of-0 record (rank and coverage cleared, unknown marker set) has
// no coverage part, for a Digested record as for any other: unknown, not 0.
func TestInputsForRepairedDigestedZeroOfZeroIsUnknown(t *testing.T) {
	r := rec("gh:a:1", "Acme", time.Hour, map[string]string{ledger.AnnCoverageUnknown: ledger.CoverageNoMustHaves})
	r.Status = posting.Digested
	in := InputsFor(r, RankContext{Now: inT0})
	if in.HasCoverage || in.Coverage != 0 {
		t.Fatalf("coverage %v (has %v), want unknown", in.Coverage, in.HasCoverage)
	}
}
