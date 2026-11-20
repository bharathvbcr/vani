package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

func reloadApp() *App { return &App{Reload: func(config.Config) {}} }

// A daemon re-reads the config on every run and inbox poll: a deprecated key
// is warned once, again only when the set of deprecated keys changes.
func TestDeprecatedKeyWarnsOncePerConfigChange(t *testing.T) {
	e, cfgPath, start, stderr := advEnv(t)
	writeLegacyConfig(t, cfgPath)
	for range 6 {
		if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(stderr.String(), "jev_model"); n != 1 {
		t.Fatalf("warned %d times over 6 reloads, want 1:\n%s", n, stderr)
	}
	// The key is fixed, then used again: that is a change, so it is said again.
	writeConfig(t, cfgPath, start)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	writeLegacyConfig(t, cfgPath)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stderr.String(), "jev_model"); n != 2 {
		t.Fatalf("warned %d times after the key came back, want 2:\n%s", n, stderr)
	}
}

func TestWarnDeprecatedOnceIsPerEnvAndPerKeySet(t *testing.T) {
	a, b := &env{configPath: "/c", stderr: &bytes.Buffer{}}, &env{configPath: "/c", stderr: &bytes.Buffer{}}
	for range 3 {
		a.warnDeprecatedOnce([]string{"x is now y"})
	}
	b.warnDeprecatedOnce([]string{"x is now y"})
	a.warnDeprecatedOnce([]string{"x is now y", "p is now q"})
	if got := strings.Count(a.stderr.(*bytes.Buffer).String(), "x is now y"); got != 2 {
		t.Errorf("env a said x %d times, want 2 (once, and again with a bigger set)", got)
	}
	if got := strings.Count(b.stderr.(*bytes.Buffer).String(), "x is now y"); got != 1 {
		t.Errorf("env b said x %d times, want 1", got)
	}
	a.warnDeprecatedOnce(nil)
}

func writeErrLog(t *testing.T, e *env, lines int) string {
	t.Helper()
	p := filepath.Join(e.stateDir, "logs", "vani.err.log")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := range lines {
		fmt.Fprintf(&sb, "line %07d %s\n", i, strings.Repeat("x", 80))
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The error log has a size bound: a reload trims one past errLogMax to its
// last lines, whole lines only, and leaves a smaller one alone.
func TestReloadBoundsTheErrorLog(t *testing.T) {
	e, _, start, stderr := advEnv(t)
	p := writeErrLog(t, e, errLogMax/95+2000)
	big, _ := os.Stat(p)
	if big.Size() <= errLogMax {
		t.Fatalf("fixture is %d bytes, want over %d", big.Size(), errLogMax)
	}
	last := fmt.Sprintf("line %07d ", errLogMax/95+2000-1)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > errLogKeep || len(got) == 0 {
		t.Fatalf("log is %d bytes after the reload, want 1..%d", len(got), errLogKeep)
	}
	if !strings.HasPrefix(string(got), "line ") || !strings.HasSuffix(string(got), "\n") || !strings.Contains(string(got), last) {
		t.Fatalf("trimmed log is not whole recent lines: head %q tail %q", got[:20], got[len(got)-20:])
	}
	if !strings.Contains(stderr.String(), "trimmed") {
		t.Errorf("the trim was not said: %q", stderr)
	}
	// Under the bound: untouched.
	small := writeErrLog(t, e, 100)
	before, _ := os.ReadFile(small)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(small); !bytes.Equal(before, after) {
		t.Error("a log under the bound was rewritten")
	}
}

// No log, a directory in its place, and a symlink to a big file elsewhere are
// all left alone, and none stops the reload.
func TestErrLogTrimLeavesOddFilesAlone(t *testing.T) {
	e, _, start, _ := advEnv(t)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatalf("no log: %v", err)
	}
	p := writeErrLog(t, e, errLogMax/95+2000)
	target := filepath.Join(t.TempDir(), "elsewhere.log")
	if err := os.Rename(p, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(target)
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Stat(target); got.Size() != want.Size() {
		t.Error("a symlinked log was trimmed")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reloadConfig(reloadApp(), start, nil); err != nil {
		t.Fatalf("directory in place of the log: %v", err)
	}
}
