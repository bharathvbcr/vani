//go:build !windows

package manualtriage

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func TestReadRefusesFIFOWithoutBlocking(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(file(dir), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Read(dir)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Fatalf("Read = %v, want ErrNotRegular", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Read blocked on a FIFO")
	}
	st, err := os.Lstat(file(dir))
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("leaf = %v %v, want the FIFO", st, err)
	}
}
