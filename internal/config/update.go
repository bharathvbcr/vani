package config

import (
	"errors"
	"fmt"
	"os"
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
func Update(path string, fn func(*Config) error) error {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := Load(path)
	if err != nil {
		return err
	}
	if err := fn(&c); err != nil {
		return err
	}
	return writeLocked(path, c)
}

// lockConfig takes the exclusive update lock, waiting up to lockWait. The
// returned func releases it; the lock also dies with the process.
func lockConfig(path string) (func(), error) {
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
