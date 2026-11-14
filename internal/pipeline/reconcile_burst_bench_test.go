package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkReconcileChangedBoard is a burst day's board: 500 postings whose text
// all changed, so the listing writes 500 events (2026-11-07 wrote about 22,000
// listing events across the run). It runs with the real fsync, as a run does:
// one per event before the board's events were grouped, one per board after.
//
//	go -C <repo> test -run '^$' -bench BenchmarkReconcileChangedBoard ./internal/pipeline/
func BenchmarkReconcileChangedBoard(b *testing.B) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for b.Loop() {
		b.StopTimer()
		l, boards, listed := benchLedger(b, 1, 500)
		changed := append(listed[0][:0:0], listed[0]...)
		for i := range changed {
			changed[i].Description = fmt.Sprintf("rewritten %d %s", i, changed[i].Description[:200])
		}
		b.StartTimer()
		s, err := Reconcile(context.Background(), l, boards[0], changed, now)
		if err != nil || s.Changed != 500 {
			b.Fatalf("%+v %v", s, err)
		}
	}
}
