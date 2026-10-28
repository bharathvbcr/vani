//go:build !windows

package marks

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func fifoStore(t *testing.T) string {
	t.Helper()
	dir := Dir(t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, fileName), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	return dir
}

func TestReadDoesNotBlockOnFIFO(t *testing.T) {
	dir := fifoStore(t)
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
	case <-time.After(800 * time.Millisecond):
		t.Fatal("Read blocked on a FIFO")
	}
	st, err := os.Lstat(filepath.Join(dir, fileName))
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("leaf = %v %v, want the FIFO", st, err)
	}
}

func TestCompactDoesNotBlockOnFIFO(t *testing.T) {
	dir := fifoStore(t)
	done := make(chan error, 1)
	go func() { done <- Compact(dir) }()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Fatalf("Compact = %v, want ErrNotRegular", err)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("Compact blocked on a FIFO")
	}
	st, err := os.Lstat(filepath.Join(dir, fileName))
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("leaf = %v %v, want the FIFO", st, err)
	}
}
