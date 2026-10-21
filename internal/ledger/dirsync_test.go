package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// countSyncDir swaps the directory-fsync seam for one that records the
// directories it was asked to sync; fail, when set, is what it returns.
func countSyncDir(t *testing.T, fail error) *[]string {
	t.Helper()
	var mu sync.Mutex
	var dirs []string
	old := syncDir
	syncDir = func(d string) error {
		mu.Lock()
		defer mu.Unlock()
		dirs = append(dirs, d)
		return fail
	}
	t.Cleanup(func() { syncDir = old })
	return &dirs
}

func TestOpenSyncsParentDirOnceOnFirstCreateOnly(t *testing.T) {
	dir := t.TempDir()
	dirs := countSyncDir(t, nil)

	f := mustOpen(t, dir)
	// The sync targets the resolved directory (t.TempDir is under a symlink on macOS).
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(*dirs) != 1 || (*dirs)[0] != want {
		t.Fatalf("first creation of events.jsonl: want exactly one SyncDir(%s), got %v", want, *dirs)
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
	if len(*dirs) != 1 {
		t.Fatalf("later opens must not sync the directory again: %v", *dirs)
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
			dirs := countSyncDir(t, nil)
			f := mustOpen(t, dir)
			f.Close()
			if len(*dirs) != 0 {
				t.Fatalf("a pre-existing file is not a creation: %v", *dirs)
			}
		})
	}
}

func TestOpenFailedDirSyncFailsClosedAndRetriesNextOpen(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("fsync dir: boom")
	dirs := countSyncDir(t, boom)
	if f, err := Open(dir); err == nil || !errors.Is(err, boom) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("a failed directory sync must fail Open loudly: %v", err)
	}
	// The lock was released and the unsynced file removed: the next open is a
	// first creation again and syncs again.
	*dirs = (*dirs)[:0]
	syncDirOK := countSyncDir(t, nil)
	f := mustOpen(t, dir)
	defer f.Close()
	if len(*syncDirOK) != 1 {
		t.Fatalf("the retry after a failed sync must sync: %v", *syncDirOK)
	}
}

func TestConcurrentFirstOpenersNeverLeaveCreationUnsynced(t *testing.T) {
	dir := t.TempDir()
	dirs := countSyncDir(t, nil)
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
	if len(*dirs) < 1 {
		t.Fatal("racing first openers: zero directory syncs is not allowed")
	}
}
