//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// A config path replaced by a FIFO must not hang the run (the daemon holds the
// ledger mutex while it reloads).
func TestAdvConfigAsFIFODoesNotHangTheRun(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(cfgPath, 0o600); err != nil {
		t.Skip("no fifo:", err)
	}
	app := &App{Reload: func(config.Config) {}}
	done := make(chan error, 1)
	go func() { _, err := e.reloadConfig(app, start, nil); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errConfigStale) {
			t.Fatalf("fifo config: %v, want errConfigStale", err)
		}
	case <-time.After(2 * time.Second):
		// Unblock the stuck open so the goroutine ends, then fail.
		if f, err := os.OpenFile(cfgPath, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		<-done
		t.Fatal("reloadConfig blocked on a FIFO config path; a daemon would hang holding the ledger mutex")
	}
}
