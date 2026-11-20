package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// panicEnv is an env whose dispatch panics with v after writing before to
// stdout, the way a command that crashed mid-reply would.
func panicEnv(out, errw *bytes.Buffer, before string, v any) *env {
	e := &env{stdout: out, stderr: errw}
	e.dispatchFn = func([]string) error {
		if before != "" {
			_, _ = e.stdout.Write([]byte(before))
		}
		panic(v)
	}
	return e
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// A panic in any command exits exitCrash, not exitUsage (a bare Go panic is 2,
// which the Mac app reads as "this vani is too old"), with a bounded one-line
// message last on stderr and, for a command that replies in JSON, the envelope.
func TestPanicInACommandExitsCrashNotUsage(t *testing.T) {
	if exitCrash == exitUsage || exitCrash == exitOK || exitCrash == exitRuntime || exitCrash == exitConfigRestart {
		t.Fatalf("exitCrash %d collides with another exit code", exitCrash)
	}
	for _, tc := range []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		{"jsonAlways command", []string{"draft"}, true},
		{"jsonFlag command asked for json", []string{"status", "--json"}, true},
		{"global json", []string{"--json", "status"}, true},
		{"plain text command", []string{"status"}, false},
		{"group with several words", []string{"jobs", "list"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var o, e bytes.Buffer
			code := runEnv(panicEnv(&o, &e, "", "boom"), tc.args)
			if code != exitCrash {
				t.Fatalf("exit %d, want %d (stderr %q)", code, exitCrash, e.String())
			}
			if got := lastLine(e.String()); got != "vani: crashed: boom" {
				t.Errorf("last stderr line %q, want %q", got, "vani: crashed: boom")
			}
			if !tc.wantJSON {
				if o.Len() != 0 {
					t.Errorf("stdout %q, want empty", o.String())
				}
				return
			}
			var r errorReply
			if err := json.Unmarshal(o.Bytes(), &r); err != nil {
				t.Fatalf("stdout %q is not the error envelope: %v", o.String(), err)
			}
			if r.Code != codeCrash || r.Error != "crashed: boom" {
				t.Errorf("envelope %+v, want code %q error %q", r, codeCrash, "crashed: boom")
			}
		})
	}
}

func TestPanicAdversarialValues(t *testing.T) {
	t.Run("oversized multi-line value stays bounded and one line", func(t *testing.T) {
		var o, e bytes.Buffer
		big := strings.Repeat("line one\nline two ", 100_000)
		if code := runEnv(panicEnv(&o, &e, "", big), []string{"draft"}); code != exitCrash {
			t.Fatalf("exit %d, want %d", code, exitCrash)
		}
		last := lastLine(e.String())
		if !strings.HasPrefix(last, "vani: crashed: ") || strings.Contains(last, "\n") || len(last) > 2048 {
			t.Errorf("last stderr line is not one bounded line (%d bytes): %.120q", len(last), last)
		}
		if e.Len() > 16<<10 || o.Len() > 16<<10 {
			t.Errorf("unbounded output: stderr %d, stdout %d bytes", e.Len(), o.Len())
		}
	})
	t.Run("error value and nil panic", func(t *testing.T) {
		for name, v := range map[string]any{"error": errNoJSON(jsonLeaf{Command: "x"}), "nil": nil, "empty string": ""} {
			var o, e bytes.Buffer
			if code := runEnv(panicEnv(&o, &e, "", v), []string{"draft"}); code != exitCrash {
				t.Errorf("%s: exit %d, want %d", name, code, exitCrash)
			}
			if !strings.HasPrefix(lastLine(e.String()), "vani: crashed: ") {
				t.Errorf("%s: stderr %q", name, e.String())
			}
		}
	})
	t.Run("a reply already written is not followed by a second one", func(t *testing.T) {
		var o, e bytes.Buffer
		if code := runEnv(panicEnv(&o, &e, "{\"partial\":true}\n", "late"), []string{"draft"}); code != exitCrash {
			t.Fatalf("exit %d, want %d", code, exitCrash)
		}
		if o.String() != "{\"partial\":true}\n" {
			t.Errorf("stdout %q gained a second reply", o.String())
		}
	})
	t.Run("concurrent envs each recover", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var o, e bytes.Buffer
				if code := runEnv(panicEnv(&o, &e, "", "x"), []string{"draft"}); code != exitCrash {
					t.Errorf("exit %d, want %d", code, exitCrash)
				}
			}()
		}
		wg.Wait()
	})
}

// A command that returns a usage error still exits 2: only a panic is a crash.
func TestUsageErrorStillExitsUsage(t *testing.T) {
	var o, e bytes.Buffer
	env := &env{stdout: &o, stderr: &e}
	env.dispatchFn = func([]string) error { return usagef("bad flag") }
	if code := runEnv(env, []string{"status"}); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
}
