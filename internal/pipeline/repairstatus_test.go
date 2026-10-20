package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// ZeroOfZero follows the stored coverage reading, not the rescorable status:
// a Digested record is matched and repaired in place (status kept, marker set,
// rank and coverage cleared), while Dead and Dropped are not, because the
// daily footer counts them by last-event day and a repair event would add them
// to today's count. Unscored still needs a rescorable status.
func TestZeroOfZeroFollowsStoredCoverage(t *testing.T) {
	zero := map[string]string{ledger.AnnRank: "0", ledger.AnnCoverage: "0"}
	for st, want := range map[posting.Status]bool{
		posting.Scored: true, posting.Explained: true, posting.Digested: true, posting.Duplicate: true,
		posting.Dead: false, posting.Dropped: false,
	} {
		if got := ZeroOfZero(ledger.Record{Status: st, Annotations: zero}); got != want {
			t.Errorf("ZeroOfZero(%s) = %v, want %v", st, got, want)
		}
	}
	if Unscored(ledger.Record{Status: posting.Digested, Annotations: map[string]string{}}) {
		t.Error("a digested record must never be Unscored")
	}
}

func TestRepairUnknownCoverageDigested(t *testing.T) {
	sc := &countingScorer{}
	r, l, k := unscoredRunner(t, "jd text", posting.Explained, sc)
	seedStoredCoverage(t, r, l, k, map[string]string{ledger.AnnRank: "0", ledger.AnnCoverage: "0"})
	if err := r.MarkDigested(ctx, []posting.Key{k}); err != nil {
		t.Fatal(err)
	}
	before := rec(t, l, k)
	if before.Status != posting.Digested {
		t.Fatalf("setup status %s", before.Status)
	}
	wrote, err := r.RepairUnknownCoverage(ctx, k)
	if err != nil || !wrote {
		t.Fatalf("wrote %v err %v", wrote, err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Digested || got.Annotations[AnnCoverageUnknown] != CoverageNoMustHaves ||
		got.Annotations[ledger.AnnRank] != "" || got.Annotations[ledger.AnnCoverage] != "" || !got.FirstSeen.Equal(before.FirstSeen) {
		t.Fatalf("repaired: status %s ann %v", got.Status, got.Annotations)
	}
	if wrote, err := r.RepairUnknownCoverage(ctx, k); err != nil || wrote {
		t.Fatalf("second run wrote %v err %v", wrote, err)
	}
	// Digesting again is a no-op: the status did not move.
	if err := r.MarkDigested(ctx, []posting.Key{k}); err != nil || !rec(t, l, k).LastEvent.Equal(got.LastEvent) {
		t.Fatalf("re-digest wrote an event: %v", err)
	}
}
