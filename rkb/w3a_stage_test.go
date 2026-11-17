package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// [205] finalize's staging folder holds the resume text and contact lines. It
// must not survive any way finalize can end: die() from a failed fit, a stop,
// a panic, or the normal return.

// TestW3AStageHelper is the child process: it makes a staging folder the way
// finalize does, then ends in the way W3A_END says.
func TestW3AStageHelper(t *testing.T) {
	end := os.Getenv("W3A_END")
	if end == "" {
		t.Skip("child process only")
	}
	dir, cleanup, err := scratchDir("rkb-finalize-")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "Resume.txt"), []byte("Jane Q. Sample\njane@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	switch end {
	case "die":
		die("chrome did not finish within 90 s")
	case "exit":
		exitNow(1)
	case "panic":
		panic("boom")
	}
}

func TestW3AStagingFolderIsRemovedOnEveryExit(t *testing.T) {
	for _, tc := range []struct {
		end  string
		code int
	}{{"die", 2}, {"exit", 1}, {"panic", 2}, {"return", 0}} {
		tmp := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^TestW3AStageHelper$")
		cmd.Env = append(os.Environ(), "W3A_END="+tc.end, "TMPDIR="+tmp)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", tc.end, err)
		}
		if tc.end != "panic" && code != tc.code {
			t.Errorf("%s: exit code %d, want %d\n%s", tc.end, code, tc.code, out)
		}
		if tc.end == "panic" && code == 0 {
			t.Errorf("panic: the child exited 0\n%s", out)
		}
		left, _ := filepath.Glob(filepath.Join(tmp, "rkb-finalize-*"))
		if len(left) != 0 {
			t.Errorf("%s: staging folder left behind: %s", tc.end, strings.Join(left, ", "))
		}
	}
}
