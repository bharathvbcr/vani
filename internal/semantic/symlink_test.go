package semantic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile/symlinktest"
)

func TestProfileSaveThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error {
		return saveProfile(path, "hash", "model", &profileData{dim: 1, vecs: [][]float32{{1}}})
	})
}

// The cache lock lives in the cache directory, so a symlinked spelling of the
// directory takes the same lock as the real one.
func TestCacheLockIsSharedThroughSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockCache(context.Background(), realDir, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lockCache(context.Background(), link, 30*time.Millisecond); !errors.Is(err, ErrWarmBusy) {
		t.Errorf("lock through the link = %v, want ErrWarmBusy", err)
	}
}
