package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
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
// path may be a symlink: it is resolved once (lockConfig), and the lock, the
// read and the write all use that target, so the link and its target share one
// lock and the edit lands in the target with the link kept. The config path is
// the one file Vani writes through a leaf link, because the operator named it.
func Update(path string, fn func(*Config) error) error {
	_, err := UpdateWithDeprecations(path, fn)
	return err
}

// UpdateWithDeprecations is Update that also returns the deprecated key names
// the file used before the rewrite (see Deprecations), for the caller to warn
// about: the rewrite replaces them with the new names. The list is returned
// whenever the file was read, even when fn or the write then failed.
func UpdateWithDeprecations(path string, fn func(*Config) error) ([]string, error) {
	target, unlock, err := lockConfig(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, old, err := LoadWithDeprecations(target)
	if err != nil {
		return nil, err
	}
	if err := fn(&c); err != nil {
		return old, err
	}
	return old, writeLocked(target, c)
}

// lockConfig resolves path once (atomicfile.Resolve, links followed, leaf
// included), takes the exclusive update lock beside that target, waiting up to
// lockWait, and returns the target and the func that releases the lock; the
// lock also dies with the process. Callers read and write the returned target,
// never path again, so the lock always guards the file that is written. The
// config itself is replaced by rename, so a lock on it would be lost with each
// save.
func lockConfig(path string) (target string, unlock func(), err error) {
	target, err = atomicfile.Resolve(path)
	if err != nil {
		return "", nil, fmt.Errorf("config: %w", err)
	}
	lockPath := target + ".lock"
	// A link into a directory that does not exist yet: the write creates it,
	// and so must the lock.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return "", nil, fmt.Errorf("config: open lock: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("config: open lock: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			return target, func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			_ = f.Close()
			return "", nil, fmt.Errorf("config: lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return "", nil, ErrBusy
		}
		time.Sleep(lockRetry)
	}
}
