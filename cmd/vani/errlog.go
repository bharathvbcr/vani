package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// The daemon's stderr goes to <state dir>/logs/vani.err.log, a file launchd
// opens for append and never trims. Every reload (a run, an inbox poll) checks
// its size, and a file past errLogMax is cut to its last errLogKeep bytes,
// starting at a line boundary.
const (
	errLogMax  = 4 << 20
	errLogKeep = 512 << 10
)

// deprecationSeen remembers, per env, the deprecated-key warning last printed
// (warnDeprecatedOnce), so a daemon says it once and not on every poll.
var deprecationSeen sync.Map

// warnDeprecatedOnce is warnDeprecated for a config that is re-read over and
// over: it prints the warnings again only when the set of deprecated keys
// differs from the last one printed (a key renamed away and used again counts
// as a change). A config with none forgets the last set.
func (e *env) warnDeprecatedOnce(old []string) {
	sig := e.configPath + "\n" + strings.Join(old, "\n")
	if len(old) == 0 {
		deprecationSeen.Delete(e)
		return
	}
	if prev, ok := deprecationSeen.Load(e); ok && prev == sig {
		return
	}
	deprecationSeen.Store(e, sig)
	e.warnDeprecated(old)
}

// trimErrLog keeps logs/vani.err.log under errLogMax. It follows no symlink and
// trims nothing it cannot read; a failure is said on stderr, never fatal. The
// file is rewritten in place because launchd holds it open for append: a line
// written during the rewrite can be lost, which is the price of a bounded log.
func (e *env) trimErrLog() {
	path := filepath.Join(e.stateDir, "logs", "vani.err.log")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= errLogMax {
		return
	}
	lock, err := filelock.TryAcquire(path + ".trim.lock")
	if errors.Is(err, filelock.ErrWouldBlock) {
		return // another vani is trimming it now
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: could not trim %s: %v\n", path, err)
		return
	}
	defer lock.Release()
	if err := trimTail(path, info.Size(), errLogKeep); err != nil {
		fmt.Fprintf(e.stderr, "vani: could not trim %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(e.stderr, "vani: trimmed %s from %d bytes to its last %d\n", path, info.Size(), errLogKeep)
}

// trimTail cuts the file at path to its last keep bytes, from a line boundary.
// seen is the size the caller's stat reported; it is never used for the
// arithmetic, because another vani (daemon and CLI both reload) may have
// trimmed the file since. The size comes from the opened file, a file no longer
// over errLogMax is left alone, and a read that returns fewer bytes than asked
// means the file changed underneath: nothing is written then.
func trimTail(path string, seen, keep int64) error {
	_ = seen
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if !st.Mode().IsRegular() || size <= errLogMax || size < keep {
		return nil
	}
	tail := make([]byte, keep)
	n, err := f.ReadAt(tail, size-keep)
	if int64(n) != keep {
		if err != nil && err != io.EOF {
			return err
		}
		return nil
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	if _, err := f.WriteAt(tail, 0); err != nil {
		return err
	}
	return f.Truncate(int64(len(tail)))
}
