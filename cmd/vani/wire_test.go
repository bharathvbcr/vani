package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/decide"
)

func clearKeys(t *testing.T) {
	for _, k := range []string{"OPENROUTER_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
}

// The metered provider path builds without any network: nothing streams until
// a request is made. This is the one path that spends money, so its
// construction (credential resolve, daily ledger, price table) is pinned here.
func TestMeteredProviderConstruction(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}

	p, err := w.provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Capability(cfg.LLM.Model); !ok {
		t.Fatalf("provider does not serve the default model %q", cfg.LLM.Model)
	}
	bp, ok := p.(*budget.Provider)
	if !ok {
		t.Fatalf("provider is %T, want *budget.Provider (metered)", p)
	}
	prices, ok := bp.PriceOf(cfg.LLM.Model)
	if !ok || prices.Input != config.GeminiInputNanoUSD || prices.Output != config.GeminiOutputNanoUSD {
		t.Fatalf("prices %+v ok=%v", prices, ok)
	}
	if got := bp.Ledger.Limit(); got != int64(cfg.Decide.DailyUSD.LLM*1e9) {
		t.Fatalf("ledger limit %d", got)
	}
	if !strings.HasPrefix(filepath.Base(bp.Ledger.Path()), "llm-") {
		t.Fatalf("ledger path %q is not a per-day file", bp.Ledger.Path())
	}
}

// A run with no usable decision backend fails before reading any board, so it
// neither spends requests nor records postings it cannot process.
func TestRunOnceFailsFastWithoutBackend(t *testing.T) {
	clearKeys(t)
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "boards-api.greenhouse.io")
	var warn bytes.Buffer
	app, err := wireApp(cfg, t.TempDir(), &warn)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	err = app.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no decision backend") {
		t.Fatalf("err = %v", err)
	}
	if n := len(app.Ledger.All()); n != 0 {
		t.Fatalf("%d postings recorded before failing", n)
	}
	if strings.Contains(warn.String(), "board gh/acme") {
		t.Fatalf("a board was read: %s", warn.String())
	}
}

func TestLightpandaBinaryResolution(t *testing.T) {
	if got, err := lightpandaBinary("/opt/lp/lightpanda"); err != nil || got != "/opt/lp/lightpanda" {
		t.Fatalf("configured: %q %v", got, err)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := lightpandaBinary(""); err == nil || !strings.Contains(err.Error(), "set lightpanda_binary") {
		t.Fatalf("missing binary: err = %v", err)
	}
	bin := filepath.Join(dir, "lightpanda")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := lightpandaBinary("")
	if err != nil || got != bin || !filepath.IsAbs(got) {
		t.Fatalf("on PATH: %q %v", got, err)
	}
}

// pageSource starts a loopback proxy for the run and stop closes it; no
// lightpanda process runs until a page board is listed.
func TestPageSourceStartsAndStopsProxy(t *testing.T) {
	c, err := egress.New(egress.Config{AllowedHosts: []string{"careers.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	w := &wired{cfg: config.Config{LightpandaBinary: "/opt/lp/lightpanda"}, warn: &bytes.Buffer{}}
	src, stop, err := w.pageSource(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if src.ATS() != posting.Page {
		t.Fatalf("ATS %s", src.ATS())
	}
	stop()

	t.Setenv("PATH", t.TempDir())
	w.cfg.LightpandaBinary = ""
	if _, _, err := w.pageSource(context.Background(), c); err == nil {
		t.Fatal("pageSource with no lightpanda binary succeeded")
	}
}

// Whatever page_interval_ms is, including off, runs are paced by robots.txt
// Crawl-delay on top of it; out of range, it is refused rather than clamped.
func TestPageRunnerInterval(t *testing.T) {
	c, err := egress.New(egress.Config{AllowedHosts: []string{"careers.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []time.Duration{0, 5 * time.Second, page.MaxInterval} {
		run, err := pageRunner("/opt/lp/lightpanda", d, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := run.(*page.Paced); !ok {
			t.Fatalf("interval %s: %T, want *page.Paced", d, run)
		}
	}
	for _, d := range []time.Duration{-time.Millisecond, page.MaxInterval + time.Millisecond} {
		if _, err := pageRunner("/opt/lp/lightpanda", d, c); err == nil || !strings.Contains(err.Error(), "page_interval_ms") {
			t.Errorf("interval %s: %v", d, err)
		}
	}
	if _, err := pageRunner("/opt/lp/lightpanda", 0, nil); err == nil {
		t.Error("pageRunner without a doer succeeded")
	}

	w := &wired{cfg: config.Config{LightpandaBinary: "/opt/lp/lightpanda", PageIntervalMS: 5000}, warn: &bytes.Buffer{}}
	_, stop, err := w.pageSource(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	w.cfg.PageIntervalMS = 60_000 // a config that skipped Validate
	if _, _, err := w.pageSource(context.Background(), c); err == nil {
		t.Fatal("pageSource accepted an out-of-range interval")
	}
}

// A page board whose browser is missing is skipped with a warning, never
// treated as an empty listing; with no other board, the run fails as unread.
func TestRunOnceSkipsPageBoardWithoutLightpanda(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	t.Setenv("PATH", t.TempDir())
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.Watchlist = []sources.Board{{ATS: posting.Page, Token: "initech-careers", Company: "Initech",
		URL: "https://careers.initech.example/jobs", LinkPattern: `^https://careers\.initech\.example/jobs/\d+$`}}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "careers.initech.example")
	var warn bytes.Buffer
	app, err := wireApp(cfg, t.TempDir(), &warn)
	if err != nil {
		t.Fatal(err)
	}
	defer app.close()
	err = app.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no board could be read") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"careers-page boards disabled", "board pg/initech-careers: careers-page source disabled this run"} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("warnings lack %q:\n%s", want, warn.String())
		}
	}
	if n := len(app.Ledger.All()); n != 0 {
		t.Fatalf("%d postings recorded", n)
	}
}

// Lappi is the shadow until promoted, then first in the chain. Building the
// runner never dials the agent.
func TestLappiShadowThenPrimary(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Lappi = &config.Lappi{Socket: "/tmp/vani-test-no-agent.sock"}
	cfg.Decide.Shadow = "lappi"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	router := func(cfg config.Config) *decide.Router {
		t.Helper()
		w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
		r, err := w.runner(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		tr, ok := r.Triager.(*jdtriage.Triager)
		if !ok {
			t.Fatalf("triager is %T", r.Triager)
		}
		rt, ok := tr.D.(*decide.Router)
		if !ok {
			t.Fatalf("decider is %T", tr.D)
		}
		return rt
	}
	rt := router(cfg)
	if got := strings.Join(rt.Backends(), ","); got != "llm" || rt.Shadow == nil || rt.Shadow.Name() != "lappi" {
		t.Fatalf("shadow mode: chain %q shadow %v", got, rt.Shadow)
	}
	cfg.Decide.Shadow = ""
	cfg.Decide.Lappi.Primary = true
	rt = router(cfg)
	if got := strings.Join(rt.Backends(), ","); got != "lappi,llm" || rt.Shadow != nil {
		t.Fatalf("primary mode: chain %q shadow %v", got, rt.Shadow)
	}
}

// A local decision model is the shadow only, never in the chain, and is asked
// over loopback with the state as a plain string under its own name.
func TestLocalModelIsAShadowOverLoopback(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a key was sent to the local model")
		}
		_, _ = io.WriteString(w, `{"answers":{"work_mode":{"choice":"remote","confidence":0.9}}}`)
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Local = &config.Local{Endpoint: srv.URL + "/v1/systemone", Model: "test-d1", Questions: "per_slot"}
	cfg.Decide.Shadow = "local"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	r, err := w.runner(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if got := strings.Join(rt.Backends(), ","); got != "llm" || rt.Shadow == nil || rt.Shadow.Name() != "local-test-d1-per_slot" {
		t.Fatalf("chain %q shadow %v", got, rt.Shadow)
	}
	q := jdtriage.Question(posting.Posting{Key: "k", Title: "Engineer", Description: "Fully remote."})
	a, err := rt.Shadow.Decide(context.Background(), q)
	if err != nil || a.Backend != "local-test-d1-per_slot" || a.Slots["work_mode"].Value != "remote" {
		t.Fatalf("answer %+v err %v", a, err)
	}
	var sent struct {
		Model     string                                   `json:"model"`
		State     string                                   `json:"state"`
		Questions map[string]struct{ Instructions string } `json:"questions"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("state is not a plain string: %v\n%s", err, body)
	}
	if sent.Model != "test-d1" || !strings.Contains(sent.State, "Fully remote.") {
		t.Errorf("sent %+v", sent)
	}
	for _, f := range jdtriage.Families { // per_slot: each slot asked its family's question
		if sent.Questions[f.Slot.Name].Instructions != f.Question {
			t.Errorf("%s instructions %q", f.Slot.Name, sent.Questions[f.Slot.Name].Instructions)
		}
	}
}

// With local.server, the runner uses only the endpoint runOnce started; before
// a server is up there is no shadow, never a guessed address.
func TestLocalServerModeUsesTheStartedEndpoint(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"answers":{"work_mode":{"choice":"remote","confidence":0.9}}}`)
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Local = &config.Local{Model: "test-d1",
		Server: &config.LocalServer{Binary: "/opt/llama/llama-server", ModelPath: "/opt/models/d1.gguf"}}
	cfg.Decide.Shadow = "local"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &warn}
	r, err := w.runner(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt := r.Triager.(*jdtriage.Triager).D.(*decide.Router); rt.Shadow != nil {
		t.Fatalf("shadow %s with no server started", rt.Shadow.Name())
	}
	if !strings.Contains(warn.String(), "local decision model disabled") {
		t.Fatalf("warnings: %s", warn.String())
	}

	w.localEndpoint = srv.URL + "/v1/systemone"
	r, err = w.runner(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if rt.Shadow == nil || rt.Shadow.Name() != "local-d1-combined" {
		t.Fatalf("shadow %v", rt.Shadow)
	}
	q := jdtriage.Question(posting.Posting{Key: "k", Title: "Engineer", Description: "Fully remote."})
	if a, err := rt.Shadow.Decide(context.Background(), q); err != nil || a.Slots["work_mode"].Value != "remote" || hits != 1 {
		t.Fatalf("answer %+v err %v hits %d", a, err, hits)
	}
}

// A local server that cannot start costs the run its shadow, not the run:
// it goes on to read boards (here, to the unreadable-board failure).
func TestRunOnceGoesOnWhenTheLocalServerFails(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	t.Setenv("PATH", t.TempDir())
	cfg := config.Default()
	cfg.Decide.OpenRouterModel = ""
	cfg.Decide.Local = &config.Local{Model: "test-d1", Server: &config.LocalServer{
		Binary: filepath.Join(t.TempDir(), "no-such-llama-server"), ModelPath: "/opt/models/d1.gguf", StartTimeoutMS: 2000}}
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
	err = app.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no board could be read") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"local decision model not started", `shadow "local-d1-combined" unavailable`} {
		if !strings.Contains(warn.String(), want) {
			t.Fatalf("warnings lack %q:\n%s", want, warn.String())
		}
	}
}

func TestHasPageBoard(t *testing.T) {
	w := &wired{cfg: config.Config{Watchlist: []sources.Board{{ATS: posting.Greenhouse}}}}
	if w.hasPageBoard() {
		t.Fatal("greenhouse-only watchlist reports a page board")
	}
	w.cfg.Watchlist = append(w.cfg.Watchlist, sources.Board{ATS: posting.Page})
	if !w.hasPageBoard() {
		t.Fatal("page board not seen")
	}
}
