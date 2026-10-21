package pipeline

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// failNth fails the nth AppendBatch (1-based) and passes the others through.
type failNth struct {
	ledger.Ledger
	n, calls int
	err      error
}

func (f *failNth) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	f.calls++
	if f.calls == f.n {
		return f.err
	}
	return f.Ledger.AppendBatch(ctx, evs)
}

// advLedger holds n Live postings whose keys carry keyPad.
func advLedger(t *testing.T, n int, keyPad string) (*ledger.File, []posting.Key, time.Time) {
	t.Helper()
	ctx := context.Background()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	keys := make([]posting.Key, n)
	seed := make([]ledger.Event, n)
	for i := range keys {
		keys[i] = posting.Key(fmt.Sprintf("gh:acme:%d%s", i, keyPad))
		p := posting.Posting{Key: keys[i], ATS: "gh", Company: "Acme", Title: fmt.Sprintf("Engineer %d", i)}
		seed[i] = ledger.Event{V: ledger.SchemaVersion, At: at, Key: keys[i], Status: posting.Live, Posting: &p}
	}
	const chunk = 256
	for i := 0; i < n; i += chunk {
		if err := l.AppendBatch(ctx, seed[i:min(i+chunk, n)]); err != nil {
			t.Fatal(err)
		}
	}
	return l, keys, at
}

func digestedCount(l ledger.Ledger, keys []posting.Key) int {
	n := 0
	for _, k := range keys {
		if r, _ := l.Get(k); r.Status == posting.Digested {
			n++
		}
	}
	return n
}

// Exactly 4096, 4097 and 8193 events split into 1, 2 and 3 batches with nothing
// lost or repeated.
func TestAdvMarkDigestedBatchBoundaries(t *testing.T) {
	for _, tc := range []struct{ n, batches int }{{ledger.MaxBatchEvents, 1}, {ledger.MaxBatchEvents + 1, 2}, {2*ledger.MaxBatchEvents + 1, 3}} {
		l, keys, at := advLedger(t, tc.n, "")
		w := &writeCounter{Ledger: l}
		if err := MarkDigested(context.Background(), w, keys, at, "digest"); err != nil {
			t.Fatal(err)
		}
		if w.batches != tc.batches || w.events != tc.n || digestedCount(l, keys) != tc.n {
			t.Errorf("n=%d: %d batches, %d events, %d digested; want %d batches", tc.n, w.batches, w.events, digestedCount(l, keys), tc.batches)
		}
	}
}

// The second batch fails: the first batch is durable, the error is the one
// error, the rest is untouched, and a retry marks only the rest.
func TestAdvMarkDigestedSecondBatchFails(t *testing.T) {
	l, keys, at := advLedger(t, ledger.MaxBatchEvents+10, "")
	boom := errors.New("disk full")
	f := &failNth{Ledger: l, n: 2, err: boom}
	err := MarkDigested(context.Background(), f, keys, at, "digest")
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want the batch failure", err)
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		t.Fatalf("%d joined errors, want a single error", len(u.Unwrap()))
	}
	if got := digestedCount(l, keys); got != ledger.MaxBatchEvents {
		t.Fatalf("%d digested after a failed second batch, want exactly the first batch (%d)", got, ledger.MaxBatchEvents)
	}
	w := &writeCounter{Ledger: l}
	if err := MarkDigested(context.Background(), w, keys, at, "digest"); err != nil || w.events != 10 {
		t.Fatalf("retry: %v, %d events written, want the 10 left", err, w.events)
	}
}

// An empty digest writes nothing and is not an error; a nil slice too.
func TestAdvMarkDigestedEmpty(t *testing.T) {
	l, _, at := advLedger(t, 1, "")
	w := &writeCounter{Ledger: l}
	for _, keys := range [][]posting.Key{nil, {}} {
		if err := MarkDigested(context.Background(), w, keys, at, "digest"); err != nil {
			t.Fatal(err)
		}
	}
	if w.appends+w.batches != 0 {
		t.Fatalf("%d writes for an empty digest", w.appends+w.batches)
	}
}

// A cancelled context writes nothing.
func TestAdvMarkDigestedCancelled(t *testing.T) {
	l, keys, at := advLedger(t, 5, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := MarkDigested(ctx, l, keys, at, "digest"); err == nil {
		t.Fatal("no error under a cancelled context")
	}
	if digestedCount(l, keys) != 0 {
		t.Fatal("events were written under a cancelled context")
	}
}
