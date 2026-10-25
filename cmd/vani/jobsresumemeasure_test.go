package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// Measurements behind gap 31 ("`jobs resume show` runs one rkb claims export per
// call and, now, one per version: is it slow enough to cache?"). Neither test
// asserts a time (a clock on a shared machine is no assertion); both log the
// seconds of n runs, min first, and skip unless VANI_MEASURE=1 so the suite stays
// fast:
//
//	VANI_MEASURE=1 go test ./cmd/vani -run TestMeasure -v
//
// The second reads the owner's live config and bank read-only (rkb's temp folder is
// a temp dir, no state is touched), and needs VANI_MEASURE_CONFIG to name it.

const measureRuns = 7

func measureEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("VANI_MEASURE") != "1" {
		t.Skip("set VANI_MEASURE=1 to measure")
	}
}

// timings runs f measureRuns times and returns the seconds of each, ascending.
func timings(t *testing.T, f func()) []float64 {
	t.Helper()
	out := make([]float64, 0, measureRuns)
	for i := 0; i < measureRuns; i++ {
		start := time.Now()
		f()
		out = append(out, time.Since(start).Seconds())
	}
	slices.Sort(out)
	return out
}

func logTimes(t *testing.T, what string, ts []float64) {
	t.Helper()
	t.Logf("%-52s n=%d min %.3fs median %.3fs max %.3fs", what, len(ts), ts[0], ts[len(ts)/2], ts[len(ts)-1])
}

// With the fake rkb everything but rkb is measured: the ledger and store reads,
// the keywords, the eligibility check and the JSON.
func TestMeasureShowWithTheFakeRkb(t *testing.T) {
	measureEnabled(t)
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: os.Getenv("VANI_STATE_DIR"), stdout: io.Discard, stderr: io.Discard}
	ctx := context.Background()
	logTimes(t, "show, 2 versions, fake rkb (check)", timings(t, func() {
		if _, err := e.resumeShow(ctx, jrKey, "", true); err != nil {
			t.Fatal(err)
		}
	}))
	logTimes(t, "show, 2 versions, fake rkb (--no-check)", timings(t, func() {
		if _, err := e.resumeShow(ctx, jrKey, "", false); err != nil {
			t.Fatal(err)
		}
	}))
}

// The real rkb: the pool (one plain claims export) and, per version, one
// used-in export of that version's choice. The choice measured is the base
// selection's own, as the pool gives it, so no stored version is read.
func TestMeasurePoolAndVerifyWithTheRealRkb(t *testing.T) {
	measureEnabled(t)
	path := os.Getenv("VANI_MEASURE_CONFIG")
	if path == "" {
		t.Skip("set VANI_MEASURE_CONFIG to the config to measure against")
	}
	e := &env{configPath: path, stateDir: t.TempDir(), stdout: io.Discard, stderr: io.Discard}
	ctx := context.Background()
	w, err := e.resumeConfig(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	rc := w.RC
	rc.WorkDir, rc.LockDir = filepath.Join(e.stateDir, "work"), ""
	rc.Runner = nil // the real rkb
	view, err := rkbresume.Pool(ctx, rc, rkbresume.Request{})
	if err != nil {
		t.Fatalf("the real pool: %v", err)
	}
	logTimes(t, "pool, real rkb (one plain claims export)", timings(t, func() {
		if _, err := rkbresume.Pool(ctx, rc, rkbresume.Request{}); err != nil {
			t.Fatal(err)
		}
	}))
	for _, k := range []int{1, 5, 20} {
		edits := make([][]byte, k)
		for i := range edits {
			edits[i] = view.Base
		}
		logTimes(t, "pool + verify "+strconv.Itoa(k)+" version(s), real rkb", timings(t, func() {
			_, vs, err := rkbresume.PoolAndVerify(ctx, rc, edits)
			if err != nil {
				t.Fatal(err)
			}
			for i, v := range vs {
				if v.Err != "" {
					t.Fatalf("choice %d: %s", i, v.Err)
				}
			}
		}))
	}
}
