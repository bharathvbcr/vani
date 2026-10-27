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

func TestOpenReadAndAppendRefuseFIFOWithinBound(t *testing.T) {
	root := evalRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	calls := map[string]func() error{
		"OpenRead": func() error {
			f, err := atomicfile.OpenRead(fifo)
			if f != nil {
				_ = f.Close()
			}
			return err
		},
		"OpenAppend": func() error {
			f, _, err := atomicfile.OpenAppend(fifo, 0)
			if f != nil {
				_ = f.Close()
			}
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- call() }()
			select {
			case err := <-done:
				if !errors.Is(err, atomicfile.ErrNotRegular) {
					t.Fatalf("%s = %v, want ErrNotRegular", name, err)
				}
			case <-time.After(800 * time.Millisecond):
				t.Fatalf("%s blocked on a FIFO", name)
			}
		})
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO leaf = %v %v", st, err)
	}
}

func TestOpenReadSymlinkToFIFODoesNotOpenIt(t *testing.T) {
	root := evalRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	link := filepath.Join(root, "log")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := atomicfile.OpenRead(link)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
			t.Fatalf("OpenRead = %v, want ErrLinkedLeaf", err)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("OpenRead blocked on a symlink to a FIFO")
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO = %v %v", st, err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link = %v %v", st, err)
	}

	done = make(chan error, 1)
	go func() {
		f, _, err := atomicfile.OpenAppend(link, 0)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
			t.Fatalf("OpenAppend = %v, want ErrLinkedLeaf", err)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("OpenAppend blocked on a symlink to a FIFO")
	}
}

func TestOpenReadFIFOAsDirectoryComponentDoesNotBlock(t *testing.T) {
	root := evalRoot(t)
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := atomicfile.OpenRead(filepath.Join(fifo, "leaf"))
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a leaf under a FIFO")
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("OpenRead blocked walking through a FIFO")
	}
}
