package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// TestRunOnceJSONFinishedRecordSave is the run-once --json contract when the
// finished last.json write does not land. A clean reply while the saved
// record is still running is the bug: the error must be visible and the
// process must exit 1.
func TestRunOnceJSONFinishedRecordSave(t *testing.T) {
	t.Run("finished write path error", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if r.Outcome == outcomeRunning {
				return writeRunRecord(stateDir, r)
			}
			return &os.PathError{Op: "write", Path: runRecordPath(stateDir), Err: os.ErrPermission}
		}
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitRuntime {
			t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, exitRuntime, stdout, stderr)
		}
		msg, _ := failureIn(t, []string{"--json", "run-once"}, stdout)
		if !strings.Contains(msg, "permission denied") || !strings.Contains(msg, "last.json") {
			t.Fatalf("error %q does not carry the path error", msg)
		}
		root := jsonObject(t, stdout)
		rec, _ := root["record"].(map[string]any)
		if rec["outcome"] != outcomeRunning {
			t.Fatalf("reply record outcome %v, want running while the error is set\n%s", rec["outcome"], stdout)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeRunning || !got.FinishedAt.IsZero() {
			t.Fatalf("last.json %+v, want the running record with no finished outcome", got)
		}
	})

	t.Run("successful finished write", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitOK {
			t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		root := jsonObject(t, stdout)
		if _, ok := root["error"]; ok {
			t.Fatalf("clean run has an error field: %s", stdout)
		}
		if _, ok := root["code"]; ok {
			t.Fatalf("clean run has a code field: %s", stdout)
		}
		rec, _ := root["record"].(map[string]any)
		if rec["outcome"] != outcomeOK {
			t.Fatalf("outcome %v, want ok\n%s", rec["outcome"], stdout)
		}
		if lastRecord(t, x.stateDir).Outcome != outcomeOK {
			t.Fatal("last.json is not the finished record")
		}
	})

	t.Run("history failure after last.json", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if err := writeRunRecord(stateDir, r); err != nil {
				return err
			}
			if r.Outcome == outcomeRunning {
				blockRunHistory(t, stateDir)
			}
			return nil
		}
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitOK {
			t.Fatalf("exit %d, want 0; a history failure after last.json must not fail the run\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		root := jsonObject(t, stdout)
		if _, ok := root["error"]; ok {
			t.Fatalf("history failure leaked into the reply: %s", stdout)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeOK {
			t.Fatalf("last.json outcome %s, want ok", got.Outcome)
		}
		if historyFileKept(runHistoryPath(x.stateDir, runID(got))) {
			t.Fatal("history kept a record the history write could not create")
		}
		if !strings.Contains(stderr, "could not keep the run in the history") {
			t.Fatalf("history failure was not warned: %q", stderr)
		}
	})

	t.Run("flush blocks the finished write", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		prev := histRec(1, outcomeOK)
		writeLast(t, x.stateDir, prev)
		old := writeFinishedHistory
		writeFinishedHistory = func(string, runRecord) error {
			return &os.PathError{Op: "write", Path: "runs/history/previous.json", Err: os.ErrPermission}
		}
		t.Cleanup(func() { writeFinishedHistory = old })

		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitRuntime {
			t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, exitRuntime, stdout, stderr)
		}
		msg, _ := failureIn(t, []string{"--json", "run-once"}, stdout)
		if !strings.Contains(msg, "permission denied") {
			t.Fatalf("flush failure hidden: %q\nstderr: %s", msg, stderr)
		}
		got := lastRecord(t, x.stateDir)
		if runID(got) != runID(prev) || got.Outcome != outcomeOK {
			t.Fatalf("last.json replaced despite the flush failure: %+v", got)
		}
	})

	t.Run("empty state dir", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relRuns := filepath.Join(wd, runRecordDir)
		if _, err := os.Lstat(relRuns); err == nil {
			t.Fatal("refusing to run: a relative runs directory already exists")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var stderr strings.Builder
		e := &env{stdout: &strings.Builder{}, stderr: &stderr}
		app := &App{
			Ledger:  fixture(),
			RunOnce: func(context.Context) error { return nil },
			Tune:    func(runOptions) {},
		}
		err = e.dailyJob(app, config.Default(), triggerManual, runOptions{NoDigest: true})(ctx)
		if exitCode(err) != exitRuntime || err == nil || !strings.Contains(err.Error(), errNoStateDir.Error()) {
			t.Fatalf("exit %d err %v\nstderr: %s", exitCode(err), err, stderr.String())
		}
		if !errors.Is(err, errNoStateDir) {
			t.Fatalf("empty state dir error lost its cause: %v", err)
		}
		if _, statErr := os.Lstat(relRuns); statErr == nil {
			t.Fatal("empty state dir wrote a relative runs directory")
		}
	})

	t.Run("cancel does not hide a save failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cancel()
		oldSignal := signalContext
		signalContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
		t.Cleanup(func() { signalContext = oldSignal })
		x := newRunOnceEnv(t, func(ctx context.Context) error { return ctx.Err() })
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if r.Outcome == outcomeRunning {
				return writeRunRecord(stateDir, r)
			}
			return &os.PathError{Op: "write", Path: runRecordPath(stateDir), Err: os.ErrPermission}
		}
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitRuntime {
			t.Fatalf("exit %d, want %d; cancellation must not turn the save failure into success\nstdout: %s\nstderr: %s", code, exitRuntime, stdout, stderr)
		}
		msg, _ := failureIn(t, []string{"--json", "run-once"}, stdout)
		if !strings.Contains(msg, "permission denied") {
			t.Fatalf("save failure hidden behind cancellation: %q\nstderr: %s", msg, stderr)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeRunning {
			t.Fatalf("last.json outcome %s, want running", got.Outcome)
		}
	})
}

// newRunOnceEnv is a run-once whose pipeline is the in-memory fixture. run
// nil succeeds without asking a decision backend or the network. The caller
// may replace recordWriter after this returns.
func newRunOnceEnv(t *testing.T, run func(context.Context) error) *isoEnv {
	t.Helper()
	if run == nil {
		run = func(context.Context) error { return nil }
	}
	x := newIsoEnv(t)
	if code, stdout, stderr := x.do("init"); code != 0 {
		t.Fatalf("init: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	oldBuild, oldNow := buildApp, now
	t.Cleanup(func() { buildApp, now = oldBuild, oldNow })
	now = func() time.Time { return time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC) }
	led := fixture()
	n := &fakeNotifier{}
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: led, Notifier: n, RunOnce: run}, nil
	}
	return x
}

// blockRunHistory puts a regular file where the history directory is, so the
// history write fails and last.json can still be written.
func blockRunHistory(t *testing.T, stateDir string) {
	t.Helper()
	dir := filepath.Join(stateDir, runRecordDir, runHistoryDir)
	if err := os.Rename(dir, dir+".aside"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jsonObject(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&m); err != nil || dec.More() {
		t.Fatalf("stdout %q is not one JSON object (%v)", stdout, err)
	}
	return m
}
