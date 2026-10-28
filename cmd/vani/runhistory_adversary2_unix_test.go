//go:build !windows

package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A UNIX-domain socket path has to fit in sockaddr_un, which is 104 bytes
// on darwin. t.TempDir lives under /var/folders and can be longer than that.
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vjs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func regularTreeContains(t *testing.T, root, marker string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, lerr := os.Lstat(path)
		if lerr != nil || !info.Mode().IsRegular() {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(body), marker) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// A socket at last.json is not opened. The node stays a socket, history is
// not invented, and the refusal is ErrNotRegular on the write.
func TestLastJSONSocketDoesNotBlock(t *testing.T) {
	historyWriterIsProduction(t)
	stateDir := shortStateDir(t)
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lastPath := runRecordPath(stateDir)
	ln, err := net.Listen("unix", lastPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()

	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	saveWithin(t, e, histRec(1, outcomeOK))

	if accepted.Load() != 0 {
		t.Fatalf("something connected to last.json %d times", accepted.Load())
	}
	info, err := os.Lstat(lastPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().IsRegular() {
		t.Fatalf("last.json socket: %v %v", info, err)
	}
	if ids := historyIDs(t, stateDir); len(ids) != 0 {
		t.Fatalf("history invented from a socket last.json: %v", ids)
	}
	if !warningLine(stderr.String(), "could not record the run") || !warningLine(stderr.String(), "not a regular file") {
		t.Fatalf("stderr: %q", stderr.String())
	}
	if warningLine(stderr.String(), "could not keep the run in the history") {
		t.Fatalf("stderr took the history path: %q", stderr.String())
	}
}

// A mode-000 regular file is unreadable even when it holds a finished record.
// Those bytes are not copied into history; the new record replaces the file.
func TestLastJSONModeZeroIsNotInvented(t *testing.T) {
	_, stateDir := setup(t)
	marker := "MODE-ZERO-LAST-MARKER"
	a := histRec(1, outcomeFailed)
	a.Error = marker
	writeLast(t, stateDir, a)
	lastPath := runRecordPath(stateDir)
	if err := os.Chmod(lastPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lastPath, 0o600) })
	if f, err := os.Open(lastPath); err == nil {
		_ = f.Close()
		t.Fatal("mode 000 last.json was readable; this does not test the unreadable case")
	}

	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeOK)
	saveWithin(t, e, b)

	if regularTreeContains(t, stateDir, marker) {
		t.Fatal("mode 000 record was copied")
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != b.Outcome {
		t.Fatalf("last.json %+v %v", got, err)
	}
	info, err := os.Lstat(lastPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json %v %v", info, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(b) {
		t.Fatalf("history %v, want only %s", ids, runID(b))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// A corrupt regular last.json is not a record. The new run is what is written,
// and the old bytes do not reappear in the history.
func TestLastJSONCorruptRegularIsNotInvented(t *testing.T) {
	marker := "CORRUPT-REGULAR-LAST-MARKER"
	a := histRec(1, outcomeFailed)
	a.Error = marker
	valid, err := marshalRunRecord(a)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{name: "truncated", raw: []byte("{" + marker)},
		{name: "trailing", raw: append(append([]byte{}, valid...), '\n', '{', '}')},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stateDir := setup(t)
			writeStateFile(t, stateDir, filepath.Join(runRecordDir, runRecordFile), string(tc.raw))
			var stderr bytes.Buffer
			e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
			b := histRec(2, outcomeCancelled)
			saveWithin(t, e, b)
			if regularTreeContains(t, stateDir, marker) {
				t.Fatal("corrupt record was copied")
			}
			got, err := readRunRecord(stateDir)
			if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != outcomeCancelled {
				t.Fatalf("last.json %+v %v", got, err)
			}
			ids := historyIDs(t, stateDir)
			if len(ids) != 1 || ids[0] != runID(b) {
				t.Fatalf("history %v, want only %s", ids, runID(b))
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr: %q", stderr.String())
			}
		})
	}
}

func statIno(t *testing.T, path string) (uint64, int64) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Ino), st.Size
}

// A symlink to a directory is not followed. The link is replaced by the new
// record; the directory and its sentinel stay put.
func TestLastJSONSymlinkToDirectoryIsReplaced(t *testing.T) {
	_, stateDir := setup(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirIno, dirSize := statIno(t, outside)
	fileIno, fileSize := statIno(t, sentinel)
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lastPath := runRecordPath(stateDir)
	if err := os.Symlink(outside, lastPath); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(1, outcomeOK)
	saveWithin(t, e, b)

	gotDir, gotDirSize := statIno(t, outside)
	gotFile, gotFileSize := statIno(t, sentinel)
	if gotDir != dirIno || gotDirSize != dirSize || gotFile != fileIno || gotFileSize != fileSize {
		t.Fatalf("outside directory changed ino/size dir %d/%d -> %d/%d file %d/%d -> %d/%d",
			dirIno, dirSize, gotDir, gotDirSize, fileIno, fileSize, gotFile, gotFileSize)
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "keep" {
		t.Fatalf("sentinel %q %v", body, err)
	}
	ents, err := os.ReadDir(outside)
	if err != nil || len(ents) != 1 || ents[0].Name() != "sentinel" {
		t.Fatalf("outside dir %+v %v", ents, err)
	}
	info, err := os.Lstat(lastPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json %v %v", info, err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) {
		t.Fatalf("last.json %+v %v", got, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(b) {
		t.Fatalf("history %v, want only %s", ids, runID(b))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// The same replacement when the link points at the runs directory itself.
// Following it would make the leaf name a directory.
func TestLastJSONSymlinkToRunsDirectoryIsReplaced(t *testing.T) {
	_, stateDir := setup(t)
	runs := filepath.Join(stateDir, runRecordDir)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(runs, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	runsIno, _ := statIno(t, runs)
	lastPath := runRecordPath(stateDir)
	if err := os.Symlink(".", lastPath); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(1, outcomeFailed)
	saveWithin(t, e, b)

	gotRuns, _ := statIno(t, runs)
	if gotRuns != runsIno {
		t.Fatalf("runs directory inode %d -> %d", runsIno, gotRuns)
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "keep" {
		t.Fatalf("sentinel %q %v", body, err)
	}
	info, err := os.Lstat(runs)
	if err != nil || !info.IsDir() {
		t.Fatalf("runs: %v %v", info, err)
	}
	lastInfo, err := os.Lstat(lastPath)
	if err != nil || !lastInfo.Mode().IsRegular() || lastInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json %v %v", lastInfo, err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != outcomeFailed {
		t.Fatalf("last.json %+v %v", got, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(b) {
		t.Fatalf("history %v, want only %s", ids, runID(b))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// A symlink to a finished record outside the state dir is not followed, so
// that record is not what the history gets. The target inode stays.
func TestLastJSONSymlinkToFinishedFileIsNotHistory(t *testing.T) {
	_, stateDir := setup(t)
	marker := "OUTSIDE-FINISHED-MARKER"
	a := histRec(1, outcomeOK)
	a.Error = marker
	raw, err := marshalRunRecord(a)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "finished.json")
	if err := os.WriteFile(outside, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ino, size := statIno(t, outside)
	if err := os.MkdirAll(filepath.Join(stateDir, runRecordDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lastPath := runRecordPath(stateDir)
	if err := os.Symlink(outside, lastPath); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
	b := histRec(2, outcomeFailed)
	saveWithin(t, e, b)

	gotIno, gotSize := statIno(t, outside)
	if gotIno != ino || gotSize != size {
		t.Fatalf("outside inode/size %d/%d -> %d/%d", ino, size, gotIno, gotSize)
	}
	gotOutside, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(gotOutside, raw) {
		t.Fatalf("outside file changed: %v", err)
	}
	if regularTreeContains(t, stateDir, marker) {
		t.Fatal("history invented the outside record")
	}
	info, err := os.Lstat(lastPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("last.json %v %v", info, err)
	}
	got, err := readRunRecord(stateDir)
	if err != nil || got == nil || runID(*got) != runID(b) || got.Outcome != outcomeFailed {
		t.Fatalf("last.json %+v %v", got, err)
	}
	ids := historyIDs(t, stateDir)
	if len(ids) != 1 || ids[0] != runID(b) {
		t.Fatalf("history %v, want only %s", ids, runID(b))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %q", stderr.String())
	}
}

// Two overlapping saves of one finished last.json. Each flush tries once and,
// only if that attempt errors, once more, so two callers write the history at
// most four times. A second successful write of the same id is the same file
// replaced, not a retry, and saveRunRecord takes no lock that claims to cover
// this overlap. The bug would be one of the finished records disappearing.
func TestConcurrentSaveKeepsFinishedRecord(t *testing.T) {
	historyWriterIsProduction(t)
	var n atomic.Int32
	old := writeFinishedHistory
	writeFinishedHistory = func(dir string, r runRecord) error {
		n.Add(1)
		return old(dir, r)
	}
	t.Cleanup(func() { writeFinishedHistory = old })

	for round := 0; round < 8; round++ {
		_, stateDir := setup(t)
		a := histRec(1, outcomeFailed)
		writeLast(t, stateDir, a)
		aBytes, err := os.ReadFile(runRecordPath(stateDir))
		if err != nil {
			t.Fatal(err)
		}
		b := histRec(2, outcomeOK)
		c := histRec(3, outcomeCancelled)
		n.Store(0)

		var wg sync.WaitGroup
		errc := make(chan string, 2)
		start := make(chan struct{})
		save := func(r runRecord) {
			defer wg.Done()
			<-start
			var stderr bytes.Buffer
			e := &env{stateDir: stateDir, stdout: io.Discard, stderr: &stderr}
			e.saveRunRecord(r)
			errc <- stderr.String()
		}
		wg.Add(2)
		go save(b)
		go save(c)
		close(start)
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: saveRunRecord did not return", round)
		}
		close(errc)
		for msg := range errc {
			if msg != "" {
				t.Fatalf("round %d stderr: %q", round, msg)
			}
		}
		if got := n.Load(); got < 1 || got > 4 {
			t.Fatalf("round %d: history flush calls = %d, want 1..4", round, got)
		}
		keptPath := runHistoryPath(stateDir, runID(a))
		kept, err := os.ReadFile(keptPath)
		if err != nil || !bytes.Equal(kept, aBytes) {
			t.Fatalf("round %d: finished record A missing or changed: %v\n%s", round, err, kept)
		}
		info, err := os.Lstat(keptPath)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("round %d: A is not a regular file: %v %v", round, info, err)
		}
		for _, rec := range []runRecord{b, c} {
			got, err := readRunRecordFile(runHistoryPath(stateDir, runID(rec)))
			if err != nil || got == nil || runID(*got) != runID(rec) || got.Outcome != rec.Outcome {
				t.Fatalf("round %d: %s missing: %+v %v", round, runID(rec), got, err)
			}
		}
		last, err := readRunRecord(stateDir)
		if err != nil || last == nil || (runID(*last) != runID(b) && runID(*last) != runID(c)) {
			t.Fatalf("round %d: last.json %+v %v", round, last, err)
		}
	}
}
