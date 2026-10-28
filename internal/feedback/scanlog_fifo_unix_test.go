//go:build !windows

package feedback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func TestReadImpressionsDoesNotBlockOnFIFO(t *testing.T) {
	state := t.TempDir()
	live := filepath.Join(Dir(state), ImpressionsFile)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(live, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadImpressions(state)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Fatalf("ReadImpressions = %v, want ErrNotRegular", err)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("ReadImpressions blocked on a FIFO")
	}
}

// A slate id forces scanLog before OpenAppend. A FIFO must not block that read.
func TestAppendSlateDoesNotBlockOnFIFO(t *testing.T) {
	state := t.TempDir()
	live := filepath.Join(Dir(state), ImpressionsFile)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(live, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := AppendImpressions(context.Background(), state, []Impression{slated("s1", SurfaceJobs, "gh:a:1", 1)})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) && !errors.Is(err, atomicfile.ErrLinkedLeaf) {
			t.Fatalf("AppendImpressions = %v, want ErrNotRegular", err)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("AppendImpressions blocked in scanLog on a FIFO")
	}
	st, err := os.Lstat(live)
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("leaf = %v %v, want the FIFO", st, err)
	}
}
