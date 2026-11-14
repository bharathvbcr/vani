package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func failFileSyncs(l *synctest.Log, err error) {
	l.Fail(func(_ string, d bool) error {
		if d {
			return nil
		}
		return err
	})
}

func TestGroupSyncsOnceAtTheEndAndReadsItsOwnWrites(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	log := synctest.Record(t, dir)
	evs := batchOf(150, "g")
	err := f.Group(t.Context(), func(g Ledger) error {
		for _, e := range evs[:100] {
			if err := g.Append(t.Context(), e); err != nil {
				return err
			}
		}
		if err := g.AppendBatch(t.Context(), evs[100:]); err != nil {
			return err
		}
		if n := len(log.Files()); n != 0 {
			t.Errorf("%d syncs inside the group, want none", n)
		}
		if _, ok := g.Get(evs[42].Key); !ok {
			t.Error("the group did not read its own write")
		}
		if n := len(g.All()); n != 150 {
			t.Errorf("All inside the group: %d, want 150", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("%d syncs for a group of 150 events, want 1", n)
	}
}

func TestGroupSyncsAtTheEventBound(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	log := synctest.Record(t, dir)
	evs := batchOf(MaxBatchEvents+1, "b")
	if err := f.Group(t.Context(), func(g Ledger) error {
		for _, e := range evs {
			if err := g.Append(t.Context(), e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(log.Files()); n != 2 {
		t.Fatalf("%d syncs for MaxBatchEvents+1 events, want 2 (the bound, then the end)", n)
	}
}

// A failed group fsync leaves nothing that is not durable: the file is cut to
// the last synced line, memory equals a fresh Open of it, and the ledger is
// broken for the handle and the File alike.
func TestGroupFailedSyncRestoresMemoryToTheSyncedLines(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	durable := batchOf(2, "d")
	if err := f.AppendBatch(t.Context(), durable); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(eventsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	log := synctest.Record(t, dir)
	boom := errors.New("fsync: boom")
	failFileSyncs(log, boom)
	var handle Ledger
	err = f.Group(t.Context(), func(g Ledger) error {
		handle = g
		for _, e := range batchOf(5, "lost") {
			if err := g.Append(t.Context(), e); err != nil {
				return err
			}
		}
		return nil
	})
	if !errors.Is(err, errBroken) {
		t.Fatalf("group over a failed fsync: %v, want errBroken", err)
	}
	if got := len(f.All()); got != 2 {
		t.Fatalf("memory holds %d records after the failed sync, want the 2 durable", got)
	}
	if _, ok := f.Get(posting.Key("gh:lost:0")); ok {
		t.Fatal("a record that never became durable is still answered")
	}
	if now, err := os.Stat(eventsPath(dir)); err != nil || now.Size() != st.Size() {
		t.Fatalf("file is %v bytes, want cut back to %d", now.Size(), st.Size())
	}
	log.Fail(nil)
	if err := handle.Append(t.Context(), ev("gh:x:1", posting.Live, t0)); !errors.Is(err, errBroken) {
		t.Fatalf("the group's handle took an append on a broken ledger: %v", err)
	}
	if err := f.Append(t.Context(), ev("gh:x:1", posting.Live, t0)); !errors.Is(err, errBroken) {
		t.Fatalf("the File took an append on a broken ledger: %v", err)
	}
	mem := f.All()
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if !reflect.DeepEqual(mem, g.All()) {
		t.Fatal("memory after the failed sync differs from a fresh Open")
	}
}

// The lock-free Reader can fold a group's lines before they are synced (it
// reads the file, not the File). When the group's sync fails and the file is
// cut back by many lines, the Reader's next Advance sees the file shrank and
// folds it again: it ends with what the File holds, not the lost lines.
func TestReaderRefoldsAfterAGroupSizedRollback(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	if err := f.AppendBatch(t.Context(), batchOf(3, "kept")); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	log := synctest.Record(t, dir)
	err = f.Group(t.Context(), func(g Ledger) error {
		for _, e := range batchOf(200, "lost") {
			if err := g.Append(t.Context(), e); err != nil {
				return err
			}
		}
		if _, err := r.Advance(); err != nil {
			return err
		}
		if n := len(r.Snapshot().Records); n != 203 {
			t.Errorf("the Reader folded %d records mid-group, want 203 (it reads unsynced lines)", n)
		}
		failFileSyncs(log, errors.New("fsync: boom"))
		return nil
	})
	if !errors.Is(err, errBroken) {
		t.Fatalf("group: %v, want errBroken", err)
	}
	changed, err := r.Advance()
	if err != nil || !changed {
		t.Fatalf("Advance after the rollback: changed %v, %v", changed, err)
	}
	if got := r.Snapshot().Records; len(got) != 3 || len(f.All()) != 3 {
		t.Fatalf("Reader %d records, File %d; want both at the 3 durable", len(got), len(f.All()))
	}
}

// The sync at the bound can fail too, mid-group: the group's later appends are
// refused and memory is cut back as at the end.
func TestGroupFailedSyncAtTheBound(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	log := synctest.Record(t, dir)
	failFileSyncs(log, errors.New("fsync: boom"))
	evs := batchOf(MaxBatchEvents+3, "m")
	var refused int
	err := f.Group(t.Context(), func(g Ledger) error {
		for _, e := range evs {
			if err := g.Append(t.Context(), e); err != nil {
				refused++
			}
		}
		return nil
	})
	// The append that reaches the bound gets the failed sync; the 3 after it
	// find the ledger broken.
	if !errors.Is(err, errBroken) || refused != 4 {
		t.Fatalf("err %v, %d refused; want errBroken and 4 refused", err, refused)
	}
	if n := len(f.All()); n != 0 {
		t.Fatalf("%d records answered, none durable", n)
	}
}

// The closing sync runs however fn ends, and fn's own error comes back as it
// was (the caller tells a failed sync from fn's error by identity).
func TestGroupSyncsWhenFnFailsIsCancelledOrPanics(t *testing.T) {
	boom := errors.New("fn: boom")
	for _, end := range []string{"error", "cancel", "panic"} {
		t.Run(end, func(t *testing.T) {
			dir := t.TempDir()
			f := mustOpen(t, dir)
			log := synctest.Record(t, dir)
			ctx, cancel := context.WithCancel(t.Context())
			evs := batchOf(4, "e")
			run := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("panicked: %v", r)
					}
				}()
				return f.Group(ctx, func(g Ledger) error {
					for _, e := range evs[:3] {
						if err := g.Append(ctx, e); err != nil {
							return err
						}
					}
					switch end {
					case "error":
						return boom
					case "cancel":
						cancel()
						return g.Append(ctx, evs[3])
					default:
						panic("fn")
					}
				})
			}
			err := run()
			switch end {
			case "error":
				if err != boom {
					t.Fatalf("err %v, want fn's error itself", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err %v, want the cancellation", err)
				}
			default:
				if err == nil {
					t.Fatal("the panic was swallowed")
				}
			}
			if n := len(log.Files()); n != 1 {
				t.Fatalf("%d syncs, want the closing one", n)
			}
			f.Close()
			g := mustOpen(t, dir)
			defer g.Close()
			if n := len(g.All()); n != 3 {
				t.Fatalf("reopened: %d records, want the 3 the group wrote", n)
			}
		})
	}
}

// A direct Append while a group is open syncs, as always, and that sync covers
// the group's earlier lines: the group's end then has nothing left to sync.
func TestDirectAppendInsideAGroupIsDurable(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	log := synctest.Record(t, dir)
	evs := batchOf(3, "x")
	if err := f.Group(t.Context(), func(g Ledger) error {
		if err := g.Append(t.Context(), evs[0]); err != nil {
			return err
		}
		if err := g.Append(t.Context(), evs[1]); err != nil {
			return err
		}
		if err := f.Append(t.Context(), evs[2]); err != nil {
			return err
		}
		if n := len(log.Files()); n != 1 {
			t.Errorf("%d syncs after the direct append, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("%d syncs in all, want 1: the direct append's covered the group", n)
	}
}

func TestCloseInsideAGroupIsRefusedAndCloseAfterSyncs(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	log := synctest.Record(t, dir)
	if err := f.Group(t.Context(), func(g Ledger) error {
		if err := g.Close(); !errors.Is(err, errCloseInGroup) {
			t.Errorf("Close through the group: %v", err)
		}
		return g.Append(t.Context(), ev("gh:c:1", posting.Live, t0))
	}); err != nil {
		t.Fatal(err)
	}
	// A group left open on another goroutine: Close syncs what it wrote.
	release := make(chan struct{})
	done := make(chan error, 1)
	wrote := make(chan struct{})
	go func() {
		done <- f.Group(t.Context(), func(g Ledger) error {
			err := g.Append(t.Context(), ev("gh:c:2", posting.Live, t0))
			close(wrote)
			<-release
			return err
		})
	}()
	<-wrote
	log.Reset()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if n := len(log.Files()); n != 1 {
		t.Fatalf("Close made %d syncs of the open group's line, want 1", n)
	}
	g := mustOpen(t, dir)
	defer g.Close()
	if n := len(g.All()); n != 2 {
		t.Fatalf("reopened: %d, want both", n)
	}
}

// A group folds exactly what the same events appended one by one fold, and
// writes the same bytes, whatever its boundaries.
func TestGroupMatchesSequentialAppends(t *testing.T) {
	for seed := range uint64(30) {
		rng := rand.New(rand.NewPCG(seed, 5))
		var evs []Event
		for i := range 40 + rng.IntN(60) {
			key := fmt.Sprintf("gh:s%d:%d", seed, rng.IntN(25))
			st := []posting.Status{posting.Live, posting.Triaged, posting.Scored, posting.Dropped, posting.Dead}[rng.IntN(5)]
			e := ev(key, st, t0.Add(time.Duration(i)*time.Minute))
			if rng.IntN(3) == 0 {
				e.Annotations = map[string]string{"n": fmt.Sprint(i)}
			}
			evs = append(evs, e)
		}
		seqDir, grpDir := t.TempDir(), t.TempDir()
		s := mustOpen(t, seqDir)
		for _, e := range evs {
			if err := s.Append(t.Context(), e); err != nil {
				t.Fatal(err)
			}
		}
		g := mustOpen(t, grpDir)
		for rest := evs; len(rest) > 0; {
			n := 1 + rng.IntN(len(rest))
			chunk := rest[:n]
			rest = rest[n:]
			if err := g.Group(t.Context(), func(l Ledger) error {
				for _, e := range chunk {
					if rng.IntN(4) == 0 {
						if err := l.AppendBatch(t.Context(), []Event{e}); err != nil {
							return err
						}
						continue
					}
					if err := l.Append(t.Context(), e); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(s.All(), g.All()) {
			t.Fatalf("seed %d: grouped fold differs from sequential", seed)
		}
		s.Close()
		g.Close()
		if a, b := fileBytes(t, seqDir), fileBytes(t, grpDir); string(a) != string(b) {
			t.Fatalf("seed %d: grouped file differs from sequential", seed)
		}
	}
}
