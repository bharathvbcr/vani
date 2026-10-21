package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// ErrBusy means another process held the config's update lock for the whole
// wait: nothing was read or written.
var ErrBusy = errors.New("config: another process is updating the config; try again")

// lockSuffix names the advisory lock file beside the config. The config itself
// is replaced by rename, so a lock on it would be lost with each save.
const lockSuffix = ".lock"

const lockRetry = 10 * time.Millisecond

// lockWait bounds how long Update waits for the lock. A variable only so a
// test can shorten it.
var lockWait = 10 * time.Second

// Update is the one way to change the config file. Under an exclusive lock on
// path+".lock" it reads the file as it is now, applies fn, validates the
// result and replaces the file atomically, so concurrent updaters (the
// extension's save_search, the app's `config exclude --set`, a CLI edit) each
// build on the last one's result instead of overwriting it.
//
// fn failing, or the result failing Validate, changes nothing. A missing or
// unreadable config is an error, never an empty start. fn runs with the lock
// held: keep it to editing the copy it is given, and do any slow check first.
//
// path may be a symlink: the lock and the write both use the resolved path, so
// the link and its target share one lock and the edit lands in the target.
func Update(path string, fn func(*Config) error) error {
	_, err := UpdateWithDeprecations(path, fn)
	return err
}

// UpdateWithDeprecations is Update that also returns the deprecated key names
// the file used before the rewrite (see Deprecations), for the caller to warn
// about: the rewrite replaces them with the new names. The list is returned
// whenever the file was read, even when fn or the write then failed.
func UpdateWithDeprecations(path string, fn func(*Config) error) ([]string, error) {
	path, unlock, err := lockConfig(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, old, err := LoadWithDeprecations(path)
	if err != nil {
		return nil, err
	}
	if err := fn(&c); err != nil {
		return old, err
	}
	return old, writeLocked(path, c)
}

// resolvePath is path with symlinks followed. A file that does not exist yet
// resolves through its directory, so the path a later write creates is the one
// every locker computes.
func resolvePath(path string) (string, error) {
	r, err := filepath.EvalSymlinks(path)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("config: resolve %s: %w", path, err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("config: resolve %s: %w", filepath.Dir(path), err)
	}
	return filepath.Join(dir, filepath.Base(path)), nil
}

// lockConfig resolves path (see resolvePath) and takes the exclusive update
// lock beside the resolved file, waiting up to lockWait. It returns the
// resolved path, which the caller must read and write instead of path, and a
// func that releases the lock; the lock also dies with the process.
func lockConfig(path string) (string, func(), error) {
	path, err := resolvePath(path)
	if err != nil {
		return "", nil, err
	}
	unlock, err := lockResolved(path)
	return path, unlock, err
}

// lockResolved locks path+lockSuffix; path is already resolved.
func lockResolved(path string) (func(), error) {
	f, err := os.OpenFile(path+lockSuffix, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: open lock: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			_ = f.Close()
			return nil, fmt.Errorf("config: lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrBusy
		}
		time.Sleep(lockRetry)
	}
}
