package main

import (
	"bytes"
	"os"
	"sync"
	"testing"
)

// A trim whose size came from an earlier stat, run after another process
// already trimmed the file (daemon + CLI both saw >4 MiB), must not write NULs.
func TestAttackStaleSizeTrimWritesNULs(t *testing.T) {
	e, _, _, _ := advEnv(t)
	p := attackLogPath(t, e)
	attackFill(t, p, errLogKeep+100, true) // already trimmed by the first process
	if err := trimTail(p, errLogMax+4096, errLogKeep); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if bytes.IndexByte(got, 0) >= 0 {
		n := bytes.Count(got, []byte{0})
		t.Fatalf("log now holds %d NUL bytes of %d: the stale trim overwrote the log", n, len(got))
	}
}

// Two trims of one oversized log at once (daemon + CLI): the file ends whole,
// within the limit, holding only log bytes.
func TestAttackConcurrentTrimsNeverWriteNULs(t *testing.T) {
	e, _, _, _ := advEnv(t)
	p := attackLogPath(t, e)
	for round := 0; round < 5; round++ {
		attackFill(t, p, errLogMax+5000, true)
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				own := &env{configPath: e.configPath, stateDir: e.stateDir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
				own.trimErrLog()
			}()
		}
		wg.Wait()
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(got, 0) >= 0 {
			t.Fatalf("round %d: log holds NUL bytes", round)
		}
		if len(got) > errLogMax || len(got) == 0 || len(got) > errLogKeep {
			t.Fatalf("round %d: log is %d bytes", round, len(got))
		}
	}
}

// Even with the lock out of the way, trimTail itself is idempotent: racing
// calls on one file never leave NULs.
func TestAttackRawConcurrentTrimTailNeverWritesNULs(t *testing.T) {
	e, _, _, _ := advEnv(t)
	p := attackLogPath(t, e)
	attackFill(t, p, errLogMax+5000, true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = trimTail(p, errLogMax+5000, errLogKeep)
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(p)
	if bytes.IndexByte(got, 0) >= 0 {
		t.Fatal("NULs after racing raw trims")
	}
}
