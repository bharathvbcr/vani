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
// path may be a symlink: the lock path (filelock.LockPath) and the write
// (atomicfile.Write) both resolve it, so the link and its target share one lock
// and the edit lands in the target.
func Update(path string, fn func(*Config) error) error {
	_, err := UpdateWithDeprecations(path, fn)
	return err
}

// UpdateWithDeprecations is Update that also returns the deprecated key names
// the file used before the rewrite (see Deprecations), for the caller to warn
// about: the rewrite replaces them with the new names. The list is returned
// whenever the file was read, even when fn or the write then failed.
func UpdateWithDeprecations(path string, fn func(*Config) error) ([]string, error) {
	unlock, err := lockConfig(path)
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

// lockConfig takes the exclusive update lock beside the config's resolved path
// (filelock.LockPath), waiting up to lockWait, and returns the func that
// releases it; the lock also dies with the process. The config itself is
// replaced by rename, so a lock on it would be lost with each save.
func lockConfig(path string) (func(), error) {
	lockPath, err := filelock.LockPath(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// A link into a directory that does not exist yet: the write creates it,
	// and so must the lock.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("config: open lock: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
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
