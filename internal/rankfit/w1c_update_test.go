package rankfit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

func w1cNoCall(t *testing.T) func(*File, bool) error {
	return func(*File, bool) error {
		t.Error("fn ran on a file Update should have refused")
		return nil
	}
}

// With no file Update writes exactly what Write writes for the same File.
func TestW1cUpdateWithNoFileMatchesWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want, got := filepath.Join(dir, "a", "w.json"), filepath.Join(dir, "b", "w.json")
	if err := Write(want, goodFile()); err != nil {
		t.Fatal(err)
	}
	err := Update(context.Background(), got, func(cur *File, exists bool) error {
		if exists {
			t.Error("exists on a missing file")
		}
		*cur = goodFile()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(want)
	b, _ := os.ReadFile(got)
	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Errorf("Update wrote\n%s\nWrite wrote\n%s", b, a)
	}
}

// A file that is empty, truncated, unknown-fielded or invalid is refused and
// left byte for byte as it was.
func TestW1cUpdateRefusesDefectiveFileAndLeavesIt(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty":     "",
		"truncated": `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","shares":{"fit":0.`,
		"unknown":   `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","spare_seniority_mismatch":["mid"],"extra":1}`,
		"version":   `{"v":9,"fitted_at":"2026-10-20T12:00:00Z","spare_seniority_mismatch":["mid"]}`,
		"trailing":  `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","spare_seniority_mismatch":["mid"]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "w.json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			err := Update(context.Background(), path, w1cNoCall(t))
			if err == nil {
				t.Fatal("a defective file was accepted")
			}
			if got, _ := os.ReadFile(path); string(got) != body {
				t.Errorf("file changed to %q", got)
			}
		})
	}
}

func TestW1cUpdateFnErrorOrInvalidResultWritesNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "w.json")
	if err := Write(path, goodFile()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	boom := errors.New("boom")
	if err := Update(context.Background(), path, func(*File, bool) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("err %v, want fn's", err)
	}
	if err := Update(context.Background(), path, func(cur *File, _ bool) error {
		cur.Shares, cur.Calibration = nil, nil
		return nil
	}); err == nil {
		t.Error("a file with nothing in it was written")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("file changed:\n%s", after)
	}
}

// A context cancelled while another writer holds the lock gives up with the
// context's error and writes nothing.
func TestW1cUpdateCancelledWhileLocked(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "w.json")
	if err := Write(path, goodFile()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	held, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err = Update(ctx, path, w1cNoCall(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want the context's", err)
	}
	cctx, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := Update(cctx, path, w1cNoCall(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("file changed")
	}
}

// Writers that each add their own backend, started together, all survive.
func TestW1cUpdateConcurrentWritersKeepEveryPart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "w.json")
	f := goodFile()
	f.Calibration = nil
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Update(context.Background(), path, func(cur *File, _ bool) error {
				if cur.Calibration == nil {
					cur.Calibration = map[string]jobinsight.Calibration{}
				}
				cur.Calibration["b"+strconv.Itoa(i)] = jobinsight.Calibration{B: float64(i)}
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Calibration) != n || got.Shares == nil {
		t.Errorf("%d of %d calibrations survived, shares %v", len(got.Calibration), n, got.Shares)
	}
}

const w1cChildEnv = "W1C_RANKFIT_UPDATE_CHILD"

// TestW1cUpdateChild is the child of the two-process test; it is a no-op unless
// the parent re-executes the test binary with the environment set.
func TestW1cUpdateChild(t *testing.T) {
	spec := os.Getenv(w1cChildEnv)
	if spec == "" {
		t.Skip("child role only")
	}
	path, id := filepath.Join(spec, "w.json"), os.Getenv(w1cChildEnv+"_ID")
	for i := range 5 {
		err := Update(context.Background(), path, func(cur *File, _ bool) error {
			if cur.Calibration == nil {
				cur.Calibration = map[string]jobinsight.Calibration{}
			}
			cur.Calibration[fmt.Sprintf("p%s-%d", id, i)] = jobinsight.Calibration{B: 1}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestW1cUpdateTwoProcessesKeepEveryPart(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "w.json"), goodFile()); err != nil {
		t.Fatal(err)
	}
	const procs = 4
	cmds := make([]*exec.Cmd, procs)
	outs := make([]bytes.Buffer, procs)
	for i := range procs {
		c := exec.Command(os.Args[0], "-test.run=^TestW1cUpdateChild$", "-test.timeout=60s")
		c.Env = append(os.Environ(), w1cChildEnv+"="+dir, w1cChildEnv+"_ID="+strconv.Itoa(i))
		c.Stdout, c.Stderr = &outs[i], &outs[i]
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = c
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outs[i].String())
		}
	}
	got, err := Load(filepath.Join(dir, "w.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := 2 + procs*5; len(got.Calibration) != want { // goodFile's two backends
		t.Errorf("%d calibrations, want %d: a write was lost", len(got.Calibration), want)
	}
	if got.Shares == nil {
		t.Error("shares lost")
	}
}
