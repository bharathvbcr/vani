package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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
	// No test reads the real Keychain: keychain_test.go clears this per test
	// and replaces keychainRead.
	_ = os.Setenv(noKeychainEnv, "1")
	keychainRead = func(context.Context, string) (string, error) {
		panic("a test reached the real Keychain read; set keychainRead")
	}
	os.Exit(m.Run())
}

func fakeLlama() {
	if p := os.Getenv("VANI_FAKE_LLAMA_ARGS"); p != "" {
		_ = os.WriteFile(p, []byte(strings.Join(os.Args[1:], " ")), 0o600)
	}
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
	mux.HandleFunc("/v1/systemone", fakeSystemOne)
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		<-sig
		_ = srv.Close()
	}()
	_ = srv.ListenAndServe()
}

// fakeSystemOne is a deterministic decision model: every question gets its
// alphabetically first option at probability 0.95; a posting whose text says
// FAILME gets a server error.
func fakeSystemOne(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Questions map[string]struct {
			Criteria map[string]string `json:"criteria"`
		} `json:"questions"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.Contains(req.State, "FAILME") {
		w.WriteHeader(500)
		return
	}
	answers := map[string]any{}
	for name, q := range req.Questions {
		var opts []string
		for o := range q.Criteria {
			opts = append(opts, o)
		}
		sort.Strings(opts)
		answers[name] = map[string]any{"choice": opts[0], "confidence": 0.95, "probabilities": map[string]float64{opts[0]: 0.95}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
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

// The configured batch and slot settings reach llama-server's command line,
// next to the Metal-only flags; a model like vllm-d3 cannot answer without them.
func TestLocalServerGetsBatchAndParallel(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	t.Setenv("PATH", t.TempDir())
	bin := filepath.Join(t.TempDir(), "llama-server")
	argsFile := filepath.Join(t.TempDir(), "args")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nVANI_FAKE_LLAMA=1 VANI_FAKE_LLAMA_ARGS=%q exec %q \"$@\"\n", argsFile, self)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "d3.gguf")
	if err := os.WriteFile(model, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.LLM = testGeminiLLM
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Local = &config.Local{Model: "test-d3", Server: &config.LocalServer{
		Binary: bin, ModelPath: model, StartTimeoutMS: 20000, BatchSize: 8192, Parallel: 1}}
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
	_ = app.RunOnce(context.Background()) // no board is readable; the server still starts
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the server was never started: %v\n%s", err, warn.String())
	}
	for _, want := range []string{"-b 8192 -ub 8192", "-np 1", "--device MTL0 -ngl all"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("llama-server args %q lack %q", got, want)
		}
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
	cfg.LLM = testGeminiLLM
	cfg.Decide.OpenRouterModel = ""
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
	if w := warn.String(); strings.Contains(w, "local decision model") || strings.Contains(w, `shadow "local-d1-combined" unavailable`) {
		t.Fatalf("run 1 lost its shadow:\n%s", w)
	}

	writeLlama(t, bin, false)
	warn.Reset()
	if err := app.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "no board could be read") {
		t.Fatalf("run 2: err = %v", err)
	}
	for _, want := range []string{"local decision model not started", `shadow "local-d1-combined" unavailable`} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("run 2 warnings lack %q (a stale endpoint was reused?):\n%s", want, warn.String())
		}
	}
}
