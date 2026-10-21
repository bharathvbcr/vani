package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
)

// noticeApp wires an App whose run fails with runErr and reports adv, and
// returns the notifier it posts to. before runs inside buildApp, after the
// config was loaded and before the run re-reads it.
func noticeApp(t *testing.T, n *fakeNotifier, runErr error, adv *advanceReport, before func()) {
	t.Helper()
	old, oldNow := buildApp, now
	t.Cleanup(func() { buildApp, now = old, oldNow })
	now = func() time.Time { return time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC) }
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		if before != nil {
			before()
		}
		return &App{
			Ledger:    fixture(),
			Notifier:  n,
			RunOnce:   func(context.Context) error { return runErr },
			RunReport: func() runDetail { return runDetail{Advance: adv} },
			Reload:    func(config.Config) {},
		}, nil
	}
}

// Under launchd nobody reads stderr: a failed run must reach the screen, once,
// in Vani's own words rather than the error's (which can carry fetched URLs).
func TestFailedRunIsAnnouncedOnce(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	wire(t, fixture(), n, errors.New("all boards down: https://boards.example/evil"))
	mustCode(t, 1, "run-once")
	if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani run failed|") || !strings.Contains(n.calls[0], "pipeline stopped") {
		t.Fatalf("notifications: %v", n.calls)
	}
	if strings.Contains(n.calls[0], "boards.example") || strings.Contains(n.calls[0], "all boards down") {
		t.Errorf("error text reached the notification: %q", n.calls[0])
	}
}

func TestCancelledRunIsSilent(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	wire(t, fixture(), n, context.Canceled)
	mustCode(t, 1, "run-once")
	if len(n.calls) != 0 {
		t.Errorf("a stopped run notified: %v", n.calls)
	}
}

func TestFatalRunSaysVaniStopped(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	wire(t, fixture(), n, fmt.Errorf("%w: ledger fsync failed", daemon.ErrFatal))
	mustCode(t, 1, "run-once")
	if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani stopped|") {
		t.Errorf("notifications: %v", n.calls)
	}
}

// The advance phase's stop reason picks the words: an outage of every
// decision backend reads differently from postings failing a stage.
func TestAdvanceStopNamesTheCause(t *testing.T) {
	for stop, want := range map[string]string{
		stopNoneRead:     "No decision backend",
		stopUnreadStreak: "No decision backend",
		stopFailStreak:   "kept failing a stage",
		stopNoneAdvanced: "kept failing a stage",
	} {
		t.Run(stop, func(t *testing.T) {
			setup(t)
			mustCode(t, 0, "init")
			n := &fakeNotifier{}
			noticeApp(t, n, errors.New("advance failed"), &advanceReport{Attempted: 20, Unread: 20, Stopped: stop}, nil)
			mustCode(t, 1, "run-once")
			if len(n.calls) != 1 || !strings.Contains(n.calls[0], want) {
				t.Errorf("notifications: %v, want %q", n.calls, want)
			}
		})
	}
}

// A config that cannot be re-read does not stop the run; the digest is still
// announced, and so is the stale config.
func TestStaleConfigIsAnnouncedBesideTheDigest(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	noticeApp(t, n, nil, nil, func() {
		if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	mustCode(t, 1, "run-once")
	if len(n.calls) != 2 || n.calls[0] != "Vani digest|2 roles to review" || !strings.HasPrefix(n.calls[1], "Vani config not reloaded|") {
		t.Errorf("notifications: %v", n.calls)
	}
}

// A digest after a partly failed advance says how many postings still wait.
func TestDigestNoticeCountsWhatWaits(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	n := &fakeNotifier{}
	noticeApp(t, n, nil, &advanceReport{Attempted: 10, Unread: 2, Failed: 1}, nil)
	mustCode(t, 0, "run-once")
	want := "Vani digest|2 roles to review; 3 postings could not be read or scored and wait for the next run"
	if len(n.calls) != 1 || n.calls[0] != want {
		t.Errorf("notifications: %v, want %q", n.calls, want)
	}
}

type failingNotifier struct{ calls int }

func (f *failingNotifier) Notify(context.Context, string, string) error {
	f.calls++
	return errors.New("osascript exited 1")
}

// A notifier that fails is a warning: the run's exit and record stand.
func TestNotifierFailureIsAWarning(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	f := &failingNotifier{}
	old := buildApp
	t.Cleanup(func() { buildApp = old })
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: fixture(), Notifier: f, RunOnce: func(context.Context) error { return errors.New("boom") }}, nil
	}
	_, e := mustCode(t, 1, "run-once")
	if f.calls != 1 || !strings.Contains(e, "notification failed: osascript exited 1") {
		t.Errorf("calls=%d stderr=%q", f.calls, e)
	}
	if r, _ := readRunRecord(stateDir); r == nil || r.Outcome != outcomeFailed {
		t.Errorf("record: %+v", r)
	}
}

// A run cancelled mid-way still gets its failure notice out: the notifier is
// handed a context the run's cancellation does not reach.
func TestAnnounceFailureOutlivesTheRunContext(t *testing.T) {
	var got error
	n := notifyFunc(func(ctx context.Context) error { got = ctx.Err(); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	(&env{stderr: io.Discard}).announceFailure(ctx, &App{Notifier: n}, runRecord{Outcome: outcomeFailed, Stage: stageDigest})
	if got != nil {
		t.Errorf("notifier saw a cancelled context: %v", got)
	}
}

type notifyFunc func(ctx context.Context) error

func (f notifyFunc) Notify(ctx context.Context, _, _ string) error { return f(ctx) }

func TestFailureNoticeTable(t *testing.T) {
	leak := "https://evil.example/" + strings.Repeat("x", 500)
	cases := []struct {
		name   string
		rec    runRecord
		err    error
		title  string
		silent bool
	}{
		{name: "ok", rec: runRecord{Outcome: outcomeOK}, silent: true},
		{name: "cancelled", rec: runRecord{Outcome: outcomeCancelled}, err: context.Canceled, silent: true},
		{name: "running", rec: runRecord{Outcome: outcomeRunning}, silent: true},
		{name: "fatal beats held back", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest},
			err: errors.Join(fmt.Errorf("%w: x", errDigestHeldBack), fmt.Errorf("%w: y", daemon.ErrFatal)), title: "Vani stopped"},
		{name: "held back", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest}, err: fmt.Errorf("%w: "+leak, errDigestHeldBack), title: "Vani digest held back"},
		{name: "stale config and failed pipeline", rec: runRecord{Outcome: outcomeFailed, Stage: stagePipeline},
			err: errors.Join(errConfigStale, errors.New(leak)), title: "Vani run failed"},
		{name: "stale config only", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest}, err: fmt.Errorf("%w: "+leak, errConfigStale), title: "Vani config not reloaded"},
		{name: "digest", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest}, err: errors.New(leak), title: "Vani run failed"},
		{name: "nil error", rec: runRecord{Outcome: outcomeFailed}, title: "Vani run failed"},
		{name: "cap is not a failure cause", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest, runDetail: runDetail{Advance: &advanceReport{Stopped: stopCap}}},
			err: errors.New(leak), title: "Vani run failed"},
		// The cause is the error's identity, never its text.
		{name: "text naming a cause is no cause", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest},
			err: errors.New(errConfigStale.Error() + "; " + errDigestHeldBack.Error()), title: "Vani run failed"},
		{name: "fatal beats a stop reason", rec: runRecord{Outcome: outcomeFailed, Stage: stagePipeline, runDetail: runDetail{Advance: &advanceReport{Stopped: stopNoneRead}}},
			err: fmt.Errorf("%w: x", daemon.ErrFatal), title: "Vani stopped"},
		{name: "a stop reason beats a stale config", rec: runRecord{Outcome: outcomeFailed, Stage: stageDigest, runDetail: runDetail{Advance: &advanceReport{Stopped: stopNoneRead}}},
			err: errConfigStale, title: "Vani run failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := c.rec
			rec.Cause = runCause(c.err)
			title, body, ok := failureNotice(rec)
			if ok == c.silent {
				t.Fatalf("ok=%v for %s", ok, c.name)
			}
			if c.silent {
				return
			}
			if title != c.title {
				t.Errorf("title %q, want %q", title, c.title)
			}
			if body == "" || strings.Contains(body, "evil.example") {
				t.Errorf("body %q", body)
			}
		})
	}
}

func TestDigestBody(t *testing.T) {
	for _, c := range []struct {
		roles int
		adv   *advanceReport
		want  string
	}{
		{1, nil, "1 role to review"},
		{2, nil, "2 roles to review"},
		{3, &advanceReport{}, "3 roles to review"},
		{1, &advanceReport{Unread: 1}, "1 role to review; 1 posting could not be read or scored and waits for the next run"},
		{5, &advanceReport{Unread: 3, Failed: 4, Deferred: 9}, "5 roles to review; 7 postings could not be read or scored and wait for the next run"},
	} {
		if got := digestBody(c.roles, c.adv); got != c.want {
			t.Errorf("digestBody(%d, %+v) = %q, want %q", c.roles, c.adv, got, c.want)
		}
	}
}
