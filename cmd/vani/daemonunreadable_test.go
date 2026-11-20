package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Text status says when daemon.json cannot be read, and why; it does not print
// what it prints for no daemon (a not-kept schedule), nor stay silent.
func TestTextStatusSaysWhenDaemonRecordIsUnreadable(t *testing.T) {
	for name, content := range map[string]string{
		"malformed": "{not json",
		"empty":     "",
		"array":     "[1,2]",
	} {
		t.Run(name, func(t *testing.T) {
			f := keptSetup(t, 750, 10)
			writeStateFile(t, f.stateDir, "runs/daemon.json", content)
			o, _ := mustCode(t, 0, "status")
			if !strings.Contains(o, "daemon: daemon.json could not be read") || !strings.Contains(o, "unknown") {
				t.Errorf("text status does not say daemon.json could not be read:\n%s", o)
			}
			if !strings.Contains(o, "daemon.json") || strings.Contains(o, "not kept") {
				t.Errorf("unreadable record printed as no daemon, or without the file:\n%s", o)
			}
			// The same facet --json reports.
			if s := statusOf(t); s.Daemon.OK || s.Daemon.Reason == "" || !strings.Contains(o, s.Daemon.Reason) {
				t.Errorf("text and json disagree: json %+v\ntext:\n%s", s.Daemon, o)
			}
		})
	}
}

// A record that is a directory (unreadable as a file) is reported the same way.
func TestTextStatusSaysWhenDaemonRecordIsADirectory(t *testing.T) {
	f := keptSetup(t, 750, 10)
	if err := os.MkdirAll(filepath.Join(f.stateDir, "runs", "daemon.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "daemon: daemon.json could not be read") {
		t.Errorf("text status is silent about an unreadable record:\n%s", o)
	}
}

// No daemon.json at all is still "no daemon": the not-kept lines, and no line
// claiming a read failure.
func TestTextStatusNoDaemonRecordIsNotAReadFailure(t *testing.T) {
	keptSetup(t, 750, 10)
	o, _ := mustCode(t, 0, "status")
	if strings.Contains(o, "could not be read") {
		t.Errorf("a missing daemon.json reported as a read failure:\n%s", o)
	}
	if !strings.Contains(o, "not kept") {
		t.Errorf("control: no daemon should print the not-kept schedule:\n%s", o)
	}
}
