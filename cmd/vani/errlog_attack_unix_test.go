//go:build unix

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// A read-only log and a FIFO never stop the reload and never hang it.
func TestAttackTrimReadOnlyAndFIFO(t *testing.T) {
	e, _, start, stderr := advEnv(t)
	p := attackLogPath(t, e)
	attackFill(t, p, errLogMax+5000, true)
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr.String(), "could not trim") {
			t.Errorf("read-only failure not said: %q", stderr)
		}
		if st, _ := os.Stat(p); st.Size() != errLogMax+5000 {
			t.Error("read-only log was modified")
		}
	}
	os.Remove(p)
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
}
