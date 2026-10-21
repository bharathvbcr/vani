package kbsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// unsavableState is a state whose Save always fails: its parent "directory" is
// a regular file.
func unsavableState(t *testing.T) *SyncState {
	t.Helper()
	file := filepath.Join(t.TempDir(), "kbsync.json")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewSyncState(filepath.Join(file, "state.json"))
}

// The batch flush after the flushEvery-th accepted block fails: that block was
// accepted by DevPrism and recorded in state (the deferred flush retries it),
// so Result must count it. It used to return before counting.
func TestApplyFailedBatchFlushStillCountsTheAcceptedBlock(t *testing.T) {
	st := unsavableState(t)
	res, err := Apply(context.Background(), &acceptCaller{}, addPlan(40), st, nil)
	if !errors.Is(err, ErrState) {
		t.Fatalf("err %v, want the failed save", err)
	}
	if res.Added != flushEvery {
		t.Fatalf("Added %d, want %d: the block whose flush failed was accepted", res.Added, flushEvery)
	}
	if len(res.Outcomes) != flushEvery {
		t.Fatalf("%d outcomes, want %d", len(res.Outcomes), flushEvery)
	}
	if len(st.Blocks) != res.Added {
		t.Fatalf("state holds %d blocks but Result counts %d: they must agree", len(st.Blocks), res.Added)
	}
	if got := res.Outcomes[flushEvery-1].ID; got != "b015" {
		t.Fatalf("last outcome %s, want the 16th block b015", got)
	}
}

func TestApplyFailedFlushOnConfirmedBlockCountsIt(t *testing.T) {
	st := unsavableState(t)
	res, err := Apply(context.Background(), &acceptCaller{}, addPlan(flushEvery), st, nil)
	if !errors.Is(err, ErrState) || res.Added != flushEvery || len(st.Blocks) != flushEvery {
		t.Fatalf("err %v added %d state %d", err, res.Added, len(st.Blocks))
	}
}
