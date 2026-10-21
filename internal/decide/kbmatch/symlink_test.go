package kbmatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/symlinktest"
)

func TestSaveThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return NewStore(path).Save() })
}

// The run lock of a link and of its target (existing or not yet created) is
// one lock.
func TestLockIsSharedBetweenSymlinkAndTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "store.json")
	link := filepath.Join(dir, "link.json")
	dangling := filepath.Join(dir, "dangling.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.json")
	if err := os.Symlink(other, dangling); err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"link then target":     {link, target},
		"target then link":     {target, link},
		"dangling then target": {dangling, other},
		"target then dangling": {other, dangling},
	} {
		unlock, err := Lock(pair[0])
		if err != nil {
			t.Fatalf("%s: first lock: %v", name, err)
		}
		if _, err := Lock(pair[1]); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: second lock = %v, want ErrBusy", name, err)
		}
		unlock()
	}
}
