package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// AppLockFile is where, under the state dir, the Vani Apply Mac app holds an
// exclusive flock while it runs and announces runs itself (RunNotice.swift,
// NotifierLock). The lock is the process's: it goes when the app quits or
// crashes, so a stale lock cannot silence the daemon.
const AppLockFile = "app/notifier.lock"

// AppLockPath is AppLockFile under stateDir.
func AppLockPath(stateDir string) string {
	return filepath.Join(stateDir, filepath.FromSlash(AppLockFile))
}

// UnlessHeld returns a Notifier that skips the notice while another process
// holds the lock at path, and otherwise posts through inner. The daemon is the
// fallback for when the app is not running.
//
// A lock that cannot be checked (unreadable file, lock error) posts through
// inner: a duplicate notice is better than none.
func UnlessHeld(path string, inner Notifier) Notifier {
	return heldGate{path: path, inner: inner}
}

type heldGate struct {
	path  string
	inner Notifier
}

// Notify implements Notifier.
func (g heldGate) Notify(ctx context.Context, title, body string) error {
	if appHolds(g.path) {
		return nil
	}
	return g.inner.Notify(ctx, title, body)
}

// appHolds reports whether another open file holds the lock at path. It only
// tests the lock (a shared try, released at once) and creates nothing.
func appHolds(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	err = filelock.TryLock(f, false)
	if errors.Is(err, filelock.ErrWouldBlock) {
		return true
	}
	if err == nil {
		_ = filelock.Unlock(f)
	}
	return false
}
