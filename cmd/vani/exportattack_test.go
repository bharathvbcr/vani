package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func newExportEnv(t *testing.T, state string) (*env, *bytes.Buffer) {
	t.Helper()
	stderr := &bytes.Buffer{}
	return &env{stateDir: state, stdout: &bytes.Buffer{}, stderr: stderr}, stderr
}

// The hook runs after the labels lock is released: while it is blocked, a
// second writer on the same label directory still completes its write.
func TestAfterWriteRunsOutsideLabelsLock(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "one", "gh:2": "two"})
	e, _ := newExportEnv(t, state)
	a, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	b.AfterWrite = nil
	entered, release := make(chan struct{}), make(chan struct{})
	a.AfterWrite = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() {
		done <- a.Put(eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	}()
	<-entered
	second := make(chan error, 1)
	go func() {
		second <- b.Put(eval.Entry{Key: "gh:2", Labels: map[string]string{"role_fit": "strong_fit"}})
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second writer: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second label write blocked while the first writer's hook was running: the hook holds the labels lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A hook that cannot write its export (the export directory is a file) does
// not fail the label, which is already on disk, and names the key-free cause
// without echoing posting text.
func TestAfterWriteFailureDoesNotFailLabelAndLeaksNoText(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "SECRET-JD-BODY-TEXT"})
	if err := os.WriteFile(filepath.Join(state, "export"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, stderr := newExportEnv(t, state)
	s, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatalf("label failed because the export failed: %v", err)
	}
	if got, _, _ := s.Labels(); len(got) != 1 {
		t.Fatalf("label not saved: %v", got)
	}
	if !strings.Contains(stderr.String(), "held-out export not updated") {
		t.Errorf("failure was silent: %q", stderr.String())
	}
	for _, leak := range []string{"SECRET-JD-BODY-TEXT", "strong_fit", "Jane", "FAKE-PERSONAL"} {
		if strings.Contains(stderr.String(), leak) {
			t.Errorf("stderr leaks %q: %s", leak, stderr.String())
		}
	}
}

// A corrupt ledger line holding text must not be echoed by the export's error.
func TestExportCorruptLedgerErrorDoesNotEchoText(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "x"})
	f, err := os.OpenFile(filepath.Join(state, "ledger", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Skip("ledger layout:", err)
	}
	_, _ = f.WriteString("{\"v\":1,\"description\":\"SECRET-JD-BODY-TEXT\" garbage\n{\"v\":1}\n")
	_ = f.Close()
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	code, o, e := do(t, "export", "heldout")
	if strings.Contains(o+e, "SECRET-JD-BODY-TEXT") {
		t.Errorf("exit %d leaks text:\nstdout %s\nstderr %s", code, o, e)
	}
}

// Default path: directories 0700, file 0600.
func TestExportDefaultPathPermissions(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "x"})
	putEntry(t, state, eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	mustCode(t, 0, "export", "heldout")
	for _, p := range []string{filepath.Join(state, "export"), filepath.Join(state, "export", "heldout")} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v, want 0700", p, st, err)
		}
	}
	if st, err := os.Stat(exportFile(state)); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("export file: %v %v, want 0600", st, err)
	}
}

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

// A panic inside the hook after the label has been saved: the label stays,
// and the panic escapes Put to the caller (documented here, not asserted away).
func TestAfterWritePanicLeavesLabelSaved(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	e, _ := newExportEnv(t, state)
	s, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.AfterWrite = func() { panic("hook boom") }
	func() {
		defer func() { t.Logf("recovered at caller: %v", recover()) }()
		_ = s.Put(eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	}()
	s.AfterWrite = nil
	if got, _, _ := s.Labels(); len(got) != 1 {
		t.Fatalf("label lost: %v", got)
	}
	// And the lock was released, so a later writer is not wedged.
	if err := s.Put(eval.Entry{Key: "gh:2", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatalf("lock wedged after a hook panic: %v", err)
	}
}

// Cost of one hook with a 1k-posting ledger: logged, bounded loosely.
func TestAfterWriteLatencyThousandPostings(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	_, state := setup(t)
	mustCode(t, 0, "init")
	descs := map[string]string{}
	body := strings.Repeat("Responsibilities include building systems. ", 120) // ~5 KB
	for i := 0; i < 1000; i++ {
		descs[fmt.Sprintf("gh:%d", i)] = body + fmt.Sprint(i)
	}
	seedExportLedger(t, state, descs)
	e, _ := newExportEnv(t, state)
	s, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.AfterWrite = nil
	for i := 0; i < 200; i++ {
		if err := s.Put(eval.Entry{Key: posting.Key(fmt.Sprintf("gh:%d", i)), Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	e.reexportHeldout(s)
	d := time.Since(start)
	t.Logf("one hook run: 1000 ledger postings (~5KB each), 200 labels: %v", d)
	if d > 5*time.Second {
		t.Errorf("hook took %v per label write", d)
	}
}
