package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The point of ReadSnapshot: a reader works while a run holds the lock, and
// sees exactly what the lock holder sees.
func TestReadSnapshotWhileLocked(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	ctx := context.Background()
	p := posting.Posting{Key: "gh:a:1", Title: "Eng"}
	e := ev("gh:a:1", posting.Scored, t0)
	e.Posting = &p
	e.Annotations = map[string]string{AnnRank: "71"}
	if err := f.Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := f.Append(ctx, ev("gh:a:2", posting.Live, t0)); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Torn || !reflect.DeepEqual(s.Records, f.All()) {
		t.Fatalf("snapshot %+v differs from the locked ledger %+v", s, f.All())
	}
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("the snapshot must not take or drop the lock: %v", err)
	}
}

func TestReadSnapshotStopsAtATornTailAndRepairsNothing(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	f.Append(context.Background(), ev("gh:c:1", posting.Live, t0))
	f.Close()
	path := filepath.Join(dir, "events.jsonl")
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:c:2","sta`)
	fh.Close()
	before, _ := os.ReadFile(path)

	s, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Torn || len(s.Records) != 1 || s.Records[0].Key != "gh:c:1" {
		t.Fatalf("snapshot %+v", s)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("a reader changed the events file")
	}
}

func TestReadSnapshotRefusesMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	good := `{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:c:1","status":"live"}`
	os.WriteFile(path, []byte(good+"\n{garbage}\n"+good+"\n"), 0o600)
	if _, err := ReadSnapshot(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestReadSnapshotOfAMissingLedgerIsAnErrorAndCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	if _, err := ReadSnapshot(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a reader created the ledger directory")
	}
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "events.jsonl"), nil, 0o600)
	s, err := ReadSnapshot(empty)
	if err != nil || len(s.Records) != 0 || s.Torn {
		t.Fatalf("empty ledger: %+v %v", s, err)
	}
}

// Reads racing appends never fail and never see a half-folded event: every
// snapshot is a prefix of the final history.
func TestReadSnapshotDuringAppends(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			e := ev(fmt.Sprintf("gh:r:%d", i), posting.Live, t0.Add(time.Duration(i)*time.Second))
			e.Annotations = map[string]string{"i": fmt.Sprint(i)}
			if err := f.Append(context.Background(), e); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	last := 0
	for i := 0; i < 50; i++ {
		s, err := ReadSnapshot(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Records) < last {
			t.Fatalf("snapshot went backwards: %d after %d", len(s.Records), last)
		}
		last = len(s.Records)
		for _, r := range s.Records {
			if r.Annotations["i"] == "" {
				t.Fatalf("half-folded record %+v", r)
			}
		}
	}
	wg.Wait()
	s, err := ReadSnapshot(dir)
	if err != nil || len(s.Records) != n {
		t.Fatalf("final snapshot: %d records, %v", len(s.Records), err)
	}
}
