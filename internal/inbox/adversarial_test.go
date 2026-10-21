package inbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPendingCountsOnlyRegularNonDotJSONFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, dir, 3)
	// leftovers an interrupted atomic write can leave, a subdirectory and a
	// symlink with a .json name, a non-json file, and bad/.
	for _, name := range []string{".x.json.tmp-123", ".hidden.json", "note.txt", ".drain.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "note.txt"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, BadDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if n, err := Pending(dir); err != nil || n != 3 {
		t.Fatalf("pending %d err %v, want 3", n, err)
	}
}

// Measures how far concurrent writers overshoot the cap (documented as a
// bound, not a quota). Writers can pass it by at most the number of writers.
func TestConcurrentWritersOvershootIsBoundedByWriterCount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, dir, MaxPending-1)
	const writers = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, full int
	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := Write(dir, batchOf("acme", job(i+1)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrSpoolFull):
				full++
			default:
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	n, _ := Pending(dir)
	t.Logf("50 racing writers on a spool at %d: accepted %d, refused %d, final pending %d (overshoot %d)", MaxPending-1, ok, full, n, n-MaxPending)
	if n-MaxPending > writers-1 {
		t.Fatalf("overshoot %d exceeds writers-1", n-MaxPending)
	}
	if ok+full != writers {
		t.Fatalf("lost a writer: ok %d full %d", ok, full)
	}
}

func TestDrainWhereBadIsARegularFileSurfacesAnErrorAndStillFolds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BadDir), []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(dir, batchOf("acme", job(1))); err != nil {
		t.Fatal(err)
	}
	var folded int
	rep, err := Drain(context.Background(), dir, 10, func(Batch) error { folded++; return nil })
	t.Logf("folded %d report %+v err %v", folded, rep, err)
	if folded != 1 || rep.Folded != 1 {
		t.Fatalf("the good batch must still fold: folded %d rep %+v", folded, rep)
	}
	if err == nil {
		t.Fatal("an unprunable bad/ must be reported, not swallowed")
	}
}

func TestDrainWhereBadIsUnreadableReportsPruneError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, filepath.Join(dir, BadDir), MaxBadFiles+3)
	if err := os.Chmod(filepath.Join(dir, BadDir), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, BadDir), 0o700) })
	_, err := Drain(context.Background(), dir, 10, func(Batch) error { return nil })
	if err == nil {
		t.Fatal("unreadable bad/ must surface an error")
	}
}

// Pruning removes only the surplus when some bad files are mid-write
// (dot-prefixed temp files count as files too and are never lost silently).
func TestPruneBadKeepsExactlyMaxAndNeverTouchesOthers(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, BadDir)
	fillSpool(t, bad, MaxBadFiles+10)
	if err := os.Mkdir(filepath.Join(bad, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pruneBad(dir); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(bad)
	files := 0
	for _, e := range ents {
		if e.Type().IsRegular() {
			files++
		}
	}
	if files != MaxBadFiles {
		t.Fatalf("%d files kept, want %d", files, MaxBadFiles)
	}
	if _, err := os.Stat(filepath.Join(bad, "sub")); err != nil {
		t.Fatal("subdir removed")
	}
	// Idempotent.
	if err := pruneBad(dir); err != nil {
		t.Fatal(err)
	}
}

// Writers keep spooling while drains (and so prunes) run: nothing errors and
// no accepted batch disappears except by being folded.
func TestWritesDuringDrainAndPruneLoseNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	fillSpool(t, filepath.Join(dir, BadDir), MaxBadFiles+50)
	var wg sync.WaitGroup
	var mu sync.Mutex
	folded := 0
	written := 0
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Write(dir, batchOf("acme", job(i+1))); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			written++
			mu.Unlock()
		}()
		if i%5 == 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := Drain(context.Background(), dir, 100, func(Batch) error {
					mu.Lock()
					folded++
					mu.Unlock()
					return nil
				})
				if err != nil && !errors.Is(err, ErrBusy) {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	if _, err := Drain(context.Background(), dir, 100, func(Batch) error { folded++; return nil }); err != nil {
		t.Fatal(err)
	}
	if folded != written {
		t.Fatalf("folded %d of %d written", folded, written)
	}
}
