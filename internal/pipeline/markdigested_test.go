package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// writeCounter counts the durable writes (one fsync each) made through it.
type writeCounter struct {
	ledger.Ledger
	appends, batches, events int
}

func (w *writeCounter) Append(ctx context.Context, e ledger.Event) error {
	w.appends++
	w.events++
	return w.Ledger.Append(ctx, e)
}

func (w *writeCounter) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	w.batches++
	w.events += len(evs)
	return w.Ledger.AppendBatch(ctx, evs)
}

// MarkDigested is the one path that marks a digest's roles: batches of at most
// ledger.MaxBatchEvents, never one fsync per role, nothing written for a digest
// that names an unknown key, and a role marked twice is marked once.
func TestMarkDigestedBatches(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	n := ledger.MaxBatchEvents + 1
	keys := make([]posting.Key, n)
	seed := make([]ledger.Event, n)
	for i := range keys {
		keys[i] = posting.Key(fmt.Sprintf("gh:acme:%d", i))
		p := posting.Posting{Key: keys[i], ATS: "gh", Company: "Acme", Title: fmt.Sprintf("Engineer %d", i)}
		seed[i] = ledger.Event{V: ledger.SchemaVersion, At: at, Key: keys[i], Status: posting.Live, Posting: &p}
	}
	for i := 0; i < n; i += ledger.MaxBatchEvents {
		if err := l.AppendBatch(ctx, seed[i:min(i+ledger.MaxBatchEvents, n)]); err != nil {
			t.Fatal(err)
		}
	}
	w := &writeCounter{Ledger: l}

	if err := MarkDigested(ctx, w, append([]posting.Key{"gh:acme:nope"}, keys...), at, "digest"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if w.events != 0 {
		t.Fatalf("%d events written for a digest that names an unknown key", w.events)
	}

	if err := MarkDigested(ctx, w, append(keys[:3:3], keys...), at, "digest"); err != nil {
		t.Fatal(err)
	}
	if w.appends != 0 || w.batches != 2 || w.events != n {
		t.Errorf("%d appends, %d batches, %d events for %d roles; want 0, 2 and %d", w.appends, w.batches, w.events, n, n)
	}
	for _, k := range []posting.Key{keys[0], keys[n-1]} {
		if r, _ := l.Get(k); r.Status != posting.Digested {
			t.Errorf("%s is %s", k, r.Status)
		}
	}
	// Marked already: nothing to write.
	if err := MarkDigested(ctx, w, keys, at, "digest"); err != nil || w.events != n {
		t.Errorf("a second marking wrote %d more events (err %v)", w.events-n, err)
	}
}
