package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// A run whose process died leaves a `running` record in last.json, and the next
// run replaces it. These tests drive the CLI end to end (run-once over a fake
// app), so they hold the behaviour, not one function: the dead run reaches
// runs/history as "interrupted" before it is replaced, once, without touching
// a run that is not dead.

// dyingRecord is a run that started a minute ago and never wrote its end.
func dyingRecord() runRecord {
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	return runRecord{PID: 424242, StartedAt: start, Outcome: outcomeRunning, Trigger: triggerManual}
}

func historyRecord(t *testing.T, stateDir, id string) *runRecord {
	t.Helper()
	r, err := readRunRecordFile(runHistoryPath(stateDir, id))
	if err != nil {
		t.Fatalf("history record %s: %v", id, err)
	}
	if r == nil {
		t.Fatalf("history record %s is missing; the history holds %v", id, historyIDs(t, stateDir))
	}
	return r
}

func stubPidAlive(t *testing.T, alive func(int) bool) {
	t.Helper()
	old := pidAlive
	pidAlive = alive
	t.Cleanup(func() { pidAlive = old })
}

func TestInterruptedRunReachesTheHistoryAtTheNextRun(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	stubPidAlive(t, func(int) bool { return false })
	dead := dyingRecord()
	dead.Boards = &boardsReport{Configured: 3, Read: 1, Failed: 1, Items: []boardReport{{ATS: "gh", Token: "a", Outcome: boardFailed, Error: "http 500"}}}
	writeLast(t, stateDir, dead)
	wireTuned(t, fixture())

	_, stderr := mustCode(t, 0, "run-once")

	got := historyRecord(t, stateDir, runID(dead))
	if got.Outcome != "interrupted" || got.PID != dead.PID || !got.StartedAt.Equal(dead.StartedAt) || got.Boards == nil || got.Boards.Failed != 1 {
		t.Errorf("archived record = %+v, want the dead run's own record with outcome interrupted", got)
	}
	if !got.FinishedAt.IsZero() {
		t.Errorf("an interrupted run has no recorded end, got %v", got.FinishedAt)
	}
	if !strings.Contains(stderr, runID(dead)) || !strings.Contains(stderr, "interrupted") {
		t.Errorf("stderr does not say the earlier run was kept: %q", stderr)
	}
	// The new run owns last.json; the dead one is only in the history.
	if last := lastRecord(t, stateDir); runID(last) == runID(dead) || last.Outcome != outcomeOK {
		t.Errorf("last.json = %+v", last)
	}
	rows := runsJSON[runsListJSON](t, "runs", "list", "--json").Runs
	var found bool
	for _, row := range rows {
		if row.ID == runID(dead) {
			found = true
			if row.State != "interrupted" || !row.HasFailures {
				t.Errorf("row %+v, want state interrupted with failures", row)
			}
		}
	}
	if !found {
		t.Errorf("runs list does not show the interrupted run: %+v", rows)
	}
	if rep := failuresOf(t, runID(dead)); !rep.HasFailures || !strings.Contains(rep.Text, "Outcome: interrupted") || !strings.Contains(rep.Text, "gh:a") {
		t.Errorf("failures of the interrupted run: %+v", rep)
	}
}

// A pid is reused: after a reboot a dead run's pid belongs to another process,
// and even this process's own pid can be in a stale record. The run that holds
// the ledger proves nothing else is running, so a "live" pid does not keep the
// record out of the history.
func TestInterruptedRunWithAReusedPidIsStillKept(t *testing.T) {
	for name, pid := range map[string]int{"own pid": os.Getpid(), "another live pid": os.Getppid()} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			stubPidAlive(t, func(int) bool { return true })
			stale := dyingRecord()
			stale.PID = pid
			if runState(stale, time.Now()) != outcomeRunning {
				t.Fatal("setup: the record must read as running under the pid check")
			}
			writeLast(t, stateDir, stale)
			wireTuned(t, fixture())

			mustCode(t, 0, "run-once")

			if got := historyRecord(t, stateDir, runID(stale)); got.Outcome != "interrupted" {
				t.Errorf("outcome %q, want interrupted", got.Outcome)
			}
		})
	}
}

// A finished run is already in the history (saveRunRecord wrote it), so the
// next run neither archives it again nor changes its file.
func TestFinishedRunIsNotArchivedAgain(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	wireTuned(t, fixture())
	mustCode(t, 0, "run-once")
	first := lastRecord(t, stateDir)
	path := runHistoryPath(stateDir, runID(first))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond) // a distinct run id
	_, stderr := mustCode(t, 0, "run-once")

	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("the finished run's history file changed (err %v)", err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 2 {
		t.Errorf("history %v, want the two finished runs and nothing archived", ids)
	}
	if strings.Contains(stderr, "never finished") {
		t.Errorf("a finished run was reported as interrupted: %q", stderr)
	}
	for _, id := range historyIDs(t, stateDir) {
		if r := historyRecord(t, stateDir, id); r.Outcome != outcomeOK {
			t.Errorf("%s outcome %s", id, r.Outcome)
		}
	}
}

// An unreadable last.json cannot be archived. The run goes on, says why, and
// replaces it; nothing half-readable reaches the history.
func TestCorruptLastRecordDoesNotStopTheNextRun(t *testing.T) {
	for name, raw := range map[string]string{
		"truncated":      `{"v":1,"pid":4`,
		"not json":       "garbage",
		"empty":          "",
		"wrong version":  `{"v":9,"pid":1,"started_at":"2026-10-22T09:00:00Z","outcome":"running"}`,
		"unknown state":  `{"v":1,"pid":1,"started_at":"2026-10-22T09:00:00Z","outcome":"vanished"}`,
		"trailing bytes": `{"v":1,"pid":1,"started_at":"2026-10-22T09:00:00Z","outcome":"running"} x`,
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			writeStateFile(t, stateDir, filepath.Join(runRecordDir, runRecordFile), raw)
			wireTuned(t, fixture())

			_, stderr := mustCode(t, 0, "run-once")

			if !strings.Contains(stderr, "previous run's record was not kept") {
				t.Errorf("stderr does not say the earlier record was lost: %q", stderr)
			}
			if r := lastRecord(t, stateDir); r.Outcome != outcomeOK {
				t.Errorf("the run after a corrupt record: %+v", r)
			}
			if ids := historyIDs(t, stateDir); len(ids) != 1 {
				t.Errorf("history %v, want only the run that just finished", ids)
			}
		})
	}
}

// One process makes many runs (the daemon). A run it finished is not an
// interrupted one at the next slot, and one it left unfinished (a failed final
// write) is archived once.
func TestSecondRunInTheSameProcessArchivesNothing(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	app := &App{Ledger: fixture(), Notifier: &fakeNotifier{}, RunOnce: func(context.Context) error { return nil }}
	job := e.dailyJob(app, cfg, triggerSchedule, runOptions{})
	for range 2 {
		if err := job(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if strings.Contains(stderr.String(), "never finished") {
		t.Errorf("the daemon archived its own finished run: %s", stderr.String())
	}
	if ids := historyIDs(t, stateDir); len(ids) != 2 {
		t.Errorf("history %v, want 2 finished runs", ids)
	}

	// Its final write failed once: last.json still says running, with its pid.
	stuck := dyingRecord()
	stuck.PID = os.Getpid()
	writeLast(t, stateDir, stuck)
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := historyRecord(t, stateDir, runID(stuck)); got.Outcome != "interrupted" {
		t.Errorf("outcome %q, want interrupted", got.Outcome)
	}
}

// rawLast rewrites last.json as r plus the given extra keys, the way a record
// from another vani version carries them; it compiles whatever this vani's
// record knows.
func rawLast(t *testing.T, stateDir string, r runRecord, extra map[string]any) {
	t.Helper()
	raw, err := marshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		m[k] = v
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runRecordFile), string(out))
}

func lastRaw(t *testing.T, stateDir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
