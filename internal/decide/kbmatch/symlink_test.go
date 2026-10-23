package kbmatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestSaveThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return NewStore(path).Save() })
}

// The store's leaf is not followed, so its lock is the one beside the leaf
// whether the leaf is a regular file, a link (existing target or dangling) or
// absent, and a link's target is a different file with a different lock. Two
// spellings of the state directory share the lock.
func TestLockIsBesideTheLeafNotTheLinkTarget(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(root, "dirlink")
	if err := os.Symlink(realDir, dirLink); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.json")
	if err := os.Symlink(outside, filepath.Join(realDir, FileName)); err != nil {
		t.Fatal(err)
	}
	unlock, err := Lock(filepath.Join(realDir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Lock(filepath.Join(dirLink, FileName)); !errors.Is(err, ErrBusy) {
		t.Errorf("lock through the directory link = %v, want ErrBusy", err)
	}
	if u, err := Lock(outside); err != nil {
		t.Errorf("the link target is a different file and takes its own lock: %v", err)
	} else {
		u()
	}
	if _, err := os.Lstat(outside + ".lock"); err != nil {
		t.Errorf("target's own lock missing: %v", err)
	}
}

// A store opened under the lock saves to the path it locked even when the leaf
// is turned into a link pointing outside afterwards: the link is replaced, the
// outside file is not written.
func TestSaveAfterLeafBecomesLinkStaysInside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	outside := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, unlock, _, err := OpenLocked(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL" {
		t.Errorf("outside file written through the planted link: %q", b)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("leaf not a regular file: %v %v", fi, err)
	}
}
