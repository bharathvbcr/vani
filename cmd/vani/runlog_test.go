package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The run record names the log of its run, and `vani runs failures` reports
// that log and no other. The app that starts a run says where its output goes
// (VANI_RUN_LOG); a path outside <state>/runs is never recorded or followed.

func runsFile(stateDir, name string) string { return filepath.Join(stateDir, runRecordDir, name) }

func writeLog(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceRecordsTheLogItWasToldAbout(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	log := runsFile(stateDir, "manual.log")
	writeLog(t, log, "")
	t.Setenv(runLogEnv, log)
	wireTuned(t, fixture())

	mustCode(t, 0, "run-once")

	rec := lastRaw(t, stateDir)
	if rec["log"] != log {
		t.Errorf("record log = %v, want %s", rec["log"], log)
	}
	if rec["id"] != runID(lastRecord(t, stateDir)) {
		t.Errorf("record id = %v, want the run's id", rec["id"])
	}
	// The app already captured this run. A second history/<id>.log would be a second copy.
	id := runID(lastRecord(t, stateDir))
	if _, err := os.Lstat(filepath.Join(stateDir, runRecordDir, runHistoryDir, id+".log")); !os.IsNotExist(err) {
		t.Errorf("run log = %v, want no per-run file beside the one VANI_RUN_LOG named", err)
	}
}

// A path the check refuses leaves the run alone: it runs, stderr says why, and
// the record names the run's own history log instead of the refused path.
func TestRunOnceIgnoresALogOutsideTheRunsFolder(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.log")
	writeLog(t, outside, "x")
	for name, bad := range map[string]string{
		"outside the state folder": outside,
		"relative":                 "runs/manual.log",
		"dot-dot":                  "STATE/runs/../config.json",
		"the state folder itself":  "STATE",
		"newline":                  "STATE/runs/a\nb.log",
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			writeLog(t, runsFile(stateDir, "manual.log"), "")
			t.Setenv(runLogEnv, strings.ReplaceAll(bad, "STATE", stateDir))
			wireTuned(t, fixture())

			_, stderr := mustCode(t, 0, "run-once")

			got, _ := lastRaw(t, stateDir)["log"].(string)
			wantDir := filepath.Join(stateDir, runRecordDir, runHistoryDir) + string(filepath.Separator)
			if !strings.HasPrefix(got, wantDir) || !strings.HasSuffix(got, ".log") {
				t.Errorf("record log = %v, want a file in %s", got, wantDir)
			}
			refused := strings.ReplaceAll(bad, "STATE", stateDir)
			if got == refused || strings.Contains(got, "..") {
				t.Errorf("record kept the refused path %q as %q", refused, got)
			}
			if !strings.Contains(stderr, runLogEnv+" ignored") {
				t.Errorf("stderr does not say the log was ignored: %q", stderr)
			}
			if r := lastRecord(t, stateDir); r.Outcome != outcomeOK {
				t.Errorf("run outcome %s; a bad log path must not stop the run", r.Outcome)
			}
		})
	}
}

func TestCheckRunLog(t *testing.T) {
	state := t.TempDir()
	runs := filepath.Join(state, runRecordDir)
	hist := filepath.Join(runs, runHistoryDir)
	for _, d := range []string{runs, hist} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	writeLog(t, filepath.Join(outside, "secret"), "x")
	writeLog(t, filepath.Join(runs, "real.log"), "x")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(runs, "link.log")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runs, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(runs, "dir.log"), 0o700); err != nil {
		t.Fatal(err)
	}

	ok := []string{
		filepath.Join(runs, "manual.log"), // not written yet
		filepath.Join(runs, "real.log"),
		filepath.Join(hist, "20261022T090000.123Z-4242.log"),
	}
	for _, p := range ok {
		if got, err := checkRunLog(state, p); err != nil || got != p {
			t.Errorf("checkRunLog(%q) = %q, %v; want it accepted as is", p, got, err)
		}
	}
	bad := map[string]string{
		"empty":                 "",
		"relative":              "runs/manual.log",
		"dot-dot":               filepath.Join(runs, "..", "config.json"),
		"dot-dot inside":        runs + "/hist/../manual.log",
		"outside":               filepath.Join(outside, "secret"),
		"the runs folder":       runs,
		"the state folder":      state,
		"a link to a file":      filepath.Join(runs, "link.log"),
		"through a linked dir":  filepath.Join(runs, "escape", "secret"),
		"a directory":           filepath.Join(runs, "dir.log"),
		"folder does not exist": filepath.Join(runs, "nope", "manual.log"),
		"control character":     filepath.Join(runs, "a\x00b.log"),
		"too long":              filepath.Join(runs, strings.Repeat("a", maxRunLogPath)),
		"sibling prefix":        runs + "-other/manual.log",
	}
	for name, p := range bad {
		if got, err := checkRunLog(state, p); err == nil {
			t.Errorf("%s: checkRunLog(%q) accepted as %q", name, p, got)
		}
	}
	// No runs folder at all: nothing to be inside of.
	if _, err := checkRunLog(t.TempDir(), filepath.Join(t.TempDir(), "runs", "manual.log")); err == nil {
		t.Error("a log with no runs folder was accepted")
	}
}

func reportOf(t *testing.T, args ...string) string {
	t.Helper()
	return failuresOf(t, args...).Text
}

func logLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "Log:") {
			out = append(out, l)
		}
	}
	return out
}

// The report of a run that recorded its log names that file, with its size,
// and not the daemon's logs that happen to exist.
func TestRunsFailuresNamesTheRunsOwnLog(t *testing.T) {
	_, stateDir := setup(t)
	log := runsFile(stateDir, "manual.log")
	writeLog(t, log, "run output\n")
	writeLog(t, filepath.Join(stateDir, "logs", "vani.err.log"), "daemon noise")
	r := histRec(1, outcomeFailed)
	rawLast(t, stateDir, r, map[string]any{"log": log})

	lines := logLines(reportOf(t))
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "Log: "+log+" (11 bytes, last written ") {
		t.Errorf("log lines %q, want only the run's own manual.log", lines)
	}
}

// manual.log is replaced by every app-started run, so an older run's record
// that names it must not claim the file the newest run is writing. The app
// keeps the replaced log as history/manual-<run id>.log, which is the older run's.
func TestRunsFailuresNeverNamesAnotherRunsLog(t *testing.T) {
	_, stateDir := setup(t)
	log := runsFile(stateDir, "manual.log")
	old, newer := histRec(1, outcomeFailed), histRec(2, outcomeFailed)
	old.PID, newer.PID = 4001, 4002
	// Both history entries carry the shared log; last.json is the newer run.
	for _, r := range []runRecord{old, newer} {
		raw := lastRawOf(t, r, map[string]any{"log": log})
		writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, runID(r)+".json"), raw)
	}
	rawLast(t, stateDir, newer, map[string]any{"log": log})
	writeLog(t, log, "the newer run's output")

	if lines := logLines(reportOf(t, runID(old))); len(lines) != 1 || !strings.Contains(lines[0], "not kept") || strings.Contains(lines[0], "bytes") {
		t.Errorf("older run, no rotated log: %q; want it said as not kept, not the newer run's file", lines)
	}
	if lines := logLines(reportOf(t, runID(newer))); len(lines) != 1 || !strings.HasPrefix(lines[0], "Log: "+log+" (") {
		t.Errorf("newest run: %q, want manual.log", lines)
	}

	rotated := runsFile(stateDir, filepath.Join(runHistoryDir, "manual-"+runID(old)+".log"))
	writeLog(t, rotated, "the older run's output")
	if lines := logLines(reportOf(t, runID(old))); len(lines) != 1 || !strings.HasPrefix(lines[0], "Log: "+rotated+" (22 bytes") {
		t.Errorf("older run with its rotated log: %q", lines)
	}
}

// The daemon makes a scheduled run (which records no log and owns last.json)
// between an app run and the app's next rotation. manual.log is still the app
// run's output, so its report names it; a later app run that records the path
// takes it over.
func TestRunsFailuresKeepsTheLogAcrossRunsThatClaimNone(t *testing.T) {
	_, stateDir := setup(t)
	log := runsFile(stateDir, "manual.log")
	writeLog(t, log, "the app run's output")
	appRun, daemonRun := histRec(1, outcomeFailed), histRec(2, outcomeFailed)
	appRun.PID, daemonRun.PID = 4001, 4002
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, runID(appRun)+".json"), lastRawOf(t, appRun, map[string]any{"log": log}))
	putHistoryRaw(t, stateDir, daemonRun)
	rawLast(t, stateDir, daemonRun, nil)

	if lines := logLines(reportOf(t, runID(appRun))); len(lines) != 1 || !strings.HasPrefix(lines[0], "Log: "+log+" (20 bytes") {
		t.Errorf("app run behind a daemon run: %q, want its own manual.log", lines)
	}

	// A later app run claims the same file: the earlier one no longer owns it.
	laterApp := histRec(3, outcomeOK)
	laterApp.PID = 4003
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, runID(laterApp)+".json"), lastRawOf(t, laterApp, map[string]any{"log": log}))
	if lines := logLines(reportOf(t, runID(appRun))); len(lines) != 1 || !strings.Contains(lines[0], "not kept") {
		t.Errorf("app run behind a later app run: %q, want not kept", lines)
	}
	// A record a later run left unreadable claims nothing.
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, runID(laterApp)+".json"), "{garbage")
	if lines := logLines(reportOf(t, runID(appRun))); len(lines) != 1 || !strings.HasPrefix(lines[0], "Log: "+log+" (") {
		t.Errorf("an unreadable later record: %q", lines)
	}
}

// A log that is gone, a record that names nothing, and a record that names a
// path outside the runs folder each say what they are; none panics or names
// a file it should not.
func TestRunsFailuresLogEdgeCases(t *testing.T) {
	t.Run("recorded but missing", func(t *testing.T) {
		_, stateDir := setup(t)
		log := runsFile(stateDir, "manual.log")
		writeLog(t, log, "x")
		rawLast(t, stateDir, histRec(1, outcomeFailed), map[string]any{"log": log})
		if err := os.Remove(log); err != nil {
			t.Fatal(err)
		}
		if lines := logLines(reportOf(t)); len(lines) != 1 || !strings.HasPrefix(lines[0], "Log: not found ("+log) {
			t.Errorf("log lines %q", lines)
		}
	})
	t.Run("a record that edited its log out of the runs folder", func(t *testing.T) {
		_, stateDir := setup(t)
		writeLog(t, runsFile(stateDir, "manual.log"), "x")
		rawLast(t, stateDir, histRec(1, outcomeFailed), map[string]any{"log": "/etc/passwd"})
		text := reportOf(t)
		if strings.Contains(text, "/etc/passwd") || !strings.Contains(text, "Log: not followed") {
			t.Errorf("report follows or names the outside path:\n%s", text)
		}
	})
	t.Run("an older record with no log lists the logs that exist", func(t *testing.T) {
		_, stateDir := setup(t)
		writeLog(t, runsFile(stateDir, "manual.log"), "x")
		writeLog(t, filepath.Join(stateDir, "logs", "vani.err.log"), "y")
		rawLast(t, stateDir, histRec(1, outcomeFailed), nil)
		lines := logLines(reportOf(t))
		if len(lines) != 2 {
			t.Errorf("log lines %q, want the two logs that exist", lines)
		}
	})
	t.Run("a record of a newer vani with fields this one does not know", func(t *testing.T) {
		_, stateDir := setup(t)
		rawLast(t, stateDir, histRec(1, outcomeFailed), map[string]any{"log_bytes": 5, "log_format": map[string]any{"a": 1}})
		if text := reportOf(t); !strings.Contains(text, "Vani run report") {
			t.Errorf("report: %s", text)
		}
	})
}

func lastRawOf(t *testing.T, r runRecord, extra map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	rawLast(t, dir, r, extra)
	b, err := os.ReadFile(runRecordPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Concurrent starts must not duplicate or corrupt the archive. The ledger lock
// means one run at a time in practice; this holds the archive's own invariants
// (one file, readable, never replaced) for callers that race anyway.
func TestArchiveInterruptedRunIsSafeUnderRacingCallers(t *testing.T) {
	_, stateDir := setup(t)
	dead := dyingRecord()
	writeLast(t, stateDir, dead)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := archiveInterruptedRun(stateDir); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("racing archive: %v", err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 1 || ids[0] != runID(dead) {
		t.Fatalf("history %v, want exactly the dead run", ids)
	}
	if got := historyRecord(t, stateDir, runID(dead)); got.Outcome != outcomeInterrupted {
		t.Errorf("outcome %s", got.Outcome)
	}
}

// An existing history entry for the run is the first archive and stands: it is
// not replaced by a later one, whatever last.json says now.
func TestArchiveInterruptedRunNeverReplacesAnEntry(t *testing.T) {
	_, stateDir := setup(t)
	dead := dyingRecord()
	finished := dead
	finished.Outcome, finished.FinishedAt, finished.Error = outcomeFailed, dead.StartedAt.Add(time.Second), "the real ending"
	putHistoryRaw(t, stateDir, finished)
	writeLast(t, stateDir, dead)

	r, err := archiveInterruptedRun(stateDir)
	if r != nil || err != nil {
		t.Fatalf("archived %v, err %v; want nothing archived", r, err)
	}
	if got := historyRecord(t, stateDir, runID(dead)); got.Outcome != outcomeFailed || got.Error != "the real ending" {
		t.Errorf("the existing entry was replaced: %+v", got)
	}
}

// A live daemon's own record is never taken for a dead run's, even by a caller
// that does not hold the ledger. This process's own pid is not the daemon's.
func TestArchiveInterruptedRunLeavesALiveDaemonsRun(t *testing.T) {
	_, stateDir := setup(t)
	live := dyingRecord()
	live.PID = 777001
	writeLast(t, stateDir, live)
	if err := writeDaemonRecord(stateDir, live.PID, time.Now()); err != nil {
		t.Fatal(err)
	}
	stubPidAlive(t, func(pid int) bool { return pid == live.PID })

	if r, err := archiveInterruptedRun(stateDir); r != nil || err != nil {
		t.Fatalf("archived the live daemon's run: %v, %v", r, err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Errorf("history %v", ids)
	}

	// The daemon is gone (its pid no longer lives): the record is a dead run's.
	stubPidAlive(t, func(int) bool { return false })
	if r, err := archiveInterruptedRun(stateDir); r == nil || err != nil {
		t.Fatalf("a dead daemon's run was not archived: %v, %v", r, err)
	}
}

func TestArchiveInterruptedRunWithNoRecordOrAFinishedOne(t *testing.T) {
	_, stateDir := setup(t)
	if r, err := archiveInterruptedRun(stateDir); r != nil || err != nil {
		t.Fatalf("no record: %v, %v", r, err)
	}
	writeLast(t, stateDir, histRec(1, outcomeCancelled))
	if r, err := archiveInterruptedRun(stateDir); r != nil || err != nil {
		t.Fatalf("a cancelled run: %v, %v", r, err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Errorf("history %v", ids)
	}
	if err := os.WriteFile(runRecordPath(stateDir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := archiveInterruptedRun(stateDir); r != nil || err == nil {
		t.Fatalf("a corrupt record: %v, %v; want an error", r, err)
	}
}

func TestInterruptedOutcomeIsReadableInTheHistory(t *testing.T) {
	_, stateDir := setup(t)
	r := dyingRecord()
	r.Outcome = outcomeInterrupted
	putHistoryRaw(t, stateDir, r)
	got, err := readRunRecordFile(runHistoryPath(stateDir, runID(r)))
	if err != nil || got == nil || runState(*got, time.Now()) != outcomeInterrupted {
		t.Fatalf("read back: %v, %v", got, err)
	}
	if want := fmt.Sprintf(`"id":%q`, runID(r)); !strings.Contains(lastRawOf(t, r, nil), want) {
		t.Errorf("a written record carries its id (%s)", want)
	}
}
