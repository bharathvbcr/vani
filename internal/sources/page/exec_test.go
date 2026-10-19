package page

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// script writes an executable POSIX shell script and returns its path. These
// tests stand in for the lightpanda binary; they need only /bin/sh.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-lightpanda")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunnerOutputAndArgs(t *testing.T) {
	r := ExecRunner{Binary: script(t, `printf '%s|' "$@"`), Timeout: 5 * time.Second}
	out, err := r.Run(context.Background(), []string{"fetch", "a b", "$(x)"})
	if err != nil || string(out) != "fetch|a b|$(x)|" {
		t.Fatalf("out %q err %v: arguments must reach the child verbatim, with no shell", out, err)
	}
}

func TestExecRunnerMinimalEnv(t *testing.T) {
	t.Setenv("VANI_SECRET", "hunter2")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	r := ExecRunner{Binary: script(t, `env`), Timeout: 5 * time.Second}
	out, err := r.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	env := string(out)
	for _, leaked := range []string{"VANI_SECRET", "hunter2", "ANTHROPIC_API_KEY", "sk-test"} {
		if strings.Contains(env, leaked) {
			t.Errorf("child environment leaks %q:\n%s", leaked, env)
		}
	}
	for _, want := range []string{"PATH=", "HOME=", "LIGHTPANDA_DISABLE_TELEMETRY=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("child environment lacks %q:\n%s", want, env)
		}
	}
}

func TestExecRunnerStdoutCap(t *testing.T) {
	r := ExecRunner{Binary: script(t, `head -c 17000000 /dev/zero`), Timeout: 20 * time.Second}
	out, err := r.Run(context.Background(), nil)
	if !errors.Is(err, ErrOutputTooLarge) || out != nil {
		t.Fatalf("got %d bytes, err %v; want nil and ErrOutputTooLarge", len(out), err)
	}
	// Exactly at the cap is accepted.
	r.Binary = script(t, `head -c 16777216 /dev/zero`)
	out, err = r.Run(context.Background(), nil)
	if err != nil || len(out) != MaxStdout {
		t.Fatalf("at the cap: %d bytes, err %v", len(out), err)
	}
}

func TestExecRunnerFailureCarriesBoundedStderr(t *testing.T) {
	r := ExecRunner{Binary: script(t, `head -c 100000 /dev/zero | tr '\0' 'e' >&2; echo; echo "boom" >&2; exit 22`), Timeout: 5 * time.Second}
	out, err := r.Run(context.Background(), nil)
	if err == nil || out != nil {
		t.Fatalf("out %q err %v", out, err)
	}
	if !strings.Contains(err.Error(), "exit status 22") || !strings.Contains(err.Error(), "boom") || len(err.Error()) > 600 {
		t.Errorf("error %q (%d bytes)", err, len(err.Error()))
	}
}

func TestExecRunnerTimeout(t *testing.T) {
	r := ExecRunner{Binary: script(t, `exec sleep 30`), Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := r.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

func TestExecRunnerContextCancel(t *testing.T) {
	r := ExecRunner{Binary: script(t, `exec sleep 30`), Timeout: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := r.Run(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

func TestExecRunnerRejectsBadConfig(t *testing.T) {
	if _, err := (ExecRunner{Binary: "lightpanda", Timeout: time.Second}).Run(context.Background(), nil); err == nil {
		t.Error("relative binary accepted: PATH lookup would be controlled by the environment")
	}
	if _, err := (ExecRunner{Binary: script(t, `true`)}).Run(context.Background(), nil); err == nil {
		t.Error("zero timeout accepted")
	}
	if _, err := (ExecRunner{Binary: "/nonexistent/lightpanda", Timeout: time.Second}).Run(context.Background(), nil); err == nil {
		t.Error("missing binary accepted")
	}
}
