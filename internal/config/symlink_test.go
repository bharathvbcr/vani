package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile/symlinktest"
)

func TestSaveThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error { return Save(path, valid()) })
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
