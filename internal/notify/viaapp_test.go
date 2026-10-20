package notify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// appRunner stands in for /usr/bin/open: launch, when set, plays the app
// starting up (taking the lock, or not).
type appRunner struct {
	mu     sync.Mutex
	name   string
	args   []string
	err    error
	launch func()
}

func (a *appRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	a.mu.Lock()
	a.name, a.args = name, args
	a.mu.Unlock()
	if a.err != nil {
		return []byte("Unable to find application named 'x'"), a.err
	}
	if a.launch != nil {
		a.launch()
	}
	return nil, nil
}

func quick(lockPath string, r Runner, fallback Notifier, warn *bytes.Buffer) viaApp {
	v := ViaApp(lockPath, r, fallback, warn).(viaApp)
	v.wait, v.poll = 200*time.Millisecond, 5*time.Millisecond
	return v
}

func TestViaAppHandsTheNoticeToTheApp(t *testing.T) {
	lock := AppLockPath(t.TempDir())
	fb := &countNotifier{}
	r := &appRunner{launch: func() { holdLock(t, lock) }}
	var warn bytes.Buffer
	if err := quick(lock, r, fb, &warn).Notify(context.Background(), "Vani digest", "3 roles to review"); err != nil {
		t.Fatal(err)
	}
	if fb.n != 0 {
		t.Fatalf("osascript posted %d notice(s) after the app took over", fb.n)
	}
	want := []string{"-g", "-j", "-b", AppBundleID, "--args", AnnounceArgument, "YES"}
	if r.name != "/usr/bin/open" || !slices.Equal(r.args, want) {
		t.Fatalf("ran %s %q, want /usr/bin/open %q", r.name, r.args, want)
	}
	// Notice text never reaches the app's argv: it words the run itself.
	for _, a := range r.args {
		if strings.Contains(a, "roles") || strings.Contains(a, "Vani digest") {
			t.Fatalf("notice text in argv: %q", r.args)
		}
	}
	if warn.Len() != 0 {
		t.Fatalf("warned on a clean handoff: %s", warn.String())
	}
}

func TestViaAppWaitsForALateLock(t *testing.T) {
	lock := AppLockPath(t.TempDir())
	fb := &countNotifier{}
	r := &appRunner{launch: func() {
		time.AfterFunc(30*time.Millisecond, func() { holdLock(t, lock) })
	}}
	if err := quick(lock, r, fb, &bytes.Buffer{}).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if fb.n != 0 {
		t.Fatalf("fell back although the app took the lock: %d", fb.n)
	}
}

func TestViaAppFallsBackWhenTheAppIsMissing(t *testing.T) {
	lock := AppLockPath(t.TempDir())
	fb := &countNotifier{}
	var warn bytes.Buffer
	r := &appRunner{err: errors.New("exit status 1")}
	if err := quick(lock, r, fb, &warn).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if fb.n != 1 {
		t.Fatalf("osascript posted %d, want 1", fb.n)
	}
	if !strings.Contains(warn.String(), "through osascript") || !strings.Contains(warn.String(), "Unable to find") {
		t.Fatalf("warn = %q", warn.String())
	}
}

// The app launched but holds its lock in another state dir (its Settings point
// elsewhere): the daemon cannot see it, so it posts rather than go silent.
func TestViaAppFallsBackWhenTheAppNeverTakesThisLock(t *testing.T) {
	lock, elsewhere := AppLockPath(t.TempDir()), AppLockPath(t.TempDir())
	fb := &countNotifier{}
	var warn bytes.Buffer
	r := &appRunner{launch: func() { holdLock(t, elsewhere) }}
	start := time.Now()
	if err := quick(lock, r, fb, &warn).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if fb.n != 1 {
		t.Fatalf("osascript posted %d, want 1", fb.n)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("wait not bounded: %s", d)
	}
	if !strings.Contains(warn.String(), "did not take") {
		t.Fatalf("warn = %q", warn.String())
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the wait created the lock file: %v", err)
	}
}

func TestViaAppStopsWaitingWhenCancelled(t *testing.T) {
	lock := AppLockPath(t.TempDir())
	fb := &countNotifier{}
	v := quick(lock, &appRunner{}, fb, &bytes.Buffer{})
	v.wait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() { done <- v.Notify(ctx, "t", "b") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify ignored cancellation")
	}
	if fb.n != 1 {
		t.Fatalf("fallback ran %d times, want 1", fb.n)
	}
}

func TestViaAppReportsAFailedFallback(t *testing.T) {
	fb := &countNotifier{err: errors.New("osascript failed")}
	err := quick(AppLockPath(t.TempDir()), &appRunner{err: errors.New("exit 1")}, fb, &bytes.Buffer{}).
		Notify(context.Background(), "t", "b")
	if err == nil || err.Error() != "osascript failed" {
		t.Fatalf("err = %v", err)
	}
}

// In a test binary the real runner launches nothing, so the handoff fails at
// once and goes to the fallback.
func TestViaAppRealRunnerLaunchesNothingInATestBinary(t *testing.T) {
	fb := &countNotifier{}
	var warn bytes.Buffer
	if err := ViaApp(AppLockPath(t.TempDir()), nil, fb, &warn).Notify(context.Background(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	if fb.n != 1 || !strings.Contains(warn.String(), ErrTestBinary.Error()) {
		t.Fatalf("fallback %d, warn %q", fb.n, warn.String())
	}
}
