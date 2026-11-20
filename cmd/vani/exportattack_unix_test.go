//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// --out through a symlinked directory writes to the real directory; onto a
// directory or a FIFO it fails without hanging and replaces nothing.
func TestExportOutSymlinkDirDirectoryAndFIFO(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "x"})
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "export", "heldout", "--out", filepath.Join(link, "o.jsonl"))
	if _, err := os.Stat(filepath.Join(real, "o.jsonl")); err != nil {
		t.Errorf("not written through the linked directory: %v", err)
	}

	dir := filepath.Join(root, "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := do(t, "export", "heldout", "--out", dir); code == 0 {
		t.Error("export onto a directory exited 0")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("directory replaced: %v %v", st, err)
	}

	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan int, 1)
	go func() { c, _, _ := do(t, "export", "heldout", "--out", fifo); done <- c }()
	select {
	case c := <-done:
		if c == 0 {
			t.Error("export onto a FIFO exited 0")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("export onto a FIFO hung")
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("FIFO replaced: %v %v", st, err)
	}
}
