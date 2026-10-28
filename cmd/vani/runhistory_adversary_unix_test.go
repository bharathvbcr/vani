//go:build !windows

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// saveWithin fails the test if saveRunRecord is still running after a second.
// A FIFO open would otherwise block the test until the package timeout.
func saveWithin(t *testing.T, e *env, r runRecord) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		e.saveRunRecord(r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("saveRunRecord did not return within a second")
	}
}

// A FIFO at the history leaf is not a kept record and must not be opened.
// A directory is covered in the portable tests; both refuse the next record.
func TestDirectoryHistoryFIFOLeafDoesNotBlock(t *testing.T) {
	historyWriterIsProduction(t)
	_, stateDir := setup(t)
	a := histRec(1, outcomeFailed)
	writeLast(t, stateDir, a)
	before, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(a))
	if err := os.MkdirAll(filepath.Dir(leaf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(leaf, 0o600); err != nil {
		t.Fatal(err)
	}
	var n int
	wrong := false
	old := writeFinishedHistory
	t.Cleanup(func() { writeFinishedHistory = old })
	writeFinishedHistory = func(dir string, r runRecord) error {
		n++
		if r.Outcome == outcomeInterrupted || runID(r) != runID(a) {
			wrong = true
		}
		return old(dir, r)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	saveWithin(t, e, histRec(2, outcomeOK))
	if wrong {
		t.Fatal("flush rewrote the finished record")
	}
	if n != 2 {
		t.Fatalf("history attempts = %d, want 2", n)
	}
	after, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("last.json changed while the history leaf was a FIFO")
	}
	info, err := os.Lstat(leaf)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO leaf: %v %v", info, err)
	}
	if historyFileKept(leaf) {
		t.Fatal("FIFO counted as kept")
	}
	if !warningKeepsHistoryError(stderr.String()) {
		t.Fatalf("stderr: %q", stderr.String())
	}
	entries, err := os.ReadDir(filepath.Dir(leaf))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(leaf) {
		t.Fatalf("history dir %v, want only the FIFO", entries)
	}
}

// A symlink to a FIFO is a leaf link. The pipe must stay a pipe, and the
// save must return within a second. The outside pipe is not the record.
func TestSymlinkHistoryLeafToFIFODoesNotBlock(t *testing.T) {
	_, stateDir := setup(t)
	a := histRec(1, outcomeOK)
	writeLast(t, stateDir, a)
	aBytes, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir, runHistoryDir), 0o700); err != nil {
		t.Fatal(err)
	}
	leaf := runHistoryPath(stateDir, runID(a))
	if err := os.Symlink(fifo, leaf); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeFailed)
	saveWithin(t, e, b)

	info, err := os.Lstat(fifo)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO behind the link: %v %v", info, err)
	}
	leafInfo, err := os.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	last, err := os.ReadFile(runRecordPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case leafInfo.Mode().IsRegular():
		body, err := os.ReadFile(leaf)
		if err != nil || !bytes.Equal(body, aBytes) {
			t.Fatalf("history leaf:\n%s\nwant\n%s\nerr %v", body, aBytes, err)
		}
		got, err := readRunRecord(stateDir)
		if err != nil || got == nil || runID(*got) != runID(b) {
			t.Fatalf("last.json %+v %v", got, err)
		}
	case leafInfo.Mode()&os.ModeSymlink != 0 && bytes.Equal(last, aBytes):
		if !warningKeepsHistoryError(stderr.String()) {
			t.Fatalf("stderr: %q", stderr.String())
		}
	default:
		t.Fatalf("leaf mode %v", leafInfo.Mode())
	}
}

// last.json as a FIFO is not a record. Opening it must not block the next
// save, and nothing is invented from the pipe.
func TestLastJSONFailureFIFODoesNotBlock(t *testing.T) {
	_, stateDir := setup(t)
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lastPath := runRecordPath(stateDir)
	if err := syscall.Mkfifo(lastPath, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(1, outcomeOK)
	saveWithin(t, e, b)
	info, err := os.Lstat(lastPath)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("last.json FIFO was replaced or opened into something else: %v %v", info, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 0 {
		t.Fatalf("history invented from a FIFO last.json: %v", ids)
	}
	if !warningLine(stderr.String(), "could not record the run") && !warningLine(stderr.String(), "could not keep the run in the history") {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// last.json as a symlink to an outside FIFO is not opened. The pipe stays a
// pipe. The link is replaced by the new regular record, and that record is
// the only history entry.
func TestLastJSONSymlinkToFIFODoesNotBlock(t *testing.T) {
	_, stateDir := setup(t)
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	lastPath := runRecordPath(stateDir)
	if err := os.Symlink(fifo, lastPath); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(1, outcomeOK)
	saveWithin(t, e, b)

	info, err := os.Lstat(fifo)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().IsRegular() {
		t.Fatalf("outside FIFO was replaced or opened into something else: %v %v", info, err)
	}
	lastInfo, err := os.Lstat(lastPath)
	if err != nil || !lastInfo.Mode().IsRegular() || lastInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json %v %v", lastInfo, err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != b.Outcome {
		t.Fatalf("last.json %+v %v", got, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(b) {
		t.Fatalf("history %v, want only %s", ids, runID(b))
	}
	kept := historyRecord(t, stateDir, runID(b))
	if runID(*kept) != runID(b) || kept.Outcome != b.Outcome {
		t.Fatalf("history record %+v", kept)
	}
	hist, err := os.ReadFile(runHistoryPath(stateDir, runID(b)))
	if err != nil {
		t.Fatal(err)
	}
	last, err := os.ReadFile(lastPath)
	if err != nil || !bytes.Equal(hist, last) {
		t.Fatalf("history is not the new record:\n%s\n%s\nerr %v", hist, last, err)
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, runRecordDir, runHistoryDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(runHistoryPath(stateDir, runID(b))) {
		t.Fatalf("history dir %v", entries)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

func warningLine(stderr, phrase string) bool {
	return bytes.Contains([]byte(stderr), []byte(phrase))
}
