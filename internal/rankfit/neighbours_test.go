package rankfit

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Round-robin: each anchor takes its nearest untaken candidate in turn, so an
// anchor with many near copies cannot fill the pool alone.
func TestYesNeighboursRoundRobin(t *testing.T) {
	t.Parallel()
	a := Vectored{Key: "a", Vector: unit(1, 0, 0)}
	b := Vectored{Key: "b", Vector: unit(0, 1, 0)}
	cands := []Vectored{
		{Key: "a1", Vector: unit(1, 0.01, 0)},
		{Key: "a2", Vector: unit(1, 0.02, 0)},
		{Key: "a3", Vector: unit(1, 0.03, 0)},
		{Key: "b1", Vector: unit(0.01, 1, 0)},
		{Key: "far", Vector: unit(0, 0, 1)},
		{Key: "nov"},                        // no vector: never near
		{Key: "wide", Vector: []float32{1}}, // another width: never near
		{Key: "a1", Vector: unit(1, 0.01, 0)},
	}
	got := YesNeighbours([]Vectored{b, a, a}, cands, 3)
	if want := []posting.Key{"a1", "b1", "a2"}; !slices.Equal(got, want) {
		t.Fatalf("pool %v, want %v (a first by key, then b, then a again)", got, want)
	}
	all := YesNeighbours([]Vectored{a, b}, cands, 100)
	if len(all) != 5 || slices.Contains(all, "nov") || slices.Contains(all, "wide") {
		t.Fatalf("an exhausted pool %v: want the 5 vectored candidates once each", all)
	}
	if YesNeighbours([]Vectored{a}, cands, 0) != nil || YesNeighbours(nil, cands, 5) != nil || YesNeighbours([]Vectored{{Key: "x"}}, cands, 5) != nil {
		t.Fatal("k 0, no anchors, or an anchor without a vector must give an empty pool")
	}
}
