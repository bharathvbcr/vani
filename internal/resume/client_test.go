package resume

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is the fake DevPrism server: the test binary re-execs
// itself with VANI_FAKE_SERVER=1. It speaks newline-delimited JSON-RPC and
// interleaves noise (a notification, a server request, a stale response) before
// every real response. The jd_text argument selects the behaviour.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("VANI_FAKE_SERVER") != "1" {
		return
	}
	runFakeServer()
	os.Exit(0)
}

func runFakeServer() {
	var logf *os.File
	if p := os.Getenv("VANI_FAKE_LOG"); p != "" {
		logf, _ = os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	}
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if logf != nil {
			logf.Write(append(append([]byte{}, line...), '\n'))
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					JD         string `json:"jd_text"`
					PersonaID  string `json:"persona_id"`
					PageBudget int    `json:"page_budget"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 {
			continue // notification: no reply
		}
		reply := func(result any) {
			send(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{}})
			send(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "ping"})
			send(map[string]any{"jsonrpc": "2.0", "id": 9999, "result": map[string]any{"stale": true}})
			send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
		}
		switch req.Method {
		case "initialize":
			if os.Getenv("VANI_FAKE_INIT") == "ok" {
				reply(map[string]any{"protocolVersion": "x"})
			} else {
				send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID),
					"error": map[string]any{"code": -32601, "message": "Method not found: initialize"}})
			}
		case "tools/list":
			prop := "jd_text"
			if os.Getenv("VANI_FAKE_TOOLS") == "v2" {
				prop = "job_text"
			}
			reply(map[string]any{"tools": []any{
				map[string]any{"name": "resume_score_and_select", "description": "d",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{prop: map[string]any{"type": "string"}}}},
				map[string]any{"name": "resume_gap_analysis", "description": "other words",
					"inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			a := req.Params.Arguments
			switch a.JD {
			case "HANG":
				continue
			case "SLOW":
				time.Sleep(500 * time.Millisecond)
			case "DIE":
				os.Exit(3)
			case "STDERR":
				fmt.Fprint(os.Stderr, strings.Repeat("x", 40000)+"TAILMARK")
				os.Exit(4)
			case "BIG":
				reply(map[string]any{"blob": strings.Repeat("A", 1<<20)})
				continue
			case "ISERR":
				reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": strings.Repeat("boom ", 400)}}})
				continue
			case "RPCERR":
				send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID),
					"error": map[string]any{"code": -32602, "message": "Missing required 'jd_text'"}})
				continue
			case "ENV":
				var keys []string
				for _, kv := range os.Environ() {
					keys = append(keys, strings.SplitN(kv, "=", 2)[0])
				}
				reply(map[string]any{"env": keys})
				continue
			case "WRAPPED":
				b, _ := json.Marshal(gapPayload(a.PageBudget, a.PersonaID))
				reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}})
				continue
			case "STRUCT":
				reply(map[string]any{"structuredContent": gapPayload(a.PageBudget, a.PersonaID), "content": []any{}})
				continue
			case "BADSHAPE":
				reply(map[string]any{"unexpected": 1})
				continue
			}
			switch req.Params.Name {
			case "resume_gap_analysis":
				reply(gapPayload(a.PageBudget, a.PersonaID))
			case "resume_score_and_select":
				reply(map[string]any{"pageBudget": a.PageBudget, "selectedBlocks": []any{
					map[string]any{"blockId": "blk-1", "score": 0.9},
					map[string]any{"blockId": "blk-2", "score": 0.5},
				}})
			case "resume_analyze_jd":
				reply(map[string]any{"profile": map[string]any{"roleTitle": "x"}, "metadata": map[string]any{}})
			default:
				reply(map[string]any{"ok": true})
			}
		}
	}
}

// gapPayload encodes page_budget in the coverage and persona in the first
// must-have so tests can see what the client sent.
func gapPayload(pageBudget int, persona string) map[string]any {
	if persona == "" {
		persona = "default"
	}
	return map[string]any{
		"coveragePercentage": pageBudget * 10,
		"mustHave": map[string]any{"items": []any{
			map[string]any{"skill": persona, "status": "covered"},
			map[string]any{"skill": "rust", "status": "weak", "evidence": []any{}},
			map[string]any{"skill": "cobol", "status": "missing"},
		}},
	}
}

func fakeConfig(t *testing.T, env ...string) Config {
	t.Helper()
	return Config{
		BinaryPath:   os.Args[0],
		Args:         []string{"-test.run=^TestHelperProcess$"},
		Env:          append([]string{"VANI_FAKE_SERVER=1"}, env...),
		CallTimeout:  5 * time.Second,
		StartTimeout: 10 * time.Second,
	}
}

func startFake(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRoundTripMapsGapAndScore(t *testing.T) {
	c := startFake(t, fakeConfig(t))
	ctx := context.Background()

	g, err := c.Gap(ctx, "a job", "ai")
	if err != nil {
		t.Fatal(err)
	}
	// 20 proves page_budget:2 was sent; "ai" proves persona_id was sent.
	want := Gap{CoveragePct: 20, MustHaves: []Coverage{
		{"ai", "covered"}, {"rust", "weak"}, {"cobol", "missing"}}}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("Gap = %+v, want %+v", g, want)
	}

	g, err = c.Gap(ctx, "a job", "")
	if err != nil || g.MustHaves[0].Requirement != "default" {
		t.Fatalf("empty persona must be omitted, got %+v err %v", g, err)
	}

	s, err := c.Score(ctx, "a job", "ai")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.BlockIDs, []string{"blk-1", "blk-2"}) || s.Total != 0 || s.Components != nil {
		t.Fatalf("Score = %+v (Total/Components must stay zero: DevPrism emits none)", s)
	}

	raw, err := c.AnalyzeJD(ctx, "a job")
	if err != nil || !strings.Contains(string(raw), `"profile"`) {
		t.Fatalf("AnalyzeJD = %s, %v", raw, err)
	}
}

func TestHandshakeOrderAndMonotonicIDs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	c := startFake(t, fakeConfig(t, "VANI_FAKE_LOG="+logPath))
	if _, err := c.Gap(context.Background(), "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Gap(context.Background(), "x", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m struct {
			ID      *int   `json:"id"`
			Method  string `json:"method"`
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(l), &m); err != nil || m.JSONRPC != "2.0" {
			t.Fatalf("bad line %q", l)
		}
		id := "-"
		if m.ID != nil {
			id = fmt.Sprint(*m.ID)
		}
		got = append(got, m.Method+"#"+id)
	}
	want := []string{"initialize#1", "notifications/initialized#-", "tools/list#2", "tools/call#3", "tools/call#4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire order = %v, want %v", got, want)
	}
}

func TestInitializeSuccessAlsoAccepted(t *testing.T) {
	c := startFake(t, fakeConfig(t, "VANI_FAKE_INIT=ok"))
	if c.ToolsHash() == "" {
		t.Fatal("no tools hash")
	}
}

func TestToolsHashPin(t *testing.T) {
	c := startFake(t, fakeConfig(t))
	pin := c.ToolsHash()
	if len(pin) != 64 {
		t.Fatalf("hash %q", pin)
	}

	cfg := fakeConfig(t)
	cfg.PinnedToolsHash = pin
	startFake(t, cfg) // matching pin starts

	cfg = fakeConfig(t, "VANI_FAKE_TOOLS=v2")
	cfg.PinnedToolsHash = pin
	if _, err := Start(context.Background(), cfg); !errors.Is(err, ErrToolsChanged) {
		t.Fatalf("schema change: err = %v, want ErrToolsChanged", err)
	}
}

func TestHashToolsIgnoresOrderAndDescriptions(t *testing.T) {
	a, err := hashTools(json.RawMessage(`{"tools":[{"name":"a","description":"x","inputSchema":{"b":1,"a":2}},{"name":"b","inputSchema":{}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := hashTools(json.RawMessage(`{"tools":[{"name":"b","inputSchema":{}},{"name":"a","description":"y","inputSchema":{"a":2,"b":1}}]}`))
	c, _ := hashTools(json.RawMessage(`{"tools":[{"name":"b","inputSchema":{}},{"name":"a","inputSchema":{"a":3,"b":1}}]}`))
	if a != b || a == c {
		t.Fatalf("a=%s b=%s c=%s", a, b, c)
	}
	for _, bad := range []string{`{}`, `{"tools":[]}`, `nope`} {
		if _, err := hashTools(json.RawMessage(bad)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: err = %v", bad, err)
		}
	}
}

func TestCallTimeoutThenLateResponseIgnored(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.CallTimeout = 200 * time.Millisecond
	c := startFake(t, cfg)

	if _, err := c.Gap(context.Background(), "HANG", ""); !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("HANG: err = %v, want ErrCallTimeout", err)
	}
}

func TestLateResponseAfterTimeoutDoesNotAnswerNextCall(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.CallTimeout = 200 * time.Millisecond
	c := startFake(t, cfg)

	if _, err := c.Gap(context.Background(), "SLOW", "late"); !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("SLOW: err = %v, want ErrCallTimeout", err)
	}
	c.cfg.CallTimeout = 5 * time.Second
	g, err := c.Gap(context.Background(), "next", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if g.MustHaves[0].Requirement != "fresh" {
		t.Fatalf("next call was answered by the stale response: %+v", g)
	}
}

func TestCallTimeoutIsCapped(t *testing.T) {
	cfg := Config{CallTimeout: time.Hour, StartTimeout: time.Hour}.withDefaults()
	if cfg.CallTimeout != MaxCallTimeout || cfg.StartTimeout != MaxStartTimeout || cfg.MaxResponseBytes != DefaultMaxResponseBytes {
		t.Fatalf("%+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Args, []string{"--mcp"}) {
		t.Fatalf("default args = %v", cfg.Args)
	}
}

func TestOversizedResponseIsErrorAndStreamResyncs(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.MaxResponseBytes = 64 << 10
	c := startFake(t, cfg)

	if _, err := c.Gap(context.Background(), "BIG", ""); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	g, err := c.Gap(context.Background(), "after", "ok")
	if err != nil || g.MustHaves[0].Requirement != "ok" {
		t.Fatalf("stream did not resync: %+v %v", g, err)
	}
}

func TestServerDiesMidCallThenRestart(t *testing.T) {
	cfg := fakeConfig(t)
	cfg.MaxRestarts = 1
	c := startFake(t, cfg)
	pin := c.ToolsHash()

	if _, err := c.Gap(context.Background(), "DIE", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("DIE: err = %v, want ErrServerGone", err)
	}
	if _, err := c.Gap(context.Background(), "again", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("call on dead server: err = %v, want ErrServerGone", err)
	}
	if err := c.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if c.ToolsHash() != pin {
		t.Fatal("tool hash changed across restart")
	}
	if _, err := c.Gap(context.Background(), "alive", ""); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if err := c.Restart(context.Background()); !errors.Is(err, ErrRestartLimit) {
		t.Fatalf("second restart: err = %v, want ErrRestartLimit", err)
	}
}

func TestRestartRefusesChangedTools(t *testing.T) {
	cfg := fakeConfig(t)
	c := startFake(t, cfg)
	c.cfg.Env = append(append([]string{}, c.cfg.Env...), "VANI_FAKE_TOOLS=v2")
	if err := c.Restart(context.Background()); !errors.Is(err, ErrToolsChanged) {
		t.Fatalf("err = %v, want ErrToolsChanged", err)
	}
	if _, err := c.Gap(context.Background(), "x", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("after refused restart: err = %v", err)
	}
}

func TestToolErrorMapping(t *testing.T) {
	c := startFake(t, fakeConfig(t))

	_, err := c.Gap(context.Background(), "ISERR", "")
	var te *ToolError
	if !errors.As(err, &te) || !strings.HasPrefix(te.Message, "boom boom") {
		t.Fatalf("isError: err = %v", err)
	}
	if len(te.Message) > errMessageBytes+len("...[truncated]") {
		t.Fatalf("message not bounded: %d bytes", len(te.Message))
	}

	_, err = c.Gap(context.Background(), "RPCERR", "")
	if !errors.As(err, &te) || te.Code != -32602 || !strings.Contains(te.Message, "jd_text") {
		t.Fatalf("rpc error: err = %v", err)
	}

	if _, err = c.Gap(context.Background(), "BADSHAPE", ""); !errors.Is(err, ErrMalformed) {
		t.Fatalf("bad shape: err = %v, want ErrMalformed", err)
	}
	if _, err = c.Score(context.Background(), "BADSHAPE", ""); !errors.Is(err, ErrMalformed) {
		t.Fatalf("bad score shape: err = %v, want ErrMalformed", err)
	}
}

func TestWrappedAndStructuredResultsAreUnwrapped(t *testing.T) {
	c := startFake(t, fakeConfig(t))
	for _, jd := range []string{"WRAPPED", "STRUCT"} {
		g, err := c.Gap(context.Background(), jd, "p")
		if err != nil || g.CoveragePct != 20 {
			t.Fatalf("%s: %+v %v", jd, g, err)
		}
	}
}

func TestStderrIsBoundedAndReportedOnDeath(t *testing.T) {
	c := startFake(t, fakeConfig(t))
	_, err := c.Gap(context.Background(), "STDERR", "")
	if !errors.Is(err, ErrServerGone) {
		t.Fatalf("err = %v", err)
	}
	// stderr is drained concurrently; give the copier a moment.
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(c.Stderr(), "TAILMARK") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := c.Stderr()
	if !strings.HasSuffix(got, "TAILMARK") || len(got) > stderrRingBytes {
		t.Fatalf("stderr len %d, suffix ok=%v", len(got), strings.HasSuffix(got, "TAILMARK"))
	}
	if len(err.Error()) > stderrRingBytes+200 {
		t.Fatalf("error text unbounded: %d", len(err.Error()))
	}
}

func TestRingBufferKeepsTail(t *testing.T) {
	r := &ringBuffer{max: 8}
	r.Write([]byte("0123456789"))
	r.Write([]byte("ab"))
	if r.String() != "456789ab" {
		t.Fatalf("%q", r.String())
	}
}

func TestChildEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("VANI_SECRET_TOKEN", "hunter2")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	c := startFake(t, fakeConfig(t))
	raw, err := c.AnalyzeJD(context.Background(), "ENV")
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Env []string }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out.Env)
	for _, k := range out.Env {
		switch k {
		case "PATH", "HOME", "TMPDIR", "VANI_FAKE_SERVER":
		default:
			// Go's runtime/exec may add nothing else; anything here is a leak.
			t.Errorf("unexpected env key %q in child (all: %v)", k, out.Env)
		}
	}
}

func TestCloseIsBoundedAndIdempotent(t *testing.T) {
	c, err := Start(context.Background(), fakeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*closeGrace+time.Second {
		t.Fatalf("Close took %v", time.Since(start))
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Gap(context.Background(), "x", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("after Close: err = %v", err)
	}
}

func TestStartFailures(t *testing.T) {
	if _, err := Start(context.Background(), Config{}); err == nil {
		t.Fatal("empty BinaryPath accepted")
	}
	if _, err := Start(context.Background(), Config{BinaryPath: "/nonexistent/devprism"}); err == nil {
		t.Fatal("missing binary accepted")
	}
	// A server that never answers the handshake must hit StartTimeout.
	cfg := fakeConfig(t)
	cfg.BinaryPath = "/bin/cat" // cat echoes our request back: id matches but method is set, so it is ignored
	cfg.Args = []string{"-"}
	cfg.Env = nil
	cfg.StartTimeout = 300 * time.Millisecond
	if _, err := Start(context.Background(), cfg); !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("silent server: err = %v, want ErrCallTimeout", err)
	}
}

type fakeHeader struct {
	h   Header
	err error
}

func (f fakeHeader) Header(context.Context) (Header, error) { return f.h, f.err }

func TestPIISeamModelArgsCarryNoHeaderFields(t *testing.T) {
	typ := reflect.TypeOf(SynthesizeArgs{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.ToLower(f.Tag.Get("json") + " " + f.Name)
		for _, banned := range []string{"header", "contact", "name", "email", "phone"} {
			if strings.Contains(tag, banned) {
				t.Errorf("SynthesizeArgs.%s (%q) is a PII-shaped field", f.Name, tag)
			}
		}
	}
	b, _ := json.Marshal(SynthesizeArgs{JD: "j", PersonaID: "p", TemplateID: "t"})
	if strings.Contains(string(b), "header_name") || strings.Contains(string(b), "contact_lines") {
		t.Fatalf("model-facing JSON has header keys: %s", b)
	}
}

func TestPIISeamWireRequestCarriesHeader(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	c := startFake(t, fakeConfig(t, "VANI_FAKE_LOG="+logPath))
	src := fakeHeader{h: Header{Name: "Test Person", ContactLines: []string{"test@example.invalid | 000"}}}
	if _, err := c.Synthesize(context.Background(), src, SynthesizeArgs{JD: "j", PersonaID: "ai", TemplateID: "typst-ats-single"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var call struct {
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &call); err != nil {
		t.Fatal(err)
	}
	a := call.Params.Arguments
	if call.Params.Name != "resume_synthesize" || a["header_name"] != "Test Person" ||
		a["page_budget"] != float64(2) || a["jd_text"] != "j" || a["template_id"] != "typst-ats-single" {
		t.Fatalf("wire arguments = %v", a)
	}
	if cl, _ := a["contact_lines"].([]any); len(cl) != 1 || cl[0] != "test@example.invalid | 000" {
		t.Fatalf("contact_lines = %v", a["contact_lines"])
	}
}

func TestSynthesizeFailsClosedWithoutHeader(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	c := startFake(t, fakeConfig(t, "VANI_FAKE_LOG="+logPath))
	ctx := context.Background()
	boom := errors.New("vault locked")
	cases := map[string]HeaderSource{
		"source error": fakeHeader{err: boom},
		"empty name":   fakeHeader{h: Header{ContactLines: []string{"a"}}},
		"too many":     fakeHeader{h: Header{Name: "n", ContactLines: []string{"1", "2", "3", "4", "5"}}},
		"nil source":   nil,
	}
	for name, src := range cases {
		if _, err := c.Synthesize(ctx, src, SynthesizeArgs{JD: "j"}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "resume_synthesize") {
		t.Fatal("a request was sent despite a failed header")
	}
}
