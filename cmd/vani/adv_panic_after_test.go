package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// nastyPanicValue is longer than panicText's bound and shorter than the run
// record's own bound, with a newline and a raw control byte. A path that
// skips panicText keeps TAILMARKER and the control byte.
func nastyPanicValue() string {
	return "HEAD\r\n\x01\x7f" + strings.Repeat("あ", 400) + "TAILMARKER"
}

func pinClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	prev := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = prev })
	return &cur
}

func panicAfterEnv(t *testing.T) (*env, string, *bytes.Buffer) {
	t.Helper()
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	stderr := &bytes.Buffer{}
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: stderr}
	return e, stateDir, stderr
}

func callJob(job daemon.Job) (err error, panicked any) {
	defer func() { panicked = recover() }()
	err = job(context.Background())
	return err, panicked
}

// allPanicLedger panics from All on the second call. dailyJob's CountsBefore
// is the first; the CountsAfter read in the bookkeeping tail is the second.
type allPanicLedger struct {
	*brokenLedger
	calls         int
	value         any
	poisonOnPanic bool
}

func newAllPanicLedger(value any, poisonOnPanic bool) *allPanicLedger {
	return &allPanicLedger{
		brokenLedger:  &brokenLedger{memLedger: fixture()},
		value:         value,
		poisonOnPanic: poisonOnPanic,
	}
}

func (a *allPanicLedger) All() []ledger.Record {
	a.calls++
	if a.calls >= 2 {
		if a.poisonOnPanic {
			a.poison(errors.New("ledger: fsync failed: input/output error"))
		}
		panic(a.value)
	}
	return a.memLedger.All()
}

type panicNotifier struct {
	calls int
	value any
}

func (p *panicNotifier) Notify(context.Context, string, string) error {
	p.calls++
	panic(p.value)
}

func assertPanicRecord(t *testing.T, stateDir string, err error, panicked any, fatal bool, notice *fakeNotifier, titlePrefix string) runRecord {
	t.Helper()
	if panicked != nil {
		t.Fatalf("panic escaped dailyJob: %v", panicked)
	}
	if err == nil || !errors.Is(err, errRunPanic) || errors.Is(err, daemon.ErrFatal) != fatal {
		t.Fatalf("returned %v, fatal=%v, want fatal=%v and errRunPanic", err, errors.Is(err, daemon.ErrFatal), fatal)
	}
	r := lastRecord(t, stateDir)
	if r.Outcome != outcomeFailed || r.FinishedAt.IsZero() {
		t.Fatalf("outcome=%s finished=%v error=%q, want a finished failed run", r.Outcome, r.FinishedAt, r.Error)
	}
	if r.Error == "" || !strings.Contains(r.Error, "panic") || strings.Contains(r.Error, "TAILMARKER") || strings.ContainsAny(r.Error, "\n\r\x01\x7f") {
		t.Fatalf("stored error %q", r.Error)
	}
	if n := utf8.RuneCountInString(r.Error); n > maxRunErrorRunes+1 {
		t.Fatalf("stored error is %d runes: %q", n, r.Error)
	}
	raw, readErr := os.ReadFile(runRecordPath(stateDir))
	if readErr != nil || bytes.Contains(raw, []byte{0x01}) || !bytes.Contains(raw, []byte(`"outcome": "failed"`)) {
		t.Fatalf("last.json read %v:\n%s", readErr, raw)
	}
	if notice != nil {
		if len(notice.calls) != 1 || !strings.HasPrefix(notice.calls[0], titlePrefix) {
			t.Fatalf("notices %v, want one %q", notice.calls, titlePrefix)
		}
		if strings.Contains(notice.calls[0], "TAILMARKER") || strings.Contains(notice.calls[0], "\x01") || strings.Contains(notice.calls[0], "HEAD") {
			t.Fatalf("notice leaked the panic text: %v", notice.calls)
		}
	}
	kept := historyRecord(t, stateDir, runID(r))
	if kept.Outcome != outcomeFailed || kept.Error != r.Error {
		t.Fatalf("history outcome=%s error=%q, want the failed record", kept.Outcome, kept.Error)
	}
	return r
}

func assertCleanPanicText(t *testing.T, stderr *bytes.Buffer, r runRecord) {
	t.Helper()
	text := panicText(nastyPanicValue())
	if text == "" || strings.Contains(text, "TAILMARKER") || strings.ContainsAny(text, "\n\r\x01\x7f") {
		t.Fatalf("panicText = %q", text)
	}
	if utf8.RuneCountInString(text) > maxPanicText+1 {
		t.Fatalf("panicText is %d runes: %q", utf8.RuneCountInString(text), text)
	}
	if !strings.Contains(r.Error, text) || !strings.Contains(r.Error, "HEAD") || !strings.Contains(r.Error, "あ") {
		t.Fatalf("stored error %q does not carry panicText %q", r.Error, text)
	}
	if strings.Contains(stderr.String(), "TAILMARKER") || strings.Contains(stderr.String(), "\x01") {
		t.Fatalf("stderr leaked the panic value:\n%s", stderr.String())
	}
}

// A RunReport panic is one bounded line in last.json, outcome failed, one notice.
func TestAdvPanicRunReportTextIsOneBoundedLine(t *testing.T) {
	e, stateDir, stderr := panicAfterEnv(t)
	n := &fakeNotifier{}
	app := &App{
		Ledger:   fixture(),
		Notifier: n,
		RunOnce:  func(context.Context) error { return nil },
		RunReport: func() runDetail {
			panic(nastyPanicValue())
		},
	}
	err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
	r := assertPanicRecord(t, stateDir, err, panicked, false, n, "Vani run failed|")
	assertCleanPanicText(t, stderr, r)
	if r.Cause == causeLedgerFatal {
		t.Fatalf("cause %q on a healthy ledger", r.Cause)
	}
}

// Ledger.All panics after RunOnce and RunReport both return. Same final record.
func TestAdvPanicLedgerAllAfterSuccessFinalises(t *testing.T) {
	e, stateDir, stderr := panicAfterEnv(t)
	n := &fakeNotifier{}
	led := newAllPanicLedger(nastyPanicValue(), false)
	reported := false
	app := &App{
		Ledger:   led,
		Notifier: n,
		RunOnce:  func(context.Context) error { return nil },
		RunReport: func() runDetail {
			reported = true
			return runDetail{}
		},
	}
	err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
	r := assertPanicRecord(t, stateDir, err, panicked, false, n, "Vani run failed|")
	assertCleanPanicText(t, stderr, r)
	if !reported || led.calls < 2 {
		t.Fatalf("reported=%v All calls=%d, want the tail read after a normal report", reported, led.calls)
	}
	if r.Cause == causeLedgerFatal {
		t.Fatalf("cause %q on a healthy ledger", r.Cause)
	}
}

// A panic value that is only spaces or empty is still a non-empty failed line.
func TestAdvPanicBlankTextIsANonEmptyLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"empty", ""},
		{"spaces", "   "},
		{"controls", "\n\x01\t  "},
		{"crlf", "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, stateDir, _ := panicAfterEnv(t)
			n := &fakeNotifier{}
			app := &App{
				Ledger:    fixture(),
				Notifier:  n,
				RunOnce:   func(context.Context) error { return nil },
				RunReport: func() runDetail { panic(tc.value) },
			}
			err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
			if panicked != nil {
				t.Fatalf("panic escaped: %v", panicked)
			}
			if err == nil || !errors.Is(err, errRunPanic) || errors.Is(err, daemon.ErrFatal) {
				t.Fatalf("returned %v, want a non-fatal errRunPanic", err)
			}
			r := lastRecord(t, stateDir)
			if r.Outcome != outcomeFailed || r.FinishedAt.IsZero() || r.Error == "" || !strings.Contains(r.Error, "panic") {
				t.Fatalf("outcome=%s error=%q finished=%v", r.Outcome, r.Error, r.FinishedAt)
			}
			if strings.ContainsAny(r.Error, "\n\r\x01") || utf8.RuneCountInString(r.Error) > maxPanicText+len("panic: ")+1 {
				t.Fatalf("stored error %q", r.Error)
			}
			if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani run failed|") {
				t.Fatalf("notices %v", n.calls)
			}
		})
	}
}

// announceFailure panics after a normal failure, and again from the recover.
// The record on disk is the failed run, not the running one.
func TestAdvPanicAnnounceFailureStillLeavesFailedRecord(t *testing.T) {
	t.Run("after a failed run", func(t *testing.T) {
		e, stateDir, _ := panicAfterEnv(t)
		n := &panicNotifier{value: nastyPanicValue()}
		app := &App{
			Ledger:    fixture(),
			Notifier:  n,
			RunOnce:   func(context.Context) error { return errors.New("pipeline broke") },
			RunReport: func() runDetail { return runDetail{} },
		}
		err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
		if err == nil && panicked == nil {
			t.Fatal("announce panic became a clean serve")
		}
		if err != nil && errors.Is(err, daemon.ErrFatal) {
			t.Fatalf("healthy ledger returned fatal %v", err)
		}
		r := lastRecord(t, stateDir)
		if r.Outcome != outcomeFailed || r.FinishedAt.IsZero() || r.Outcome == outcomeRunning {
			t.Fatalf("outcome=%s finished=%v error=%q", r.Outcome, r.FinishedAt, r.Error)
		}
		text := panicText(nastyPanicValue())
		if !strings.Contains(r.Error, "panic") || !strings.Contains(r.Error, "pipeline broke") || !strings.Contains(r.Error, text) {
			t.Fatalf("stored error %q, want the run error and panicText", r.Error)
		}
		if strings.Contains(r.Error, "TAILMARKER") || strings.ContainsAny(r.Error, "\n\r\x01\x7f") {
			t.Fatalf("stored error %q", r.Error)
		}
		if n.calls < 1 {
			t.Fatalf("notifier calls %d, want the failure notice to have started", n.calls)
		}
	})

	t.Run("after RunReport panics", func(t *testing.T) {
		e, stateDir, _ := panicAfterEnv(t)
		n := &panicNotifier{value: "notice\n\x01exploded"}
		app := &App{
			Ledger:    fixture(),
			Notifier:  n,
			RunOnce:   func(context.Context) error { return nil },
			RunReport: func() runDetail { panic("report boom") },
		}
		err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
		if err == nil && panicked == nil {
			t.Fatal("announce panic became a clean serve")
		}
		r := lastRecord(t, stateDir)
		if r.Outcome != outcomeFailed || r.FinishedAt.IsZero() || !strings.Contains(r.Error, "panic") || strings.Contains(r.Error, "\x01") {
			t.Fatalf("outcome=%s error=%q finished=%v", r.Outcome, r.Error, r.FinishedAt)
		}
		if !strings.Contains(r.Error, "report boom") {
			t.Fatalf("stored error %q, want the report panic saved before the notice panic", r.Error)
		}
	})
}

// A broken ledger makes the bookkeeping panic fatal. A healthy one, even
// when the panic value itself is ErrFatal, does not.
func TestAdvPanicBrokenLedgerIsFatalAndHealthyIsNot(t *testing.T) {
	t.Run("broken", func(t *testing.T) {
		e, stateDir, _ := panicAfterEnv(t)
		n := &fakeNotifier{}
		l := &brokenLedger{memLedger: fixture()}
		app := &App{
			Ledger:   l,
			Notifier: n,
			RunOnce:  func(context.Context) error { return nil },
			RunReport: func() runDetail {
				l.poison(errors.New("ledger: fsync failed: input/output error"))
				panic(nastyPanicValue())
			},
		}
		err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
		r := assertPanicRecord(t, stateDir, err, panicked, true, nil, "")
		if r.Cause != causeLedgerFatal {
			t.Fatalf("cause %q, want %s", r.Cause, causeLedgerFatal)
		}
		if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani stopped|") {
			t.Fatalf("notices %v, want one stopped notice", n.calls)
		}
	})

	t.Run("healthy panic value is ErrFatal", func(t *testing.T) {
		e, stateDir, _ := panicAfterEnv(t)
		n := &fakeNotifier{}
		app := &App{
			Ledger:    fixture(),
			Notifier:  n,
			RunOnce:   func(context.Context) error { return nil },
			RunReport: func() runDetail { panic(daemon.ErrFatal) },
		}
		err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
		r := assertPanicRecord(t, stateDir, err, panicked, false, n, "Vani run failed|")
		if r.Cause == causeLedgerFatal || strings.Contains(r.Error, "\n") {
			t.Fatalf("cause %q error %q", r.Cause, r.Error)
		}
	})

	t.Run("counts read poisons then panics", func(t *testing.T) {
		e, stateDir, _ := panicAfterEnv(t)
		n := &fakeNotifier{}
		led := newAllPanicLedger("counts boom", true)
		app := &App{
			Ledger:    led,
			Notifier:  n,
			RunOnce:   func(context.Context) error { return nil },
			RunReport: func() runDetail { return runDetail{} },
		}
		err, panicked := callJob(e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true}))
		r := assertPanicRecord(t, stateDir, err, panicked, true, nil, "")
		if r.Cause != causeLedgerFatal || !strings.Contains(r.Error, "panic") {
			t.Fatalf("cause %q error %q", r.Cause, r.Error)
		}
		if len(n.calls) != 1 || !strings.HasPrefix(n.calls[0], "Vani stopped|") {
			t.Fatalf("notices %v", n.calls)
		}
	})
}

// errConfigRestart before the bookkeeping tail puts the previous last.json
// bytes back and does not leave a failed run.
func TestAdvPanicConfigRestartRestoresPriorBytes(t *testing.T) {
	t.Run("before a record is written", func(t *testing.T) {
		cfgPath, stateDir := setup(t)
		mustCode(t, 0, "init")
		start, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		prior := histRec(4, outcomeOK)
		writeLast(t, stateDir, prior)
		before, err := os.ReadFile(runRecordPath(stateDir))
		if err != nil {
			t.Fatal(err)
		}
		running := start
		running.Schedule.DailyAt = "03:15"
		runs := 0
		app := &App{
			Ledger:   fixture(),
			Notifier: &fakeNotifier{},
			Reload:   func(config.Config) {},
			RunOnce: func(context.Context) error {
				runs++
				return nil
			},
			RunReport: func() runDetail {
				t.Error("RunReport after a restart")
				return runDetail{}
			},
		}
		e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		got := e.dailyJob(app, running, triggerSchedule, runOptions{NoDigest: true})(context.Background())
		if !errors.Is(got, errConfigRestart) || errors.Is(got, daemon.ErrFatal) {
			t.Fatalf("returned %v, want errConfigRestart", got)
		}
		if runs != 0 {
			t.Fatalf("pipeline ran %d times", runs)
		}
		after, err := os.ReadFile(runRecordPath(stateDir))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("last.json changed:\n%s\n%s\nerr %v", before, after, err)
		}
		if r := lastRecord(t, stateDir); r.Outcome != outcomeOK {
			t.Fatalf("outcome %s", r.Outcome)
		}
	})

	t.Run("after the running record is written", func(t *testing.T) {
		cfgPath, stateDir := setup(t)
		mustCode(t, 0, "init")
		start, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		prior := histRec(5, outcomeOK)
		prior.Error = "prior-bytes"
		writeLast(t, stateDir, prior)
		before, err := os.ReadFile(runRecordPath(stateDir))
		if err != nil {
			t.Fatal(err)
		}
		oldStat := statExecutable
		t.Cleanup(func() { statExecutable = oldStat })
		stats := 0
		statExecutable = func(string) (binIdentity, error) {
			stats++
			if stats == 1 {
				return binIdentity{dev: 1, ino: 1, size: 1, mtime: 1}, nil
			}
			return binIdentity{dev: 1, ino: 2, size: 2, mtime: 2}, nil
		}
		runs := 0
		app := &App{
			Ledger:   fixture(),
			Notifier: &fakeNotifier{},
			Reload:   func(config.Config) {},
			RunOnce: func(context.Context) error {
				runs++
				return errors.New("should not run")
			},
		}
		e := &env{
			configPath: cfgPath,
			stateDir:   stateDir,
			stdout:     &bytes.Buffer{},
			stderr:     &bytes.Buffer{},
			binary:     &binWatch{path: "vani-test", start: binIdentity{dev: 1, ino: 1, size: 1, mtime: 1}},
		}
		got := e.dailyJob(app, start, triggerSchedule, runOptions{NoDigest: true})(context.Background())
		if !errors.Is(got, errConfigRestart) || errors.Is(got, daemon.ErrFatal) {
			t.Fatalf("returned %v, want errConfigRestart (stats %d)", got, stats)
		}
		if runs != 0 {
			t.Fatalf("pipeline ran %d times", runs)
		}
		after, err := os.ReadFile(runRecordPath(stateDir))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("last.json changed:\n%s\n%s\nerr %v", before, after, err)
		}
		if bytes.Contains(after, []byte(`"outcome": "failed"`)) || bytes.Contains(after, []byte(`"outcome": "running"`)) {
			t.Fatalf("restart left a run record:\n%s", after)
		}
	})
}

// A finished failed record is still what the next save flushes. A symlink at
// the history leaf is replaced, not followed.
func TestAdvPanicFailedRecordFlushesPastSymlink(t *testing.T) {
	_, stateDir := setup(t)
	failed := histRec(1, outcomeFailed)
	writeLast(t, stateDir, failed)
	failedBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	const sentinel = "SENTINEL-NOT-THE-RECORD"
	if err := os.WriteFile(outside, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir, runHistoryDir), 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(failed))
	if err := os.Symlink(outside, leaf); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	next := histRec(2, outcomeOK)
	e.saveRunRecord(next)

	info, err := os.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("history leaf mode %v, want a regular file", info.Mode())
	}
	got, err := os.ReadFile(leaf)
	if err != nil || !bytes.Equal(got, failedBytes) {
		t.Fatalf("history bytes:\n%s\nwant\n%s\nerr %v", got, failedBytes, err)
	}
	sent, err := os.ReadFile(outside)
	if err != nil || string(sent) != sentinel {
		t.Fatalf("symlink target = %q, %v; the leaf was followed", sent, err)
	}
	last := lastRecord(t, stateDir)
	if last.Outcome != outcomeOK || runID(last) != runID(next) {
		t.Fatalf("last.json %+v", last)
	}
	kept := historyRecord(t, stateDir, runID(failed))
	if kept.Outcome != outcomeFailed {
		t.Fatalf("flushed outcome %s, want failed", kept.Outcome)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

// The run after a RunReport panic is that later run's ok record. The panicked
// run is in the history as failed, not left running.
func TestAdvPanicNextRunReplacesAPanickedRun(t *testing.T) {
	e, stateDir, _ := panicAfterEnv(t)
	clock := pinClock(t, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	n := &fakeNotifier{}
	reports := 0
	app := &App{
		Ledger:   fixture(),
		Notifier: n,
		RunOnce:  func(context.Context) error { return nil },
		RunReport: func() runDetail {
			reports++
			if reports == 1 {
				panic(nastyPanicValue())
			}
			return runDetail{}
		},
	}
	job := e.dailyJob(app, config.Default(), triggerSchedule, runOptions{NoDigest: true})
	err, panicked := callJob(job)
	first := assertPanicRecord(t, stateDir, err, panicked, false, n, "Vani run failed|")
	*clock = clock.Add(time.Second)
	err, panicked = callJob(job)
	if panicked != nil || err != nil {
		t.Fatalf("second run: err %v panic %v", err, panicked)
	}
	second := lastRecord(t, stateDir)
	if second.Outcome != outcomeOK || second.FinishedAt.IsZero() || runID(second) == runID(first) {
		t.Fatalf("last.json outcome=%s id=%s first=%s", second.Outcome, runID(second), runID(first))
	}
	if second.Error != "" {
		t.Fatalf("successful run stored %q", second.Error)
	}
	kept := historyRecord(t, stateDir, runID(first))
	if kept.Outcome != outcomeFailed || strings.Contains(kept.Error, "TAILMARKER") {
		t.Fatalf("first history outcome=%s error=%q", kept.Outcome, kept.Error)
	}
	raw, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil || bytes.Contains(raw, []byte(`"outcome": "running"`)) || !bytes.Contains(raw, []byte(`"outcome": "ok"`)) {
		t.Fatalf("last.json:\n%s\nerr %v", raw, err)
	}
	if len(n.calls) != 1 {
		t.Fatalf("notices %v, want only the failed run", n.calls)
	}
}
