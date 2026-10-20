package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func batchOf(n int, prefix string) []Event {
	evs := make([]Event, n)
	for i := range evs {
		evs[i] = ev(fmt.Sprintf("gh:%s:%d", prefix, i), posting.Live, t0)
	}
	return evs
}

func fileBytes(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, eventsName))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAppendBatchWritesAndFoldsInOrder(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	evs := batchOf(5, "b")
	if err := f.AppendBatch(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if len(f.All()) != 5 {
		t.Fatalf("%d records, want 5", len(f.All()))
	}
	if n := strings.Count(string(fileBytes(t, dir)), "\n"); n != 5 {
		t.Fatalf("%d lines on disk, want 5", n)
	}
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if len(g.All()) != 5 {
		t.Fatalf("reopened: %d records, want 5", len(g.All()))
	}
}

// One bad event refuses the whole batch: nothing reaches the file or memory,
// and the handle is not poisoned.
func TestAppendBatchValidationIsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	ctx := context.Background()
	evs := batchOf(3, "v")
	evs[2].Status = "bogus"
	if err := f.AppendBatch(ctx, evs); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("err = %v, want ErrInvalidEvent", err)
	}
	if len(f.All()) != 0 || len(fileBytes(t, dir)) != 0 {
		t.Fatalf("a refused batch left %d records, %d bytes", len(f.All()), len(fileBytes(t, dir)))
	}
	if err := f.AppendBatch(ctx, batchOf(2, "ok")); err != nil {
		t.Fatalf("handle poisoned by a refused batch: %v", err)
	}
	long := batchOf(2, "long")
	long[1].Annotations = map[string]string{"a": strings.Repeat("x", MaxLineBytes)}
	if err := f.AppendBatch(ctx, long); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
	if len(f.All()) != 2 {
		t.Fatalf("an over-long line let part of the batch in: %d records", len(f.All()))
	}
}

func TestAppendBatchEmptyDoesNoIO(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	// Even a handle that would fail every write accepts an empty batch.
	f.Close()
	if err := f.AppendBatch(context.Background(), nil); err != nil {
		t.Fatalf("empty batch on a closed ledger: %v", err)
	}
	if err := f.AppendBatch(context.Background(), []Event{}); err != nil {
		t.Fatal(err)
	}
	if len(fileBytes(t, dir)) != 0 {
		t.Fatal("an empty batch wrote bytes")
	}
}

func TestAppendBatchBounds(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	ctx := context.Background()
	if err := f.AppendBatch(ctx, batchOf(MaxBatchEvents+1, "n")); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("too many events: %v, want ErrBatchTooLarge", err)
	}
	// Each line is legal (about 900 KiB) but together they pass MaxBatchBytes.
	big := batchOf(MaxBatchBytes/(900<<10)+2, "big")
	for i := range big {
		big[i].Annotations = map[string]string{"a": strings.Repeat("x", 900<<10)}
	}
	if err := f.AppendBatch(ctx, big); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("too many bytes: %v, want ErrBatchTooLarge", err)
	}
	if len(f.All()) != 0 || len(fileBytes(t, dir)) != 0 {
		t.Fatal("an over-bound batch wrote something")
	}
	if err := f.AppendBatch(ctx, batchOf(MaxBatchEvents, "max")); err != nil {
		t.Fatalf("a batch of exactly MaxBatchEvents: %v", err)
	}
}

func TestAppendBatchSameKeyTwiceLaterWins(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	a := ev("gh:k:1", posting.Live, t0)
	a.Annotations = map[string]string{"x": "1", "y": "keep"}
	b := ev("gh:k:1", posting.Dropped, t0.Add(1))
	b.Annotations = map[string]string{"x": "2"}
	if err := f.AppendBatch(ctx, []Event{a, b}); err != nil {
		t.Fatal(err)
	}
	check := func(f *File) {
		t.Helper()
		r, _ := f.Get("gh:k:1")
		if r.Status != posting.Dropped || r.Annotations["x"] != "2" || r.Annotations["y"] != "keep" {
			t.Fatalf("record = %+v", r)
		}
	}
	check(f)
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	check(g)
}

func TestAppendBatchCutsTornTailFirst(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	if err := f.Append(ctx, ev("gh:t:1", posting.Live, t0)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	fh, err := os.OpenFile(filepath.Join(dir, eventsName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(`{"v":1,"at":"2026-10-01T1`)
	fh.Close()

	g := mustOpen(t, dir)
	if !g.Recovered {
		t.Fatal("torn tail not detected")
	}
	if err := g.AppendBatch(ctx, batchOf(3, "after")); err != nil {
		t.Fatal(err)
	}
	g.Close()
	h := mustOpen(t, dir) // would be ErrCorrupt if the torn bytes stayed mid-file
	defer h.Close()
	if len(h.All()) != 4 {
		t.Fatalf("%d records, want 4", len(h.All()))
	}
}

func TestAppendBatchFailedWritePoisonsAndLeavesMemory(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	if err := f.Append(ctx, ev("gh:w:0", posting.Live, t0)); err != nil {
		t.Fatal(err)
	}
	rw := f.events
	ro, err := os.Open(filepath.Join(dir, eventsName)) // WriteAt and Truncate both fail
	if err != nil {
		t.Fatal(err)
	}
	f.events = ro
	if err := f.AppendBatch(ctx, batchOf(3, "w")); !errors.Is(err, errBroken) {
		t.Fatalf("err = %v, want errBroken", err)
	}
	f.events = rw
	ro.Close()
	if len(f.All()) != 1 {
		t.Fatalf("memory changed by a failed batch: %d records", len(f.All()))
	}
	if err := f.AppendBatch(ctx, batchOf(1, "later")); !errors.Is(err, errBroken) {
		t.Fatalf("batch after poisoning: %v, want errBroken", err)
	}
	if err := f.Append(ctx, ev("gh:w:9", posting.Live, t0)); !errors.Is(err, errBroken) {
		t.Fatalf("append after poisoning: %v, want errBroken", err)
	}
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if len(g.All()) != 1 {
		t.Fatalf("reopened: %d records, want 1", len(g.All()))
	}
}

func TestAppendBatchClosedAndCancelled(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.AppendBatch(cctx, batchOf(2, "c")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if len(f.All()) != 0 || len(fileBytes(t, dir)) != 0 {
		t.Fatal("a cancelled batch wrote something")
	}
	f.Close()
	if err := f.AppendBatch(context.Background(), batchOf(2, "c")); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v, want ErrClosed", err)
	}
}

// A crash between the write and the fsync may leave a prefix of the batch.
// Whole lines of it fold; a torn last line is ignored.
func TestAppendBatchCrashPrefixReplays(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	if err := f.AppendBatch(context.Background(), batchOf(3, "p")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	full := fileBytes(t, dir)
	lines := strings.SplitAfter(string(full), "\n")
	for name, tc := range map[string]struct {
		keep int
		torn bool
		recs int
	}{
		"two whole lines":       {len(lines[0]) + len(lines[1]), false, 2},
		"two lines and a piece": {len(lines[0]) + len(lines[1]) + 10, true, 2},
		"nothing":               {0, false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			cp := t.TempDir()
			if err := os.WriteFile(filepath.Join(cp, eventsName), full[:tc.keep], 0o600); err != nil {
				t.Fatal(err)
			}
			snap, err := ReadSnapshot(cp)
			if err != nil {
				t.Fatal(err)
			}
			if len(snap.Records) != tc.recs || snap.Torn != tc.torn {
				t.Fatalf("%d records torn=%v, want %d torn=%v", len(snap.Records), snap.Torn, tc.recs, tc.torn)
			}
		})
	}
}

func TestAppendAndAppendBatchConcurrent(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	var wg sync.WaitGroup
	const workers, per = 8, 25
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w%2 == 0 {
				for _, e := range batchOf(per, fmt.Sprintf("a%d", w)) {
					if err := f.Append(ctx, e); err != nil {
						t.Error(err)
					}
				}
				return
			}
			evs := batchOf(per, fmt.Sprintf("b%d", w))
			for i := 0; i < per; i += 5 {
				if err := f.AppendBatch(ctx, evs[i:i+5]); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if len(g.All()) != workers*per {
		t.Fatalf("reopened: %d records, want %d", len(g.All()), workers*per)
	}
}

// BenchmarkAppendBatch1000 against BenchmarkAppend1000: the batch does one
// fsync where the loop does 1000 (inferred from the code, not counted).
func BenchmarkAppendBatch1000(b *testing.B) {
	ctx := context.Background()
	evs := batchOf(1000, "bench")
	for b.Loop() {
		b.StopTimer()
		f, err := Open(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := f.AppendBatch(ctx, evs); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		f.Close()
		b.StartTimer()
	}
}

func BenchmarkAppend1000(b *testing.B) {
	ctx := context.Background()
	evs := batchOf(1000, "bench")
	for b.Loop() {
		b.StopTimer()
		f, err := Open(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		for _, e := range evs {
			if err := f.Append(ctx, e); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		f.Close()
		b.StartTimer()
	}
}
