//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// fileIdent is the leaf identity a read must not replace. Dev and Ino come
// from Lstat, so a symlink is the link itself and not its target.
type fileIdent struct {
	dev, ino uint64
	mode     os.FileMode
}

func fileIdentOf(t *testing.T, path string) fileIdent {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("lstat %s: stat type %T", path, info.Sys())
	}
	return fileIdent{dev: uint64(st.Dev), ino: uint64(st.Ino), mode: info.Mode()}
}

func sameIdent(t *testing.T, path string, before fileIdent) {
	t.Helper()
	after := fileIdentOf(t, path)
	if after != before {
		t.Fatalf("%s identity changed: before %+v after %+v", path, before, after)
	}
}

// holdFIFOWriter blocks in open(WRONLY) on a FIFO. opened reports whether a
// reader connected. The cleanup opens O_NONBLOCK so a still-blocked writer
// is released without the test blocking in open.
func holdFIFOWriter(t *testing.T, path string) (opened func() bool) {
	t.Helper()
	hit := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			errc <- err
			return
		}
		_ = f.Close()
		close(hit)
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-errc:
		t.Fatalf("fifo writer %s: %v", path, err)
	case <-hit:
		t.Fatalf("fifo %s already had a reader", path)
	default:
	}
	t.Cleanup(func() {
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
		select {
		case <-hit:
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	})
	return func() bool {
		select {
		case <-hit:
			return true
		default:
			return false
		}
	}
}

func assertUnopened(t *testing.T, path string, before fileIdent, opened func() bool) {
	t.Helper()
	sameIdent(t, path, before)
	if opened != nil && opened() {
		t.Fatalf("%s was opened", path)
	}
}

// recordOfLen is one valid run record of exactly n bytes. Truncating a
// one-byte-over file to maxRunRecord would therefore decode.
func recordOfLen(t *testing.T, n int) []byte {
	t.Helper()
	prefix := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","pid":7,"outcome":"ok","error":"`)
	suffix := []byte(`"}`)
	pad := n - len(prefix) - len(suffix)
	if pad < 1 {
		t.Fatalf("record size %d is below the fixed prefix", n)
	}
	out := make([]byte, 0, n)
	out = append(out, prefix...)
	out = append(out, bytes.Repeat([]byte("q"), pad)...)
	out = append(out, suffix...)
	if len(out) != n {
		t.Fatalf("record length %d, want %d", len(out), n)
	}
	return out
}

func writeBytes(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNotRegularRecord(t *testing.T, path string) {
	t.Helper()
	rec, err := readRecordWithin(t, path, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("readRunRecordFile(%s) = %+v, %v; want not a regular file", path, rec, err)
	}
	if rec != nil {
		t.Fatalf("readRunRecordFile returned a record for %s: %+v", path, rec)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readRunRecordFile(%s) is ErrNotExist, so a restart would remove it", path)
	}
	raw, berr := readBoundedFile(path, maxRunRecord)
	if berr == nil || len(raw) != 0 || errors.Is(berr, os.ErrNotExist) || !strings.Contains(berr.Error(), "not a regular file") {
		t.Fatalf("readBoundedFile(%s) = %d bytes, %v", path, len(raw), berr)
	}
}

// restartJob runs one daily job whose binary identity changes on the second
// stat, which is the reload after the last.json snapshot. It fails if the
// job is still running after 2s.
func restartJob(t *testing.T, stateDir string) string {
	t.Helper()
	old := statExecutable
	t.Cleanup(func() { statExecutable = old })
	var calls atomic.Int32
	start := binIdentity{dev: 1, ino: 1, size: 1, mtime: 1}
	statExecutable = func(string) (binIdentity, error) {
		if calls.Add(1) == 1 {
			return start, nil
		}
		return binIdentity{dev: 9, ino: 9, size: 9, mtime: 9}, nil
	}
	var stderr bytes.Buffer
	e := &env{
		stateDir: stateDir,
		stdout:   &bytes.Buffer{},
		stderr:   &stderr,
		binary:   &binWatch{path: "vani-test", start: start},
	}
	job := e.dailyJob(&App{}, config.Default(), triggerSchedule, runOptions{NoDigest: true})
	done := make(chan error, 1)
	began := time.Now()
	go func() { done <- job(context.Background()) }()
	select {
	case got := <-done:
		if time.Since(began) >= 2*time.Second {
			t.Fatalf("daily job took %s, want under 2s\n%s", time.Since(began), stderr.String())
		}
		if !errors.Is(got, errConfigRestart) {
			t.Fatalf("returned %v, want errConfigRestart\n%s", got, stderr.String())
		}
		if calls.Load() < 2 || !strings.Contains(stderr.String(), "binary was replaced") {
			t.Fatalf("checks=%d, snapshot was not taken before restart\n%s", calls.Load(), stderr.String())
		}
		return stderr.String()
	case <-time.After(2 * time.Second):
		t.Fatalf("daily job blocked\n%s", stderr.String())
	}
	return ""
}

func TestLastJSONAdversaryLeaf(t *testing.T) {
	t.Run("fifo", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "last.json")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, path)
		opened := holdFIFOWriter(t, path)
		assertNotRegularRecord(t, path)
		assertUnopened(t, path, before, opened)
	})

	t.Run("symlink to fifo", func(t *testing.T) {
		dir := t.TempDir()
		fifo := filepath.Join(dir, "pipe")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "last.json")
		if err := os.Symlink(fifo, link); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, fifo)
		linkBefore := fileIdentOf(t, link)
		opened := holdFIFOWriter(t, fifo)
		assertNotRegularRecord(t, link)
		assertUnopened(t, fifo, before, opened)
		sameIdent(t, link, linkBefore)
	})

	t.Run("symlink to other run", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "other.json")
		rec := histRec(9, outcomeFailed)
		rec.Error = "outside-run-marker"
		raw, err := marshalRunRecord(rec)
		if err != nil {
			t.Fatal(err)
		}
		writeBytes(t, outside, raw)
		link := filepath.Join(dir, "runs", "last.json")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		// Relative, so a follower would resolve it from the link's directory
		// and a naive open of the link path would still return this run.
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, outside)
		got, err := readRecordWithin(t, link, time.Second)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") || got != nil {
			t.Fatalf("symlink read = %+v, %v", got, err)
		}
		if got != nil && (got.Error == rec.Error || runID(*got) == runID(rec)) {
			t.Fatalf("returned the target record %+v", got)
		}
		sameIdent(t, outside, before)
		after, err := os.ReadFile(outside)
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatalf("target bytes changed: %v %q", err, after)
		}
	})

	t.Run("relative symlink escape", func(t *testing.T) {
		dir := t.TempDir()
		outsideDir := filepath.Join(dir, "outside")
		if err := os.Mkdir(outsideDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outsideDir, "secret.json")
		rec := histRec(4, outcomeOK)
		rec.Error = "relative-escape-marker"
		raw, err := marshalRunRecord(rec)
		if err != nil {
			t.Fatal(err)
		}
		writeBytes(t, target, raw)
		linkDir := filepath.Join(dir, "state", "runs")
		if err := os.MkdirAll(linkDir, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "last.json")
		if err := os.Symlink(filepath.Join("..", "..", "outside", "secret.json"), link); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, target)
		assertNotRegularRecord(t, link)
		sameIdent(t, target, before)
		after, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatalf("escaped target changed: %v %q", err, after)
		}
	})

	t.Run("symlink to directory", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "last.json")
		if err := os.Symlink(sub, link); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, sub)
		assertNotRegularRecord(t, link)
		sameIdent(t, sub, before)
	})

	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "last.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, path)
		assertNotRegularRecord(t, path)
		sameIdent(t, path, before)
	})

	t.Run("symlink loop", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "last.json")
		b := filepath.Join(dir, "other.json")
		if err := os.Symlink(b, a); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(a, b); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, a)
		assertNotRegularRecord(t, a)
		sameIdent(t, a, before)
		self := filepath.Join(dir, "self.json")
		if err := os.Symlink(self, self); err != nil {
			t.Fatal(err)
		}
		selfBefore := fileIdentOf(t, self)
		assertNotRegularRecord(t, self)
		sameIdent(t, self, selfBefore)
	})

	t.Run("dangling symlink", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "last.json")
		if err := os.Symlink(filepath.Join(dir, "missing-target.json"), link); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, link)
		assertNotRegularRecord(t, link)
		sameIdent(t, link, before)
	})

	t.Run("socket", func(t *testing.T) {
		dir := shortStateDir(t)
		path := filepath.Join(dir, "last.json")
		ln, err := net.Listen("unix", path)
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
		before := fileIdentOf(t, path)
		assertNotRegularRecord(t, path)
		sameIdent(t, path, before)
		if accepted.Load() != 0 {
			t.Fatalf("socket accepted %d connections", accepted.Load())
		}
	})
}

func TestRunRecordAdversaryBounds(t *testing.T) {
	dir := t.TempDir()

	small := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","outcome":"ok"}`)
	smallPath := filepath.Join(dir, "small.json")
	writeBytes(t, smallPath, small)
	got, err := readRunRecordFile(smallPath)
	if err != nil || got == nil || got.V != runRecordVersion || got.Outcome != outcomeOK || !got.StartedAt.Equal(time.Date(2026, 10, 22, 9, 1, 0, 123_000_000, time.UTC)) {
		t.Fatalf("small record %+v %v", got, err)
	}
	info, err := os.Lstat(smallPath)
	if err != nil || info.Size() != int64(len(small)) || !info.Mode().IsRegular() {
		t.Fatalf("small file %v %v", info, err)
	}

	spaced := append(append([]byte{}, small...), '\n', ' ', '\t')
	spacedPath := filepath.Join(dir, "spaced.json")
	writeBytes(t, spacedPath, spaced)
	got, err = readRunRecordFile(spacedPath)
	if err != nil || got == nil || got.Outcome != outcomeOK {
		t.Fatalf("trailing whitespace is not junk: %+v %v", got, err)
	}

	exact := recordOfLen(t, maxRunRecord)
	exactPath := filepath.Join(dir, "exact.json")
	writeBytes(t, exactPath, exact)
	got, err = readRunRecordFile(exactPath)
	if err != nil || got == nil || got.Outcome != outcomeOK || got.PID != 7 || len(got.Error) == 0 || strings.Trim(got.Error, "q") != "" {
		t.Fatalf("exact max record %+v %v", got, err)
	}

	over := recordOfLen(t, maxRunRecord+1)
	overPath := filepath.Join(dir, "over.json")
	writeBytes(t, overPath, over)
	got, err = readRunRecordFile(overPath)
	if err == nil || got != nil {
		t.Fatalf("one-byte-over valid record decoded: %+v %v", got, err)
	}
	if got != nil && strings.Contains(got.Error, "q") {
		t.Fatalf("decoded the oversized record: %+v", got)
	}
	info, err = os.Lstat(overPath)
	if err != nil || info.Size() != int64(maxRunRecord+1) {
		t.Fatalf("over file size %v %v", info, err)
	}

	// The first maxRunRecord bytes are a valid record; the extra byte is outside it.
	trunc := append(recordOfLen(t, maxRunRecord), 'x')
	truncPath := filepath.Join(dir, "trunc.json")
	writeBytes(t, truncPath, trunc)
	got, err = readRunRecordFile(truncPath)
	if err == nil || got != nil {
		t.Fatalf("truncated-to-max would decode, got %+v %v", got, err)
	}

	base := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","pid":7,"outcome":"ok"}`)
	second := []byte(`{"v":1,"started_at":"2026-10-22T09:02:00.123Z","pid":8,"outcome":"failed","error":"SECOND-VALUE"}`)
	for _, junk := range [][]byte{
		append(append([]byte{}, base...), second...),
		append(append([]byte{}, base...), []byte(" true")...),
		append(append([]byte{}, base...), []byte("\n[]")...),
		append(append([]byte{}, base...), []byte("\x00")...),
		append(append([]byte{}, base...), []byte(",")...),
	} {
		path := filepath.Join(dir, "junk-"+strings.Trim(string(junk[len(base):]), "\n\x00")+".json")
		if strings.ContainsAny(filepath.Base(path), "/\x00") {
			path = filepath.Join(dir, "junk-nul.json")
		}
		writeBytes(t, path, junk)
		got, err = readRunRecordFile(path)
		if err == nil || got != nil {
			t.Fatalf("trailing junk %q decoded: %+v %v", junk[len(base):], got, err)
		}
		if got != nil && (got.PID == 8 || got.Error == "SECOND-VALUE" || got.PID == 7) {
			t.Fatalf("trailing junk returned a record: %+v", got)
		}
		if !strings.Contains(err.Error(), "trailing") && !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("trailing junk error %v", err)
		}
	}

	for _, v := range []string{"0", "2", "99", "-1"} {
		raw := []byte(`{"v":` + v + `,"started_at":"2026-10-22T09:01:00.123Z","outcome":"ok"}`)
		path := filepath.Join(dir, "ver-"+v+".json")
		writeBytes(t, path, raw)
		got, err = readRunRecordFile(path)
		if err == nil || got != nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("version %s: %+v %v", v, got, err)
		}
	}

	missing, err := readRunRecordFile(filepath.Join(dir, "missing.json"))
	if missing != nil || err != nil {
		t.Fatalf("missing file: %+v %v, want nil, nil", missing, err)
	}
	if _, err := readBoundedFile(filepath.Join(dir, "missing.json"), maxRunRecord); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing readBoundedFile %v, want ErrNotExist", err)
	}
}

func TestHistoryAdversaryOutsideSymlink(t *testing.T) {
	stateDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "real.json")
	rec := histRec(3, outcomeOK)
	rec.Error = "history-outside-marker"
	raw, err := marshalRunRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, outside, raw)
	id := runID(histRec(8, outcomeCancelled))
	path := runHistoryPath(stateDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	before := fileIdentOf(t, outside)

	got, err := readRecordWithin(t, path, time.Second)
	if err == nil || got != nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("history read = %+v, %v", got, err)
	}
	if got != nil && (got.Error == rec.Error || runID(*got) == runID(rec)) {
		t.Fatalf("history read returned the target %+v", got)
	}

	var stdout, stderr bytes.Buffer
	e := &env{stateDir: stateDir, stdout: &stdout, stderr: &stderr}
	ref, ferr := e.findRun(id)
	if ferr == nil || ref.Rec.Error == rec.Error || runID(ref.Rec) == runID(rec) || !strings.Contains(ferr.Error(), "not a regular file") {
		t.Fatalf("findRun = %+v, %v", ref, ferr)
	}
	if err := e.runsList([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), rec.Error) || strings.Contains(stderr.String(), rec.Error) {
		t.Fatalf("runs list parsed the target\nstdout %s\nstderr %s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "not a regular file") {
		t.Fatalf("runs list stdout %s", stdout.String())
	}
	stdout.Reset()
	if err := e.cmdRuns([]string{"show", "--json", id}); err == nil || !strings.Contains(stdout.String(), "not a regular file") || strings.Contains(stdout.String(), rec.Error) {
		t.Fatalf("runs show %v stdout %s", err, stdout.String())
	}

	sameIdent(t, outside, before)
	after, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("history target changed: %v %q", err, after)
	}

	t.Run("history fifo", func(t *testing.T) {
		fifoID := runID(histRec(6, outcomeFailed))
		fifoPath := runHistoryPath(stateDir, fifoID)
		if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
			t.Fatal(err)
		}
		fifoBefore := fileIdentOf(t, fifoPath)
		opened := holdFIFOWriter(t, fifoPath)
		assertNotRegularRecord(t, fifoPath)
		ref, ferr := e.findRun(fifoID)
		if ferr == nil || !strings.Contains(ferr.Error(), "not a regular file") || ref.Rec.Error != "" {
			t.Fatalf("findRun fifo = %+v, %v", ref, ferr)
		}
		stdout.Reset()
		if err := e.runsList([]string{"--json"}); err != nil {
			t.Fatal(err)
		}
		assertUnopened(t, fifoPath, fifoBefore, opened)
		if strings.Contains(stdout.String(), fifoID) && !strings.Contains(stdout.String(), "not a regular file") {
			t.Fatalf("runs list read the fifo: %s", stdout.String())
		}
	})
}

func TestLastJSONAdversaryConfigRestart(t *testing.T) {
	t.Run("regular bytes restored", func(t *testing.T) {
		stateDir := t.TempDir()
		path := runRecordPath(stateDir)
		// Three trailing spaces: a re-marshal would not reproduce them.
		prior := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","pid":4001,"outcome":"ok","error":"prior-bytes"}   `)
		writeBytes(t, path, prior)
		stderr := restartJob(t, stateDir)
		if strings.Contains(stderr, "could not restore") || strings.Contains(stderr, "could not drop") {
			t.Fatalf("stderr %s", stderr)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, prior) {
			t.Fatalf("restored %q err %v, want exact prior bytes", after, err)
		}
		got, err := readRunRecordFile(path)
		if err != nil || got == nil || got.Error != "prior-bytes" || got.Outcome != outcomeOK || got.PID != 4001 {
			t.Fatalf("restored record %+v %v", got, err)
		}
	})

	t.Run("missing prior removed", func(t *testing.T) {
		stateDir := t.TempDir()
		path := runRecordPath(stateDir)
		stderr := restartJob(t, stateDir)
		if strings.Contains(stderr, "could not record the run") || strings.Contains(stderr, "could not drop") {
			t.Fatalf("stderr %s", stderr)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("last.json after missing-prior restart: %v", err)
		}
	})

	t.Run("fifo prior not replaced", func(t *testing.T) {
		stateDir := shortStateDir(t)
		path := runRecordPath(stateDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, path)
		opened := holdFIFOWriter(t, path)
		stderr := restartJob(t, stateDir)
		assertUnopened(t, path, before, opened)
		if !strings.Contains(stderr, "could not read the run record to restore it after a config restart") || !strings.Contains(stderr, "not a regular file") {
			t.Fatalf("stderr %s", stderr)
		}
		if strings.Contains(stderr, "could not drop the run record") {
			t.Fatalf("restart removed a fifo:\n%s", stderr)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().IsRegular() {
			t.Fatalf("last.json is no longer a fifo: %v %v", info, err)
		}
	})

	t.Run("directory prior not removed", func(t *testing.T) {
		stateDir := t.TempDir()
		path := runRecordPath(stateDir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, path)
		stderr := restartJob(t, stateDir)
		sameIdent(t, path, before)
		if strings.Contains(stderr, "could not drop the run record") {
			t.Fatalf("stderr %s", stderr)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory prior: %v %v", info, err)
		}
	})

	t.Run("socket prior not replaced", func(t *testing.T) {
		stateDir := shortStateDir(t)
		path := runRecordPath(stateDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", path)
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
		before := fileIdentOf(t, path)
		stderr := restartJob(t, stateDir)
		sameIdent(t, path, before)
		if accepted.Load() != 0 {
			t.Fatalf("restart connected to the socket %d times\n%s", accepted.Load(), stderr)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().IsRegular() {
			t.Fatalf("socket prior: %v %v", info, err)
		}
		if strings.Contains(stderr, "could not drop the run record") {
			t.Fatalf("stderr %s", stderr)
		}
	})

	t.Run("symlink snapshot is not the outside record", func(t *testing.T) {
		stateDir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "prior.json")
		rec := histRec(2, outcomeOK)
		rec.Error = "symlink-snapshot-marker"
		raw, err := marshalRunRecord(rec)
		if err != nil {
			t.Fatal(err)
		}
		writeBytes(t, outside, raw)
		path := runRecordPath(stateDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		before := fileIdentOf(t, outside)
		stderr := restartJob(t, stateDir)
		sameIdent(t, outside, before)
		after, err := os.ReadFile(outside)
		if err != nil || !bytes.Equal(after, raw) {
			t.Fatalf("snapshot wrote the symlink target: %v %q", err, after)
		}
		leaf, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		// Target bytes are the proof the snapshot did not follow the link.
		// A regular leaf must be the replacement saveRunRecord wrote, not those bytes.
		if leaf.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil || bytes.Contains(body, []byte("symlink-snapshot-marker")) {
				t.Fatalf("restored the followed target into last.json: %q %v\n%s", body, err, stderr)
			}
		}
	})
}

func TestLastJSONAdversaryUnicodeStateDir(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "naïve-履歴書")
	path := runRecordPath(stateDir)
	raw, err := marshalRunRecord(histRec(1, outcomeOK))
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, path, raw)
	got, err := readRunRecordFile(path)
	if err != nil || got == nil || runID(*got) != runID(histRec(1, outcomeOK)) || got.Outcome != outcomeOK {
		t.Fatalf("unicode regular record %+v %v", got, err)
	}

	fifoDir := filepath.Join(t.TempDir(), "履歴書-🔥")
	fifo := runRecordPath(fifoDir)
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	before := fileIdentOf(t, fifo)
	opened := holdFIFOWriter(t, fifo)
	assertNotRegularRecord(t, fifo)
	assertUnopened(t, fifo, before, opened)

	// The restart path has to use the same unicode state dir for the snapshot
	// and the restore, not a cleaned ASCII form of it.
	prior := []byte(`{"v":1,"started_at":"2026-10-22T09:01:00.123Z","pid":4001,"outcome":"ok","error":"unicode-prior"} `)
	writeBytes(t, path, prior)
	stderr := restartJob(t, stateDir)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, prior) {
		t.Fatalf("unicode restore %q %v\n%s", after, err, stderr)
	}
}

// TestLastJSONAdversaryDeviceNode reads a character device the process can
// already stat. Creating one with mknod is not permitted in this environment,
// and /dev/null takes the same non-regular branch as a device named last.json.
func TestLastJSONAdversaryDeviceNode(t *testing.T) {
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		t.Run(path, func(t *testing.T) {
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeCharDevice == 0 || info.Mode().IsRegular() {
				t.Fatalf("%s mode %v, want a character device", path, info.Mode())
			}
			before := fileIdentOf(t, path)
			assertNotRegularRecord(t, path)
			sameIdent(t, path, before)
		})
	}
}
