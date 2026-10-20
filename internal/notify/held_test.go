package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

type countNotifier struct {
	n   int
	err error
}

func (c *countNotifier) Notify(context.Context, string, string) error {
	c.n++
	return c.err
}

// holdLock takes the exclusive lock the Mac app holds, on its own open file,
// as another process would.
func holdLock(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := filelock.TryLock(f, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestAppLockPath(t *testing.T) {
	if got, want := AppLockPath("/s"), filepath.Join("/s", "app", "notifier.lock"); got != want {
		t.Errorf("AppLockPath = %q, want %q", got, want)
	}
}

func TestUnlessHeldPostsWhenNoAppRuns(t *testing.T) {
	dir := t.TempDir()
	inner := &countNotifier{}
	// No lock file at all: the app has never run.
	if err := UnlessHeld(AppLockPath(dir), inner).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if inner.n != 1 {
		t.Fatalf("posted %d, want 1", inner.n)
	}
	if _, err := os.Stat(AppLockPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the check created the lock file: %v", err)
	}

	// A lock file left by an app that quit (no lock held): the daemon posts.
	f := holdLock(t, AppLockPath(dir))
	_ = f.Close()
	if err := UnlessHeld(AppLockPath(dir), inner).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if inner.n != 2 {
		t.Fatalf("posted %d, want 2", inner.n)
	}
}

func TestUnlessHeldSkipsWhileTheAppHoldsTheLock(t *testing.T) {
	dir := t.TempDir()
	f := holdLock(t, AppLockPath(dir))
	inner := &countNotifier{err: errors.New("must not be called")}
	g := UnlessHeld(AppLockPath(dir), inner)
	for range 2 {
		if err := g.Notify(context.Background(), "t", "b"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.n != 0 {
		t.Fatalf("posted %d while the app held the lock", inner.n)
	}
	// The check released its own probe: the holder still holds, and once it
	// lets go the daemon posts again.
	if err := filelock.Unlock(f); err != nil {
		t.Fatal(err)
	}
	inner.err = nil
	if err := g.Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if inner.n != 1 {
		t.Fatalf("posted %d after the app let go, want 1", inner.n)
	}
}

func TestUnlessHeldPostsWhenTheLockCannotBeChecked(t *testing.T) {
	// A lock path that cannot be opened must not silence the daemon.
	dir := t.TempDir()
	inner := &countNotifier{}
	if err := UnlessHeld(filepath.Join(dir, "missing", "deeper", "notifier.lock"), inner).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if inner.n != 1 {
		t.Fatalf("posted %d, want 1", inner.n)
	}
}

func TestUnlessHeldReturnsTheInnerError(t *testing.T) {
	inner := &countNotifier{err: errors.New("osascript failed")}
	err := UnlessHeld(AppLockPath(t.TempDir()), inner).Notify(context.Background(), "t", "b")
	if err == nil || err.Error() != "osascript failed" {
		t.Fatalf("err = %v", err)
	}
}
