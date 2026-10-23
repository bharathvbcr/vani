package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The exclusion of an unreadable-body record must not reopen it (a check that
// could not run is not a pass), and must not stop the exclusions of other,
// readable records in the same run.
func TestApplyExclusionsOneUnreadableBodyDoesNotBlockTheRest(t *testing.T) {
	dir := t.TempDir()
	l := reopenLedger(t, dir, nil)
	hit := ghPost(t, "1", "we require relocation to Mars")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{hit}, t0); err != nil {
		t.Fatal(err)
	}
	p := DropPolicy{Exclude: descExclude("relocation")}
	if ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), p, nil, nil); err != nil || len(ch.Drop) != 1 {
		t.Fatalf("first pass: %+v %v", ch, err)
	}
	l = reopenLedger(t, dir, l)
	path := filepath.Join(dir, "events.jsonl")
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(raw, []byte("relocation"), []byte("relocxxxxx"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := ghPost(t, "2", "also requires relocation")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{fresh}, t0.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ch, err := ApplyExclusions(ctx, l, t0.Add(2*time.Hour), p, nil, nil)
	if err != nil || ch.BodyUnavailable != 1 || len(ch.Drop) != 1 || len(ch.Reopen) != 0 {
		t.Fatalf("want 1 unavailable, 1 drop, no reopen, no error: %+v %v", ch, err)
	}
	if got := rec(t, l, hit.Key).Status; got != posting.Dropped {
		t.Fatalf("unreadable excluded record is %s, want dropped (err %v)", got, err)
	}
	if got := rec(t, l, fresh.Key).Status; got != posting.Dropped {
		t.Fatalf("a readable matching posting is %s: one unreadable body blocked the exclusion run (err %v)", got, err)
	}
}
