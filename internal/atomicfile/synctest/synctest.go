// Package synctest lets a test count, fail or force the fsyncs Vani's stores
// make through atomicfile.Sync and atomicfile.SyncDir, scoped to one directory
// so parallel tests never share a count. Only tests import it.
package synctest

import (
	"slices"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// Log records the syncs under one directory.
type Log struct {
	mu    sync.Mutex
	files []string
	dirs  []string
	fail  func(path string, dir bool) error
}

// Record starts recording every sync under dir until the test ends. The syncs
// still happen as they would without it (skipped under atomicfile.RunWithoutSync,
// real otherwise), unless Fail says to fail them.
func Record(t testing.TB, dir string) *Log {
	t.Helper()
	l := &Log{}
	t.Cleanup(atomicfile.ObserveSyncForTest(dir, false, l.see))
	return l
}

func (l *Log) see(path string, dir bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if dir {
		l.dirs = append(l.dirs, path)
	} else {
		l.files = append(l.files, path)
	}
	if l.fail != nil {
		return l.fail(path, dir)
	}
	return nil
}

// Fail makes every later sync under the directory return fn's error in place
// of syncing (nil from fn lets that sync go ahead). Fail(nil) stops failing.
func (l *Log) Fail(fn func(path string, dir bool) error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail = fn
}

// Files is the file syncs so far, by file name, in order.
func (l *Log) Files() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.files)
}

// Dirs is the directory syncs so far, in order.
func (l *Log) Dirs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.dirs)
}

// Reset forgets the syncs recorded so far.
func (l *Log) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files, l.dirs = nil, nil
}

// Real makes every sync under dir a real fsync until the test ends, even
// under atomicfile.RunWithoutSync: the test of a store's real durability path.
func Real(t testing.TB, dir string) {
	t.Helper()
	t.Cleanup(atomicfile.ObserveSyncForTest(dir, true, nil))
}
