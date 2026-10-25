package main

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// A daemon keeps executing the image it started from. Reinstalling the app
// replaces the file at the daemon's path (the old copy is deleted and a new
// one written, so a new inode), but the running process still maps the old
// one: it then rejects a config key the new binary added and writes scores
// under a profile hash nothing else reads. binWatch is how the daemon notices
// that its own file was replaced, so it can exit exitConfigRestart between
// runs and let its supervisor start the new one. It is checked where the
// config-restart decision is made (reloadConfig), never mid-run.

// binIdentity names one file on disk: the device and inode where the platform
// has them, plus size and modification time so an overwrite in place (same
// inode, new bytes) differs too.
type binIdentity struct {
	dev, ino uint64
	size     int64
	mtime    int64 // UnixNano
}

// executablePath and statExecutable are the daemon's two reads of the file
// system for this check; tests replace them.
//
// The path is os.Executable's, kept as given and never resolved through
// symlinks, and statExecutable follows symlinks (os.Stat, not Lstat). So the
// identity is that of whatever the path runs now: a reinstall that deletes and
// rewrites a regular file (the app bundle's copy, put there by ditto) and one
// that repoints a symlink (a Homebrew-style link) both change it. Resolving
// the link once at start would pin the old target and miss a repoint. On Linux
// os.Executable reads /proc/self/exe and reports a deleted binary as
// "<path> (deleted)", which does not stat: the check then stays quiet, never
// wrongly restarting. The daemon is supervised on macOS.
var (
	executablePath = os.Executable
	statExecutable = func(path string) (binIdentity, error) {
		fi, err := os.Stat(path)
		if err != nil {
			return binIdentity{}, err
		}
		if !fi.Mode().IsRegular() {
			return binIdentity{}, fmt.Errorf("%s is not a regular file", path)
		}
		id := binIdentity{size: fi.Size(), mtime: fi.ModTime().UnixNano()}
		id.dev, id.ino = devIno(fi)
		return id, nil
	}
)

// binWatch is the identity the daemon started with. A nil *binWatch is a check
// that could not be set up; replaced is false on it.
type binWatch struct {
	path  string
	start binIdentity

	mu   sync.Mutex
	said bool
}

// watchOwnBinary records the running executable's identity. When it cannot,
// it says so once on stderr and returns nil: the daemon then never restarts
// for a replaced binary, and the log shows that the check was off rather than
// passing.
func watchOwnBinary(stderr io.Writer) *binWatch {
	path, err := executablePath()
	if err == nil {
		var id binIdentity
		if id, err = statExecutable(path); err == nil {
			return &binWatch{path: path, start: id}
		}
	}
	fmt.Fprintf(stderr, "vani: daemon: could not identify its own binary (%s); it will not restart when the binary is replaced\n",
		oneLine(err.Error(), 200))
	return nil
}

// replaced reports whether the file at the daemon's path is now a different
// file from the one it started from, and says so once on stderr. A path that
// is missing or cannot be read is not a replacement: an install in progress
// deletes the old copy first, so the next check decides. It costs one stat.
func (w *binWatch) replaced(stderr io.Writer) bool {
	if w == nil {
		return false
	}
	cur, err := statExecutable(w.path)
	if err != nil || cur == w.start {
		return false
	}
	w.mu.Lock()
	first := !w.said
	w.said = true
	w.mu.Unlock()
	if first {
		fmt.Fprintln(stderr, "vani: daemon: the vani binary was replaced on disk; restarting so the new one runs")
	}
	return true
}
