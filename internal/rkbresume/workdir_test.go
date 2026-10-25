package rkbresume

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkStale makes an old build folder holding a file, and returns its path.
func mkStale(t *testing.T, work, name string) string {
	t.Helper()
	p := filepath.Join(work, name)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "page.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	chtimesAgo(t, p, MaxDeadline+time.Hour)
	return p
}

// A work folder with more entries than the cap: one sweep looks at no more than
// maxSweep of them, removes only stale prefixed folders among those, never
// follows the symlink or removes the unrelated folder, and terminates. The next
// sweep makes progress on what the first left, so the backlog drains.
func TestSweepStopsAtTheCapAndNeverTouchesWhatItShouldNot(t *testing.T) {
	work := t.TempDir()
	const stale = maxSweep + 50
	for i := 0; i < stale; i++ {
		mkStale(t, work, fmt.Sprintf("%s%04d", buildDirPrefix, i))
	}
	unrelated := mkStale(t, work, "something-else")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	chtimesAgo(t, outside, 24*time.Hour)
	link := filepath.Join(work, buildDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		n := 0
		es, err := os.ReadDir(work)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.IsDir() && len(e.Name()) > len(buildDirPrefix) && e.Name()[:len(buildDirPrefix)] == buildDirPrefix && e.Name() != filepath.Base(link) {
				n++
			}
		}
		return n
	}

	done := make(chan int, 1)
	go func() { done <- sweepStale(work, MaxDeadline, time.Now()) }()
	var first int
	select {
	case first = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the sweep did not terminate")
	}

	// It looked at maxSweep entries; at most two of them (the unrelated folder
	// and the symlink) were not removable.
	if first > maxSweep || first < maxSweep-2 {
		t.Fatalf("the first sweep removed %d folders, want between %d and %d", first, maxSweep-2, maxSweep)
	}
	if left := count(); left != stale-first {
		t.Fatalf("%d stale folders left, want %d: the sweep removed something other than what it counted", left, stale-first)
	}
	for _, p := range []string{unrelated, keep} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was touched: %v", err)
	}

	// The backlog drains over the next sweeps; each terminates and removes
	// something until none is left.
	for i := 0; i < 5 && count() > 0; i++ {
		if n := sweepStale(work, MaxDeadline, time.Now()); n == 0 {
			t.Fatalf("sweep %d removed nothing with %d stale folders left", i+2, count())
		}
	}
	if left := count(); left != 0 {
		t.Errorf("%d stale folders survive repeated sweeps", left)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the symlink target was emptied: %v", err)
	}
}
