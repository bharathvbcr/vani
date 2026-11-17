package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/indexutil"
)

func w4blockShorten(t *testing.T, notice, wait time.Duration) {
	t.Helper()
	oldN, oldW := indexutil.NoticeAfter, indexutil.LockWait
	indexutil.NoticeAfter, indexutil.LockWait = notice, wait
	t.Cleanup(func() { indexutil.NoticeAfter, indexutil.LockWait = oldN, oldW })
}

// w4blockResolved is the index path as the waiting line spells it.
func w4blockResolved(t *testing.T, path string) string {
	t.Helper()
	rp, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		t.Fatal(err)
	}
	return rp
}

// An import blocked by another one says so once on stderr, then fails busy with
// nothing on stdout.
func TestW4bLockImportsPrintWaitingLine(t *testing.T) {
	_, stateDir := setup(t)
	w4blockShorten(t, 30*time.Millisecond, 300*time.Millisecond)
	for _, tc := range []struct {
		kind, index string
		csv         string
	}{
		{"lca", lcaIndexPath(stateDir), writeCSV(t, "lca.csv", w4bguardCSV(2))},
		{"employer", employerIndexPath(stateDir), writeCSV(t, "hd2024.csv", testIPEDS)},
	} {
		_, release, err := indexutil.Lock(t.Context(), tc.index)
		if err != nil {
			t.Fatal(err)
		}
		o, e := mustCode(t, 1, tc.kind, "import", tc.csv)
		release()
		if n := strings.Count(e, "waiting for another import of "+w4blockResolved(t, tc.index)+" to finish"); n != 1 {
			t.Errorf("%s: %d waiting lines in stderr:\n%s", tc.kind, n, e)
		}
		if !strings.Contains(e, "busy") || o != "" {
			t.Errorf("%s: stderr %q stdout %q, want a busy failure", tc.kind, e, o)
		}
	}
}

// An import that is not blocked prints no waiting line.
func TestW4bLockUnblockedImportIsQuiet(t *testing.T) {
	setup(t)
	w4blockShorten(t, 30*time.Millisecond, time.Second)
	_, e := mustCode(t, 0, "lca", "import", writeCSV(t, "lca.csv", w4bguardCSV(2)))
	if strings.Contains(e, "waiting") {
		t.Fatalf("stderr: %q", e)
	}
}

// Ctrl-C while waiting stops the import at once, not after LockWait, and is not
// reported as busy.
func TestW4bLockImportWaitStopsOnInterrupt(t *testing.T) {
	_, stateDir := setup(t)
	w4blockShorten(t, time.Hour, time.Minute)
	_, release, err := indexutil.Lock(t.Context(), lcaIndexPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := signalContext
	t.Cleanup(func() { signalContext = old })
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		return ctx, cancel
	}
	start := time.Now()
	_, e := mustCode(t, 1, "lca", "import", writeCSV(t, "lca.csv", w4bguardCSV(2)))
	if time.Since(start) > 10*time.Second || strings.Contains(e, "busy") || !strings.Contains(e, "canceled") {
		t.Fatalf("after %v: stderr %q", time.Since(start), e)
	}
}
