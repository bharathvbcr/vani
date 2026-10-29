package suggest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// A lock held by another process never blocks or fails the command for long: the
// record is skipped after at most outcomeLockWait and the caller warns.
func TestOutcomeAppendSkipsTheRecordWhenTheLockStaysHeld(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the lock bound")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, OutcomeDir), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := filelock.TryAcquire(filepath.Join(dir, OutcomeDir, OutcomeFile+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	err = AppendOutcome(dir, FailureOutcome(OpSuggest, time.Now(), "provider"))
	took := time.Since(t0)
	if err == nil || !errors.Is(err, filelock.ErrWouldBlock) {
		t.Fatalf("a held lock gave %v, want an error wrapping ErrWouldBlock", err)
	}
	if took < time.Second || took > 4*time.Second {
		t.Errorf("waited %s, want about %s", took, outcomeLockWait)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, OutcomeDir, OutcomeFile)); len(b) != 0 {
		t.Errorf("a record was written without the lock: %q", b)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := AppendOutcome(dir, FailureOutcome(OpSuggest, time.Now(), "provider")); err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
	if outs, bad := readOutcomes(t, dir); len(outs) != 1 || bad != 0 {
		t.Errorf("%d records, %d broken", len(outs), bad)
	}
}
