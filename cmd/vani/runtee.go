package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// maxRunLogBytes is how much of one run's own log is kept. Past it, further
// output is counted (log_dropped) and the run continues. The process log
// (vani.err.log) is separate and is not capped here.
const maxRunLogBytes = 1 << 20

// cappedTee copies what a run writes to stderr into an optional second file,
// up to a byte cap. The primary write is what the caller sees. A failure of
// the second file is counted and then ignored, so a full disk cannot fail a run.
type cappedTee struct {
	mu      sync.Mutex
	primary io.Writer
	extra   io.Writer
	remain  int
	dropped int
}

func newCappedTee(primary io.Writer) *cappedTee {
	if primary == nil {
		primary = io.Discard
	}
	return &cappedTee{primary: primary}
}

func (t *cappedTee) Write(p []byte) (int, error) {
	n, err := t.primary.Write(p)
	t.mu.Lock()
	t.writeExtra(p)
	t.mu.Unlock()
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// writeExtra holds t.mu. It keeps the prefix that still fits and counts lines
// in what it drops. A dropped write with no newline counts as one line.
func (t *cappedTee) writeExtra(p []byte) {
	if t.extra == nil || len(p) == 0 {
		return
	}
	take := len(p)
	if take > t.remain {
		take = t.remain
	}
	if take > 0 {
		if _, werr := t.extra.Write(p[:take]); werr != nil {
			t.noteDropped(p)
			t.extra = nil
			t.remain = 0
			return
		}
		t.remain -= take
	}
	if take < len(p) {
		t.noteDropped(p[take:])
	}
}

func (t *cappedTee) noteDropped(p []byte) {
	if len(p) == 0 {
		return
	}
	n := bytes.Count(p, []byte{'\n'})
	if n == 0 {
		n = 1
	}
	t.dropped += n
}

// setExtra sends later writes to w as well, up to limit bytes, and starts the
// dropped-line count over. w may be nil, which stops the copy.
func (t *cappedTee) setExtra(w io.Writer, limit int) {
	t.mu.Lock()
	t.extra = w
	t.remain = limit
	t.dropped = 0
	if limit < 0 {
		t.remain = 0
	}
	t.mu.Unlock()
}

// clearExtra stops the copy and returns how many lines were dropped.
func (t *cappedTee) clearExtra() int {
	t.mu.Lock()
	n := t.dropped
	t.extra = nil
	t.remain = 0
	t.dropped = 0
	t.mu.Unlock()
	return n
}

// openOwnedRunLog creates runs/history/<id>.log, mode 0600, after the same
// path check a VANI_RUN_LOG goes through. The directory is created first so
// that check can resolve it. A symlink at the name is refused and not written.
func openOwnedRunLog(stateDir, id string) (*os.File, string, error) {
	if !validRunID(id) {
		return nil, "", errors.New("run id is not a history name")
	}
	dir := filepath.Join(stateDir, runRecordDir, runHistoryDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, id+".log")
	if _, err := checkRunLog(stateDir, path); err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", err
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = errors.New("run log is not a regular file")
		}
		return nil, "", err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = errors.New("run log is not a regular file")
		}
		return nil, "", err
	}
	return f, path, nil
}
