package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A refresh event that heals an unreadable Dropped record must not re-date the
// drop: the digest footer counts "Dropped today" from the record's LastEvent
// (cmd/vani countsFor), and the reconcile code itself says a dropped record gets
// no event for that reason.
func TestW1dadv2HealDoesNotRedateDrop(t *testing.T) {
	l, p := w1dEvicted(t, posting.Dropped, nil)
	before := rec(t, l, p.Key)
	later := t0.Add(72 * time.Hour)
	q := p
	q.Description = w1dText
	s, err := Reconcile(ctx, l, board, []posting.Posting{q}, later)
	if err != nil {
		t.Fatal(err)
	}
	after := rec(t, l, p.Key)
	if after.Status != posting.Dropped {
		t.Fatalf("status %s, want dropped (summary %+v)", after.Status, s)
	}
	if !after.LastEvent.Equal(before.LastEvent) {
		t.Fatalf("a heal moved the dropped record's LastEvent from %s to %s (summary %+v): the digest would count it as dropped on %s",
			before.LastEvent, after.LastEvent, s, after.LastEvent.Format("2006-01-02"))
	}
}

// The heal is one event: it carries the body, so the next run reads it back and
// appends nothing, and the record's annotations and status are untouched.
func TestW1dadv2HealIsOnceAndKeepsAnnotations(t *testing.T) {
	l, p := w1dEvicted(t, posting.Dropped, nil)
	before := rec(t, l, p.Key)
	q := p
	q.Description = w1dText
	if s, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(time.Hour)); err != nil || s.Refreshed != 1 {
		t.Fatalf("first run: %+v, %v", s, err)
	}
	mid := rec(t, l, p.Key)
	for k, v := range before.Annotations {
		if mid.Annotations[k] != v {
			t.Fatalf("annotation %s changed: %q -> %q", k, v, mid.Annotations[k])
		}
	}
	if len(mid.Annotations) != len(before.Annotations) {
		t.Fatalf("annotations %v -> %v", before.Annotations, mid.Annotations)
	}
	full, err := l.Hydrate(p.Key)
	if err != nil || full.Posting.Description != w1dText {
		t.Fatalf("body after the heal %q, %v", full.Posting.Description, err)
	}
	s, err := Reconcile(ctx, l, board, []posting.Posting{q}, t0.Add(2*time.Hour))
	if err != nil || s.Refreshed != 0 || s.Unchanged != 1 {
		t.Fatalf("second run: %+v, %v (a heal repeated)", s, err)
	}
}

var _ = ledger.SchemaVersion
