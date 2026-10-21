//go:build !windows

package atomicfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// A FIFO at a program-named leaf is refused, never replaced and never opened
// (opening it would block): the pipe is someone's, not a data file Vani wrote.
func TestWriteRefusesFIFOLeaf(t *testing.T) {
	root := evalRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	for name, call := range map[string]func() error{
		"Write": func() error { return atomicfile.Write(fifo, []byte("x")) },
		"OpenAppend": func() error {
			f, _, err := atomicfile.OpenAppend(fifo, 0)
			if f != nil {
				_ = f.Close()
			}
			return err
		},
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if !errors.Is(err, atomicfile.ErrNotRegular) {
				t.Errorf("%s onto a FIFO = %v, want ErrNotRegular", name, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s hung on a FIFO", name)
		}
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO was replaced: %v %v", st, err)
	}
}

// A leaf link to a FIFO is just a leaf link: replaced by a regular file, the
// pipe untouched, nothing hangs.
func TestWriteOntoFIFOLinkReplacesLink(t *testing.T) {
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
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Write hung on a FIFO link")
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO behind the link was touched: %v %v", st, err)
	}
	if st, err := os.Lstat(link); err != nil || !st.Mode().IsRegular() {
		t.Errorf("link not replaced: %v %v", st, err)
	}
}
