package kbsync

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// For a failure at the 1st, 15th, 16th, 17th, 32nd, 33rd and last call: the
// Result must equal what reached the persisted state (flush succeeding).
func TestApplyResultEqualsPersistedStateWhateverCallFails(t *testing.T) {
	const total = 40
	for _, failAt := range []int{1, 2, 15, 16, 17, 18, 32, 33, 40} {
		t.Run(fmt.Sprint("fail@", failAt), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kbsync.json")
			st := NewSyncState(path)
			res, err := Apply(context.Background(), &acceptCaller{failAt: failAt}, addPlan(total), st, nil)
			if err == nil {
				t.Fatal("want the call failure")
			}
			if res.Added != failAt-1 || len(res.Outcomes) != failAt-1 {
				t.Fatalf("Added %d outcomes %d, want %d", res.Added, len(res.Outcomes), failAt-1)
			}
			disk, lerr := LoadState(path)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if len(disk.Blocks) != res.Added || len(st.Blocks) != res.Added {
				t.Fatalf("Result %d, memory %d, disk %d: must agree", res.Added, len(st.Blocks), len(disk.Blocks))
			}
		})
	}
}

// A failing flush at the boundary: Result, memory and the error all say the
// 16 accepted blocks; the disk holds none (unsavable), and the error is the
// state error rather than nil.
func TestApplyUnsavableStateAtEveryBoundaryReportsErrorAndCounts(t *testing.T) {
	for _, total := range []int{1, flushEvery - 1, flushEvery, flushEvery + 1, 2 * flushEvery} {
		st := unsavableState(t)
		res, err := Apply(context.Background(), &acceptCaller{}, addPlan(total), st, nil)
		if err == nil {
			t.Fatalf("total %d: unsaved state reported success: %+v", total, res)
		}
		want := total
		if total >= flushEvery {
			want = flushEvery // the batch flush fails and stops the run
		}
		if res.Added != want || len(st.Blocks) != res.Added {
			t.Fatalf("total %d: Added %d, state %d, want %d", total, res.Added, len(st.Blocks), want)
		}
	}
}
