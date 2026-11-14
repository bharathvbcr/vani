package atomicfile

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Sync fsyncs f (F_FULLFSYNC on darwin, about 8 ms a call on the owner's Mac
// on 2026-11-14). Every durable write in Vani goes through Sync or SyncDir so
// that tests can count the syncs, fail one, or skip them (SetSyncForTest,
// ObserveSyncForTest, RunWithoutSync); a run never sets any of those, and then
// Sync is f.Sync.
func Sync(f *os.File) error {
	return syncVia(f.Name(), false, f.Sync)
}

// SyncDir fsyncs dir, making a rename, link or create inside it durable: a
// file's own fsync does not cover the directory entry that names it.
func SyncDir(dir string) error {
	return syncVia(dir, true, func() error { return syncDirReal(dir) })
}

// syncObserver is one test's view of the syncs under its directories.
type syncObserver struct {
	under []string // the directory as given and as resolved (t.TempDir is under a link on macOS)
	real  bool     // sync for real even under RunWithoutSync
	fn    func(path string, dir bool) error
}

var (
	syncMu   sync.RWMutex
	syncBase func(path string, dir bool) error // nil in a run: the real fsync
	syncObs  []*syncObserver
)

func syncVia(path string, dir bool, real func() error) error {
	syncMu.RLock()
	base, obs := syncBase, observerFor(path)
	syncMu.RUnlock()
	if obs != nil {
		if obs.fn != nil {
			if err := obs.fn(path, dir); err != nil {
				return err
			}
		}
		if obs.real {
			return real()
		}
	}
	if base != nil {
		return base(path, dir)
	}
	return real()
}

// observerFor is the observer with the longest directory containing path.
// Caller holds syncMu.
func observerFor(path string) *syncObserver {
	var best *syncObserver
	bestLen := -1
	for _, o := range syncObs {
		for _, u := range o.under {
			if within(path, u) && len(u) > bestLen {
				best, bestLen = o, len(u)
			}
		}
	}
	return best
}

// within reports whether path is dir or lies under it (a sibling that only
// shares a name prefix is not under it).
func within(path, dir string) bool {
	if path == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(path, dir)
}

// SetSyncForTest replaces every sync not claimed by an observer with fn and
// returns the function that puts the previous one back. It is for tests only;
// nothing in a run calls it.
func SetSyncForTest(fn func(path string, dir bool) error) (restore func()) {
	syncMu.Lock()
	old := syncBase
	syncBase = fn
	syncMu.Unlock()
	return func() {
		syncMu.Lock()
		syncBase = old
		syncMu.Unlock()
	}
}

// ObserveSyncForTest calls fn with every sync of a file or directory under dir
// (the deepest observing directory wins) and returns the function that stops
// it. A non-nil error from fn is returned in place of the sync. Otherwise the
// sync goes ahead: for real when real is set, else as it would without the
// observer (skipped under RunWithoutSync). It is for tests only, through the
// synctest package; nothing in a run calls it.
func ObserveSyncForTest(dir string, real bool, fn func(path string, dir bool) error) (stop func()) {
	o := &syncObserver{under: []string{filepath.Clean(dir)}, real: real, fn: fn}
	if r := resolveExisting(o.under[0]); r != o.under[0] {
		o.under = append(o.under, r)
	}
	syncMu.Lock()
	syncObs = append(syncObs, o)
	syncMu.Unlock()
	return func() {
		syncMu.Lock()
		defer syncMu.Unlock()
		for i, x := range syncObs {
			if x == o {
				syncObs = append(syncObs[:i], syncObs[i+1:]...)
				return
			}
		}
	}
}

// resolveExisting resolves the links in the deepest existing ancestor of dir
// and keeps the rest as given, so a directory a test observes before a store
// creates it still matches the resolved paths the store syncs.
func resolveExisting(dir string) string {
	rest := ""
	for p := dir; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return dir
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// RunWithoutSync runs a test binary (a *testing.M) with every fsync skipped,
// except under a directory a test observes with real set, and returns m.Run's
// exit code for os.Exit. It is for the TestMain of a package whose tests write
// thousands of durable lines: at ~8 ms a sync they spent minutes in the drive
// cache (pipeline's TestDeathRuleBoundsFuzz 158.8 s, 15.0 s without the ledger's
// syncs). The packages' sync-count tests still see every sync through
// ObserveSyncForTest; one real round trip per store keeps the real path tested.
//
// A run with -test.bench set keeps every real fsync: a benchmark measures what
// a run pays, and the committed ledger and reconcile benchmarks include the
// syncs.
func RunWithoutSync(m interface{ Run() int }) int {
	if benchmarking() {
		return m.Run()
	}
	restore := SetSyncForTest(func(string, bool) error { return nil })
	defer restore()
	return m.Run()
}

// benchmarking reports whether this test binary was asked to run benchmarks.
// TestMain runs before the testing flags are parsed, so it parses them (as
// TestMain may) when nothing has yet.
func benchmarking() bool {
	f := flag.Lookup("test.bench")
	if f == nil {
		return false
	}
	if !flag.Parsed() {
		flag.Parse()
	}
	return f.Value.String() != ""
}
