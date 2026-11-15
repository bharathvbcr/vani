package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// scripts/test.sh is the one gate runner (and what release.sh calls). These tests run it with fake `bun` and
// `swift` on PATH and a private run directory, so nothing real is built or run. They pin the rules that keep a
// check that could not run from reading like one that passed: a skip is named in the summary (and fails under
// --strict), a failure is a non-zero exit, a failure that did not reproduce serially is said so, and a refused
// argument never runs anything.

const fakeBun = `#!/bin/sh
# FAKE_BUN: pass | flaky (fails under --parallel, passes serially) | fail
case "$FAKE_BUN" in
  pass) echo "Ran 3 tests across 1 files. [0.10s]"; exit 0 ;;
  flaky)
    case "$*" in
      *--parallel*) echo "test/x.test.ts:"; echo "(fail) a timing edge [6000.00ms]"; exit 1 ;;
      *) echo "Ran 3 tests across 1 files. [0.10s]"; exit 0 ;;
    esac ;;
  *) echo "test/x.test.ts:"; echo "(fail) a real failure"; exit 1 ;;
esac
`

const fakeSwift = `#!/bin/sh
# build and a parallel pass always succeed; the serial pass (--filter) prints FAKE_SWIFT_SKIPS skipped tests.
case "$1" in
  build) exit 0 ;;
esac
case "$*" in
  *--filter*)
    n=${FAKE_SWIFT_SKIPS:-0}
    i=0
    while [ "$i" -lt "$n" ]; do
      echo "/x/S.swift:1: -[A.B t$i] : Test skipped - no Go vani at /x/vani; build it to run the contract test"
      echo "Test Case '-[A.B t$i]' skipped (0.001 seconds)."
      i=$((i + 1))
    done
    echo "	 Executed 5 tests, with $n tests skipped and 0 failures (0 unexpected) in 0.1 (0.1) seconds"
    exit ${FAKE_SWIFT_SERIAL_RC:-0} ;;
esac
exit 0
`

func testScript(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/test.sh is a bash script")
	}
	p, err := filepath.Abs("../../scripts/test.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// runTestScript runs the script with fake bun and swift first on PATH and returns its output, exit code
// and the run directory.
func runTestScript(t *testing.T, env []string, args ...string) (out string, code int, runs string) {
	t.Helper()
	bin := t.TempDir()
	for name, body := range map[string]string{"bun": fakeBun, "swift": fakeSwift} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runs = t.TempDir()
	cmd := exec.Command(testScript(t), args...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "VANI_TEST_RUNS="+runs)
	cmd.Env = append(cmd.Env, env...)
	b, err := cmd.CombinedOutput()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(b), code, runs
}

func TestTestScriptRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--suite", "rust"},
		{"--suite"},
		{"--suite", "go,,swift,nope"},
		{"--wat"},
		{"--keep", "many"},
		{"--keep"},
	} {
		out, code, runs := runTestScript(t, []string{"FAKE_BUN=pass"}, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2:\n%s", args, code, out)
		}
		if entries, _ := os.ReadDir(runs); len(entries) != 0 {
			t.Errorf("%v: a refused argument still made a run directory: %v", args, entries)
		}
	}
}

func TestTestScriptStatusWithoutARunIsRefused(t *testing.T) {
	out, code, _ := runTestScript(t, nil, "status")
	if code != 2 || !strings.Contains(out, "no run yet") {
		t.Fatalf("status with no run: exit %d, out %q", code, out)
	}
}

func TestTestScriptBunPassFailAndFlake(t *testing.T) {
	out, code, _ := runTestScript(t, []string{"FAKE_BUN=pass"}, "--suite", "bun")
	if code != 0 || !strings.Contains(out, "pass (Ran 3 tests") {
		t.Errorf("pass: exit %d:\n%s", code, out)
	}

	out, code, _ = runTestScript(t, []string{"FAKE_BUN=fail"}, "--suite", "bun")
	if code != 1 || !strings.Contains(out, "FAIL") || strings.Contains(out, "bun   pass") {
		t.Errorf("a real failure must exit 1 and say FAIL: exit %d:\n%s", code, out)
	}

	// Fails under --parallel, passes serially: not hidden (the summary says it did not reproduce), not a FAIL.
	out, code, _ = runTestScript(t, []string{"FAKE_BUN=flaky"}, "--suite", "bun")
	if code != 0 || !strings.Contains(out, "did not reproduce serially") {
		t.Errorf("flaky: exit %d:\n%s", code, out)
	}
	// ... but a retry that turned red green is not a clean pass for --strict.
	out, code, _ = runTestScript(t, []string{"FAKE_BUN=flaky"}, "--suite", "bun", "--strict")
	if code != 1 || !strings.Contains(out, "strict: bun") {
		t.Errorf("--strict must fail a run that only passed on a serial re-run: exit %d:\n%s", code, out)
	}
}

func TestTestScriptSkippedSwiftTestsAreNamedAndStrictFails(t *testing.T) {
	// --ui: the window tests are run, so only the fake's three skips are in play.
	out, code, runs := runTestScript(t, []string{"FAKE_SWIFT_SKIPS=3"}, "--suite", "swift", "--ui")
	if code != 0 {
		t.Fatalf("skips without --strict: exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "3 tests SKIPPED") || strings.Contains(out, "swift  pass (") || strings.Contains(out, "NOT RUN") {
		t.Errorf("a skip must not read as a plain pass (and --ui leaves nothing NOT RUN):\n%s", out)
	}
	if !strings.Contains(out, "no Go vani at /x/vani") {
		t.Errorf("the skip reason is missing from the summary:\n%s", out)
	}
	latest, err := filepath.EvalSymlinks(filepath.Join(runs, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(latest, "swift.skipped")); err != nil || strings.TrimSpace(string(b)) != "3" {
		t.Errorf("swift.skipped = %q, %v; release.sh reads this", b, err)
	}

	out, code, _ = runTestScript(t, []string{"FAKE_SWIFT_SKIPS=3"}, "--suite", "swift", "--ui", "--strict")
	if code != 1 || !strings.Contains(out, "strict") {
		t.Errorf("--strict with skips: exit %d:\n%s", code, out)
	}

	out, code, _ = runTestScript(t, []string{"FAKE_SWIFT_SKIPS=0"}, "--suite", "swift", "--ui", "--strict")
	if code != 0 || !strings.Contains(out, "swift  pass (") {
		t.Errorf("no skips under --strict: exit %d:\n%s", code, out)
	}
}

// Without --ui the window tests (they launch a host macOS makes the active app) are never run, and the summary
// says so instead of reading as a pass; the serial pass must not have been asked for them.
func TestTestScriptUITestsAreNotRunByDefault(t *testing.T) {
	out, code, runs := runTestScript(t, []string{"FAKE_SWIFT_SKIPS=0"}, "--suite", "swift")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "UI tests NOT RUN") || !strings.Contains(out, "--ui") || strings.Contains(out, "swift  pass (") {
		t.Errorf("not running the UI tests must not read as a pass:\n%s", out)
	}
	latest, err := filepath.EvalSymlinks(filepath.Join(runs, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(latest, "swift.skipped")); err != nil || strings.TrimSpace(string(b)) == "0" || strings.TrimSpace(string(b)) == "" {
		t.Errorf("swift.skipped = %q, %v; NOT RUN tests count as skipped for release.sh and --strict", b, err)
	}
	_, code, _ = runTestScript(t, []string{"FAKE_SWIFT_SKIPS=0"}, "--suite", "swift", "--strict")
	if code != 1 {
		t.Errorf("--strict must fail a run that did not run the UI tests, exit %d", code)
	}
}

// A class that can skip but runs in the parallel pass would report its skip as a pass, so every XCTSkip-throwing
// test class must be derived, including one that inherits from a shared base (ResumeStudioViewsTests: StudioFakeCase),
// and the window classes (opt-in with --ui) must not be.
func TestTestScriptDerivesEverySkipCapableClass(t *testing.T) {
	out, code, _ := runTestScript(t, nil, "--print-skip-classes")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got := map[string]bool{}
	for _, c := range strings.Split(strings.TrimSpace(out), "|") {
		got[c] = true
	}
	// Every class declared in a Swift test file that throws XCTSkip, read independently of the script.
	var want []string
	classRe := regexp.MustCompile(`class ([A-Za-z0-9_]+) *: *[A-Za-z0-9_]+`)
	window := map[string]bool{"JobsGapsPanelTests": true, "JobsControlsTests": true, "JobsBackToTopTests": true, "JobsTabInvisibleTests": true, "JobsTabHost": true}
	_ = filepath.WalkDir("../../macos/Tests", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".swift") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil || !strings.Contains(string(b), "XCTSkip") {
			return nil
		}
		for _, m := range classRe.FindAllStringSubmatch(string(b), -1) {
			if !window[m[1]] {
				want = append(want, m[1])
			}
		}
		return nil
	})
	if len(want) < 10 {
		t.Fatalf("found only %d skip-capable classes in macos/Tests: the check did not run", len(want))
	}
	for _, c := range want {
		if !got[c] {
			t.Errorf("%s throws XCTSkip but is not in the serial pass; a skip there would read as a pass", c)
		}
	}
	for c := range window {
		if got[c] {
			t.Errorf("window class %s must stay out of the default run (it launches the UI host)", c)
		}
	}
	if !got["ResumeStudioViewsTests"] {
		t.Error("ResumeStudioViewsTests (a StudioFakeCase subclass with two XCTSkips) is missing")
	}
}

func TestTestScriptSwiftSerialFailureFails(t *testing.T) {
	out, code, _ := runTestScript(t, []string{"FAKE_SWIFT_SERIAL_RC=1"}, "--suite", "swift")
	if code != 1 || !strings.Contains(out, "FAIL (parallel rc=0, serial rc=1") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestTestScriptKeepsOnlyTheNewestRuns(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bun"), []byte(fakeBun), 0o755); err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	for i := 0; i < 4; i++ {
		cmd := exec.Command(testScript(t), "--suite", "bun", "--keep", "2")
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "VANI_TEST_RUNS="+runs, "FAKE_BUN=pass")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run %d: %v\n%s", i, err, b)
		}
	}
	// The new run's directory is made first, then everything past the newest --keep is removed.
	dirs, _ := filepath.Glob(filepath.Join(runs, "2*"))
	if len(dirs) != 2 {
		t.Errorf("%d run directories kept after 4 runs with --keep 2, want 2: %v", len(dirs), dirs)
	}
	if _, err := os.Stat(filepath.Join(runs, "latest", "exit")); err != nil {
		t.Errorf("latest must point at a finished run (exit file): %v", err)
	}
}
