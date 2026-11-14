package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type seen struct {
	files, dirs []string
}

func observe(t *testing.T, dir string, real bool, fail error) *seen {
	t.Helper()
	s := &seen{}
	t.Cleanup(ObserveSyncForTest(dir, real, func(p string, d bool) error {
		if d {
			s.dirs = append(s.dirs, p)
		} else {
			s.files = append(s.files, p)
		}
		return fail
	}))
	return s
}

func TestWriteSyncsTheFileOnceAndTheDirectoryOnce(t *testing.T) {
	dir := t.TempDir()
	s := observe(t, dir, false, nil)
	if err := Write(filepath.Join(dir, "a.json"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(s.files) != 1 || len(s.dirs) != 1 {
		t.Fatalf("files %v dirs %v, want one of each", s.files, s.dirs)
	}
}

func TestTheDeepestObserverSeesTheSync(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "in")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	so, si := observe(t, outer, false, nil), observe(t, inner, false, nil)
	if err := Write(filepath.Join(inner, "a"), nil); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(outer, "b"), nil); err != nil {
		t.Fatal(err)
	}
	if len(si.files) != 1 || len(so.files) != 1 {
		t.Fatalf("inner %v outer %v: each write belongs to its own directory", si.files, so.files)
	}
	// A sibling sharing a name prefix is not under the directory.
	sib := outer + "x"
	if err := os.Mkdir(sib, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sib) })
	if err := Write(filepath.Join(sib, "c"), nil); err != nil {
		t.Fatal(err)
	}
	if len(so.files) != 1 {
		t.Fatalf("%s was counted under %s: %v", sib, outer, so.files)
	}
}

// A store creates its directory after the test starts observing it; the
// store's syncs name the resolved path (t.TempDir is under a link on macOS).
func TestAnObserverSeesADirectoryCreatedAfterIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later", "store")
	s := observe(t, dir, false, nil)
	if err := Write(filepath.Join(dir, "a.json"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(s.files) != 1 || len(s.dirs) != 1 {
		t.Fatalf("files %v dirs %v, want one of each", s.files, s.dirs)
	}
}

func TestAnObserverErrorReplacesTheSync(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("boom")
	observe(t, dir, false, boom)
	f, err := os.Create(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Sync(f); !errors.Is(err, boom) {
		t.Fatalf("Sync: %v, want boom", err)
	}
	if err := SyncDir(dir); !errors.Is(err, boom) {
		t.Fatalf("SyncDir: %v, want boom", err)
	}
}

func TestRunWithoutSyncSkipsUnlessAnObserverAsksForReal(t *testing.T) {
	var base []string
	restore := SetSyncForTest(func(p string, _ bool) error { base = append(base, p); return nil })
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Sync(f); err != nil || len(base) != 1 {
		t.Fatalf("unobserved sync went to %v, %v; want the base", base, err)
	}
	stop := ObserveSyncForTest(dir, true, nil)
	if err := Sync(f); err != nil || len(base) != 1 {
		t.Fatalf("a real observer's sync reached the base: %v, %v", base, err)
	}
	stop()
	restore()
	// With the base put back and no observer, a sync is the real one again.
	if err := Sync(f); err != nil || len(base) != 1 {
		t.Fatalf("after restore: %v, %v", base, err)
	}
	// A closed file's real sync fails: proof the real path ran, not a stub.
	f.Close()
	if err := Sync(f); err == nil {
		t.Fatal("a real sync of a closed file succeeded")
	}
}

func TestRunWithoutSyncReturnsTheCodeAndRestores(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	code := RunWithoutSync(fakeRun(func() int {
		if err := Sync(f); err != nil { // closed: only a skipped sync succeeds
			t.Errorf("sync under RunWithoutSync: %v", err)
		}
		return 3
	}))
	if code != 3 {
		t.Errorf("code %d, want 3", code)
	}
	if err := Sync(f); err == nil {
		t.Error("the skip outlived RunWithoutSync")
	}
}

type fakeRun func() int

func (f fakeRun) Run() int { return f() }
