package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
)

// SIGINT while the run waits for another run's lock: the signal context is
// created before the lock is taken, so the wait ends at once with a clear
// message, nothing is asked and the corrupt store is left alone.
func TestKBMatchInterruptDuringLockWait(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core"})}
	useFake(t, f)
	oldWait := kbmatchLockWait
	t.Cleanup(func() { kbmatchLockWait = oldWait })
	kbmatchLockWait = time.Minute

	path := filepath.Join(k.state, kbmatch.FileName)
	if err := os.MkdirAll(k.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	hold, err := kbmatch.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	// The stand-in for SIGINT: the context the command is given ends.
	ctx, cancel := context.WithCancel(context.Background())
	old := signalContext
	t.Cleanup(func() { signalContext = old })
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	code, _, e := do(t, "kbmatch", "run")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the run waited %s after the interrupt", d)
	}
	if code == 0 || !strings.Contains(e, "interrupted while waiting") {
		t.Errorf("want an interrupted message and a failing exit: %d, %s", code, e)
	}
	if strings.Contains(e, "another run is using the store") {
		t.Errorf("an interrupt was reported as a busy store: %s", e)
	}
	if len(f.contexts) != 0 {
		t.Errorf("an interrupted run asked the model")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the corrupt store was removed without the lock: %v", err)
	}
}

// A dry run on a corrupt store restarts nothing, so it must not say it does.
func TestKBMatchDryRunCorruptStoreWording(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(map[string]string{})})
	path := filepath.Join(k.state, kbmatch.FileName)
	if err := os.MkdirAll(k.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, e := mustCode(t, 0, "kbmatch", "run", "--dry-run")
	if !strings.Contains(e, "a real run would start it again") {
		t.Errorf("dry run wording: %s", e)
	}
	if strings.Contains(e, "; starting it again") {
		t.Errorf("a dry run claimed an action: %s", e)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "{not json" {
		t.Errorf("a dry run touched the store: %q %v", raw, err)
	}
}
