package rkbresume

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerReportsExitCodeAndBothStreams(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}, Env: []string{}}, 5*time.Second)
	if err != nil || out.Exit != 3 || strings.TrimSpace(out.Stdout) != "out" || strings.TrimSpace(out.Stderr) != "err" {
		t.Fatalf("out = %+v err = %v", out, err)
	}
	out, err = ExecRunner{}.Run(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "exit 0"}, Env: []string{}}, 5*time.Second)
	if err != nil || out.Exit != 0 {
		t.Fatalf("out = %+v err = %v", out, err)
	}
}

func TestExecRunnerPassesOnlyTheEnvironmentItIsGiven(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "secret-value")
	out, err := ExecRunner{}.Run(context.Background(), Command{Path: "/usr/bin/env", Env: []string{"ONLY=this"}}, 5*time.Second)
	if err != nil || strings.TrimSpace(out.Stdout) != "ONLY=this" {
		t.Fatalf("env = %q err = %v", out.Stdout, err)
	}
}

func TestExecRunnerBoundsTimeAndOutput(t *testing.T) {
	start := time.Now()
	_, err := ExecRunner{}.Run(context.Background(), Command{Path: "/bin/sleep", Args: []string{"30"}, Env: []string{}}, 150*time.Millisecond)
	if !errors.Is(err, ErrStepTimeout) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	out, err := ExecRunner{}.Run(context.Background(), Command{Path: "/usr/bin/yes", Env: []string{}}, 300*time.Millisecond)
	if !errors.Is(err, ErrStepTimeout) {
		t.Fatalf("err = %v", err)
	}
	if len(out.Stdout) > maxProcOutput {
		t.Fatalf("captured %d bytes, cap %d", len(out.Stdout), maxProcOutput)
	}
}

func TestExecRunnerPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := ExecRunner{}.Run(ctx, Command{Path: "/bin/sleep", Args: []string{"30"}, Env: []string{}}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExecRunnerMissingBinaryIsAnErrorNotAnExitCode(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), Command{Path: "/no/such/rkb", Env: []string{}}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "/no/such/rkb") {
		t.Fatalf("err = %v", err)
	}
}

func TestClipIsOneLineAndBounded(t *testing.T) {
	got := clip("a\n\n  b\tc"+strings.Repeat("é", 500), 20)
	if strings.ContainsAny(got, "\n\t") || len([]rune(got)) != 20 {
		t.Errorf("clip = %q (%d runes)", got, len([]rune(got)))
	}
	if clip("\xff\xfe ok", 10) != "? ok" {
		t.Errorf("clip of invalid UTF-8 = %q", clip("\xff\xfe ok", 10))
	}
}
