package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
)

// countSyncDir records the syncs under dir; fail, when set, is what every
// directory sync returns.
func countSyncDir(t *testing.T, dir string, fail error) *synctest.Log {
	t.Helper()
	l := synctest.Record(t, dir)
	if fail != nil {
		l.Fail(func(_ string, d bool) error {
			if d {
				return fail
			}
			return nil
		})
	}
	return l
}

func TestOpenSyncsParentDirOnceOnFirstCreateOnly(t *testing.T) {
	dir := t.TempDir()
	dirs := countSyncDir(t, dir, nil)

	f := mustOpen(t, dir)
	// The sync targets the resolved directory (t.TempDir is under a symlink on macOS).
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirs.Dirs(); len(got) != 1 || got[0] != want {
		t.Fatalf("first creation of events.jsonl: want exactly one SyncDir(%s), got %v", want, got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f = mustOpen(t, dir)
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := dirs.Dirs(); len(got) != 1 {
		t.Fatalf("later opens must not sync the directory again: %v", got)
	}
}

func TestOpenExistingEmptyAndPartialFilesDoNotSync(t *testing.T) {
	for name, content := range map[string]string{
		"empty file":        "",
		"only partial line": `{"v":1,"key":"gh:x`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, eventsName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			dirs := countSyncDir(t, dir, nil)
			f := mustOpen(t, dir)
			f.Close()
			if got := dirs.Dirs(); len(got) != 0 {
				t.Fatalf("a pre-existing file is not a creation: %v", got)
			}
		})
	}
}

func TestOpenFailedDirSyncFailsClosedAndRetriesNextOpen(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("fsync dir: boom")
	dirs := countSyncDir(t, dir, boom)
	if f, err := Open(dir); err == nil || !errors.Is(err, boom) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("a failed directory sync must fail Open loudly: %v", err)
	}
	// The lock was released and the unsynced file removed: the next open is a
	// first creation again and syncs again.
	dirs.Fail(nil)
	dirs.Reset()
	f := mustOpen(t, dir)
	defer f.Close()
	if got := dirs.Dirs(); len(got) != 1 {
		t.Fatalf("the retry after a failed sync must sync: %v", got)
	}
}

func TestConcurrentFirstOpenersNeverLeaveCreationUnsynced(t *testing.T) {
	dir := t.TempDir()
	dirs := countSyncDir(t, dir, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := Open(dir)
			if err == nil {
				f.Close()
			} else if !errors.Is(err, ErrLocked) {
				t.Errorf("open: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(dirs.Dirs()) < 1 {
		t.Fatal("racing first openers: zero directory syncs is not allowed")
	}
}

// Each append is one file sync; a batch of any size is one.
func TestAppendAndBatchSyncTheFileOnceEach(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	log := countSyncDir(t, dir, nil)
	for i, e := range batchOf(3, "one") {
		if err := f.Append(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		if got := len(log.Files()); got != i+1 {
			t.Fatalf("after %d appends: %d file syncs", i+1, got)
		}
	}
	log.Reset()
	if err := f.AppendBatch(t.Context(), batchOf(500, "many")); err != nil {
		t.Fatal(err)
	}
	if got := log.Files(); len(got) != 1 || len(log.Dirs()) != 0 {
		t.Fatalf("a 500-event batch: files %d dirs %v, want 1 file sync", len(got), log.Dirs())
	}
}

// A failed fsync breaks the ledger: the event is not folded, the line is cut,
// later appends are refused, and a reopen sees only what was acknowledged.
func TestFailedFsyncBreaksTheLedgerAndKeepsOnlyAcknowledgedEvents(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	evs := batchOf(2, "fs")
	if err := f.Append(t.Context(), evs[0]); err != nil {
		t.Fatal(err)
	}
	log := countSyncDir(t, dir, nil)
	boom := errors.New("fsync: boom")
	log.Fail(func(_ string, d bool) error {
		if !d {
			return boom
		}
		return nil
	})
	if err := f.Append(t.Context(), evs[1]); !errors.Is(err, errBroken) {
		t.Fatalf("append over a failed fsync: %v, want errBroken", err)
	}
	if _, ok := f.Get(evs[1].Key); ok {
		t.Fatal("the unacknowledged event was folded")
	}
	log.Fail(nil)
	if err := f.Append(t.Context(), evs[1]); !errors.Is(err, errBroken) {
		t.Fatalf("a broken ledger took an append: %v", err)
	}
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if n := len(g.All()); n != 1 {
		t.Fatalf("reopened: %d records, want the 1 acknowledged", n)
	}
}

// The real fsync path, once, end to end: under RunWithoutSync every other test
// here skips it.
func TestRealFsyncRoundTrip(t *testing.T) {
	dir := t.TempDir()
	synctest.Real(t, dir)
	f := mustOpen(t, dir)
	evs := batchOf(4, "real")
	if err := f.Append(t.Context(), evs[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.AppendBatch(t.Context(), evs[1:]); err != nil {
		t.Fatal(err)
	}
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if n := len(g.All()); n != 4 {
		t.Fatalf("reopened: %d records, want 4", n)
	}
}
