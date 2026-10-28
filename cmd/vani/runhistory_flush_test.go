package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A finished record whose history write failed lives only in last.json.
// The next save must not replace that file while the history still cannot
// take it: archiveInterruptedRun will not keep a record that is not running.
func TestNextSaveKeepsFinishedRecordWhileHistoryStaysBlocked(t *testing.T) {
	_, stateDir := setup(t)
	writeStateFile(t, stateDir, "runs/history", "a file where the directory belongs")
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}

	a := histRec(1, outcomeFailed)
	e.saveRunRecord(a)
	kept, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || got.Outcome != a.Outcome || runID(*got) != runID(a) {
		t.Fatalf("last.json after A: %+v %v", got, err)
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("stderr: %q", stderr.String())
	}

	b := histRec(2, outcomeRunning)
	b.FinishedAt = time.Time{}
	e.saveRunRecord(b)
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, after) {
		t.Fatalf("last.json was replaced while history was blocked:\n%s\n%s", kept, after)
	}
	if strings.Count(stderr.String(), "could not keep the run in the history:") != 2 {
		t.Fatalf("stderr: %q", stderr.String())
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("warning swallowed the error: %q", stderr.String())
	}
}

// Once the history directory can be written, the blocked finished record is
// the one that lands there — its own bytes, not an interrupted rewrite —
// and last.json becomes the new run.
func TestBlockedHistoryIsFlushedBeforeTheNextRecord(t *testing.T) {
	_, stateDir := setup(t)
	blocker := filepath.Join(stateDir, "runs", "history")
	writeStateFile(t, stateDir, "runs/history", "a file where the directory belongs")
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}

	a := histRec(1, outcomeFailed)
	e.saveRunRecord(a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(blocker, 0o700); err != nil {
		t.Fatal(err)
	}

	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)
	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
	if err != nil {
		t.Fatalf("history of A: %v", err)
	}
	if !bytes.Equal(hist, aBytes) {
		t.Fatalf("history is not A's bytes:\n%s\nwant\n%s", hist, aBytes)
	}
	kept := historyRecord(t, stateDir, runID(a))
	if kept.Outcome != a.Outcome || kept.Outcome == outcomeInterrupted {
		t.Fatalf("history outcome %+v, want %s", kept, a.Outcome)
	}
	last, err := readRunRecord(stateDir)
	if err != nil || last == nil || runID(*last) != runID(b) || last.Outcome != b.Outcome {
		t.Fatalf("last.json: %+v %v", last, err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 2 {
		t.Fatalf("history %v, want A and B", ids)
	}
}

// archiveInterruptedRun does not adopt a finished record, even when the
// history never received it. A running record is still archived elsewhere.
func TestArchiveInterruptedRunLeavesFinishedRecordMissingFromHistory(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(3, outcomeFailed)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	r, err := archiveInterruptedRun(stateDir)
	if r != nil || err != nil {
		t.Fatalf("archived %v, err %v; want nothing", r, err)
	}
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("last.json changed:\n%s\n%s", before, after)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Fatalf("history written: %v", ids)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || got.Outcome != outcomeFailed {
		t.Fatalf("outcome: %+v %v", got, err)
	}
}

// A symlink at the history leaf is not the record. The flush replaces it
// with the finished record's own bytes before last.json moves on.
func TestSymlinkHistoryLeafIsNotAKeptRecord(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(1, outcomeOK)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir, runHistoryDir), 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(a))
	if err := os.Symlink(filepath.Join(stateDir, "not-the-record.json"), leaf); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeFailed)
	e.saveRunRecord(b)

	info, err := os.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("history leaf mode %v, want a regular file", info.Mode())
	}
	got, err := os.ReadFile(leaf)
	if err != nil || !bytes.Equal(got, aBytes) {
		t.Fatalf("history bytes:\n%s\nwant\n%s\nerr %v", got, aBytes, err)
	}
	last, err := readRunRecord(stateDir)
	if err != nil || last == nil || runID(*last) != runID(b) {
		t.Fatalf("last.json %+v %v", last, err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

// A directory wearing the history file's name does not hold the record.
// last.json stays until a regular file does.
func TestDirectoryHistoryLeafDoesNotCountAsKept(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(1, outcomeCancelled)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(a))
	if err := os.MkdirAll(filepath.Join(leaf, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(2, outcomeOK))
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("last.json changed:\n%s\n%s", before, after)
	}
	info, err := os.Lstat(leaf)
	if err != nil || info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("leaf: %v %v", info, err)
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// A prune error after the history file is in place must not block the new
// record. The newest file is kept; an older name that cannot be removed is
// the prune failure.
func TestPruneErrorAfterFlushDoesNotBlockTheNextRecord(t *testing.T) {
	_, stateDir := setup(t)
	// Older than A, and past the keep count once A is written, so prune tries
	// to remove it. A non-empty directory cannot be removed; the write stood.
	stuck := filepath.Join(stateDir, runRecordDir, runHistoryDir, "20261022T080000.000Z-1.json", "blocker")
	if err := os.MkdirAll(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= maxRunHistory; i++ {
		putHistoryRaw(t, stateDir, histRec(i, outcomeOK))
	}
	a := histRec(maxRunHistory+1, outcomeOK)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(maxRunHistory+2, outcomeFailed)
	e.saveRunRecord(b)

	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
	if err != nil || !bytes.Equal(hist, aBytes) {
		t.Fatalf("history of A: %v\n%s", err, hist)
	}
	last, err := readRunRecord(stateDir)
	if err != nil || last == nil || runID(*last) != runID(b) {
		t.Fatalf("last.json %+v %v", last, err)
	}
	// B's own history write hits the same prune error. That stays a warning
	// after last.json is already B; it must not have stopped the flush of A.
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("prune error swallowed: %q", stderr.String())
	}
}

// The flush tries twice and stops, even when every attempt fails. A retry
// inside writeRunHistory would make a caller's own retry unbounded; the
// second attempt is the last.
func TestFlushRetriesHistoryOnceThenStops(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	old := writeFinishedHistory
	writeFinishedHistory = func(string, runRecord) error {
		n++
		return errors.New("history disk busy")
	}
	t.Cleanup(func() { writeFinishedHistory = old })
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(2, outcomeOK))
	if n != 2 {
		t.Fatalf("history attempts = %d, want 2", n)
	}
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("last.json changed:\n%s\n%s", before, after)
	}
	if !strings.Contains(stderr.String(), "history disk busy") {
		t.Fatalf("stderr: %q", stderr.String())
	}
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Fatalf("history %v", ids)
	}
}

// The second attempt is the one that keeps the record when the first fails.
func TestFlushSecondHistoryAttemptKeepsTheRecord(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	old := writeFinishedHistory
	writeFinishedHistory = func(dir string, r runRecord) error {
		n++
		if n == 1 {
			return errors.New("history disk busy")
		}
		return old(dir, r)
	}
	t.Cleanup(func() { writeFinishedHistory = old })
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)
	if n != 2 {
		t.Fatalf("history attempts = %d, want 2", n)
	}
	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
	if err != nil || !bytes.Equal(hist, aBytes) {
		t.Fatalf("history of A: %v\n%s", err, hist)
	}
	last, err := readRunRecord(stateDir)
	if err != nil || last == nil || runID(*last) != runID(b) {
		t.Fatalf("last.json %+v %v", last, err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestSavingTheSameRunTwiceIsOneHistoryRow(t *testing.T) {
	_, stateDir := setup(t)
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	a := histRec(1, outcomeFailed)
	e.saveRunRecord(a)
	a.Error = "a later ending of the same run"
	e.saveRunRecord(a)
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(a) {
		t.Fatalf("history %v, want one row", ids)
	}
	got := historyRecord(t, stateDir, runID(a))
	if got.Error != a.Error || got.Outcome != outcomeFailed {
		t.Fatalf("history row: %+v", got)
	}
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
}

func TestRunningRecordIsReplacedWhenHistoryCannotBeWritten(t *testing.T) {
	_, stateDir := setup(t)
	running := histRec(1, outcomeRunning)
	running.FinishedAt = time.Time{}
	writeLast(t, stateDir, running)
	writeStateFile(t, stateDir, "runs/history", "a file where the directory belongs")
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != outcomeOK {
		t.Fatalf("last.json %+v %v", got, err)
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

func TestSaveRunRecordEmptyStateDirWarnsAndWritesNothing(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	var stderr bytes.Buffer
	e := &env{stateDir: "", stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(1, outcomeOK))
	if !strings.Contains(stderr.String(), "could not record the run") {
		t.Fatalf("stderr: %q", stderr.String())
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatalf("working directory %v (err %v)", entries, err)
	}
}

func TestSaveRunRecordUnicodeStateDir(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "状态")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	a := histRec(1, outcomeOK)
	e.saveRunRecord(a)
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(a) {
		t.Fatalf("last.json %+v %v", got, err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 1 || ids[0] != runID(a) {
		t.Fatalf("history %v", ids)
	}
}

func warningKeepsHistoryError(stderr string) bool {
	lines := 0
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.Contains(line, "could not keep the run in the history") {
			continue
		}
		lines++
		rest, ok := strings.CutPrefix(line, "vani: could not keep the run in the history:")
		if !ok || strings.TrimSpace(rest) == "" || strings.Contains(rest, "<nil>") {
			return false
		}
	}
	return lines > 0
}
