package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// A --list-only run asks no decision backend anything, so it must not start the
// local llama.cpp server either: loading a model costs seconds and gigabytes
// for a run that never asks it a question. The fake llama-server records its
// command line when it starts, so a file that exists is a server that started.

// localServerConfig is a config whose local decision model is a started
// server: the fake llama-server writes its argv to argsFile.
func localServerConfig(t *testing.T) (cfg config.Config, argsFile string) {
	t.Helper()
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	t.Setenv("PATH", t.TempDir())
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(t.TempDir(), "args")
	bin := filepath.Join(t.TempDir(), "llama-server")
	script := fmt.Sprintf("#!/bin/sh\nVANI_FAKE_LLAMA=1 VANI_FAKE_LLAMA_ARGS=%q exec %q \"$@\"\n", argsFile, self)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "d1.gguf")
	if err := os.WriteFile(model, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Local = &config.Local{Model: "test-d1", Server: &config.LocalServer{
		Binary: bin, ModelPath: model, StartTimeoutMS: 20000}}
	cfg.Decide.Shadow = "local"
	return cfg, argsFile
}

func TestListOnlyRunDoesNotStartTheLocalServer(t *testing.T) {
	cfg, argsFile := localServerConfig(t)
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")
	w.tune(runOptions{ListOnly: true})
	if err := w.runOnce(context.Background()); err != nil {
		t.Fatalf("list-only run: %v\n%s", err, warn.String())
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatalf("a list-only run started llama-server")
	}
	if w.localEndpoint != "" {
		t.Errorf("a list-only run left an endpoint %q", w.localEndpoint)
	}
	// Nothing was asked, so a missing shadow is not news: the run's output must
	// not say the local model is disabled or the shadow unavailable.
	for _, noise := range []string{"local decision model", "shadow"} {
		if strings.Contains(warn.String(), noise) {
			t.Errorf("list-only run warned about the unused local model (%q):\n%s", noise, warn.String())
		}
	}
}

// The same wired app, tuned back to a full run, starts the server: the list-only
// tune is per run and neither disables the server for good nor leaves it
// running (the daemon reuses one wired app for every run).
func TestFullRunAfterListOnlyStartsTheLocalServer(t *testing.T) {
	cfg, argsFile := localServerConfig(t)
	w, warn := openWired(t, cfg)
	spoolBatch(t, w.stateDir, "careers-acme-com", "Data Engineer")

	w.tune(runOptions{ListOnly: true})
	if err := w.runOnce(context.Background()); err != nil {
		t.Fatalf("list-only run: %v\n%s", err, warn.String())
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("the list-only run started llama-server")
	}

	w.tune(runOptions{})
	_ = w.runOnce(context.Background())
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("the full run did not start llama-server: %v\n%s", err, warn.String())
	}
	if w.localEndpoint != "" {
		t.Errorf("the finished run left endpoint %q", w.localEndpoint)
	}
}

// An inbox poll advances postings whatever the last tune said, so it keeps the
// server: it must not inherit --list-only. startRun is the seam both use.
func TestStartRunStartsTheLocalServerOnlyWhenAdvancing(t *testing.T) {
	cfg, argsFile := localServerConfig(t)
	w, warn := openWired(t, cfg)
	ctx := context.Background()

	_, _, stop, err := w.startRun(ctx, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, warn.String())
	}
	stop()
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("startRun(advance=false) started llama-server")
	}

	_, _, stop, err = w.startRun(ctx, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, warn.String())
	}
	defer stop()
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("startRun(advance=true) did not start llama-server: %v", err)
	}
	if w.localEndpoint == "" {
		t.Error("startRun(advance=true) left no endpoint")
	}
}
