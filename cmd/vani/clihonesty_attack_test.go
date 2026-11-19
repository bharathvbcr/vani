package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// hostileHistory plants history entries that are not readable records beside
// one good entry; a reader that errors on any of them must report them, never
// drop them silently or abort the listing.
func hostileHistory(t *testing.T, stateDir string) (good string) {
	t.Helper()
	good = putHistoryRaw(t, stateDir, histRec(5, outcomeOK))
	dir := filepath.Join(stateDir, runRecordDir, runHistoryDir)
	if err := os.Mkdir(filepath.Join(dir, "20260101T000001.001Z-1.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, good+".json"), filepath.Join(dir, "20260101T000002.002Z-2.json")); err != nil {
		t.Fatal(err)
	}
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, "20260101T000003.003Z-3.json"), "{\"v\":1,\"outcome\":\"o")
	writeStateFile(t, stateDir, filepath.Join(runRecordDir, runHistoryDir, "20260101T000004.004Z-4.json"), strings.Repeat("x", maxRunRecord+10))
	return good
}

func TestAttackRunsListHostileEntriesAreProblemsNotSilentSkips(t *testing.T) {
	_, stateDir := setup(t)
	good := hostileHistory(t, stateDir)
	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	found := false
	for _, r := range l.Runs {
		if r.ID == good {
			found = true
		}
	}
	if !found {
		t.Errorf("the good entry vanished from the listing: %+v", l.Runs)
	}
	// Four hostile entries: each is a listed problem (none silently counted as read).
	if len(l.Problems) != 4 {
		t.Errorf("want 4 problems for 4 hostile entries, got %d: %+v", len(l.Problems), l.Problems)
	}
}

func TestAttackDoctorHostileHistoryDoesNotAbort(t *testing.T) {
	state, _ := doctorSetup(t)
	doctorSeams(t, &doctorFake{}, 0)
	hostileHistory(t, state)
	code, o, e := do(t, "doctor", "--dry-run")
	if code == 2 || strings.Contains(o+e, "panic") || strings.Contains(o+e, "nil pointer") {
		t.Fatalf("exit %d\n%s\n%s", code, o, e)
	}
}

func TestAttackRunsShowSymlinkedHistoryEntryRefused(t *testing.T) {
	_, stateDir := setup(t)
	good := putHistoryRaw(t, stateDir, histRec(5, outcomeOK))
	dir := filepath.Join(stateDir, runRecordDir, runHistoryDir)
	id := "20260101T000002.002Z-2"
	if err := os.Symlink(filepath.Join(dir, good+".json"), filepath.Join(dir, id+".json")); err != nil {
		t.Fatal(err)
	}
	code, o, e := do(t, "runs", "show", id)
	if code == 0 || code == 2 || !strings.Contains(o+e, "not a regular file") {
		t.Fatalf("exit %d: %s %s", code, o, e)
	}
}

// A listed history file replaced by a directory between listing and read is a
// problem (or a skip), never a nil dereference.
func TestAttackHistoryEntryReplacedByDirMidRead(t *testing.T) {
	_, stateDir := setup(t)
	id := putHistoryRaw(t, stateDir, histRec(1, outcomeOK))
	old := readHistoryRecord
	readHistoryRecord = func(path string) (*runRecord, error) {
		_ = os.Remove(path)
		_ = os.Mkdir(path, 0o700)
		return old(path)
	}
	t.Cleanup(func() { readHistoryRecord = old })
	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	if len(l.Problems) != 1 || l.Problems[0].ID != id {
		t.Errorf("want the replaced entry reported as a problem: %+v", l)
	}
}

// ---- rescore ----

// A rescore that ran and then failed is not "refused": its counts mean attempted.
func TestAttackRescoreFailedAfterPlanningIsNotRefused(t *testing.T) {
	l := rescoreLedger(t, rescoreMix)
	rep, err := rescoreWith(context.Background(), l, 10, 2, func(context.Context, posting.Key) error { return errors.New("devprism down") }, t.Logf)
	if err == nil {
		t.Fatal("want a failure")
	}
	if rep.Refused || rep.Plan.Eligible == 0 {
		t.Fatalf("a rescore that planned and attempted must not be Refused: %+v", rep)
	}
}

// Every refusal path of wired.rescore sets Refused.
func TestAttackWiredRescoreRefusalPathsSetRefused(t *testing.T) {
	l := rescoreLedger(t, rescoreMix)
	w := &wired{led: l, cfg: config.Config{}}
	rep, err := w.rescore(context.Background(), 5)
	if !errors.Is(err, errScoringOff) || !rep.Refused {
		t.Fatalf("scoring off: %+v %v", rep, err)
	}
}

// ---- semantic warm ----

func TestAttackWarmShortfallFailedWithSkipped(t *testing.T) {
	err := warmShortfall(semanticReport{Failed: 1, Skipped: 9})
	if err == nil || !strings.Contains(err.Error(), "failed to embed") {
		t.Fatalf("%v", err)
	}
	if code := exitCode("semantic", err); code != exitRuntime {
		t.Fatalf("exit %d, want %d (not usage)", code, exitRuntime)
	}
}

// ---- readEdit ----

type zeroStream struct{ n atomic.Int64 }

func (z *zeroStream) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	z.n.Add(int64(len(p)))
	return len(p), nil
}

func TestAttackReadEditEndlessStdinIsBounded(t *testing.T) {
	z := &zeroStream{}
	old := resumeInput
	resumeInput = z
	t.Cleanup(func() { resumeInput = old })
	_, e := readEdit(context.Background())
	if e == nil {
		t.Fatal("an endless stdin must be refused")
	}
	if got := z.n.Load(); got > int64(maxResumeEdit)+64<<10 {
		t.Errorf("read %d bytes, bound is %d", got, maxResumeEdit)
	}
}

type errStream struct{}

func (errStream) Read([]byte) (int, error) { return 0, errors.New("stdin closed by peer") }

func TestAttackReadEditStdinErrorIsBadRequestNotCancel(t *testing.T) {
	old := resumeInput
	resumeInput = errStream{}
	t.Cleanup(func() { resumeInput = old })
	_, e := readEdit(context.Background())
	if e == nil || e.code == rkbresume.CodeCancel {
		t.Fatalf("%+v", e)
	}
}

// Cancel racing EOF: whichever wins, no panic, no deadlock, and a success
// returns the whole edit.
func TestAttackReadEditCancelRacesEOF(t *testing.T) {
	old := resumeInput
	t.Cleanup(func() { resumeInput = old })
	for i := 0; i < 300; i++ {
		resumeInput = bytes.NewReader([]byte("{}"))
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		raw, e := readEdit(ctx)
		if e == nil && string(raw) != "{}" {
			t.Fatalf("partial edit %q", raw)
		}
		if e != nil && e.code != rkbresume.CodeCancel {
			t.Fatalf("%+v", e)
		}
	}
}

// The abandoned reader goroutine ends once stdin closes: it does not leak for
// the life of a long-running process.
func TestAttackReadEditAbandonedReaderEndsWhenStdinCloses(t *testing.T) {
	old := resumeInput
	t.Cleanup(func() { resumeInput = old })
	base := runtime.NumGoroutine()
	var writers []*io.PipeWriter
	for i := 0; i < 20; i++ {
		pr, pw := io.Pipe()
		writers = append(writers, pw)
		resumeInput = pr
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, e := readEdit(ctx); e == nil || e.code != rkbresume.CodeCancel {
			t.Fatalf("%+v", e)
		}
	}
	for _, pw := range writers {
		_ = pw.Close()
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > base+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base+2 {
		t.Errorf("%d goroutines, started with %d: abandoned readers leaked", n, base)
	}
}
