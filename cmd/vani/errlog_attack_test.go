package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func attackLogPath(t *testing.T, e *env) string {
	t.Helper()
	p := filepath.Join(e.stateDir, "logs", "vani.err.log")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func attackFill(t *testing.T, p string, size int, newline bool) {
	t.Helper()
	b := bytes.Repeat([]byte("y"), size)
	if newline {
		for i := 79; i < size; i += 80 {
			b[i] = '\n'
		}
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A log with no newline at all keeps its last errLogKeep bytes whole.
func TestAttackTrimNoNewline(t *testing.T) {
	e, _, start, _ := advEnv(t)
	p := attackLogPath(t, e)
	attackFill(t, p, errLogMax+1000, false)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Size() != errLogKeep {
		t.Fatalf("size %d, want %d", st.Size(), errLogKeep)
	}
}

// Exactly errLogMax is left alone; one byte over is trimmed.
func TestAttackTrimBoundary(t *testing.T) {
	e, _, start, _ := advEnv(t)
	p := attackLogPath(t, e)
	attackFill(t, p, errLogMax, true)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() != errLogMax {
		t.Fatalf("exactly-max log changed to %d", st.Size())
	}
	attackFill(t, p, errLogMax+1, true)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() > errLogKeep {
		t.Fatalf("one over max not trimmed: %d", st.Size())
	}
}

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

// A writer appending O_APPEND lines while trims run: what remains is whole
// lines only (each line is either intact or absent), no NULs, and it shrinks.
func TestAttackTrimAgainstAppender(t *testing.T) {
	e, _, start, _ := advEnv(t)
	p := attackLogPath(t, e)
	for round := 0; round < 8; round++ {
		attackFill(t, p, errLogMax+4096, true)
		w, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		line := []byte("APPENDED " + strings.Repeat("z", 100) + "\n")
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					w.Write(line)
				}
			}
		}()
		_, err = e.reloadConfig(reloadApp(), start, nil)
		close(stop)
		wg.Wait()
		w.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(p)
		if bytes.IndexByte(got, 0) >= 0 {
			t.Fatalf("round %d: NUL bytes in trimmed log", round)
		}
		for i, l := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n") {
			if l != strings.Repeat("y", 79) && l != string(line[:len(line)-1]) {
				t.Fatalf("round %d: garbled line %d: %q", round, i, l)
			}
		}
	}
}

// Two stat-sized trims in sequence on the same file (daemon + CLI): the second
// one sees the already-trimmed file.
func TestAttackTwoTrimsSequential(t *testing.T) {
	e, _, start, _ := advEnv(t)
	p := attackLogPath(t, e)
	attackFill(t, p, errLogMax+4096, true)
	for range 2 {
		e.reloadConfig(reloadApp(), start, nil)
	}
	got, _ := os.ReadFile(p)
	if bytes.IndexByte(got, 0) >= 0 || len(got) == 0 {
		t.Fatalf("log damaged: %d bytes", len(got))
	}
}

// 45: a --board run, then a valid edited config applies the NEW config.
func TestAttackBoardRunThenEditedConfigUsesNew(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	start.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "a"}, {ATS: posting.Greenhouse, Token: "b"}}
	start.AllowedHosts = append(start.AllowedHosts, "boards-api.greenhouse.io")
	writeConfig(t, cfgPath, start)
	w, app := lifecycleApp(t, start)
	if _, err := e.reloadConfig(app, start, []string{"gh:a"}); err != nil {
		t.Fatal(err)
	}
	edited := start
	edited.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "a"}, {ATS: posting.Greenhouse, Token: "b"}, {ATS: posting.Greenhouse, Token: "c"}}
	writeConfig(t, cfgPath, edited)
	if _, err := e.reloadConfig(app, start, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(w.cfg.Watchlist); got != 3 {
		t.Fatalf("holds %d boards, want the edited 3", got)
	}
}

// 45: --board, removed config, restored: ends with the whole list, and the
// concurrent-reload map use is race-free.
func TestAttackBoardCutRemovedThenRestoredAndConcurrent(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	start.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "a"}, {ATS: posting.Greenhouse, Token: "b"}}
	start.AllowedHosts = append(start.AllowedHosts, "boards-api.greenhouse.io")
	writeConfig(t, cfgPath, start)
	w, app := lifecycleApp(t, start)
	e.reloadConfig(app, start, []string{"gh:a"})
	os.Remove(cfgPath)
	e.reloadConfig(app, start, nil)
	writeConfig(t, cfgPath, start)
	e.reloadConfig(app, start, nil)
	if got := len(w.cfg.Watchlist); got != 2 {
		t.Fatalf("holds %d boards, want 2", got)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				boardCutHeld.Store(e, struct{}{})
			} else {
				boardCutHeld.Delete(e)
			}
			deprecationSeen.Delete(e)
		}()
	}
	wg.Wait()
}

// 47: same keys in another order is a different signature: pin what happens.
func TestAttackDeprecatedKeyRemovedThenReaddedAndOneShotUnchanged(t *testing.T) {
	a := &env{configPath: "/c", stderr: &bytes.Buffer{}}
	a.warnDeprecatedOnce([]string{"x is now y"})
	a.warnDeprecatedOnce(nil)
	a.warnDeprecatedOnce([]string{"x is now y"})
	if n := strings.Count(a.stderr.(*bytes.Buffer).String(), "x is now y"); n != 2 {
		t.Fatalf("re-added key warned %d times, want 2", n)
	}
	a.warnDeprecated([]string{"x is now y"})
	a.warnDeprecated([]string{"x is now y"})
	if n := strings.Count(a.stderr.(*bytes.Buffer).String(), "x is now y"); n != 4 {
		t.Fatalf("plain warnDeprecated changed: %d", n)
	}
}

// 48: a huge daemon.json is read bounded and reported without its content.
func TestAttackHugeDaemonRecord(t *testing.T) {
	f := keptSetup(t, 750, 10)
	writeStateFile(t, f.stateDir, "runs/daemon.json", "{\"secret\":\""+strings.Repeat("S", 8<<20)+"\"}")
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "daemon.json could not be read") {
		t.Errorf("huge record not reported:\n%.500s", o)
	}
	if strings.Contains(o, "SSSSSSSS") {
		t.Error("record content leaked into status")
	}
}
