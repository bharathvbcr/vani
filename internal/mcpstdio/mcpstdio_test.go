package mcpstdio

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/mcpstdio/mcpfake"
)

func TestMain(m *testing.M) {
	if os.Getenv(mcpfake.EnvVar) != "" {
		mcpfake.Run()
		return
	}
	os.Exit(m.Run())
}

func fakeConfig(env ...string) Config {
	return Config{
		BinaryPath:   os.Args[0],
		Env:          append([]string{mcpfake.EnvVar + "=1"}, env...),
		CallTimeout:  5 * time.Second,
		StartTimeout: 10 * time.Second,
	}
}

func startFake(t *testing.T, cfg Config) *Session {
	t.Helper()
	s, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *Session, name string, args string) (Result, error) {
	t.Helper()
	var a jsontext.Value
	if args != "" {
		a = jsontext.Value(args)
	}
	return s.CallTool(context.Background(), name, a)
}

func TestHandshakeListAndCall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	s := startFake(t, fakeConfig(mcpfake.EnvLog+"="+logPath))
	if info := s.Info(); info.Name != "mcpfake" || info.Version != "1.0" || info.Protocol != "2025-06-18" {
		t.Fatalf("Info = %+v", info)
	}
	list, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if list.Truncated || len(list.Tools) < 10 {
		t.Fatalf("list = %+v", list)
	}
	if _, err := call(t, s, "read_text_json", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logPath)
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		id := "-"
		if m.ID != nil {
			id = string(rune('0' + *m.ID))
		}
		got = append(got, m.Method+"#"+id)
	}
	want := []string{"initialize#1", "notifications/initialized#-", "tools/list#2", "tools/call#3"}
	if !slices.Equal(got, want) {
		t.Fatalf("wire order = %v, want %v", got, want)
	}
}

func TestReadOnlyIsTheJSONLiteralTrueOnly(t *testing.T) {
	s := startFake(t, fakeConfig())
	list, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"read_struct": true, "writer": false, "no_annotations": false, "string_hint": false, "hint_missing": false,
	} {
		tool, ok := list.Find(name)
		if !ok || tool.ReadOnly != want {
			t.Errorf("%s: found %v read_only %v, want %v", name, ok, tool.ReadOnly, want)
		}
	}
	if _, ok := list.Find("nope"); ok {
		t.Error("Find found a tool the server did not list")
	}
}

func TestResultKindsPassBytesThrough(t *testing.T) {
	s := startFake(t, fakeConfig())
	for _, tc := range []struct {
		tool, kind, has string
	}{
		{"read_struct", KindStructured, `"ok":false`},
		{"read_text_json", KindTextJSON, `"n":1.50`}, // 1.50 survives: never decoded and re-encoded
		{"read_text", KindText, `"plain words, not JSON"`},
		{"read_multi", KindContent, `"second"`},
	} {
		r, err := call(t, s, tc.tool, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		if r.Kind != tc.kind || !strings.Contains(string(r.Value), tc.has) || !r.Value.IsValid() {
			t.Errorf("%s: kind %q value %s", tc.tool, r.Kind, r.Value)
		}
	}
}

func TestToolFailuresAreErrors(t *testing.T) {
	s := startFake(t, fakeConfig())
	_, err := call(t, s, "read_iserror", "")
	var te *ToolError
	if !errors.As(err, &te) || te.Code != 0 || !strings.HasPrefix(te.Message, "boom boom") || len(te.Message) > errMessageBytes+len("...[truncated]") {
		t.Fatalf("isError: %v", err)
	}
	_, err = call(t, s, "read_rpcerror", "")
	if !errors.As(err, &te) || te.Code != -32602 || !strings.Contains(te.Message, "repo_path") {
		t.Fatalf("rpc error: %v", err)
	}
	for _, name := range []string{"read_empty", "read_null_result"} {
		if _, err := call(t, s, name, ""); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
	// The session is still usable after each failure.
	if _, err := call(t, s, "read_struct", ""); err != nil {
		t.Fatal(err)
	}
}

func TestArgumentsArrive(t *testing.T) {
	s := startFake(t, fakeConfig())
	r, err := call(t, s, "read_echo", `{"repo_path":"/x","n":2}`)
	if err != nil || !strings.Contains(string(r.Value), `"repo_path":"/x"`) {
		t.Fatalf("%s %v", r.Value, err)
	}
	r, err = call(t, s, "read_echo", "")
	if err != nil || !strings.Contains(string(r.Value), `"args":{}`) {
		t.Fatalf("nil args must send {}: %s %v", r.Value, err)
	}
}

func TestStartFailures(t *testing.T) {
	if _, err := Start(context.Background(), Config{}); err == nil {
		t.Fatal("empty BinaryPath accepted")
	}
	if _, err := Start(context.Background(), Config{BinaryPath: "/nonexistent/server"}); !errors.Is(err, ErrServerGone) {
		t.Fatalf("missing binary: %v", err)
	}
	if _, err := Start(context.Background(), fakeConfig(mcpfake.EnvMode+"="+mcpfake.ModeExitEarly)); !errors.Is(err, ErrServerGone) || !strings.Contains(err.Error(), "before the handshake") {
		t.Fatalf("early exit: %v", err)
	}
	var te *ToolError
	if _, err := Start(context.Background(), fakeConfig(mcpfake.EnvMode+"="+mcpfake.ModeInitError)); !errors.As(err, &te) {
		t.Fatalf("initialize error must fail closed: %v", err)
	}
	cfg := fakeConfig(mcpfake.EnvMode + "=" + mcpfake.ModeInitHang)
	cfg.StartTimeout = 300 * time.Millisecond
	start := time.Now()
	if _, err := Start(context.Background(), cfg); !errors.Is(err, ErrTimeout) {
		t.Fatalf("silent server: %v", err)
	}
	if time.Since(start) > 2*closeGrace+2*time.Second {
		t.Fatalf("a hung start took %v", time.Since(start))
	}
	// cat echoes the request: its id matches but it carries a method, so it is
	// not a response and the start times out rather than accepting it.
	cfg = Config{BinaryPath: "/bin/cat", Args: []string{"-"}, StartTimeout: 300 * time.Millisecond}
	if _, err := Start(context.Background(), cfg); !errors.Is(err, ErrTimeout) {
		t.Fatalf("echoing server: %v", err)
	}
}

func TestListingShapes(t *testing.T) {
	ctx := context.Background()
	s := startFake(t, fakeConfig(mcpfake.EnvMode+"="+mcpfake.ModeEmptyTools))
	if l, err := s.ListTools(ctx); err != nil || len(l.Tools) != 0 || l.Truncated {
		t.Fatalf("empty: %+v %v", l, err)
	}
	s = startFake(t, fakeConfig(mcpfake.EnvMode+"="+mcpfake.ModePaged))
	l, err := s.ListTools(ctx)
	if err != nil || l.Truncated {
		t.Fatalf("paged: %v", err)
	}
	if _, ok := l.Find("writer"); !ok || len(l.Tools) < 10 {
		t.Fatalf("paged listing lost the second page: %d tools", len(l.Tools))
	}
	s = startFake(t, fakeConfig(mcpfake.EnvMode+"="+mcpfake.ModeEndlessPages))
	if l, err := s.ListTools(ctx); err != nil || !l.Truncated || len(l.Tools) != MaxToolPages {
		t.Fatalf("endless: %d tools truncated %v err %v", len(l.Tools), l.Truncated, err)
	}
	for _, mode := range []string{mcpfake.ModePagedLoop, mcpfake.ModeDupTools, mcpfake.ModeBadList} {
		s = startFake(t, fakeConfig(mcpfake.EnvMode+"="+mode))
		if _, err := s.ListTools(ctx); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: err = %v, want ErrMalformed", mode, err)
		}
	}
	cfg := fakeConfig(mcpfake.EnvMode + "=" + mcpfake.ModeListHang)
	cfg.CallTimeout = 200 * time.Millisecond
	s = startFake(t, cfg)
	if _, err := s.ListTools(ctx); !errors.Is(err, ErrTimeout) {
		t.Fatalf("hung listing: %v", err)
	}
}

func TestCallTimeoutThenLateResponseIgnored(t *testing.T) {
	cfg := fakeConfig()
	cfg.CallTimeout = 200 * time.Millisecond
	s := startFake(t, cfg)
	if _, err := call(t, s, "read_hang", ""); !errors.Is(err, ErrTimeout) {
		t.Fatalf("hang: %v", err)
	}
	r, err := call(t, s, "read_struct", "")
	if err != nil || r.Kind != KindStructured {
		t.Fatalf("next call: %+v %v", r, err)
	}
}

func TestOversizedLineIsErrorAndStreamResyncs(t *testing.T) {
	cfg := fakeConfig()
	cfg.MaxLineBytes = 64 << 10
	s := startFake(t, cfg)
	if _, err := call(t, s, "read_big", ""); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("big: %v", err)
	}
	if r, err := call(t, s, "read_struct", ""); err != nil || r.Kind != KindStructured {
		t.Fatalf("stream did not resync: %+v %v", r, err)
	}
}

func TestServerDiesMidCall(t *testing.T) {
	s := startFake(t, fakeConfig())
	if _, err := call(t, s, "read_die", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("die: %v", err)
	}
	if _, err := call(t, s, "read_struct", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("call on a dead server: %v", err)
	}
	if _, err := s.ListTools(context.Background()); !errors.Is(err, ErrServerGone) {
		t.Fatalf("list on a dead server: %v", err)
	}
}

func TestStderrIsBoundedAndTheTailReported(t *testing.T) {
	s := startFake(t, fakeConfig())
	_, err := call(t, s, "read_stderr", "")
	if !errors.Is(err, ErrServerGone) {
		t.Fatalf("err = %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(s.Stderr(), "TAILMARK") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.Stderr(); !strings.HasSuffix(got, "TAILMARK") || len(got) > stderrRingBytes {
		t.Fatalf("stderr len %d", len(got))
	}
	if len(err.Error()) > errMessageBytes+200 {
		t.Fatalf("error text unbounded: %d", len(err.Error()))
	}
}

func TestChildEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("VANI_SECRET_TOKEN", "hunter2")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	s := startFake(t, fakeConfig())
	r, err := call(t, s, "read_env", "")
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Env []string `json:"env"` }
	if err := json.Unmarshal(r.Value, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range out.Env {
		switch k {
		case "PATH", "HOME", "TMPDIR", mcpfake.EnvVar:
		default:
			t.Errorf("unexpected env key %q in child (all: %v)", k, out.Env)
		}
	}
}

func TestCloseIsBoundedAndIdempotent(t *testing.T) {
	s, err := Start(context.Background(), fakeConfig())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s.Close()
	if time.Since(start) > 2*closeGrace+time.Second {
		t.Fatalf("Close took %v", time.Since(start))
	}
	s.Close()
	if _, err := call(t, s, "read_struct", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("after Close: %v", err)
	}
}

// A server that stops reading stdin fills the pipe: a request still being
// written when the call's deadline ends returns ErrTimeout and kills the
// process instead of blocking forever, and the next call sees ErrServerGone.
func TestWriteToServerThatStopsReadingIsBounded(t *testing.T) {
	cfg := fakeConfig()
	cfg.CallTimeout = 500 * time.Millisecond
	s := startFake(t, cfg)
	if _, err := call(t, s, "read_stopread", ""); err != nil {
		t.Fatalf("stopread: %v", err)
	}
	big := `{"x":"` + strings.Repeat("x", 4<<20) + `"}`
	done := make(chan error, 1)
	go func() {
		_, err := call(t, s, "read_struct", big)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("blocked write: %v", err)
		}
	case <-time.After(4*closeGrace + 5*time.Second):
		t.Fatal("a blocked write hung the call")
	}
	if _, err := call(t, s, "read_struct", ""); !errors.Is(err, ErrServerGone) {
		t.Fatalf("after the kill: %v", err)
	}
}

func TestCancellationIsReturnedAsIs(t *testing.T) {
	s := startFake(t, fakeConfig())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := s.CallTool(ctx, "read_hang", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestBoundsCutOnRuneBoundaries(t *testing.T) {
	long := strings.Repeat("é", errMessageBytes)
	if b := bound(long); !strings.HasSuffix(b, "...[truncated]") || !utf8Valid(b) {
		t.Fatalf("bound: %q", b[len(b)-20:])
	}
	if b := boundTail(long + "END"); !strings.HasSuffix(b, "END") || !utf8Valid(b) || len(b) > errMessageBytes+len("[truncated]...") {
		t.Fatalf("boundTail: %d", len(b))
	}
	if bound("short") != "short" || boundTail("short") != "short" {
		t.Fatal("short strings must pass unchanged")
	}
	r := &ringBuffer{max: 8}
	r.Write([]byte("0123456789"))
	r.Write([]byte("ab"))
	if r.String() != "456789ab" {
		t.Fatalf("ring %q", r.String())
	}
}

func TestConfigBounds(t *testing.T) {
	c := Config{CallTimeout: time.Hour, StartTimeout: time.Hour}.withDefaults()
	if c.CallTimeout != MaxCallTimeout || c.StartTimeout != MaxStartTimeout || c.MaxLineBytes != DefaultMaxLineBytes || c.ClientName != "vani" || c.Args != nil {
		t.Fatalf("%+v", c)
	}
	c = Config{}.withDefaults()
	if c.CallTimeout != DefaultCallTimeout || c.StartTimeout != DefaultStartTimeout {
		t.Fatalf("%+v", c)
	}
}

func utf8Valid(s string) bool { return jsontext.Value(`"` + s + `"`).IsValid() }
