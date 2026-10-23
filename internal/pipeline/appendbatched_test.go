package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type failSecond struct {
	ledger.Ledger
	calls int
}

func (f *failSecond) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	f.calls++
	if f.calls == 2 {
		return errors.New("disk full")
	}
	return f.Ledger.AppendBatch(ctx, evs)
}

// A batch that fails after earlier ones were written says how many events were
// already written.
func TestMarkDigestedPartialFailureReportsWritten(t *testing.T) {
	l, keys, at := advLedger(t, ledger.MaxBatchEvents+5, "")
	err := MarkDigested(context.Background(), &failSecond{Ledger: l}, keys, at, "digest")
	want := fmt.Sprintf("%d of %d written", ledger.MaxBatchEvents, len(keys))
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want it to say %q and wrap the cause", err, want)
	}
	if got := digestedCount(l, keys); got != ledger.MaxBatchEvents {
		t.Errorf("%d digested, want %d", got, ledger.MaxBatchEvents)
	}
}

// An event that alone exceeds the byte limit is an error naming its key; the
// events before it are written and none after.
func TestAppendBatchedOversizedEventNamesItsKey(t *testing.T) {
	l, keys, at := advLedger(t, 3, "")
	mk := func(k posting.Key, reason string) ledger.Event {
		return ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Digested, Reason: reason}
	}
	evs := []ledger.Event{mk(keys[0], "digest"), mk(keys[1], strings.Repeat("r", ledger.MaxBatchBytes)), mk(keys[2], "digest")}
	n, err := appendBatched(context.Background(), l, evs)
	if err == nil || !strings.Contains(err.Error(), string(keys[1])) {
		t.Fatalf("err = %v, want it to name %q", err, keys[1])
	}
	if n != 1 || digestedCount(l, keys) != 1 {
		t.Errorf("written %d, digested %d; want 1 and 1", n, digestedCount(l, keys))
	}
}
