package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var histT0 = time.Date(2026, 10, 22, 9, 0, 0, 123_000_000, time.UTC)

// histRec is a finished run that started i minutes after histT0.
func histRec(i int, outcome string) runRecord {
	start := histT0.Add(time.Duration(i) * time.Minute)
	r := runRecord{PID: 4000 + i, StartedAt: start, FinishedAt: start.Add(3 * time.Minute), Outcome: outcome, Trigger: triggerSchedule}
	if outcome == outcomeFailed {
		r.Stage, r.Error = stagePipeline, "pipeline: no decision backend read 20 postings in a row"
	}
	return r
}

// putHistoryRaw places a record in the history without pruning, as a run
// of an older or newer vani, or a hand copy, could.
func putHistoryRaw(t *testing.T, stateDir string, r runRecord) string {
	t.Helper()
	raw, err := marshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	id := runID(r)
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, id+".json"), string(raw))
	return id
}

func historyIDs(t *testing.T, stateDir string) []string {
	t.Helper()
	files, _, err := listRunHistory(stateDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	return ids
}

func TestRunIDIsUTCFixedWidthAndSafeAsAnArgument(t *testing.T) {
	zone := time.FixedZone("x", -7*3600)
	r := runRecord{PID: 42, StartedAt: time.Date(2026, 10, 22, 2, 0, 0, 123_000_000, zone)}
	if got, want := runID(r), "20261022T090000.123Z-42"; got != want {
		t.Fatalf("runID = %q, want %q (UTC, millisecond, pid)", got, want)
	}
	if !validRunID(runID(r)) {
		t.Errorf("an id vani makes is not one it accepts")
	}
	for _, bad := range []string{"", "latest", "../runs/last", "20261022T090000.123Z-42/../x", "20261022T090000Z-42", "20261022T090000.123Z-", "20261022T090000.123Z-42.json", "20261022T090000.123Z-99999999999", "x\n20261022T090000.123Z-42"} {
		if validRunID(bad) {
			t.Errorf("validRunID(%q) = true", bad)
		}
	}
}

// A finished run is written to the history as well as last.json, byte for byte
// the same; the start-of-run record ("running") is last.json only.
func TestSaveRunRecordKeepsFinishedRunsInHistoryOnly(t *testing.T) {
	_, stateDir := setup(t)
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}

	running := histRec(0, outcomeRunning)
	running.FinishedAt = time.Time{}
	e.saveRunRecord(running)
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Fatalf("a run in progress reached the history: %v", ids)
	}

	done := histRec(0, outcomeFailed)
	e.saveRunRecord(done)
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(done) {
		t.Fatalf("history = %v, want [%s]", ids, runID(done))
	}
	last, _ := os.ReadFile(runRecordPath(stateDir))
	kept, _ := os.ReadFile(runHistoryPath(stateDir, runID(done)))
	if len(last) == 0 || !bytes.Equal(last, kept) {
		t.Errorf("history copy differs from last.json:\n%s\n%s", last, kept)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr: %s", stderr.String())
	}
	if err := writeRunHistory(stateDir, running); err == nil {
		t.Errorf("writeRunHistory accepted a run that has not ended")
	}
}

// Through the real run path: every run-once leaves its record, and the run
// before it is still there. (Against code without the history only last.json
// exists, so this fails there.)
func TestRunOnceKeepsEveryRunInHistory(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)
	tick := 0
	old := now
	now = func() time.Time { tick++; return histT0.Add(time.Duration(tick) * time.Second) }
	t.Cleanup(func() { now = old })

	mustCode(t, 0, "run-once")
	mustCode(t, 0, "run-once")
	runs, err := filepath.Glob(filepath.Join(stateDir, runRecordDir, runHistoryDir, "*.json"))
	if err != nil || len(runs) != 2 {
		t.Fatalf("history after two runs = %v (%v), want two records", runs, err)
	}
	ids := historyIDs(t, stateDir)
	for _, id := range ids {
		r, err := readRunRecordFile(runHistoryPath(stateDir, id))
		if err != nil || r.Outcome != outcomeOK || runID(*r) != id {
			t.Errorf("history %s: %+v, %v", id, r, err)
		}
	}
	last, err := readRunRecord(stateDir)
	if err != nil || runID(*last) != ids[0] {
		t.Errorf("last.json is %v (%v), want the newest history entry %s", last, err, ids[0])
	}
}

func TestPruneKeepsTheNewestN(t *testing.T) {
	_, stateDir := setup(t)
	for i := range maxRunHistory + 5 {
		putHistoryRaw(t, stateDir, histRec(i, outcomeOK))
	}
	removed, err := pruneRunHistory(stateDir, maxRunHistory, maxRunHistoryBytes)
	if err != nil || removed != 5 {
		t.Fatalf("removed %d, %v; want 5", removed, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != maxRunHistory || ids[0] != runID(histRec(maxRunHistory+4, "")) || ids[len(ids)-1] != runID(histRec(5, "")) {
		t.Fatalf("kept %d ids, %s .. %s; want the newest %d", len(ids), ids[0], ids[len(ids)-1], maxRunHistory)
	}
}

func TestWriteRunHistoryPrunesAsItGoes(t *testing.T) {
	_, stateDir := setup(t)
	for i := range maxRunHistory + 3 {
		if err := writeRunHistory(stateDir, histRec(i, outcomeOK)); err != nil {
			t.Fatal(err)
		}
	}
	if ids := historyIDs(t, stateDir); len(ids) != maxRunHistory {
		t.Fatalf("%d records kept, want the bound %d", len(ids), maxRunHistory)
	}
}

// The size bound keeps the newest records that fit, and always the newest.
func TestPruneSizeBoundKeepsTheNewestEvenWhenItIsOver(t *testing.T) {
	_, stateDir := setup(t)
	big := func(i int) runRecord {
		r := histRec(i, outcomeFailed)
		r.Error = strings.Repeat("x", 400)
		return r
	}
	for i := range 6 {
		putHistoryRaw(t, stateDir, big(i))
	}
	files, _, _ := listRunHistory(stateDir, 0)
	one := files[0].Size
	// Room for two and a half records: the newest two stay.
	if _, err := pruneRunHistory(stateDir, 30, one*5/2); err != nil {
		t.Fatal(err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 2 || ids[0] != runID(big(5)) || ids[1] != runID(big(4)) {
		t.Fatalf("kept %v, want the newest two", ids)
	}
	// A bound below one record still keeps the newest: the run just finished.
	if _, err := pruneRunHistory(stateDir, 30, 1); err != nil {
		t.Fatal(err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 1 || ids[0] != runID(big(5)) {
		t.Fatalf("kept %v, want only the newest", ids)
	}
}

// Pruning touches record files named by runID and nothing else: the app's
// rotated logs, notes, temp files and last.json stay.
func TestPruneLeavesForeignFilesAlone(t *testing.T) {
	_, stateDir := setup(t)
	foreign := []string{
		"runs/last.json", "runs/manual.log",
		"runs/history/manual-20260101T000000Z.log", "runs/history/notes.txt",
		"runs/history/20260101T000000.000Z-1.json.tmp", "runs/history/.20260101T000000.000Z-1.json123",
		"runs/history/20260101T000000.000Z-1.log",
	}
	for _, f := range foreign {
		writeStateFile(t, stateDir, f, "keep")
	}
	for i := range 4 {
		putHistoryRaw(t, stateDir, histRec(i, outcomeOK))
	}
	if _, err := pruneRunHistory(stateDir, 1, maxRunHistoryBytes); err != nil {
		t.Fatal(err)
	}
	for _, f := range foreign {
		if b, err := os.ReadFile(filepath.Join(stateDir, f)); err != nil || string(b) != "keep" {
			t.Errorf("%s was touched: %q, %v", f, b, err)
		}
	}
	if ids := historyIDs(t, stateDir); len(ids) != 1 {
		t.Errorf("kept %v", ids)
	}
}

func TestPruneOfAMissingDirectoryIsNothing(t *testing.T) {
	_, stateDir := setup(t)
	if n, err := pruneRunHistory(stateDir, 30, 1<<20); n != 0 || err != nil {
		t.Fatalf("%d, %v", n, err)
	}
}

// Many processes finishing at once (a record write is not under the ledger
// lock): every write lands whole, nothing errors on a file another pruner
// already removed, and the bound holds once the writers settle.
func TestConcurrentFinalisesStayBounded(t *testing.T) {
	_, stateDir := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writeRunHistory(stateDir, histRec(i, outcomeOK)); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write: %v", err)
	}
	// Racing pruners may each leave a few extra; one more write settles it.
	if err := writeRunHistory(stateDir, histRec(100, outcomeOK)); err != nil {
		t.Fatal(err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != maxRunHistory {
		t.Fatalf("%d records after the writers settled, want %d", len(ids), maxRunHistory)
	}
	for _, id := range ids {
		if r, err := readRunRecordFile(runHistoryPath(stateDir, id)); err != nil || runID(*r) != id {
			t.Errorf("record %s is torn or misnamed: %v", id, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(stateDir, runRecordDir, runHistoryDir))
	for _, en := range entries {
		if !strings.HasSuffix(en.Name(), ".json") {
			t.Errorf("leftover file %s", en.Name())
		}
	}
}

// A history that cannot be written is a warning: last.json, which status
// reads, is already written and the run does not fail.
func TestHistoryWriteFailureIsAWarningAndKeepsLastJSON(t *testing.T) {
	_, stateDir := setup(t)
	writeStateFile(t, stateDir, "runs/history", "a file where the directory belongs")
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(1, outcomeFailed))
	if r, err := readRunRecord(stateDir); err != nil || r == nil || r.Outcome != outcomeFailed {
		t.Fatalf("last.json lost: %v %v", r, err)
	}
	if !strings.Contains(stderr.String(), "could not keep the run in the history") {
		t.Errorf("no warning on stderr: %q", stderr.String())
	}
}

// A last.json that cannot be written stops there: the history is not written
// from a record status could not read.
func TestLastJSONFailureSkipsHistory(t *testing.T) {
	_, stateDir := setup(t)
	writeStateFile(t, stateDir, "runs/last.json/blocker", "last.json is a directory")
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(1, outcomeOK))
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Errorf("history %v written though the record was not", ids)
	}
	if !strings.Contains(stderr.String(), "could not record the run") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

// The history file is the record's own encoding, 0600: it holds no field the
// record does not, so nothing new (a key, a header, a path) enters through it.
func TestHistoryHoldsNothingBeyondTheRecord(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeFailed)
	if err := writeRunHistory(stateDir, r); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(runHistoryPath(stateDir, runID(r)))
	want, _ := marshalRunRecord(r)
	if !bytes.Equal(raw, want) {
		t.Errorf("history bytes differ from the record encoding")
	}
	if info, err := os.Stat(runHistoryPath(stateDir, runID(r))); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("history file: %v, %v; want mode 0600", info, err)
	}
}
