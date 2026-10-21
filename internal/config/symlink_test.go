package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestSaveThroughSymlinks(t *testing.T) {
	symlinktest.RunFollow(t, func(path string) error { return Save(path, valid()) })
}

// A link and its target share one update lock: an Update through the link run
// inside an Update of the target (and the reverse) finds it held.
func TestUpdateLockIsSharedBetweenSymlinkAndTarget(t *testing.T) {
	old := lockWait
	lockWait = 50 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := Save(target, valid()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string][2]string{"link inside target": {target, link}, "target inside link": {link, target}} {
		var inner error
		err := Update(p[0], func(*Config) error {
			inner = Update(p[1], func(*Config) error { return nil })
			return nil
		})
		if err != nil {
			t.Fatalf("%s: outer: %v", name, err)
		}
		if !errors.Is(inner, ErrBusy) {
			t.Errorf("%s: inner = %v, want ErrBusy", name, inner)
		}
	}
}

// The link is resolved once: if it is retargeted while Update holds the lock,
// the write still lands on the file that was locked, not on the new target.
func TestUpdateWritesTheFileItLocked(t *testing.T) {
	dir := t.TempDir()
	x, y := filepath.Join(dir, "x.json"), filepath.Join(dir, "y.json")
	link := filepath.Join(dir, "config.json")
	if err := Save(x, valid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(x, link); err != nil {
		t.Fatal(err)
	}
	err := Update(link, func(c *Config) error {
		if err := os.Remove(link); err != nil {
			return err
		}
		return os.Symlink(y, link)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(y); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the write followed the retargeted link to y.json: %v", err)
	}
}
