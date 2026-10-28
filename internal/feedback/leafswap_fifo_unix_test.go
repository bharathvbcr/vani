//go:build !windows

package feedback

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func TestRefuseLeafSwapFIFOReturnsWithinBound(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, ImpressionsFile)
	f, created, err := openImpression(live)
	if err != nil || !created {
		t.Fatalf("open created=%v err=%v", created, err)
	}
	opened, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(live, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() { done <- refuseLeafSwap(live, opened) }()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Fatalf("refuseLeafSwap = %v, want ErrNotRegular", err)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("refuseLeafSwap blocked on a FIFO")
	}
	if _, err := os.Lstat(filepath.Join(dir, RotatedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation moved the FIFO: %v", err)
	}
}
