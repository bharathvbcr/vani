package ibm

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
)

// A search that lists fewer postings than it counts, within the allowance that
// keeps it, is kept and its gap counted (Stats.Short), per search and never
// below zero; Report receives a listed board's counts, a failed one's never.
func TestShortListingIsCounted(t *testing.T) {
	var a []map[string]any
	for i := 0; i < 150; i++ {
		a = append(a, numbered(t, 3000+i, "Engineer"))
	}
	shifted := append(append([]map[string]any{}, a[:100]...), a[99:149]...) // row 99 again, row 149 never read
	var st Stats
	calls := 0
	s, _ := newSource(handler(t, map[string][]map[string]any{"a": shifted, "b": a[:20]}, map[string]int{"a": 150}))
	s.opt = Options{Report: func(_ sources.Board, x Stats) { st = x; calls++ }}
	got, err := s.List(context.Background(), ibmBoard("a", "b"))
	if err != nil || len(got) != 149 {
		t.Fatalf("%d postings, err %v; want 149", len(got), err)
	}
	if calls != 1 || st.Short != 1 || st.Listed != 149 {
		t.Fatalf("%d reports, %+v; want Short 1 (search a), Listed 149", calls, st)
	}

	// A board that fails reports nothing: it is not reconciled.
	lost := append(append([]map[string]any{}, a[:100]...), a[70:120]...)
	calls = 0
	s, _ = newSource(handler(t, map[string][]map[string]any{"a": lost}, map[string]int{"a": 150}))
	s.opt = Options{Report: func(sources.Board, Stats) { calls++ }}
	if _, err := s.List(context.Background(), ibmBoard("a")); err == nil || calls != 0 {
		t.Fatalf("err %v, %d reports; want a failure and no report", err, calls)
	}
}
