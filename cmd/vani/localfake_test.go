package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// The test binary doubles as a fake llama-server when VANI_FAKE_LLAMA is set,
// so runOnce can start a real child process through internal/localserver.
func TestMain(m *testing.M) {
	if os.Getenv("VANI_FAKE_LLAMA") != "" {
		fakeLlama()
		return
	}
	os.Exit(m.Run())
}

func fakeLlama() {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
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

// writeLlama writes an executable at p: this test binary in fake-server mode
// when ok, else a binary that exits at once. Start passes a minimal env, so
// the mode is baked into the script.
func writeLlama(t *testing.T, p string, ok bool) {
	t.Helper()
	script := "#!/bin/sh\necho 'load_model: failed' >&2\nexit 1\n"
	if ok {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		script = fmt.Sprintf("#!/bin/sh\nVANI_FAKE_LLAMA=1 exec %q \"$@\"\n", self)
	}
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

// One app runs many times under the daemon. A run whose local server fails to
// start must run without the shadow, never with the last run's stopped server.
func TestLocalServerEndpointDoesNotOutliveItsRun(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	t.Setenv("PATH", t.TempDir()) // no lightpanda: the page board is unreadable
	bin := filepath.Join(t.TempDir(), "llama-server")
	writeLlama(t, bin, true)
	model := filepath.Join(t.TempDir(), "d1.gguf")
	if err := os.WriteFile(model, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Decide.JevModel = ""
	cfg.Decide.Local = &config.Local{Model: "test-d1", Server: &config.LocalServer{
		Binary: bin, ModelPath: model, StartTimeoutMS: 20000}}
	cfg.Decide.Shadow = "local"
	cfg.Watchlist = []sources.Board{{ATS: posting.Page, Token: "initech-careers", Company: "Initech",
		URL: "https://careers.initech.example/jobs", LinkPattern: `^https://careers\.initech\.example/jobs/\d+$`}}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "careers.initech.example")
	var warn bytes.Buffer
	app, err := wireApp(cfg, t.TempDir(), &warn)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()

	if err := app.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "no board could be read") {
		t.Fatalf("run 1: err = %v", err)
	}
	if w := warn.String(); strings.Contains(w, "local decision model") || strings.Contains(w, `shadow "local" unavailable`) {
		t.Fatalf("run 1 lost its shadow:\n%s", w)
	}

	writeLlama(t, bin, false)
	warn.Reset()
	if err := app.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "no board could be read") {
		t.Fatalf("run 2: err = %v", err)
	}
	for _, want := range []string{"local decision model not started", `shadow "local" unavailable`} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("run 2 warnings lack %q (a stale endpoint was reused?):\n%s", want, warn.String())
		}
	}
}
