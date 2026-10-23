package inbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fillSpool puts n valid-looking batch files in dir without going through
// Write (a thousand fsyncs buys nothing here).
func fillSpool(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		name := filepath.Join(dir, fmt.Sprintf("%020d-fill%04d.json", i+1, i))
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteRefusesThe1001stBatchWithErrSpoolFull(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, dir, MaxPending-1)
	if _, err := Write(dir, batchOf("acme", job(1))); err != nil {
		t.Fatalf("the %dth batch must still be accepted: %v", MaxPending, err)
	}
	if n, _ := Pending(dir); n != MaxPending {
		t.Fatalf("pending %d, want %d", n, MaxPending)
	}
	_, err := Write(dir, batchOf("acme", job(2)))
	if !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("the %dst batch: err %v, want ErrSpoolFull", MaxPending+1, err)
	}
	if n, _ := Pending(dir); n != MaxPending {
		t.Fatalf("a refused write left a file: pending %d", n)
	}
}

func TestSpoolFullClearsAfterADrain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, dir, MaxPending)
	if _, err := Write(dir, batchOf("acme", job(1))); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("err %v", err)
	}
	if _, err := Drain(context.Background(), dir, MaxDrainFiles, func(Batch) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, batchOf("acme", job(1))); err != nil {
		t.Fatalf("after a drain the spool takes batches again: %v", err)
	}
}

func TestWriteIgnoresBadDirAndDotfilesInTheCap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, dir, MaxPending-1)
	fillSpool(t, filepath.Join(dir, BadDir), 300)
	if err := os.WriteFile(filepath.Join(dir, ".tmp.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, batchOf("acme", job(1))); err != nil {
		t.Fatalf("only pending batches count toward the cap: %v", err)
	}
}

func TestWriteToMissingDirStillCreatesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never", Dir)
	if _, err := Write(dir, batchOf("acme", job(1))); err != nil {
		t.Fatal(err)
	}
}

func TestDrainPrunesBadToTheNewest200(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	const n = MaxBadFiles + 40
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d-junk.json", i+1)), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		// File i+1 is spooled i minutes after the first.
		if err := os.Chtimes(filepath.Join(dir, fmt.Sprintf("%020d-junk.json", i+1)), base, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Drain(context.Background(), dir, n, func(Batch) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(dir, BadDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > MaxBadFiles {
		t.Fatalf("bad/ holds %d files after a drain, want at most %d", len(ents), MaxBadFiles)
	}
	// The newest survive, the oldest are gone.
	for i := range n {
		_, err := os.Stat(filepath.Join(dir, BadDir, fmt.Sprintf("%020d-junk.json", i+1)))
		kept := err == nil
		if want := i >= n-MaxBadFiles; kept != want {
			t.Fatalf("file %d kept=%v, want %v", i+1, kept, want)
		}
	}
}

func TestPruneBadTieBreaksByNameDeterministically(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, BadDir)
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	same := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const n = MaxBadFiles + 5
	for i := range n {
		p := filepath.Join(bad, fmt.Sprintf("f%04d.json", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, same, same); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBad(dir); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		_, err := os.Stat(filepath.Join(bad, fmt.Sprintf("f%04d.json", i)))
		if kept, want := err == nil, i >= 5; kept != want {
			t.Fatalf("f%04d kept=%v want %v: equal mtimes must drop the lowest names first", i, kept, want)
		}
	}
}

func TestPruneBadEdgeCases(t *testing.T) {
	if err := pruneBad(t.TempDir()); err != nil {
		t.Fatalf("no bad dir: %v", err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, BadDir)
	if err := os.MkdirAll(filepath.Join(bad, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pruneBad(dir); err != nil {
		t.Fatalf("empty bad dir with a subdirectory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bad, "subdir")); err != nil {
		t.Fatal("pruning must leave directories alone")
	}
}
