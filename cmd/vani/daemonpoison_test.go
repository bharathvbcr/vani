package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
)

// brokenLedger is a memLedger that can be poisoned, as a *ledger.File is
// after a write it could not cut back.
type brokenLedger struct {
	*memLedger
	mu     sync.Mutex
	broken error
}

func (b *brokenLedger) poison(err error) { b.mu.Lock(); b.broken = err; b.mu.Unlock() }
func (b *brokenLedger) Broken() error    { b.mu.Lock(); defer b.mu.Unlock(); return b.broken }

// jumpClock fires every wait at once by moving its time forward.
type jumpClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *jumpClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *jumpClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.t
	return ch
}

func wireDaemon(t *testing.T, l *brokenLedger, run func(context.Context) error) {
	t.Helper()
	old, oldNow, oldClock := buildApp, now, daemonClock
	t.Cleanup(func() { buildApp, now, daemonClock = old, oldNow, oldClock })
	clock := &jumpClock{t: time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)}
	daemonClock, now = clock, clock.Now
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: run}, nil
	}
}

// A run that leaves the ledger poisoned ends the daemon: exit status 1 with
// the write error on stderr, instead of a daemon that fails every later run
// until someone restarts it.
func TestPoisonedLedgerEndsTheDaemon(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	runs := 0
	wireDaemon(t, l, func(context.Context) error {
		runs++
		l.poison(errors.New("ledger: fsync failed: input/output error"))
		return errors.New("pipeline: append: ledger: fsync failed")
	})
	_, stderr := mustCode(t, exitRuntime, "daemon")
	if runs != 1 {
		t.Errorf("%d runs, want the daemon to stop after the one that poisoned the ledger", runs)
	}
	if !strings.Contains(stderr, "fsync failed: input/output error") {
		t.Errorf("stderr does not name the write failure:\n%s", stderr)
	}
}

// A ledger already poisoned when a run comes due ends the daemon without
// running the pipeline against it.
func TestAlreadyPoisonedLedgerRunsNothing(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	l.poison(errors.New("ledger: fsync failed: input/output error"))
	runs := 0
	wireDaemon(t, l, func(context.Context) error { runs++; return nil })
	mustCode(t, exitRuntime, "daemon")
	if runs != 0 {
		t.Errorf("the pipeline ran %d times against a poisoned ledger", runs)
	}
	rec, err := readRunRecord(stateDir)
	if err != nil || rec == nil || rec.Outcome != outcomeFailed || !strings.Contains(rec.Error, "fsync failed") {
		t.Fatalf("run record %+v, %v; want a failed run naming the poison", rec, err)
	}
}

func TestLedgerBrokenIsFatal(t *testing.T) {
	if err := ledgerBroken(fixture()); err != nil {
		t.Fatalf("a ledger without Broken: %v", err)
	}
	l := &brokenLedger{memLedger: fixture()}
	if err := ledgerBroken(l); err != nil {
		t.Fatalf("a healthy ledger: %v", err)
	}
	cause := errors.New("cut failed")
	l.poison(cause)
	if err := ledgerBroken(l); !errors.Is(err, daemon.ErrFatal) || !errors.Is(err, cause) {
		t.Fatalf("%v, want daemon.ErrFatal wrapping the cause", err)
	}
}

// idleClock never fires a wait: the daemon sits between daily runs, so only
// the inbox poll runs.
type idleClock struct{}

func (idleClock) Now() time.Time                       { return time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC) }
func (idleClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// A schedule saved while the daemon sits between runs exits 75. The inbox
// poll is what notices; the daily job is not started and no run is recorded.
func TestScheduleChangeExitsSoTheDaemonRestarts(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	start, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	oldBuild, oldClock, oldInterval, oldSig := buildApp, daemonClock, inboxPollInterval, signalContext
	t.Cleanup(func() {
		buildApp, daemonClock, inboxPollInterval, signalContext = oldBuild, oldClock, oldInterval, oldSig
	})
	parent, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	signalContext = func() (context.Context, context.CancelFunc) { return parent, func() {} }
	daemonClock, inboxPollInterval = idleClock{}, 5*time.Millisecond
	var polls atomic.Int32
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{
			Ledger:    fixture(),
			Reload:    func(config.Config) {},
			RunOnce:   func(context.Context) error { return nil },
			PollInbox: func(context.Context) error { polls.Add(1); return nil },
		}, nil
	}
	type result struct {
		code   int
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, _, stderr := do(t, "daemon")
		done <- result{code, stderr}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for polls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if polls.Load() < 1 {
		stop()
		<-done
		t.Fatal("the inbox poll never ran")
	}
	seen := polls.Load()
	edited := start
	edited.Schedule.DailyAt = "03:15"
	writeConfig(t, cfgPath, edited)
	select {
	case r := <-done:
		if r.code != exitConfigRestart {
			t.Fatalf("exit %d, want %d\n%s", r.code, exitConfigRestart, r.stderr)
		}
		if !strings.Contains(r.stderr, "restarting") || !strings.Contains(r.stderr, "schedule") {
			t.Fatalf("stderr:\n%s", r.stderr)
		}
		if strings.Contains(r.stderr, "job failed") {
			t.Fatalf("a schedule change was logged as a failed job:\n%s", r.stderr)
		}
		if polls.Load() > seen+1 {
			t.Fatalf("poll kept running after the schedule change: %d then %d", seen, polls.Load())
		}
		rec, rerr := readRunRecord(stateDir)
		if rerr != nil || rec != nil {
			t.Fatalf("a schedule change wrote a run record: %v %+v", rerr, rec)
		}
	case <-time.After(3 * time.Second):
		stop()
		<-done
		t.Fatal("the daemon stayed up after the schedule changed")
	}
}

// A poll that leaves the ledger poisoned ends the daemon within a poll
// interval, not at the next daily slot hours away.
func TestPoisonedByAnInboxPollEndsTheDaemon(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	oldBuild, oldClock, oldInterval, oldSig := buildApp, daemonClock, inboxPollInterval, signalContext
	t.Cleanup(func() {
		buildApp, daemonClock, inboxPollInterval, signalContext = oldBuild, oldClock, oldInterval, oldSig
	})
	daemonClock, inboxPollInterval = idleClock{}, 5*time.Millisecond
	stop := make(chan struct{})
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-stop; cancel() }()
		return ctx, cancel
	}
	var polls atomic.Int32
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error { return nil },
			PollInbox: func(context.Context) error {
				polls.Add(1)
				l.poison(errors.New("ledger: fsync failed: input/output error"))
				return nil
			}}, nil
	}
	type result struct {
		code   int
		stderr string
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		code, _, stderr := do(t, "daemon")
		done <- result{code, stderr}
	}()
	select {
	case r := <-done:
		if r.code != exitRuntime || !strings.Contains(r.stderr, "fsync failed") {
			t.Errorf("exit %d, stderr:\n%s", r.code, r.stderr)
		}
		if n := polls.Load(); n != 1 {
			t.Errorf("%d polls, want the daemon to stop after the one that poisoned the ledger", n)
		}
		if took := time.Since(started); took > 2*time.Second {
			t.Errorf("took %v to end", took)
		}
	case <-time.After(3 * time.Second):
		close(stop)
		<-done
		t.Fatal("the daemon kept running with a poisoned ledger after an inbox poll")
	}
}
