//go:build !windows

package atomicfile_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// Write onto a link to a FIFO must not hang. It does not open the FIFO: it
// renames a temp file over the resolved path, replacing the pipe with a
// regular file.
func TestWriteOntoFIFOLinkDoesNotHang(t *testing.T) {
	root := evalRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	link := filepath.Join(root, "cfg.json")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- atomicfile.Write(link, []byte("x")) }()
	select {
	case err := <-done:
		st, e := os.Lstat(fifo)
		t.Logf("Write onto FIFO link returned %v; fifo still a pipe: %v", err, e == nil && st.Mode()&os.ModeNamedPipe != 0)
	case <-time.After(3 * time.Second):
		t.Fatal("Write hung on a FIFO")
	}
}
