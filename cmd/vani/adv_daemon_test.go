package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bytes"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
)

func advRecoveredEnv(t *testing.T, stateDir string) *env {
	return &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
}

// panic(nil) and panic(error) are both recovered, finalised as failed runs, and
// announced once.
func TestAdvPanicValues(t *testing.T) {
	for name, val := range map[string]any{
		"nil":         nil,
		"error":       errors.New("plain error"),
		"fatal error": fmt.Errorf("%w: pretend ledger", daemon.ErrFatal),
		"int":         42,
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			n := &fakeNotifier{}
			app := &App{Ledger: fixture(), Notifier: n, RunOnce: func(context.Context) error { panic(val) }}
			err := runPanicking(t, advRecoveredEnv(t, stateDir).dailyJob(app, config.Default(), triggerSchedule, runOptions{}), context.Background())
			if err == nil || !errors.Is(err, errRunPanic) {
				t.Fatalf("returned %v, want errRunPanic", err)
			}
			r := lastRecord(t, stateDir)
			if r.Outcome != outcomeFailed || r.FinishedAt.IsZero() || !strings.Contains(r.Error, "panic") {
				t.Fatalf("record %+v", r)
			}
			if len(n.calls) != 1 {
				t.Fatalf("notifications %v, want exactly one", n.calls)
			}
			t.Logf("%s: err=%v fatal=%v cause=%q", name, err, errors.Is(err, daemon.ErrFatal), r.Cause)
		})
	}
}

// A run that poisons the ledger and then panics is a ledger_fatal failure that
// ends the daemon, not a panic the daemon schedules past.
func TestAdvPanicAfterPoisonEndsTheDaemon(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	runs := 0
	wireDaemon(t, l, func(context.Context) error {
		runs++
		l.poison(errors.New("ledger: fsync failed: input/output error"))
		panic("after a partial write")
	})
	_, stderr := mustCode(t, exitRuntime, "daemon")
	if runs != 1 {
		t.Fatalf("%d runs after a poisoning panic", runs)
	}
	if !strings.Contains(stderr, "fsync failed") {
		t.Errorf("stderr lacks the poison:\n%s", stderr)
	}
	if r := lastRecord(t, stateDir); r.Outcome != outcomeFailed || r.Cause != causeLedgerFatal || r.FinishedAt.IsZero() {
		t.Errorf("record %+v, want failed ledger_fatal", r)
	}
}

// A panic that leaves the ledger healthy (records finalised, nothing poisoned)
// does not poison later runs, and each run announces itself once.
func TestAdvRepeatedPanicsKeepSchedulingAndAnnounceOncePerRun(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := &brokenLedger{memLedger: fixture()}
	n := &fakeNotifier{}
	oldBuild, oldNow, oldClock := buildApp, now, daemonClock
	t.Cleanup(func() { buildApp, now, daemonClock = oldBuild, oldNow, oldClock })
	clock := &jumpClock{t: time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)}
	daemonClock, now = clock, clock.Now
	runs := 0
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: n, RunOnce: func(context.Context) error {
			runs++
			if runs <= 4 {
				panic(fmt.Sprintf("panic %d", runs))
			}
			l.poison(errors.New("stop the daemon"))
			return errors.New("stop")
		}}, nil
	}
	mustCode(t, exitRuntime, "daemon")
	if runs != 5 {
		t.Fatalf("%d runs, want 4 panicking then the stopping one", runs)
	}
	failed := 0
	for _, c := range n.calls {
		if strings.HasPrefix(c, "Vani run failed|") {
			failed++
		}
	}
	if failed != 4 {
		t.Errorf("%d failure notices for 4 panicking runs: %v", failed, n.calls)
	}
	_ = lastRecord(t, stateDir)
}

// seqLedger reports a different poison every time it is asked, so which cause
// the daemon returns shows which loop won.
type seqLedger struct {
	*memLedger
	n atomic.Int32
}

func (s *seqLedger) Broken() error { return fmt.Errorf("poison-%d", s.n.Add(1)) }

// Two loops (the inbox poll and the daily run) hit a fatal at once: the daemon
// ends exactly once with one of the causes and no race.
func TestAdvTwoFatalsEndTheDaemonOnce(t *testing.T) {
	for rep := 0; rep < 20; rep++ {
		setup(t)
		mustCode(t, 0, "init")
		l := &seqLedger{memLedger: fixture()}
		oldBuild, oldClock, oldInterval, oldNow := buildApp, daemonClock, inboxPollInterval, now
		clock := &jumpClock{t: time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)}
		daemonClock, now, inboxPollInterval = clock, clock.Now, time.Millisecond
		var polls atomic.Int32
		buildApp = func(config.Config, string, io.Writer) (*App, error) {
			return &App{Ledger: l, Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error { return nil },
				PollInbox: func(context.Context) error { polls.Add(1); return nil }}, nil
		}
		done := make(chan [2]any, 1)
		go func() {
			code, _, stderr := do(t, "daemon")
			done <- [2]any{code, stderr}
		}()
		select {
		case r := <-done:
			stderr := r[1].(string)
			if r[0].(int) != exitRuntime || strings.Count(stderr, "poison-") < 1 {
				t.Fatalf("rep %d: exit %v stderr:\n%s", rep, r[0], stderr)
			}
			lines := 0
			for _, ln := range strings.Split(stderr, "\n") {
				if strings.Contains(ln, "poison-") && strings.HasPrefix(ln, "vani: ") && !strings.Contains(ln, "inbox poll") {
					lines++
				}
			}
			if lines != 1 {
				t.Fatalf("rep %d: the cause was reported %d times:\n%s", rep, lines, stderr)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("rep %d: the daemon did not end", rep)
		}
		buildApp, daemonClock, inboxPollInterval, now = oldBuild, oldClock, oldInterval, oldNow
	}
}
