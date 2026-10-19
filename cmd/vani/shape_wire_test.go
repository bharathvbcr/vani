package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/llm/gemini"
	"github.com/bharathvbcr/Manvi/manvi/llm/transport"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

// catalogueLLM answers Capability from Manvi's real Anthropic catalogue and
// records what it is sent, standing in for the network adapter.
type catalogueLLM struct{ draftLLM }

func (c *catalogueLLM) Name() string { return anthropic.Name }
func (c *catalogueLLM) Capability(model string) (llm.Capability, bool) {
	return anthropic.Capability(model)
}

// AttemptGateSupported mirrors the real adapter's promise.
func (c *catalogueLLM) AttemptGateSupported() bool { return true }

// budget.Provider refuses an inner provider that does not promise the attempt
// gate. pricedProvider wraps the real adapters in production, so it must pass
// that promise (and the other optional contracts) through.
func TestPricedProviderForwardsTheRealAdaptersContracts(t *testing.T) {
	noKey := func() (credentials.Secret, error) { return credentials.Secret{}, errors.New("no key in tests") }
	for _, inner := range []llm.Provider{anthropic.New("", noKey), gemini.New("", noKey)} {
		var p any = pricedProvider{Provider: inner}
		g, ok := p.(transport.AttemptGatedProvider)
		if !ok || !g.AttemptGateSupported() {
			t.Errorf("%s: the attempt-gate promise is hidden; budget.Provider would refuse every request", inner.Name())
		}
		if _, ok := p.(budget.MaxTokensReporter); !ok {
			t.Errorf("%s: AppliedMaxTokens not forwarded", inner.Name())
		}
	}
	// The anthropic adapter fills in an unset bound; the forwarder must report its value.
	a := anthropic.New("", noKey)
	req := llm.Request{Model: "claude-opus-5-5"}
	if got, want := (pricedProvider{Provider: a}).AppliedMaxTokens(req), a.AppliedMaxTokens(req); got != want {
		t.Errorf("AppliedMaxTokens = %d, adapter says %d", got, want)
	}
	if !(pricedProvider{Provider: a}).RejectionUnbilled(400) && a.RejectionUnbilled(400) {
		t.Error("RejectionUnbilled not forwarded")
	}
}

// sseMessages plays Anthropic's Messages streaming API on loopback and keeps
// the request body it was sent.
func sseMessages(t *testing.T, text string) (url string, bodies *[]string) {
	t.Helper()
	url, bodies, _ = sseServer(t, text)
	return url, bodies
}

// sseServer is sseMessages that also records each request's headers.
func sseServer(t *testing.T, text string) (url string, bodies *[]string, headers *[]http.Header) {
	t.Helper()
	var got []string
	var hdrs []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		hdrs = append(hdrs, r.Header.Clone())
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":120,"output_tokens":1}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(text) + `}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":30}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got, &hdrs
}

// anthropicWired is a wired whose config selects the Anthropic adapter, with a
// test key in the environment.
func anthropicWired(t *testing.T) *wired {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-real")
	cfg := config.Config{}
	cfg.LLM = config.LLM{Provider: "anthropic", Model: "claude-opus-5-5", InputNanoUSDPerToken: 4000, OutputNanoUSDPerToken: 20000}
	cfg.Decide.DailyUSD.LLM = 10
	return &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
}

// An API key not scoped to a workspace is refused by Anthropic with a 400
// unless every request names one (anthropic-workspace-id). Manvi reads it from
// ANTHROPIC_WORKSPACE_ID, and its endpoint from ANTHROPIC_BASE_URL; Vani's
// provider() must honour both, as Manvi's own CLI does.
func TestProviderSendsTheConfiguredWorkspace(t *testing.T) {
	ctx := context.Background()
	url, _, headers := sseServer(t, "ok")
	w := anthropicWired(t)
	t.Setenv("ANTHROPIC_BASE_URL", url)
	t.Setenv(credentials.AnthropicWorkspaceEnv, "wrkspc_01TestWorkspace")
	p, err := w.provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Model: "claude-opus-5-5", MaxTokens: 64, System: "s",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "q"}}}}}
	llmbackend.Shape(p, &req)
	if _, err := llmbackend.Turn(ctx, p, req); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if len(*headers) != 1 {
		t.Fatalf("%d requests reached the configured base URL, want 1", len(*headers))
	}
	if got := (*headers)[0].Get(anthropic.WorkspaceHeader); got != "wrkspc_01TestWorkspace" {
		t.Errorf("%s = %q, want the configured workspace", anthropic.WorkspaceHeader, got)
	}
}

// No workspace configured means no header: right for a workspace-scoped key.
func TestProviderSendsNoWorkspaceWhenUnset(t *testing.T) {
	ctx := context.Background()
	url, _, headers := sseServer(t, "ok")
	w := anthropicWired(t)
	t.Setenv("ANTHROPIC_BASE_URL", url)
	t.Setenv(credentials.AnthropicWorkspaceEnv, "")
	p, err := w.provider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Model: "claude-opus-5-5", MaxTokens: 64, System: "s",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "q"}}}}}
	llmbackend.Shape(p, &req)
	if _, err := llmbackend.Turn(ctx, p, req); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if len(*headers) != 1 || (*headers)[0].Get(anthropic.WorkspaceHeader) != "" {
		t.Errorf("headers = %v, want one request without %s", *headers, anthropic.WorkspaceHeader)
	}
}

// A malformed workspace id fails when the provider is built, naming the
// variable, before any request is sent or billed.
func TestProviderRefusesAMalformedWorkspace(t *testing.T) {
	url, bodies, _ := sseServer(t, "ok")
	w := anthropicWired(t)
	t.Setenv("ANTHROPIC_BASE_URL", url)
	for _, bad := range []string{"wrkspc 01", "wrkspc_01\r\nX-Injected: 1", strings.Repeat("w", 129)} {
		t.Setenv(credentials.AnthropicWorkspaceEnv, bad)
		_, err := w.provider(context.Background())
		if err == nil || !strings.Contains(err.Error(), credentials.AnthropicWorkspaceEnv) {
			t.Errorf("workspace %q: err = %v, want a refusal naming %s", bad, err, credentials.AnthropicWorkspaceEnv)
		}
	}
	if len(*bodies) != 0 {
		t.Errorf("%d requests sent for a malformed workspace", len(*bodies))
	}
}

// The production chain, end to end on loopback: budget.Provider over
// pricedProvider over the real Anthropic adapter. Before the fixes this failed
// twice over: pricedProvider hid the adapter's attempt-gate promise (budget
// refused every request), and temperature 0 was refused by the adapter's
// pre-flight check.
func TestRealAnthropicAdapterThroughTheMeteredChain(t *testing.T) {
	ctx := context.Background()
	url, bodies := sseMessages(t, "A short note.")
	key := func() (credentials.Secret, error) { return credentials.NewSecret("test-key-not-real", "test"), nil }
	l, err := budget.Open(ctx, filepath.Join(t.TempDir(), "llm.json"), int64(10*1e9))
	if err != nil {
		t.Fatal(err)
	}
	p := &budget.Provider{Inner: pricedProvider{Provider: anthropic.New(url, key), prices: budget.Prices{Rates: budget.Rates{Input: 4000, Output: 20000, CacheWrite: 4000, CacheRead: 4000}}}, Ledger: l}
	req := llm.Request{Model: "claude-opus-5-5", MaxTokens: 256, System: "s",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "q"}}}}}
	llmbackend.Shape(p, &req)
	text, err := llmbackend.Turn(ctx, p, req)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if text != "A short note." {
		t.Errorf("text = %q", text)
	}
	if len(*bodies) != 1 || strings.Contains((*bodies)[0], "temperature") {
		t.Errorf("sent bodies = %v", *bodies)
	}
	snap, err := l.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(snap) == fmt.Sprint(budget.Snapshot{}) {
		t.Error("the ledger recorded nothing for a metered call")
	}
}

// The production chain is budget.Provider{pricedProvider{adapter}}. Shape must
// see through both wrappers, or every Opus request still carries temperature
// and Manvi refuses it before sending.
func TestShapeSeesThroughTheMeteredChain(t *testing.T) {
	ctx := context.Background()
	inner := &catalogueLLM{draftLLM{text: "ok"}}
	l, err := budget.Open(ctx, filepath.Join(t.TempDir(), "llm.json"), int64(10*1e9))
	if err != nil {
		t.Fatal(err)
	}
	p := &budget.Provider{Inner: pricedProvider{Provider: inner, prices: budget.Prices{Rates: budget.Rates{Input: 4000, Output: 20000, CacheWrite: 4000, CacheRead: 4000}}}, Ledger: l}

	req := llm.Request{Model: "claude-opus-5-5", MaxTokens: 256,
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "q"}}}}}
	llmbackend.Shape(p, &req)
	if req.Temperature != nil || req.Effort != "low" || req.MaxTokens != 256+llmbackend.ReasoningHeadroom {
		t.Fatalf("shaped through the chain: temp %v effort %q max %d", req.Temperature, req.Effort, req.MaxTokens)
	}
	if err := anthropic.ValidateRequest(req, anthropic.ThinkingDefault); err != nil {
		t.Fatalf("Manvi's pre-flight refuses the shaped request: %v", err)
	}
}
