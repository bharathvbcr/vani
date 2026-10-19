package kbsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// acceptCaller accepts every upsert, except that call failAt (1-based) fails.
type acceptCaller struct{ failAt, n int }

func (c *acceptCaller) CallTool(_ context.Context, _ string, args any) (json.RawMessage, error) {
	c.n++
	if c.n == c.failAt {
		return nil, errors.New("devprism down")
	}
	var blk struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args.(map[string]json.RawMessage)["block"], &blk); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"success": true, "blockId": blk.ID})
}

func addPlan(n int) SyncPlan {
	var p SyncPlan
	for i := range n {
		id := fmt.Sprintf("b%03d", i)
		p.Items = append(p.Items, Item{ID: id, Action: ActionAdd, Hash: "h" + id, Payload: json.RawMessage(`{"id":"` + id + `"}`)})
	}
	return p
}

// Every accepted block used to rewrite and fsync the whole state file: n
// blocks, n saves. Saves now come every flushEvery blocks and once at the end.
func TestApplyBatchesStateSaves(t *testing.T) {
	const n = 40
	path := filepath.Join(t.TempDir(), "kbsync.json")
	st := NewSyncState(path)
	res, err := Apply(context.Background(), &acceptCaller{}, addPlan(n), st, nil)
	if err != nil || res.Added != n {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if want := n/flushEvery + 1; st.saves != want {
		t.Errorf("%d saves for %d accepted blocks, want %d", st.saves, n, want)
	}
	disk, err := LoadState(path)
	if err != nil || len(disk.Blocks) != n {
		t.Fatalf("state on disk: %v, %d blocks, want %d", err, len(disk.Blocks), n)
	}
}

// A stop mid-batch still saves what DevPrism accepted, and nothing it did not.
func TestApplyStopSavesTheAcceptedBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kbsync.json")
	st := NewSyncState(path)
	const failAt = flushEvery + 5 // past one full batch, inside the next
	res, err := Apply(context.Background(), &acceptCaller{failAt: failAt}, addPlan(40), st, nil)
	if err == nil {
		t.Fatal("a failed upsert did not stop the apply")
	}
	if res.Added != failAt-1 {
		t.Fatalf("added %d, want %d", res.Added, failAt-1)
	}
	disk, lerr := LoadState(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(disk.Blocks) != failAt-1 {
		t.Fatalf("%d blocks on disk, want the %d DevPrism accepted", len(disk.Blocks), failAt-1)
	}
	if _, ok := disk.Blocks[fmt.Sprintf("b%03d", failAt-1)]; ok {
		t.Error("the block whose upsert failed was recorded")
	}
}

// A save that fails at the end is reported, not lost behind a nil error.
func TestApplyReportsAFailedFinalSave(t *testing.T) {
	st := NewSyncState(filepath.Join(t.TempDir(), "kbsync.json", "under-a-file"))
	// The parent "kbsync.json" does not exist yet: make it a file so the
	// state directory cannot be created.
	if err := NewSyncState(filepath.Dir(st.path)).Save(); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(context.Background(), &acceptCaller{}, addPlan(3), st, nil)
	if !errors.Is(err, ErrState) {
		t.Fatalf("err %v, want the failed save", err)
	}
	if res.Added != 3 {
		t.Errorf("added %d, want 3 (DevPrism accepted them)", res.Added)
	}
}
