package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/mcpstdio"
	"github.com/bharathvbcr/Vani/internal/mcpstdio/mcpfake"
)

// withFakeMCP replaces the server table with one server, "fake", that is this
// test binary acting as mcpfake in mode (none when ""). It returns the file
// the fake logs every received line to.
func withFakeMCP(t *testing.T, mode string) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "mcp.log")
	env := []string{mcpfake.EnvVar + "=1", mcpfake.EnvLog + "=" + logPath}
	if mode != "" {
		env = append(env, mcpfake.EnvMode+"="+mode)
	}
	saved, savedStart, savedInput := mcpServers, mcpStartTimeout, mcpInput
	mcpServers = []mcpServerSpec{
		{"fake", "the test fake", func() (string, []string, error) { return os.Args[0], nil, nil }, env},
		{"absent", "not installed", func() (string, []string, error) { return "", nil, errors.New("absent is not on PATH") }, nil},
		{"gone", "a path that does not exist", func() (string, []string, error) { return "/nonexistent/mcp-server", nil, nil }, nil},
	}
	mcpStartTimeout = 5 * time.Second
	t.Cleanup(func() { mcpServers, mcpStartTimeout, mcpInput = saved, savedStart, savedInput })
	return logPath
}

// oneJSON decodes stdout as exactly one JSON object.
func oneJSON(t *testing.T, args []string, stdout string) map[string]json.RawMessage {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var m map[string]json.RawMessage
	if err := dec.Decode(&m); err != nil || dec.More() {
		t.Fatalf("vani %v: stdout %q is not one JSON object (%v)", args, stdout, err)
	}
	return m
}

func str(t *testing.T, m map[string]json.RawMessage, k string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(m[k], &s); err != nil {
		t.Fatalf("%s: %s is not a string: %s", k, m[k], err)
	}
	return s
}

// mcpDo runs one vani mcp command and checks the exit code and that stdout
// is one JSON object.
func mcpDo(t *testing.T, x *isoEnv, exit int, args ...string) map[string]json.RawMessage {
	t.Helper()
	code, o, e := x.do(args...)
	if code != exit {
		t.Fatalf("vani %v: exit %d, want %d\nstdout: %s\nstderr: %s", args, code, exit, o, e)
	}
	return oneJSON(t, args, o)
}

func sentCall(t *testing.T, logPath string) bool {
	t.Helper()
	b, _ := os.ReadFile(logPath)
	return strings.Contains(string(b), `"method":"tools/call"`)
}

func TestMCPCallReadOnlyPassesThePayloadThrough(t *testing.T) {
	withFakeMCP(t, "")
	x := newIsoEnv(t)
	for _, tc := range []struct {
		tool, kind, has string
	}{
		{"read_struct", "structured", `"scan shed"`},
		{"read_text_json", "text_json", `"n":1.50`},
		{"read_text", "text", `"plain words, not JSON"`},
		{"read_multi", "content", `"second"`},
	} {
		for _, args := range [][]string{
			{"--json", "mcp", "call", "fake", tc.tool},
			{"mcp", "call", "fake", tc.tool},
			{"mcp", "call", "--timeout", "5s", "fake", "--json", tc.tool},
		} {
			m := mcpDo(t, x, exitOK, args...)
			if str(t, m, "result_kind") != tc.kind || str(t, m, "server") != "fake" || str(t, m, "tool") != tc.tool {
				t.Errorf("%v: %v", args, m)
			}
			if !strings.Contains(string(m["result"]), tc.has) {
				t.Errorf("%v: result %s lacks %s", args, m["result"], tc.has)
			}
			if tc.tool == "read_struct" {
				// A facet that failed stays failed: nothing is summarised.
				var r struct {
					Facets map[string]struct {
						OK    *bool  `json:"ok"`
						Error string `json:"error"`
					} `json:"facets"`
				}
				if err := json.Unmarshal(m["result"], &r); err != nil {
					t.Fatalf("%v: %v", args, err)
				}
				a, b := r.Facets["a"], r.Facets["b"]
				if a.OK == nil || *a.OK || a.Error != "scan shed" || b.OK == nil || !*b.OK {
					t.Errorf("%v: facets %s", args, m["result"])
				}
			}
		}
	}
}

func TestMCPCallSendsTheArguments(t *testing.T) {
	withFakeMCP(t, "")
	x := newIsoEnv(t)
	m := mcpDo(t, x, exitOK, "--json", "mcp", "call", "fake", "read_echo", "--args", `{"repo_path":"/abs/repo","n":2}`)
	if !strings.Contains(string(m["result"]), `"repo_path":"/abs/repo"`) {
		t.Fatalf("result %s", m["result"])
	}
	mcpInput = strings.NewReader(`{"from":"stdin"}`)
	m = mcpDo(t, x, exitOK, "--json", "mcp", "call", "fake", "read_echo", "--args-file", "-")
	if !strings.Contains(string(m["result"]), `"from":"stdin"`) {
		t.Fatalf("stdin args: %s", m["result"])
	}
	p := filepath.Join(t.TempDir(), "args.json")
	os.WriteFile(p, []byte(`{"from":"file"}`), 0o600)
	m = mcpDo(t, x, exitOK, "--json", "mcp", "call", "fake", "read_echo", "--args-file", p)
	if !strings.Contains(string(m["result"]), `"from":"file"`) {
		t.Fatalf("file args: %s", m["result"])
	}
	m = mcpDo(t, x, exitOK, "--json", "mcp", "call", "fake", "read_echo")
	if !strings.Contains(string(m["result"]), `"args":{}`) {
		t.Fatalf("no args must send {}: %s", m["result"])
	}
}

// A tool the server does not mark read-only with the JSON literal true is
// refused before tools/call is sent, whatever the flags.
func TestMCPCallRefusesWritersBeforeSending(t *testing.T) {
	logPath := withFakeMCP(t, "")
	x := newIsoEnv(t)
	for _, tool := range []string{"writer", "no_annotations", "string_hint", "hint_missing"} {
		m := mcpDo(t, x, exitRuntime, "--json", "mcp", "call", "fake", tool, "--args", "{}")
		if str(t, m, "code") != codeMCPWriteRefused || !strings.Contains(str(t, m, "error"), tool) {
			t.Errorf("%s: %v", tool, m)
		}
	}
	if sentCall(t, logPath) {
		t.Fatal("a tools/call reached the server for a refused tool")
	}
	// No flag turns it off.
	code, _, _ := x.do("--json", "mcp", "call", "fake", "writer", "--allow-write")
	if code != exitUsage || sentCall(t, logPath) {
		t.Fatalf("--allow-write: exit %d", code)
	}
}

func TestMCPFailureCodes(t *testing.T) {
	withFakeMCP(t, "")
	x := newIsoEnv(t)
	for _, tc := range []struct {
		args []string
		code string
		has  string
	}{
		{[]string{"call", "nobody", "read_struct"}, codeMCPUnknownServer, "fake, absent, gone"},
		{[]string{"call", "absent", "read_struct"}, codeMCPUnavailable, "not on PATH"},
		{[]string{"call", "gone", "read_struct"}, codeMCPUnavailable, "/nonexistent/mcp-server"},
		{[]string{"tools", "gone"}, codeMCPUnavailable, "/nonexistent/mcp-server"},
		{[]string{"call", "fake", "nope"}, codeMCPUnknownTool, "vani mcp tools fake"},
		{[]string{"call", "fake", "read_iserror"}, codeMCPToolError, "boom"},
		{[]string{"call", "fake", "read_rpcerror"}, codeMCPToolError, "repo_path is required"},
		{[]string{"call", "fake", "read_empty"}, codeMCPMalformed, "neither structuredContent nor content"},
		{[]string{"call", "fake", "read_die"}, codeMCPUnavailable, "gone"},
		{[]string{"call", "fake", "read_hang", "--timeout", "1s"}, codeMCPTimeout, "timed out"},
		{[]string{"tools", "fake", "--schema", "nope"}, codeMCPUnknownTool, "no tool named"},
	} {
		args := append([]string{"--json", "mcp"}, tc.args...)
		m := mcpDo(t, x, exitRuntime, args...)
		if str(t, m, "code") != tc.code || !strings.Contains(str(t, m, "error"), tc.has) {
			t.Errorf("%v: %v", tc.args, m)
		}
	}
}

func TestMCPServerSideFailuresAtStart(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{
		{mcpfake.ModeInitError, codeMCPUnavailable},
		{mcpfake.ModeExitEarly, codeMCPUnavailable},
		{mcpfake.ModeListHang, codeMCPTimeout},
		{mcpfake.ModeBadList, codeMCPMalformed},
		{mcpfake.ModeDupTools, codeMCPMalformed},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			logPath := withFakeMCP(t, tc.mode)
			x := newIsoEnv(t)
			m := mcpDo(t, x, exitRuntime, "--json", "mcp", "call", "fake", "read_struct", "--timeout", "1s")
			if str(t, m, "code") != tc.code {
				t.Fatalf("%v", m)
			}
			if sentCall(t, logPath) {
				t.Fatal("a tool was called on a server whose listing failed")
			}
		})
	}
	t.Run("init_hang", func(t *testing.T) {
		withFakeMCP(t, mcpfake.ModeInitHang)
		mcpStartTimeout = 300 * time.Millisecond
		x := newIsoEnv(t)
		m := mcpDo(t, x, exitRuntime, "--json", "mcp", "tools", "fake")
		if str(t, m, "code") != codeMCPTimeout {
			t.Fatalf("%v", m)
		}
	})
}

// A listing cut short cannot say a tool does not exist.
func TestMCPTruncatedListing(t *testing.T) {
	withFakeMCP(t, mcpfake.ModeEndlessPages)
	x := newIsoEnv(t)
	m := mcpDo(t, x, exitOK, "--json", "mcp", "tools", "fake")
	if string(m["truncated"]) != "true" || string(m["count"]) != fmt.Sprint(mcpstdio.MaxToolPages) {
		t.Fatalf("%v", m)
	}
	m = mcpDo(t, x, exitRuntime, "--json", "mcp", "call", "fake", "read_struct")
	if str(t, m, "code") != codeMCPIncomplete {
		t.Fatalf("%v", m)
	}
	// One found on the pages read still runs.
	mcpDo(t, x, exitOK, "--json", "mcp", "call", "fake", "read_page_1")
}

func TestMCPEmptyToolList(t *testing.T) {
	withFakeMCP(t, mcpfake.ModeEmptyTools)
	x := newIsoEnv(t)
	m := mcpDo(t, x, exitOK, "--json", "mcp", "tools", "fake")
	if string(m["count"]) != "0" || string(m["tools"]) != "[]" {
		t.Fatalf("%v", m)
	}
	m = mcpDo(t, x, exitRuntime, "--json", "mcp", "call", "fake", "anything")
	if str(t, m, "code") != codeMCPUnknownTool {
		t.Fatalf("%v", m)
	}
}

func TestMCPTools(t *testing.T) {
	withFakeMCP(t, "")
	x := newIsoEnv(t)
	m := mcpDo(t, x, exitOK, "--json", "mcp", "tools", "fake")
	var rows []mcpToolRow
	if err := json.Unmarshal(m["tools"], &rows); err != nil {
		t.Fatal(err)
	}
	ro := 0
	for _, r := range rows {
		if r.InputSchema != nil {
			t.Errorf("%s: a schema without --schema", r.Name)
		}
		if r.ReadOnly {
			ro++
		}
		if (r.Name == "writer" || r.Name == "string_hint") && r.ReadOnly {
			t.Errorf("%s listed read-only", r.Name)
		}
	}
	if string(m["count"]) != fmt.Sprint(len(rows)) || string(m["read_only"]) != fmt.Sprint(ro) || ro == 0 || ro == len(rows) {
		t.Fatalf("count %s read_only %s rows %d ro %d", m["count"], m["read_only"], len(rows), ro)
	}
	if !strings.Contains(string(m["server_info"]), `"name":"mcpfake"`) {
		t.Fatalf("server_info %s", m["server_info"])
	}
	m = mcpDo(t, x, exitOK, "--json", "mcp", "tools", "fake", "--schema", "read_echo")
	if string(m["count"]) != "1" || !strings.Contains(string(m["tools"]), `"input_schema":{"type":"object"}`) {
		t.Fatalf("--schema: %v", m)
	}
}

// The tail of a dying server's stderr reaches the reply bounded, on one line,
// with secret-looking values replaced.
func TestMCPStderrInErrorsIsRedactedAndBounded(t *testing.T) {
	withFakeMCP(t, "")
	x := newIsoEnv(t)
	m := mcpDo(t, x, exitRuntime, "--json", "mcp", "call", "fake", "read_stderr")
	msg := str(t, m, "error")
	if strings.Contains(msg, "abcdefghijklmnop0123") || strings.Contains(msg, "sk-abcdefghijklmnopqrstuvwx") {
		t.Fatalf("a secret reached the reply: %s", msg)
	}
	if strings.Contains(msg, "\n") || len([]rune(msg)) > mcpErrRunes+1 {
		t.Fatalf("message not one bounded line: %d runes", len([]rune(msg)))
	}
}

func TestMCPUsageErrors(t *testing.T) {
	logPath := withFakeMCP(t, "")
	x := newIsoEnv(t)
	big := filepath.Join(t.TempDir(), "big.json")
	os.WriteFile(big, []byte(`{"x":"`+strings.Repeat("x", mcpMaxArgsBytes)+`"}`), 0o600)
	for _, args := range [][]string{
		{"mcp"},
		{"mcp", "bogus"},
		{"mcp", "tools"},
		{"mcp", "tools", "fake", "extra"},
		{"mcp", "call", "fake"},
		{"mcp", "call", "fake", "read_echo", "extra"},
		{"mcp", "call", "fake", "read_echo", "--args", "[1]"},
		{"mcp", "call", "fake", "read_echo", "--args", "{"},
		{"mcp", "call", "fake", "read_echo", "--args", `{"a":1,"a":2}`},
		{"mcp", "call", "fake", "read_echo", "--args", "{}", "--args-file", "-"},
		{"mcp", "call", "fake", "read_echo", "--args-file", big},
		{"mcp", "call", "fake", "read_echo", "--args-file", "/nonexistent/args.json"},
		{"mcp", "call", "fake", "read_echo", "--timeout", "0s"},
		{"mcp", "call", "fake", "read_echo", "--timeout", "10m"},
		{"mcp", "servers", "extra"},
	} {
		code, o, _ := x.do(args...)
		if code != exitUsage {
			t.Errorf("vani %v: exit %d, want %d", args, code, exitUsage)
		}
		if o != "" {
			t.Errorf("vani %v: a usage error under the command's own form wrote %q", args, o)
		}
		g := append([]string{"--json"}, args...)
		code, o, _ = x.do(g...)
		if code != exitUsage || onlyError(t, g, o).Code != codeUsage {
			t.Errorf("vani %v: exit %d stdout %q", g, code, o)
		}
	}
	if sentCall(t, logPath) {
		t.Fatal("a usage error reached the server")
	}
}

func TestMCPServers(t *testing.T) {
	withFakeMCP(t, "")
	x := newIsoEnv(t)
	m := mcpDo(t, x, exitOK, "--json", "mcp", "servers")
	var out mcpServersReply
	if err := json.Unmarshal([]byte(mustMarshal(t, m)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Servers) != 3 || !out.Servers[0].Available || out.Servers[1].Available || out.Servers[1].Error == "" {
		t.Fatalf("%+v", out)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The shipped table is the two owner-installed servers, resolved where they
// are installed, and nothing reads a server from config or the environment.
func TestMCPShippedTable(t *testing.T) {
	var names []string
	for _, s := range mcpServers {
		names = append(names, s.Name)
		if s.env != nil {
			t.Errorf("%s: the shipped table adds environment", s.Name)
		}
	}
	if strings.Join(names, ",") != "gitpulse,devmap" {
		t.Fatalf("table %v", names)
	}
	saved := mcpLookPath
	t.Cleanup(func() { mcpLookPath = saved })
	mcpLookPath = func(name string) (string, error) { return "/fake/bin/" + name, nil }
	bin, args, err := resolveDevMapMCP()
	if err != nil || bin != "/fake/bin/devmap" || len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("devmap: %s %v %v", bin, args, err)
	}
	mcpLookPath = func(string) (string, error) { return "", errors.New("not found") }
	if _, _, err := resolveDevMapMCP(); err == nil {
		t.Fatal("devmap resolved with nothing on PATH")
	}
	if bin, args, err := resolveGitPulseMCP(); err == nil && (bin != gitpulseAppMCP || args != nil) {
		t.Fatalf("gitpulse resolved to %s %v with nothing on PATH", bin, args)
	}
}

func TestMCPCodeMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("x: %w", mcpstdio.ErrTimeout), codeMCPTimeout},
		{fmt.Errorf("x: %w", mcpstdio.ErrResponseTooLarge), codeMCPTooLarge},
		{fmt.Errorf("x: %w", mcpstdio.ErrMalformed), codeMCPMalformed},
		{fmt.Errorf("x: %w", mcpstdio.ErrServerGone), codeMCPUnavailable},
		{fmt.Errorf("x: %w", &mcpstdio.ToolError{Message: "m"}), codeMCPToolError},
		{&mcpRefusal{codeMCPWriteRefused, "w"}, codeMCPWriteRefused},
		{errors.New("other"), codeRuntime},
	} {
		if got := mcpCode(tc.err); got != tc.code {
			t.Errorf("%v: %s, want %s", tc.err, got, tc.code)
		}
	}
}
