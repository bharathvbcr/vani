package localserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake llama-server when LOCALSERVER_FAKE is set:
// it parses --port and behaves as the mode says.
func TestMain(m *testing.M) {
	if mode := os.Getenv("LOCALSERVER_FAKE"); mode != "" {
		fakeServer(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeServer(mode string) {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	switch mode {
	case "crash":
		fmt.Fprintln(os.Stderr, "load_model: unsupported decision model type: lfm2-d1")
		os.Exit(1)
	case "never-ready":
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		<-sig
		os.Exit(0)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Hour)
	}
	// "ok": serve /health and /v1/systemone until SIGTERM. A leaked secret in
	// the environment, or a start that would let llama.cpp fall back to CPU,
	// is reported as unhealthy.
	args := " " + strings.Join(os.Args[1:], " ") + " "
	metalOnly := strings.Contains(args, " --device MTL0 ") && strings.Contains(args, " -ngl all ")
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if os.Getenv("OPENROUTER_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" || !metalOnly {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		<-sig
		_ = srv.Close()
	}()
	_ = srv.ListenAndServe()
}

// fakeBinary is a tiny shell wrapper so Start's absolute-path binary runs this
// test executable in fake mode (Start passes a minimal env, so the mode is
// baked into the wrapper).
func fakeBinary(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "llama-server")
	script := fmt.Sprintf("#!/bin/sh\nLOCALSERVER_FAKE=%s exec %q \"$@\"\n", mode, self)
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func model(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.gguf")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartsServesAndStops(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "must-not-reach-the-server")
	s, err := Start(context.Background(), Config{Binary: fakeBinary(t, "ok"), Model: model(t), StartTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err) // includes the env-leak check: a leaked key makes /health fail
	}
	if !strings.HasPrefix(s.Endpoint, "http://127.0.0.1:") || !strings.HasSuffix(s.Endpoint, Path) {
		t.Errorf("endpoint %q", s.Endpoint)
	}
	s.Stop()
	s.Stop() // idempotent
	if !s.Exited() {
		t.Fatal("server still running after Stop")
	}
}

func TestCrashIsReportedWithStderr(t *testing.T) {
	_, err := Start(context.Background(), Config{Binary: fakeBinary(t, "crash"), Model: model(t), StartTimeout: 20 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") || !strings.Contains(err.Error(), "lfm2-d1") {
		t.Fatalf("err = %v", err)
	}
}

func TestNeverReadyTimesOutAndIsStopped(t *testing.T) {
	start := time.Now()
	_, err := Start(context.Background(), Config{Binary: fakeBinary(t, "never-ready"), Model: model(t), StartTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "not ready after") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

func TestCancelledContextStopsStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	_, err := Start(ctx, Config{Binary: fakeBinary(t, "never-ready"), Model: model(t), StartTimeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopKillsAServerThatIgnoresTerm(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 10 s grace period")
	}
	_, err := Start(context.Background(), Config{Binary: fakeBinary(t, "ignore-term"), Model: model(t), StartTimeout: time.Second})
	if err == nil {
		t.Fatal("a server that never answered /health was accepted")
	}
	// Start returned, so Stop (grace, then kill) completed: the process is gone.
}

func TestConfigRefused(t *testing.T) {
	m := model(t)
	for _, c := range []Config{
		{Binary: "llama-server", Model: m},
		{Binary: "/bin/true", Model: "m.gguf"},
		{Binary: "/bin/true", Model: filepath.Join(t.TempDir(), "missing.gguf")},
		{Binary: "/bin/true", Model: m, ContextSize: 100},
		{Binary: "/bin/true", Model: m, ContextSize: MaxContextSize + 1},
		{Binary: "/bin/true", Model: m, StartTimeout: -time.Second},
		{Binary: "/bin/true", Model: m, StartTimeout: MaxStartTimeout + time.Second},
		{Binary: "/bin/true", Model: m, BatchSize: 16},
		{Binary: "/bin/true", Model: m, BatchSize: DefaultContextSize + 1},
		{Binary: "/bin/true", Model: m, ContextSize: 4096, BatchSize: 8192},
		{Binary: "/bin/true", Model: m, Parallel: -1},
		{Binary: "/bin/true", Model: m, Parallel: MaxParallel + 1},
	} {
		if _, err := Start(context.Background(), c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func TestArgs(t *testing.T) {
	base := "-m /m.gguf --host 127.0.0.1 --port 9 -c 8192 --device MTL0 -ngl all"
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{Model: "/m.gguf", ContextSize: 8192}, base},
		{Config{Model: "/m.gguf", ContextSize: 8192, BatchSize: 8192}, base + " -b 8192 -ub 8192"},
		{Config{Model: "/m.gguf", ContextSize: 8192, Parallel: 1}, base + " -np 1"},
		{Config{Model: "/m.gguf", ContextSize: 8192, BatchSize: 4096, Parallel: 2}, base + " -b 4096 -ub 4096 -np 2"},
	} {
		if got := strings.Join(args(c.cfg, 9), " "); got != c.want {
			t.Errorf("args(%+v)\n got %s\nwant %s", c.cfg, got, c.want)
		}
	}
}

// The batch and slot flags reach a real start, and the Metal-only flags stay.
func TestStartsWithBatchAndParallel(t *testing.T) {
	s, err := Start(context.Background(), Config{Binary: fakeBinary(t, "ok"), Model: model(t),
		StartTimeout: 20 * time.Second, BatchSize: 4096, Parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	s.Stop()
}

func TestStderrKeepsTheTail(t *testing.T) {
	c := &capped{limit: 10}
	_, _ = c.Write([]byte("0123456789abcdef"))
	if c.String() != "6789abcdef" {
		t.Errorf("kept %q", c.String())
	}
}
