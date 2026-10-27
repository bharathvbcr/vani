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

// A FIFO at impressions.jsonl must not block the append or take a row.
func TestAppendRefusesFIFOImpressionsLog(t *testing.T) {
	state := t.TempDir()
	dir := Dir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, ImpressionsFile)
	if err := syscall.Mkfifo(live, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := AppendImpressions(context.Background(), state, []Impression{imp("gh:a:1", 1)})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrNotRegular) {
			t.Fatalf("AppendImpressions = %v, want ErrNotRegular", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("append blocked on a FIFO")
	}
	st, err := os.Lstat(live)
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("leaf = %v %v, want the FIFO", st, err)
	}
}
