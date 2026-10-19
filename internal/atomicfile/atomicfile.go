// Package atomicfile replaces files so that a reader, or a crash, sees the old
// contents or the new ones and never part of either. It is the one owner of
// Vani's write-temp-then-rename step; the state, config, index and cache
// writers all go through it.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// syncDir is SyncDir; a variable only so tests can see that Write reached it.
var syncDir = SyncDir

// Write replaces path with data, mode 0600, creating the parent directory
// (0700) if it is missing. The temp file is fsynced before the rename and the
// directory after it, so once Write returns nil the new contents survive a
// crash. An error after the rename says the file was replaced but its
// durability is unknown; writing the same data again is safe.
func Write(path string, data []byte) (err error) {
	name := filepath.Base(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("atomic write %s: create directory: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomic write %s: create temp file: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("atomic write %s: chmod temp file: %w", name, err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("atomic write %s: write temp file: %w", name, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("atomic write %s: sync temp file: %w", name, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("atomic write %s: close temp file: %w", name, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomic write %s: replace file: %w", name, err)
	}
	if serr := syncDir(dir); serr != nil {
		return fmt.Errorf("atomic write %s: replaced, but syncing the directory failed: %w", name, serr)
	}
	return nil
}
