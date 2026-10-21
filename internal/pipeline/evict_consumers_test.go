package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func reopenLedger(t *testing.T, dir string, old *ledger.File) *ledger.File {
	t.Helper()
	if old != nil {
		old.Close()
	}
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// A Dropped record's reading is inherited by a copy even after a restart has
// evicted the dropped record's description: the cache key is a hash of that
// description, so seed must read it back.
func TestInheritFromDroppedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	l := reopenLedger(t, dir, nil)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", longDesc)
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{okAns("onsite", 0.95)}}
	if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"}).Advance(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if rec(t, l, a.Key).Status != posting.Dropped {
		t.Fatalf("setup: %s, want dropped", rec(t, l, a.Key).Status)
	}
	l = reopenLedger(t, dir, l)
	for _, r := range l.All() {
		if r.Key == a.Key && !r.BodyEvicted {
			t.Fatal("setup: the dropped record's body is resident")
		}
	}
	if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"}).Advance(ctx, b.Key); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, b.Key); tr.calls != 1 || got.Annotations[AnnTriageInheritedFrom] != string(a.Key) {
		t.Fatalf("calls %d, ann %v: the dropped reading was not inherited", tr.calls, got.Annotations)
	}
}

// descExclude excludes postings whose description mentions the word.
func descExclude(word string) func(posting.Posting) (string, bool) {
	return func(p posting.Posting) (string, bool) {
		if strings.Contains(p.Description, word) {
			return "description mentions " + word, true
		}
		return "", false
	}
}

// An exclusion on the description still holds for a record it dropped once a
// restart has evicted that description, and is lifted only when it really no
// longer matches.
func TestApplyExclusionsJudgesEvictedDroppedByItsText(t *testing.T) {
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
	if r := l.All()[0]; !r.BodyEvicted || r.Status != posting.Dropped {
		t.Fatalf("setup: %+v", r)
	}
	ch, err := ApplyExclusions(ctx, l, t0.Add(2*time.Hour), p, nil, nil)
	if err != nil || len(ch.Reopen) != 0 || len(ch.Drop) != 0 {
		t.Fatalf("second pass after restart: %+v %v (the exclusion still matches)", ch, err)
	}
	if rec(t, l, hit.Key).Status != posting.Dropped {
		t.Fatal("an evicted excluded record was reopened")
	}
	ch, err = ApplyExclusions(ctx, l, t0.Add(3*time.Hour), DropPolicy{Exclude: descExclude("nothing")}, nil, nil)
	if err != nil || len(ch.Reopen) != 1 {
		t.Fatalf("lifted exclusion: %+v %v", ch, err)
	}
	if got := rec(t, l, hit.Key); got.Status != posting.Live || got.BodyEvicted || got.Posting.Description != hit.Description {
		t.Fatalf("reopened record: %+v", got)
	}
}
