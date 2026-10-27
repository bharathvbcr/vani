package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Adversarial checks for the history flush contract. They do not replace
// TestHistoryWriteFailureIsAWarningAndKeepsLastJSON; that test stays as it is.

func historyWriterIsProduction(t *testing.T) {
	t.Helper()
	if reflect.ValueOf(writeFinishedHistory).Pointer() != reflect.ValueOf(writeRunHistory).Pointer() {
		t.Fatal("writeFinishedHistory is not writeRunHistory")
	}
}

func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			tgt, rerr := os.Readlink(path)
			if rerr != nil {
				return rerr
			}
			out[rel] = "link:" + tgt
		case info.IsDir():
			out[rel] = "dir"
		default:
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			out[rel] = "file:" + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func treesSame(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("tree changed size %d -> %d\nbefore %#v\nafter %#v", len(before), len(after), before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed\nbefore %q\nafter %q", k, v, after[k])
		}
	}
}

func runningHist(i int) runRecord {
	r := histRec(i, outcomeRunning)
	r.FinishedAt = time.Time{}
	return r
}

// A file (or a symlink to a file) where the history directory belongs: the
// first finished save is kept in last.json and warned; a second, different
// record does not change a single state byte; after the directory is restored
// the third save writes that blocked record's own bytes, not an interrupted
// rewrite, and then the new record.
func TestBlockedHistoryFileHoldsLastJSONUntilTheDirectoryReturns(t *testing.T) {
	for _, secondName := range []string{"running", "finished"} {
		t.Run(secondName, func(t *testing.T) {
			for _, plantName := range []string{"file", "symlink"} {
				t.Run(plantName, func(t *testing.T) {
					_, stateDir := setup(t)
					outside := filepath.Join(t.TempDir(), "not-the-history")
					if err := os.WriteFile(outside, []byte("outside-sentinel"), 0o600); err != nil {
						t.Fatal(err)
					}
					blocker := filepath.Join(stateDir, runRecordDir, runHistoryDir)
					switch plantName {
					case "file":
						writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir), "not-a-directory")
					case "symlink":
						if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, blocker); err != nil {
							t.Fatal(err)
						}
					default:
						t.Fatalf("plant %s", plantName)
					}
					var stderr bytes.Buffer
					e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}

					a := histRec(1, outcomeFailed)
					e.saveRunRecord(a)
					kept, err := os.ReadFile(runRecordPath(stateDir))
					if err != nil {
						t.Fatal(err)
					}
					want, err := marshalRunRecord(a)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(kept, want) {
						t.Fatalf("last.json after the first save:\n%s\nwant\n%s", kept, want)
					}
					if !warningKeepsHistoryError(stderr.String()) {
						t.Fatalf("stderr: %q", stderr.String())
					}
					snap := treeOf(t, stateDir)

					var b runRecord
					if secondName == "running" {
						b = runningHist(2)
					} else {
						b = histRec(2, outcomeCancelled)
					}
					e.saveRunRecord(b)
					treesSame(t, snap, treeOf(t, stateDir))
					if strings.Count(stderr.String(), "could not keep the run in the history:") != 2 {
						t.Fatalf("stderr: %q", stderr.String())
					}
					if !warningKeepsHistoryError(stderr.String()) {
						t.Fatalf("warning swallowed the error: %q", stderr.String())
					}
					outsideNow, err := os.ReadFile(outside)
					if err != nil || string(outsideNow) != "outside-sentinel" {
						t.Fatalf("outside file: %q %v", outsideNow, err)
					}

					if err := os.Remove(blocker); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(blocker, 0o700); err != nil {
						t.Fatal(err)
					}
					stderr.Reset()
					c := histRec(3, outcomeOK)
					e.saveRunRecord(c)
					hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(hist, kept) {
						t.Fatalf("history is not the blocked record's bytes:\n%s\nwant\n%s", hist, kept)
					}
					got := historyRecord(t, stateDir, runID(a))
					if got.Outcome != a.Outcome || got.Outcome == outcomeInterrupted {
						t.Fatalf("history outcome %q, want %q", got.Outcome, a.Outcome)
					}
					last, err := readRunRecord(stateDir)
					if err != nil || last == nil || runID(*last) != runID(c) || last.Outcome != c.Outcome {
						t.Fatalf("last.json: %+v %v", last, err)
					}
					ids := historyIDs(t, stateDir)
					if len(ids) != 2 || ids[0] != runID(c) || ids[1] != runID(a) {
						t.Fatalf("history %v, want [%s %s]", ids, runID(c), runID(a))
					}
					if _, err := readRunRecordFile(runHistoryPath(stateDir, runID(b))); err != nil || historyHasID(ids, runID(b)) {
						t.Fatalf("rejected record %s entered history: %v", runID(b), ids)
					}
					if stderr.Len() != 0 {
						t.Fatalf("stderr after restore: %s", stderr.String())
					}
					outsideNow, err = os.ReadFile(outside)
					if err != nil || string(outsideNow) != "outside-sentinel" {
						t.Fatalf("outside file after restore: %q %v", outsideNow, err)
					}
				})
			}
		})
	}
}

func historyHasID(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

// No record, and a running record, are not flushed. The new record may replace
// last.json even when the history directory cannot be created. A running
// record is not rewritten into the history as interrupted when history works.
func TestNextSaveWithNothingToFlushWritesTheNewRecord(t *testing.T) {
	t.Run("no record", func(t *testing.T) {
		_, stateDir := setup(t)
		writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir), "not-a-directory")
		var stderr bytes.Buffer
		e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
		a := histRec(1, outcomeOK)
		e.saveRunRecord(a)
		got, err := readRunRecord(stateDir)
		if err != nil || got == nil || runID(*got) != runID(a) || got.Outcome != outcomeOK {
			t.Fatalf("last.json %+v %v", got, err)
		}
		if !warningKeepsHistoryError(stderr.String()) {
			t.Fatalf("stderr: %q", stderr.String())
		}
	})
	t.Run("running is not archived", func(t *testing.T) {
		_, stateDir := setup(t)
		running := runningHist(1)
		writeLast(t, stateDir, running)
		var stderr bytes.Buffer
		e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
		b := histRec(2, outcomeFailed)
		e.saveRunRecord(b)
		ids := historyIDs(t, stateDir)
		if len(ids) != 1 || ids[0] != runID(b) {
			t.Fatalf("history %v", ids)
		}
		if historyRecord(t, stateDir, runID(b)).Outcome == outcomeInterrupted {
			t.Fatal("new record stored as interrupted")
		}
		if _, err := os.Lstat(runHistoryPath(stateDir, runID(running))); err == nil {
			t.Fatal("running record was flushed into history")
		}
		last, err := readRunRecord(stateDir)
		if err != nil || last == nil || runID(*last) != runID(b) {
			t.Fatalf("last.json %+v %v", last, err)
		}
		if stderr.Len() != 0 {
			t.Fatal(stderr.String())
		}
	})
}

// A symlink leaf is not a kept record. Replacing it must not write through to
// the outside target. If the leaf cannot be replaced, last.json stays.
func TestSymlinkHistoryLeafDoesNotWriteThrough(t *testing.T) {
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
	target := filepath.Join(t.TempDir(), "sentinel.json")
	const sentinel = "sentinel-do-not-touch"
	if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(filepath.Dir(leaf), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, leaf); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeFailed)
	e.saveRunRecord(b)

	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTarget) != sentinel {
		t.Fatalf("wrote through the symlink: %q", gotTarget)
	}
	info, err := os.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	last, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0:
		body, err := os.ReadFile(leaf)
		if err != nil || !bytes.Equal(body, aBytes) {
			t.Fatalf("history leaf:\n%s\nwant\n%s\nerr %v", body, aBytes, err)
		}
		got, err := readRunRecord(stateDir)
		if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != b.Outcome {
			t.Fatalf("last.json %+v %v", got, err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr: %s", stderr.String())
		}
	case info.Mode()&os.ModeSymlink != 0 && bytes.Equal(last, aBytes):
		if !warningKeepsHistoryError(stderr.String()) {
			t.Fatalf("stderr: %q", stderr.String())
		}
		got, err := os.Readlink(leaf)
		if err != nil || got != rel {
			t.Fatalf("symlink retargeted to %q (%v)", got, err)
		}
	default:
		t.Fatalf("leaf mode %v, last.json changed=%v", info.Mode(), !bytes.Equal(last, aBytes))
	}
}

// The same leaf, pointed at last.json itself. Writing through would make the
// history read back as the new record.
func TestSymlinkHistoryLeafAimedAtLastJSONIsReplacedOrLeft(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir, runHistoryDir), 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(a))
	rel, err := filepath.Rel(filepath.Dir(leaf), runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, leaf); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)

	info, err := os.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	last, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !bytes.Equal(last, aBytes) {
			t.Fatal("last.json changed while the history leaf was still a symlink")
		}
		body, err := os.ReadFile(leaf)
		if err != nil || !bytes.Equal(body, aBytes) {
			t.Fatalf("symlink target changed:\n%s\nerr %v", body, err)
		}
		return
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("leaf mode %v", info.Mode())
	}
	body, err := os.ReadFile(leaf)
	if err != nil || !bytes.Equal(body, aBytes) {
		t.Fatalf("history leaf:\n%s\nwant\n%s\nerr %v", body, aBytes, err)
	}
	if bytes.Equal(last, aBytes) {
		t.Fatal("symlink was replaced but last.json did not advance")
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) {
		t.Fatalf("last.json %+v %v", got, err)
	}
}

// A directory wearing the history name is not a kept record, empty or not.
// last.json stays, including when the directory could have been removed.
func TestDirectoryHistoryEmptyLeafIsNotKept(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(4, outcomeCancelled)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(a))
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(5, outcomeOK))
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("last.json changed:\n%s\n%s", before, after)
	}
	info, err := os.Lstat(leaf)
	if err != nil || !info.IsDir() {
		t.Fatalf("leaf: %v %v", info, err)
	}
	if historyFileKept(leaf) {
		t.Fatal("directory leaf counted as kept")
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("stderr: %q", stderr.String())
	}
	if strings.Count(stderr.String(), "could not keep the run in the history:") != 1 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// A regular file is kept even when its bytes are not the record. Flush must
// not rewrite it, and must not call the history writer at all.
func TestFlushLeavesARegularHistoryFileAlone(t *testing.T) {
	historyWriterIsProduction(t)
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	leaf := runHistoryPath(stateDir, runID(a))
	const stale = "not-the-record"
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, filepath.Base(leaf)), stale)
	var n int
	old := writeFinishedHistory
	t.Cleanup(func() { writeFinishedHistory = old })
	writeFinishedHistory = func(dir string, r runRecord) error {
		n++
		return old(dir, r)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)
	if n != 0 {
		t.Fatalf("history writer calls = %d, want 0", n)
	}
	body, err := os.ReadFile(leaf)
	if err != nil || string(body) != stale {
		t.Fatalf("history bytes %q, err %v", body, err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) {
		t.Fatalf("last.json %+v %v", got, err)
	}
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
}

// The first history write is the only one when it returns nil. An error is
// retried once, including a prune error after the file already exists, and
// that error does not block the next record. The hook is always restored.
func TestFlushRetriesHistoryAtMostTwice(t *testing.T) {
	historyWriterIsProduction(t)
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("nil is one call", func(t *testing.T) {
		var n int
		old := writeFinishedHistory
		t.Cleanup(func() { writeFinishedHistory = old })
		writeFinishedHistory = func(dir string, r runRecord) error {
			n++
			if r.Outcome == outcomeInterrupted || runID(r) != runID(a) {
				t.Errorf("flushed %+v, want %s", r, runID(a))
			}
			return old(dir, r)
		}
		var stderr bytes.Buffer
		e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
		b := histRec(2, outcomeOK)
		e.saveRunRecord(b)
		if n != 1 {
			t.Fatalf("calls = %d, want 1", n)
		}
		hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
		if err != nil || !bytes.Equal(hist, aBytes) {
			t.Fatalf("history %v\n%s", err, hist)
		}
		if historyRecord(t, stateDir, runID(a)).Outcome != a.Outcome {
			t.Fatal("outcome rewritten")
		}
		got, err := readRunRecord(stateDir)
		if err != nil || got == nil || runID(*got) != runID(b) {
			t.Fatalf("last.json %+v %v", got, err)
		}
		if stderr.Len() != 0 {
			t.Fatal(stderr.String())
		}
	})
}

func TestPruneErrorAfterTheFileExistsDoesNotBlock(t *testing.T) {
	historyWriterIsProduction(t)
	_, stateDir := setup(t)
	a := histRec(1, outcomeOK)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var saw []runRecord
	old := writeFinishedHistory
	t.Cleanup(func() { writeFinishedHistory = old })
	writeFinishedHistory = func(dir string, r runRecord) error {
		n++
		saw = append(saw, r)
		if err := old(dir, r); err != nil {
			return err
		}
		return errors.New("prune failed after the file existed")
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeFailed)
	e.saveRunRecord(b)
	if n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	if len(saw) != 2 || saw[0].Outcome != a.Outcome || saw[1].Outcome != a.Outcome || runID(saw[0]) != runID(a) || runID(saw[1]) != runID(a) {
		t.Fatalf("flushed records: %+v", saw)
	}
	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
	if err != nil || !bytes.Equal(hist, aBytes) {
		t.Fatalf("history %v\n%s", err, hist)
	}
	if historyRecord(t, stateDir, runID(a)).Outcome == outcomeInterrupted {
		t.Fatal("blocked record stored as interrupted")
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != b.Outcome {
		t.Fatalf("last.json %+v %v", got, err)
	}
	if strings.Contains(stderr.String(), "could not keep the run in the history") {
		t.Fatalf("prune error blocked or was warned after the file existed: %q", stderr.String())
	}
	if ids := historyIDs(t, stateDir); len(ids) != 2 {
		t.Fatalf("history %v, want A and B", ids)
	}
}

// A reported success that leaves no regular file is still a failure, and it
// is not retried into a loop: one call, last.json unchanged.
func TestFlushDoesNotAdvanceWhenTheWriteClaimsSuccess(t *testing.T) {
	historyWriterIsProduction(t)
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	old := writeFinishedHistory
	t.Cleanup(func() { writeFinishedHistory = old })
	writeFinishedHistory = func(string, runRecord) error {
		n++
		return nil
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(2, outcomeOK))
	if n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("last.json changed without a history file")
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("stderr: %q", stderr.String())
	}
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Fatalf("history %v", ids)
	}
}

func TestSavingTheSameRunTwiceThroughAReplacedSymlinkIsOneRow(t *testing.T) {
	_, stateDir := setup(t)
	target := filepath.Join(t.TempDir(), "sentinel.json")
	const sentinel = "sentinel-same-run"
	if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	a := histRec(1, outcomeFailed)
	e.saveRunRecord(a)
	leaf := runHistoryPath(stateDir, runID(a))
	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(filepath.Dir(leaf), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, leaf); err != nil {
		t.Fatal(err)
	}
	a.Outcome = outcomeOK
	a.Error = ""
	e.saveRunRecord(a)
	gotTarget, err := os.ReadFile(target)
	if err != nil || string(gotTarget) != sentinel {
		t.Fatalf("target %q %v", gotTarget, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(a) {
		t.Fatalf("history %v", ids)
	}
	got := historyRecord(t, stateDir, runID(a))
	if got.Outcome != outcomeOK || got.Error != "" || got.Outcome == outcomeInterrupted {
		t.Fatalf("row %+v", got)
	}
	info, err := os.Lstat(leaf)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("leaf %v %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(leaf))
	if err != nil {
		t.Fatal(err)
	}
	nJSON := 0
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".json") {
			nJSON++
		}
	}
	if nJSON != 1 {
		t.Fatalf("json names in history: %d (%v)", nJSON, entries)
	}
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
}

func TestSaveRunRecordUnicodeStateDirStillFlushesTheFinishedRecord(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "naïve 状态", "ε")
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	a := histRec(1, outcomeFailed)
	e.saveRunRecord(a)
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(runHistoryPath(stateDir, runID(a))); err != nil {
		t.Fatal(err)
	}
	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)
	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(a)))
	if err != nil || !bytes.Equal(hist, aBytes) {
		t.Fatalf("flushed history %v\n%s", err, hist)
	}
	if historyRecord(t, stateDir, runID(a)).Outcome != outcomeFailed {
		t.Fatal("flushed record was rewritten")
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) {
		t.Fatalf("last.json %+v %v", got, err)
	}
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
}

func TestArchiveInterruptedRunLeavesFinishedBytesAlone(t *testing.T) {
	for _, outcome := range []string{outcomeOK, outcomeFailed, outcomeCancelled, outcomeSkipped, outcomeInterrupted} {
		t.Run(outcome, func(t *testing.T) {
			_, stateDir := setup(t)
			a := histRec(1, outcome)
			writeLast(t, stateDir, a)
			before, err := os.ReadFile(runRecordPath(stateDir))
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "sentinel.json")
			const sentinel = "sentinel-archive"
			if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir, runHistoryDir), 0o700); err != nil {
				t.Fatal(err)
			}
			leaf := runHistoryPath(stateDir, runID(a))
			if err := os.Symlink(target, leaf); err != nil {
				t.Fatal(err)
			}
			r, err := archiveInterruptedRun(stateDir)
			if r != nil || err != nil {
				t.Fatalf("archived %v, err %v", r, err)
			}
			after, err := os.ReadFile(runRecordPath(stateDir))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("last.json changed:\n%s\n%s", before, after)
			}
			info, err := os.Lstat(leaf)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("leaf %v %v", info, err)
			}
			gotTarget, err := os.ReadFile(target)
			if err != nil || string(gotTarget) != sentinel {
				t.Fatalf("target %q %v", gotTarget, err)
			}
			got, err := readRunRecord(stateDir)
			if err != nil || got == nil || got.Outcome != outcome {
				t.Fatalf("outcome %+v %v", got, err)
			}
		})
	}
}

func TestSaveRunRecordEmptyStateDirLeavesTheWorkingTree(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(filepath.Join(cwd, "runs", "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "note.txt"), []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := treeOf(t, cwd)
	var stderr bytes.Buffer
	e := &env{stateDir: "", stdout: io.Discard, stderr: &stderr}
	e.saveRunRecord(histRec(1, outcomeOK))
	e.saveRunRecord(runningHist(2))
	if strings.Count(stderr.String(), "could not record the run") != 2 {
		t.Fatalf("stderr: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "could not keep the run in the history") {
		t.Fatalf("empty state dir took the history path: %q", stderr.String())
	}
	treesSame(t, before, treeOf(t, cwd))
}

// An unreadable last.json is not copied into the history, including when the
// path is a symlink. The outside target stays. The new record is what is written.
func TestLastJSONFailureOnUnreadableSymlinkDoesNotInventHistory(t *testing.T) {
	_, stateDir := setup(t)
	marker := "UNREADABLE-LAST-MARKER"
	raw := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","outcome":"failed","error":"` + marker + `"`)
	target := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lastPath := runRecordPath(stateDir)
	if err := os.Symlink(target, lastPath); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeOK)
	e.saveRunRecord(b)

	gotTarget, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(gotTarget, raw) {
		t.Fatalf("wrote through last.json: %q %v", gotTarget, err)
	}
	lastInfo, err := os.Lstat(lastPath)
	if err != nil || !lastInfo.Mode().IsRegular() || lastInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json %v %v", lastInfo, err)
	}
	last, err := os.ReadFile(lastPath)
	if err != nil || strings.Contains(string(last), marker) {
		t.Fatalf("last.json still holds the unreadable record:\n%s\nerr %v", last, err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) {
		t.Fatalf("last.json %+v %v", got, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(b) {
		t.Fatalf("history %v", ids)
	}
	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(b)))
	if err != nil || strings.Contains(string(hist), marker) {
		t.Fatalf("history invented the unreadable record: %q %v", hist, err)
	}
	if historyRecord(t, stateDir, runID(b)).Outcome == outcomeInterrupted {
		t.Fatal("new record stored as interrupted")
	}
}

func TestLastJSONFailureOversizedIsNotInvented(t *testing.T) {
	_, stateDir := setup(t)
	marker := "OVERSIZE-LAST-MARKER"
	raw := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","pid":1,"outcome":"ok","error":"` + marker + strings.Repeat("x", maxRunRecord) + `"}`)
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runRecordFile), string(raw))
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(3, outcomeFailed)
	e.saveRunRecord(b)
	last, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil || strings.Contains(string(last), marker) {
		t.Fatalf("last.json:\n%s\nerr %v", last, err)
	}
	err = filepath.WalkDir(filepath.Join(stateDir, runRecordDir, runHistoryDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(body), marker) {
			t.Errorf("%s contains the oversized record", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != outcomeFailed {
		t.Fatalf("last.json %+v %v", got, err)
	}
}

// History that cannot be written is a warning on the real run path. The run
// still exits 0, and a finished last.json that could not be flushed is not
// replaced. Nothing is written in the working directory.
func TestHistoryWriteFailureDoesNotFailTheRun(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	hist := filepath.Join(stateDir, runRecordDir, runHistoryDir)
	if err := os.RemoveAll(hist); err != nil {
		t.Fatal(err)
	}
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir), "not-a-directory")
	_, stderr := mustCode(t, 0, "run-once")
	if !warningKeepsHistoryError(stderr) {
		t.Fatalf("stderr: %q", stderr)
	}
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("run-once replaced a blocked last.json:\n%s\n%s", before, after)
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Fatalf("working directory %v (err %v)", entries, err)
	}
}
