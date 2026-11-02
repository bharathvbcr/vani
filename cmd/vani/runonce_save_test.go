package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
// may replace recordWriter after this returns. recordWriter stays nil: that
// is writeRunRecord, not a writer that drops errors.
func newRunOnceEnv(t *testing.T, run func(context.Context) error) *isoEnv {
	t.Helper()
	dir := t.TempDir()
	return newRunOnceEnvAt(t, filepath.Join(dir, "config.json"), filepath.Join(dir, "state"), run)
}

// newRunOnceEnvAt is newRunOnceEnv with the caller's config and state paths.
// stateDir is the test's directory, never the user's Library state.
func newRunOnceEnvAt(t *testing.T, cfgPath, stateDir string, run func(context.Context) error) *isoEnv {
	t.Helper()
	if run == nil {
		run = func(context.Context) error { return nil }
	}
	if strings.Contains(stateDir, filepath.Join("Library", "Application Support", "Vani")) {
		t.Fatalf("refusing state dir %q", stateDir)
	}
	x := &isoEnv{stateDir: stateDir}
	x.ev = &env{configPath: cfgPath, stateDir: stateDir, stdout: &x.o, stderr: &x.e}
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

// blankErr is a non-nil write failure whose text is empty. omitempty must not
// hide it: the reply's error string has to stay non-empty.
type blankErr struct{}

func (blankErr) Error() string { return "" }

// TestRunOnceJSONSaveAdversary attacks the finished-record save contract:
// ignored running-write errors, empty and wrapped finished-write errors, a
// pipeline failure joined with a save failure, cancellation after a successful
// finished write, a history failure only after last.json, a flush that must
// not replace the previous record, an empty state dir, a last.json symlink,
// one save from the panic recover path, and a non-ASCII state dir.
func TestRunOnceJSONSaveAdversary(t *testing.T) {
	t.Run("failure struct keeps omitempty", func(t *testing.T) {
		typ := reflect.TypeOf(failure{})
		for _, name := range []string{"Error", "Code"} {
			f, ok := typ.FieldByName(name)
			if !ok {
				t.Fatalf("failure.%s missing", name)
			}
			tag := f.Tag.Get("json")
			if !strings.HasSuffix(tag, ",omitempty") {
				t.Fatalf("failure.%s tag %q, omitempty must stay", name, tag)
			}
		}
	})

	t.Run("running write fails finished write succeeds", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if r.Outcome == outcomeRunning {
				return errors.New("running-ignored")
			}
			return writeRunRecord(stateDir, r)
		}
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitOK {
			t.Fatalf("exit %d, want 0; a running-record write failure is ignored\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		root := jsonObject(t, stdout)
		if _, ok := root["error"]; ok {
			t.Fatalf("running-write failure leaked into the reply: %s", stdout)
		}
		if _, ok := root["code"]; ok {
			t.Fatalf("clean run has a code field: %s", stdout)
		}
		rec, _ := root["record"].(map[string]any)
		if rec["outcome"] != outcomeOK {
			t.Fatalf("outcome %v, want ok\n%s", rec["outcome"], stdout)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeOK || got.FinishedAt.IsZero() {
			t.Fatalf("last.json %+v, want the finished record", got)
		}
	})

	t.Run("finished write plain wrapped and empty errors", func(t *testing.T) {
		plain := errors.New("plain-disk")
		wrapped := fmt.Errorf("wrap-disk: %w", errors.New("inner-disk"))
		joinedEmpty := errors.Join(errors.New(""))
		percentEmpty := fmt.Errorf("%w", errors.New(""))
		for _, tc := range []struct {
			name string
			err  error
			bits []string
		}{
			{"plain", plain, []string{"plain-disk", "could not save the finished run record"}},
			{"wrapped", wrapped, []string{"wrap-disk", "inner-disk", "could not save the finished run record"}},
			{"blank", blankErr{}, []string{"could not save the finished run record"}},
			{"join empty", joinedEmpty, []string{"could not save the finished run record"}},
			{"percent-w empty", percentEmpty, []string{"could not save the finished run record"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.name != "plain" && tc.name != "wrapped" && tc.err.Error() != "" {
					t.Fatalf("input error text = %q, want empty", tc.err.Error())
				}
				if tc.err == nil {
					t.Fatal("input error is nil")
				}
				x := newRunOnceEnv(t, nil)
				x.ev.recordWriter = func(stateDir string, r runRecord) error {
					if r.Outcome == outcomeRunning {
						return writeRunRecord(stateDir, r)
					}
					return tc.err
				}
				code, stdout, stderr := x.do("--json", "run-once")
				msg := assertFinishedSaveFailed(t, code, stdout, stderr, tc.bits...)
				if strings.TrimSpace(msg) == "" {
					t.Fatalf("write failed but the reply error is blank: %q\nstdout: %s", msg, stdout)
				}
				if !strings.Contains(stdout, `"error":`) {
					t.Fatalf("omitempty hid the error: %s", stdout)
				}
				assertReplyStillRunning(t, x.stateDir, stdout)
			})
		}
	})

	t.Run("pipeline failure joined with finished write failure", func(t *testing.T) {
		x := newRunOnceEnv(t, func(context.Context) error { return errors.New("boards-down") })
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if r.Outcome == outcomeRunning {
				return writeRunRecord(stateDir, r)
			}
			return errors.New("plain-disk")
		}
		code, stdout, stderr := x.do("--json", "run-once")
		assertFinishedSaveFailed(t, code, stdout, stderr, "boards-down", "plain-disk")
		assertReplyStillRunning(t, x.stateDir, stdout)
	})

	t.Run("cancel with a successful finished write", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		oldSignal := signalContext
		signalContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
		t.Cleanup(func() { signalContext = oldSignal })
		x := newRunOnceEnv(t, func(ctx context.Context) error { return ctx.Err() })
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitCode(context.Canceled) {
			t.Fatalf("exit %d, want the cancel outcome %d\nstdout: %s\nstderr: %s", code, exitCode(context.Canceled), stdout, stderr)
		}
		if code == exitOK || code == exitUsage {
			t.Fatalf("cancel exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		msg, gotCode := failureIn(t, []string{"--json", "run-once"}, stdout)
		if gotCode != codeRuntime || !strings.Contains(msg, "context canceled") {
			t.Fatalf("error %q code %q\nstderr: %s", msg, gotCode, stderr)
		}
		root := jsonObject(t, stdout)
		if root["state"] == outcomeRunning || root["state"] == outcomeOK {
			t.Fatalf("state %v with a cancelled run\n%s", root["state"], stdout)
		}
		rec, _ := root["record"].(map[string]any)
		if rec["outcome"] != outcomeCancelled {
			t.Fatalf("reply outcome %v, want cancelled; not a clean ok with a running record\n%s", rec["outcome"], stdout)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeCancelled || got.FinishedAt.IsZero() {
			t.Fatalf("last.json %+v, want the finished cancelled record", got)
		}
	})

	t.Run("history file after the finished last.json write", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if err := writeRunRecord(stateDir, r); err != nil {
				return err
			}
			if r.Outcome == outcomeRunning {
				return nil
			}
			onDisk := lastRecord(t, stateDir)
			if onDisk.Outcome != outcomeOK || onDisk.FinishedAt.IsZero() {
				t.Fatalf("history blocked before the finished record landed: %+v", onDisk)
			}
			blockRunHistory(t, stateDir)
			return nil
		}
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitOK {
			t.Fatalf("exit %d, want 0; history failure after last.json must not fail the run\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		root := jsonObject(t, stdout)
		if _, ok := root["error"]; ok {
			t.Fatalf("history failure leaked into the reply: %s", stdout)
		}
		if strings.Contains(stdout, "could not keep the run in the history") {
			t.Fatalf("history warning is in the reply: %s", stdout)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeOK || got.Error != "" || got.FinishedAt.IsZero() {
			t.Fatalf("last.json %+v, want a finished ok record with no error", got)
		}
		info, err := os.Lstat(filepath.Join(x.stateDir, runRecordDir, runHistoryDir))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("history path is not the blocking file: %v %v", info, err)
		}
		if !strings.Contains(stderr, "could not keep the run in the history") {
			t.Fatalf("history failure was not warned: %q", stderr)
		}
	})

	t.Run("flush of the previous record fails", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		prev := histRec(1, outcomeOK)
		writeLast(t, x.stateDir, prev)
		before, err := os.ReadFile(runRecordPath(x.stateDir))
		if err != nil {
			t.Fatal(err)
		}
		old := writeFinishedHistory
		writeFinishedHistory = func(string, runRecord) error { return errors.New("flush-denied") }
		t.Cleanup(func() { writeFinishedHistory = old })
		code, stdout, stderr := x.do("--json", "run-once")
		assertFinishedSaveFailed(t, code, stdout, stderr, "flush-denied")
		after, err := os.ReadFile(runRecordPath(x.stateDir))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("previous last.json changed\nbefore %s\nafter %s", before, after)
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
		e := &env{stdout: &strings.Builder{}, stderr: &strings.Builder{}}
		if e.recordWriter != nil {
			t.Fatal("nil recordWriter was replaced")
		}
		err = e.saveFinishedRun(histRec(1, outcomeOK))
		if err == nil || exitCode(err) != exitRuntime || !errors.Is(err, errNoStateDir) || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("exit %d err %v", exitCode(err), err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		app := &App{
			Ledger:  fixture(),
			RunOnce: func(context.Context) error { return nil },
			Tune:    func(runOptions) {},
		}
		err = e.dailyJob(app, config.Default(), triggerManual, runOptions{NoDigest: true})(ctx)
		if err == nil || exitCode(err) != exitRuntime || !errors.Is(err, errNoStateDir) || strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("dailyJob exit %d err %v", exitCode(err), err)
		}
		if _, statErr := os.Lstat(relRuns); statErr == nil {
			t.Fatal("empty state dir wrote a relative runs directory")
		}
	})

	t.Run("last.json symlink", func(t *testing.T) {
		t.Run("absolute file", func(t *testing.T) {
			x := newRunOnceEnv(t, nil)
			outside := filepath.Join(t.TempDir(), "leaf.json")
			const sentinel = "link-sentinel"
			if err := os.WriteFile(outside, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			link := plantLastLink(t, x.stateDir, outside)
			code, stdout, stderr := x.do("--json", "run-once")
			assertSymlinkReplaced(t, x.stateDir, link, code, stdout, stderr)
			if b, err := os.ReadFile(outside); err != nil || string(b) != sentinel {
				t.Fatalf("wrote through the symlink: %q %v", b, err)
			}
		})
		t.Run("relative file", func(t *testing.T) {
			x := newRunOnceEnv(t, nil)
			outsideDir := filepath.Join(filepath.Dir(x.stateDir), "outside")
			if err := os.MkdirAll(outsideDir, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(outsideDir, "leaf.json")
			const sentinel = "rel-sentinel"
			if err := os.WriteFile(outside, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(filepath.Join(x.stateDir, runRecordDir), outside)
			if err != nil {
				t.Fatal(err)
			}
			link := plantLastLink(t, x.stateDir, rel)
			code, stdout, stderr := x.do("--json", "run-once")
			assertSymlinkReplaced(t, x.stateDir, link, code, stdout, stderr)
			if b, err := os.ReadFile(outside); err != nil || string(b) != sentinel {
				t.Fatalf("wrote through the relative symlink: %q %v", b, err)
			}
		})
		t.Run("dangling", func(t *testing.T) {
			x := newRunOnceEnv(t, nil)
			target := filepath.Join(t.TempDir(), "missing.json")
			link := plantLastLink(t, x.stateDir, target)
			code, stdout, stderr := x.do("--json", "run-once")
			assertSymlinkReplaced(t, x.stateDir, link, code, stdout, stderr)
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dangling target was created: %v", err)
			}
		})
		t.Run("loop", func(t *testing.T) {
			x := newRunOnceEnv(t, nil)
			link := runRecordPath(x.stateDir)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(link, link); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := x.do("--json", "run-once")
			assertSymlinkReplaced(t, x.stateDir, link, code, stdout, stderr)
		})
	})

	t.Run("panic recover saves the finished record once", func(t *testing.T) {
		x := newRunOnceEnv(t, nil)
		buildApp = func(config.Config, string, io.Writer) (*App, error) {
			return &App{
				Ledger:    fixture(),
				Notifier:  &fakeNotifier{},
				RunOnce:   func(context.Context) error { return nil },
				RunReport: func() runDetail { panic("rep-boom") },
			}, nil
		}
		var finished int
		x.ev.recordWriter = func(stateDir string, r runRecord) error {
			if r.Outcome == outcomeRunning {
				return writeRunRecord(stateDir, r)
			}
			finished++
			if finished >= 2 {
				return errors.New("second-finished")
			}
			return writeRunRecord(stateDir, r)
		}
		code, stdout, stderr, panicked := doNoPanic(x, "--json", "run-once")
		if panicked != nil {
			t.Fatalf("panic escaped the run: %v\nstdout: %s\nstderr: %s", panicked, stdout, stderr)
		}
		if finished != 1 {
			t.Fatalf("finished saves = %d, want 1 (the recover path saves once)", finished)
		}
		if code != exitRuntime {
			t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, exitRuntime, stdout, stderr)
		}
		msg, gotCode := failureIn(t, []string{"--json", "run-once"}, stdout)
		if gotCode != codeRuntime || !strings.Contains(msg, "rep-boom") || strings.Contains(msg, "second-finished") {
			t.Fatalf("error %q code %q\nstderr: %s", msg, gotCode, stderr)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeFailed || got.FinishedAt.IsZero() || !strings.Contains(got.Error, "rep-boom") {
			t.Fatalf("last.json %+v, want the finished failed record", got)
		}
	})

	t.Run("unicode state dir", func(t *testing.T) {
		root := t.TempDir()
		state := filepath.Join(root, "state 東京")
		if !strings.Contains(state, " ") || !strings.Contains(state, "東京") {
			t.Fatalf("state dir %q has no space or non-ASCII", state)
		}
		x := newRunOnceEnvAt(t, filepath.Join(root, "config.json"), state, nil)
		if x.ev.recordWriter != nil {
			t.Fatal("nil recordWriter was replaced before the run")
		}
		code, stdout, stderr := x.do("--json", "run-once")
		if code != exitOK {
			t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
		if x.ev.recordWriter != nil {
			t.Fatal("the run installed a record writer")
		}
		rootObj := jsonObject(t, stdout)
		if _, ok := rootObj["error"]; ok {
			t.Fatalf("clean run has an error field: %s", stdout)
		}
		if !strings.Contains(stdout, "東京") {
			t.Fatalf("reply path lost the state dir: %s", stdout)
		}
		got := lastRecord(t, x.stateDir)
		if got.Outcome != outcomeOK || got.FinishedAt.IsZero() {
			t.Fatalf("last.json %+v, want the finished record", got)
		}
		if !strings.HasPrefix(runRecordPath(x.stateDir), root) {
			t.Fatalf("record path %s is outside %s", runRecordPath(x.stateDir), root)
		}
	})
}

func assertFinishedSaveFailed(t *testing.T, code int, stdout, stderr string, bits ...string) string {
	t.Helper()
	if code != exitRuntime {
		t.Fatalf("exit %d, want %d (not %d)\nstdout: %s\nstderr: %s", code, exitRuntime, exitUsage, stdout, stderr)
	}
	msg, gotCode := failureIn(t, []string{"--json", "run-once"}, stdout)
	if gotCode != codeRuntime || strings.TrimSpace(msg) == "" {
		t.Fatalf("error %q code %q\nstderr: %s", msg, gotCode, stderr)
	}
	for _, bit := range bits {
		if !strings.Contains(msg, bit) {
			t.Fatalf("error %q missing %q\nstderr: %s", msg, bit, stderr)
		}
	}
	return msg
}

func assertReplyStillRunning(t *testing.T, stateDir, stdout string) {
	t.Helper()
	got := lastRecord(t, stateDir)
	if got.Outcome != outcomeRunning || !got.FinishedAt.IsZero() {
		t.Fatalf("last.json %+v, want the running record", got)
	}
	root := jsonObject(t, stdout)
	rec, _ := root["record"].(map[string]any)
	if rec["outcome"] != outcomeRunning {
		t.Fatalf("reply outcome %v, want running while the error is set\n%s", rec["outcome"], stdout)
	}
}

func plantLastLink(t *testing.T, stateDir, target string) string {
	t.Helper()
	link := runRecordPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func assertSymlinkReplaced(t *testing.T, stateDir, link string, code int, stdout, stderr string) {
	t.Helper()
	if code != exitOK {
		t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	root := jsonObject(t, stdout)
	if _, ok := root["error"]; ok {
		t.Fatalf("symlink replacement has an error field: %s", stdout)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json was not replaced by a regular file: %v %v", info, err)
	}
	got := lastRecord(t, stateDir)
	if got.Outcome != outcomeOK || got.FinishedAt.IsZero() {
		t.Fatalf("last.json %+v, want the finished record", got)
	}
	ents, err := os.ReadDir(filepath.Dir(link))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// doNoPanic runs one command and recovers a panic so the test process stays up.
func doNoPanic(x *isoEnv, args ...string) (code int, stdout, stderr string, panicked any) {
	defer func() { panicked = recover() }()
	code, stdout, stderr = x.do(args...)
	return code, stdout, stderr, panicked
}
