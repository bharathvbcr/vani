package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func countSyncs(t *testing.T, fail error) *[]string {
	t.Helper()
	var dirs []string
	old := syncDir
	syncDir = func(d string) error {
		dirs = append(dirs, d)
		if fail != nil {
			return fail
		}
		return old(d)
	}
	t.Cleanup(func() { syncDir = old })
	return &dirs
}

func TestWriteReplacesAndSyncsTheDirectory(t *testing.T) {
	dirs := countSyncs(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, body := range []string{"one", "two"} {
		if err := Write(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != body {
			t.Fatalf("read %q, %v; want %q", got, err, body)
		}
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
	if len(*dirs) != 2 || (*dirs)[0] != dir {
		t.Errorf("directory syncs %v, want two of %s", *dirs, dir)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

func TestWriteCreatesMissingParents(t *testing.T) {
	countSyncs(t, nil)
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "x.json")
	if err := Write(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b")} {
		if st, err := os.Stat(d); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v, want a 0700 directory", d, st, err)
		}
	}
}

// A directory that cannot be synced leaves the file replaced but not known
// durable: the caller is told, rather than handed a nil it cannot trust.
func TestDirectorySyncFailureIsReported(t *testing.T) {
	countSyncs(t, errors.New("sync refused"))
	path := filepath.Join(t.TempDir(), "x.json")
	err := Write(path, []byte("new"))
	if err == nil || !strings.Contains(err.Error(), "sync refused") || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("err %v; want the directory sync failure, saying the file was replaced", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("file holds %q after the rename", got)
	}
}

func TestFailedWriteLeavesTheOldFileAndNoTemp(t *testing.T) {
	countSyncs(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := Write(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	// A directory in the way of the rename: every step before it succeeds.
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(blocked, []byte("x")); err == nil {
		t.Fatal("replacing a non-empty directory succeeded")
	}
	// A parent that is a file cannot be created.
	if err := Write(filepath.Join(path, "under-a-file"), []byte("x")); err == nil {
		t.Fatal("writing under a regular file succeeded")
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Errorf("old file now %q", got)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}
