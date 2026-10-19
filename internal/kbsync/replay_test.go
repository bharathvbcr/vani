package kbsync

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sentBlocks returns each upserted block's bytes by id, from the fake's calls
// after skip.
func sentBlocks(t *testing.T, f *fakeDevPrism, skip int) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range f.upserts()[skip:] {
		var in struct {
			Block json.RawMessage `json:"block"`
		}
		if err := json.Unmarshal(c.Args, &in); err != nil {
			t.Fatal(err)
		}
		var id struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(in.Block, &id); err != nil {
			t.Fatal(err)
		}
		out[id.ID] = string(in.Block)
	}
	return out
}

// A crash between DevPrism accepting a batch and the state save re-sends the
// batch on the next run. That replay must be byte-identical to the first
// send, updatedAt included: it used to carry the replaying run's time.
func TestReplayedBatchIsByteIdentical(t *testing.T) {
	f := newFake()
	r := fixtureResolved(t)
	t1, t2 := fixedNow, fixedNow.Add(26*time.Hour)

	cur, err := FetchState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	lost := NewSyncState("") // never saved: the crash
	if _, err := Apply(context.Background(), f, Plan(r, cur, *lost, t1), &SyncState{Blocks: map[string]string{}, Synced: map[string]string{}, path: t.TempDir() + "/s.json"}, nil); err != nil {
		t.Fatal(err)
	}
	first := sentBlocks(t, f, 0)
	if len(first) != 3 {
		t.Fatalf("first send: %d blocks", len(first))
	}

	cur, err = FetchState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan(r, cur, *lost, t2)
	for _, it := range p.Items {
		if it.Action != ActionUpdate {
			t.Fatalf("replayed %s as %s, want an update of the block DevPrism already holds", it.ID, it.Action)
		}
		// DevPrism parses the block into its own struct, so the replay must
		// carry the same JSON value; key order is whatever DevPrism's copy has
		// (this fake re-sorts keys), but every value and updatedAt's exact
		// string must match the first send.
		if it.Payload == nil || !sameJSON(t, it.Payload, []byte(first[it.ID])) {
			t.Errorf("%s replay differs from the first send:\nfirst:  %s\nreplay: %s", it.ID, first[it.ID], it.Payload)
		}
		var a, b struct {
			UpdatedAt json.RawMessage `json:"updatedAt"`
		}
		if json.Unmarshal(it.Payload, &a) != nil || json.Unmarshal([]byte(first[it.ID]), &b) != nil || string(a.UpdatedAt) != string(b.UpdatedAt) {
			t.Errorf("%s updatedAt %s, first sent %s", it.ID, a.UpdatedAt, b.UpdatedAt)
		}
	}
}

func sameJSON(t *testing.T, x, y []byte) bool {
	t.Helper()
	var a, b any
	if json.Unmarshal(x, &a) != nil || json.Unmarshal(y, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// A real change still gets the time of the run that made it.
func TestChangedBlockGetsANewStamp(t *testing.T) {
	f := newFake()
	r := fixtureResolved(t)
	st := NewSyncState(t.TempDir() + "/s.json")
	cur, _ := FetchState(context.Background(), f)
	if _, err := Apply(context.Background(), f, Plan(r, cur, *st, fixedNow), st, nil); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(fixtureBank, "Handled {{clmQps}} requests.", "Handled {{clmQps}} requests daily.", 1)
	later := fixedNow.Add(48 * time.Hour)
	cur, _ = FetchState(context.Background(), f)
	p := Plan(ResolveBank(mustBank(t, changed), fixtureClaims), cur, *st, later)
	n := 0
	for _, it := range p.Items {
		if it.Action != ActionUpdate {
			continue
		}
		n++
		var blk struct {
			UpdatedAt string `json:"updatedAt"`
		}
		if err := json.Unmarshal(it.Payload, &blk); err != nil || blk.UpdatedAt != stamp(later) {
			t.Errorf("%s: updatedAt %q, %v; want %q", it.ID, blk.UpdatedAt, err, stamp(later))
		}
	}
	if n != 1 {
		t.Fatalf("%d updates, want the one changed block", n)
	}
}
