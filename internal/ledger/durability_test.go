package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A last line that is whole JSON but not a valid event is a finished line
// from some writer (here a newer schema), not a torn write. It used to be
// reported as Recovered and cut off by the next Append.
func TestCompleteInvalidLastLineIsCorrupt(t *testing.T) {
	for name, line := range map[string]string{
		"newer schema": `{"v":2,"at":"2026-10-01T12:00:00Z","key":"gh:c:2","status":"live"}`,
		"unknown key":  `{"v":1,"at":"2026-10-01T12:00:00Z","key":"zz:c:2","status":"live"}`,
		"wrong type":   `{"v":"one","at":"2026-10-01T12:00:00Z","key":"gh:c:2","status":"live"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := mustOpen(t, dir)
			if err := f.Append(context.Background(), ev("gh:c:1", posting.Live, t0)); err != nil {
				t.Fatal(err)
			}
			f.Close()
			path := filepath.Join(dir, eventsName)
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fh.WriteString(line + "\n"); err != nil {
				t.Fatal(err)
			}
			fh.Close()
			before, _ := os.ReadFile(path)

			if g, err := Open(dir); !errors.Is(err, ErrCorrupt) {
				if g != nil {
					g.Close()
				}
				t.Fatalf("Open: %v, want ErrCorrupt", err)
			}
			if _, err := ReadSnapshot(dir); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("ReadSnapshot: %v, want ErrCorrupt", err)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatal("the refused line was altered")
			}
		})
	}
}

// A write that fails and whose partial line cannot be cut leaves the file
// past goodSize unknown. The old code dropped the Truncate error and kept
// appending at goodSize, which could leave a complete garbage line mid-file.
// Now the handle refuses every later Append until the ledger is reopened.
func TestUncutFailedWritePoisonsTheHandle(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	if err := f.Append(ctx, ev("gh:p:1", posting.Live, t0)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, eventsName)
	rw := f.events
	ro, err := os.Open(path) // WriteAt and Truncate both fail on it
	if err != nil {
		t.Fatal(err)
	}
	f.events = ro
	if err := f.Append(ctx, ev("gh:p:2", posting.Live, t0)); !errors.Is(err, errBroken) {
		t.Fatalf("failed, uncut write: %v, want errBroken", err)
	}
	f.events = rw // the file is writable again; the handle still must not trust it
	ro.Close()
	if err := f.Append(ctx, ev("gh:p:3", posting.Live, t0)); !errors.Is(err, errBroken) {
		t.Fatalf("append after poisoning: %v, want errBroken", err)
	}
	if err := f.Broken(); !errors.Is(err, errBroken) {
		t.Fatalf("Broken() = %v, want the poison", err)
	}
	if _, ok := f.Get("gh:p:1"); !ok || len(f.All()) != 1 {
		t.Fatalf("memory should still hold the one acknowledged event: %v", f.All())
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	g := mustOpen(t, dir)
	defer g.Close()
	if err := g.Broken(); err != nil {
		t.Fatalf("a reopened ledger is still broken: %v", err)
	}
	if len(g.All()) != 1 {
		t.Fatalf("reopened: %d records, want 1", len(g.All()))
	}
	if err := g.Append(ctx, ev("gh:p:4", posting.Live, t0)); err != nil {
		t.Fatalf("a reopened ledger appends again: %v", err)
	}
}

// An event refused before anything is written does not poison the handle.
func TestRefusedEventDoesNotPoison(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	ctx := context.Background()
	e := ev("gh:q:1", posting.Live, t0)
	e.Annotations = map[string]string{MissedRunsKey: "-1"} // refused before any write
	if err := f.Append(ctx, e); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid event: %v", err)
	}
	if err := f.Append(ctx, ev("gh:q:2", posting.Live, t0)); err != nil {
		t.Fatalf("append after a refused event: %v", err)
	}
}
