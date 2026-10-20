package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// countingLedger counts the calls that reach the ledger, and can refuse batches.
type countingLedger struct {
	ledger.Ledger
	appends, batches, batched int
	failBatch                 error
}

func (c *countingLedger) Append(cx context.Context, e ledger.Event) error {
	c.appends++
	return c.Ledger.Append(cx, e)
}

func (c *countingLedger) AppendBatch(cx context.Context, evs []ledger.Event) error {
	c.batches++
	if c.failBatch != nil {
		return c.failBatch
	}
	c.batched += len(evs)
	return c.Ledger.AppendBatch(cx, evs)
}

// The write phase is one fsync-bearing batch, not one Append per event.
func TestNearDupWritesOneBatch(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t1)
	ndAdd(t, l, "3", "Beta", "Designer", "Remote", words("art", 100), t0)
	ndAdd(t, l, "4", "Beta", "Designer", "Remote", words("art", 100), t1)
	c := &countingLedger{Ledger: l}
	rep := annotate(t, c, t1, NearDupOptions{})
	if rep.Written != 2 || c.batches != 1 || c.batched != 2 || c.appends != 0 {
		t.Fatalf("written %d, %d batches of %d events, %d single appends", rep.Written, c.batches, c.batched, c.appends)
	}
}

// A refused batch leaves nothing written and reports so.
func TestNearDupBatchErrorLeavesWrittenZero(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	second := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t1)
	c := &countingLedger{Ledger: l, failBatch: errors.New("disk full")}
	rep, err := AnnotateNearDuplicates(ctx, c, t1, NearDupOptions{})
	if err == nil || rep.Written != 0 {
		t.Fatalf("err %v, written %d", err, rep.Written)
	}
	wantLive(t, l, second)
}
