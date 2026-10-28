//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// readRecordWithin fails the test when readRunRecordFile is still running
// after within. Opening a FIFO for read blocks until a writer arrives, which
// would otherwise hold the package until its timeout.
func readRecordWithin(t *testing.T, path string, within time.Duration) (*runRecord, error) {
	t.Helper()
	type result struct {
		rec *runRecord
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, err := readRunRecordFile(path)
		done <- result{rec, err}
	}()
	select {
	case got := <-done:
		return got.rec, got.err
	case <-time.After(within):
		t.Fatalf("readRunRecordFile(%s) blocked for %s", path, within)
		return nil, nil
	}
}

func assertNotRegular(t *testing.T, path string) {
	t.Helper()
	rec, err := readRecordWithin(t, path, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("readRunRecordFile: record=%v err=%v, want a non-regular file", rec, err)
	}
	if rec != nil {
		t.Fatalf("decoded a non-regular file as %+v", rec)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("non-regular file reported as missing")
	}
}

func TestLastJSONFIFOIsNotOpened(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	assertNotRegular(t, path)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("last.json is no longer a FIFO: %v %v", info, err)
	}
}

func TestLastJSONSymlinkTargetIsNotRead(t *testing.T) {
	stateDir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "other.json")
	outsideRec := histRec(9, outcomeFailed)
	outsideRec.Error = "outside-run-marker"
	raw, err := marshalRunRecord(outsideRec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(stateDir, "runs", "last.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	rec, err := readRecordWithin(t, link, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err %v, want a non-regular file", err)
	}
	if rec != nil && (rec.Error == outsideRec.Error || runID(*rec) == runID(outsideRec)) {
		t.Fatalf("decoded the symlink target: %+v", rec)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("target bytes changed: %v\n%s", err, after)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("last.json is no longer a symlink: %v %v", info, err)
	}
}

func TestLastJSONSymlinkDirectoryAndLoop(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(dir, "last.json")
	if err := os.Symlink(sub, dirLink); err != nil {
		t.Fatal(err)
	}
	assertNotRegular(t, dirLink)

	loop := filepath.Join(dir, "loop.json")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	assertNotRegular(t, loop)
}

func TestRunRecordRegularFileStillDecodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last.json")
	rec := histRec(1, outcomeOK)
	raw, err := marshalRunRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRunRecordFile(path)
	if err != nil || got == nil || runID(*got) != runID(rec) || got.Outcome != outcomeOK {
		t.Fatalf("got %+v err %v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(raw)) {
		t.Fatalf("record file: %v %v", info, err)
	}

	// The file is exactly one small valid record: no trailing junk, no padding.
	minimal := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","outcome":"ok"}`)
	small := filepath.Join(dir, "small.json")
	if err := os.WriteFile(small, minimal, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readRunRecordFile(small)
	if err != nil || got == nil || got.Outcome != outcomeOK || got.V != runRecordVersion || got.StartedAt.IsZero() {
		t.Fatalf("small record: %+v %v", got, err)
	}
	info, err = os.Lstat(small)
	if err != nil || info.Size() != int64(len(minimal)) {
		t.Fatalf("small file size %v %v, want %d", info, err, len(minimal))
	}

	missing, err := readRunRecordFile(filepath.Join(dir, "missing.json"))
	if missing != nil || err != nil {
		t.Fatalf("missing file: record=%v err=%v, want nil, nil", missing, err)
	}
}

func TestHistorySymlinkIsNotRead(t *testing.T) {
	stateDir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "real.json")
	outsideRec := histRec(3, outcomeOK)
	outsideRec.Error = "history-outside-marker"
	raw, err := marshalRunRecord(outsideRec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	path := runHistoryPath(stateDir, runID(histRec(8, outcomeCancelled)))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	rec, err := readRecordWithin(t, path, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("history read: record=%v err=%v, want a non-regular file", rec, err)
	}
	if rec != nil && (rec.Error == outsideRec.Error || runID(*rec) == runID(outsideRec)) {
		t.Fatalf("history read returned the symlink target: %+v", rec)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("history target bytes changed: %v\n%s", err, after)
	}
}

func TestLastJSONDailyFIFOSnapshotDoesNotBlock(t *testing.T) {
	stateDir := t.TempDir()
	path := runRecordPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	old := statExecutable
	t.Cleanup(func() { statExecutable = old })
	var calls atomic.Int32
	start := binIdentity{dev: 1, ino: 1, size: 1, mtime: 1}
	statExecutable = func(string) (binIdentity, error) {
		if calls.Add(1) == 1 {
			return start, nil
		}
		return binIdentity{dev: 1, ino: 2, size: 2, mtime: 2}, nil
	}

	var stderr bytes.Buffer
	e := &env{
		stateDir: stateDir,
		stdout:   &bytes.Buffer{},
		stderr:   &stderr,
		binary:   &binWatch{path: "vani-test", start: start},
	}
	job := e.dailyJob(&App{}, config.Default(), triggerSchedule, runOptions{NoDigest: true})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- job(ctx) }()
	var got error
	select {
	case got = <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("daily job blocked reading a FIFO last.json")
	}
	if !errors.Is(got, errConfigRestart) {
		t.Fatalf("returned %v, want errConfigRestart", got)
	}
	if calls.Load() < 2 {
		t.Fatalf("reload checks = %d, snapshot read did not return", calls.Load())
	}
	logged := false
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(line, "could not read the run record to restore it after a config restart") &&
			strings.Contains(line, "not a regular file") {
			logged = true
		}
		if strings.Contains(line, "could not drop the run record") {
			t.Fatalf("restart removed a non-regular record:\n%s", stderr.String())
		}
	}
	if !logged {
		t.Fatalf("stderr did not report the unread snapshot:\n%s", stderr.String())
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("last.json is no longer a FIFO: %v %v", info, err)
	}
}
