package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedStoredCoverage writes the annotations an old run stored for a score.
func seedStoredCoverage(t *testing.T, r *Runner, l ledger.Ledger, k posting.Key, ann map[string]string) {
	t.Helper()
	if err := r.emit(ctx, k, rec(t, l, k).Status, "rescored", ann); err != nil {
		t.Fatal(err)
	}
}

// A stored 0 with no missing must-have is DevPrism's 0 of 0 and is marked
// unknown without a DevPrism call; a genuine zero (must-haves missing) is left
// alone; a second run writes nothing.
func TestRepairUnknownCoverage(t *testing.T) {
	sc := &countingScorer{}
	r, l, zero := unscoredRunner(t, "jd text", posting.Explained, sc)
	seedStoredCoverage(t, r, l, zero, map[string]string{ledger.AnnRank: "0", ledger.AnnCoverage: "0", ledger.AnnBlocks: "8"})

	r2, l2, genuine := unscoredRunner(t, "jd text", posting.Explained, sc)
	seedStoredCoverage(t, r2, l2, genuine, map[string]string{ledger.AnnRank: "0", ledger.AnnCoverage: "0", ledger.AnnMissing: "cuda; go (weak)"})
	before := rec(t, l2, genuine)

	if !ZeroOfZero(rec(t, l, zero)) || ZeroOfZero(before) {
		t.Fatal("ZeroOfZero must match the 0-of-0 record and not the genuine zero")
	}
	wrote, err := r.RepairUnknownCoverage(ctx, zero)
	if err != nil || !wrote {
		t.Fatalf("repair: wrote %v err %v", wrote, err)
	}
	got := rec(t, l, zero)
	if got.Annotations[ledger.AnnCoverage] != "" || got.Annotations[ledger.AnnRank] != "" ||
		got.Annotations[ledger.AnnCoverageUnknown] != ledger.CoverageNoMustHaves || got.Status != posting.Explained ||
		got.Annotations[ledger.AnnExplanation] == "" || got.Annotations[ledger.AnnBlocks] != "8" {
		t.Fatalf("repaired record: status %s ann %v", got.Status, got.Annotations)
	}
	if Unscored(got) {
		t.Fatal("a repaired record must not be rescored again")
	}

	// Idempotent: the second run is a no-op and writes no event.
	after := rec(t, l, zero)
	if wrote, err := r.RepairUnknownCoverage(ctx, zero); err != nil || wrote || !rec(t, l, zero).LastEvent.Equal(after.LastEvent) {
		t.Fatalf("second run: wrote %v err %v", wrote, err)
	}
	// The genuine zero is untouched.
	if wrote, err := r2.RepairUnknownCoverage(ctx, genuine); err != nil || wrote {
		t.Fatalf("genuine zero: wrote %v err %v", wrote, err)
	}
	if g := rec(t, l2, genuine); !g.LastEvent.Equal(before.LastEvent) || g.Annotations[ledger.AnnCoverage] != "0" || g.Annotations[ledger.AnnCoverageUnknown] != "" {
		t.Fatalf("genuine zero changed: %v", g.Annotations)
	}
	if sc.calls != 0 {
		t.Fatalf("repair called DevPrism %d time(s)", sc.calls)
	}
}
