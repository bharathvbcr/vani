package main

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/mcpstdio"
)

// vani mcp asks a local MCP server directly, without an agent host's MCP
// connection: it starts the server's binary, lists or calls one tool, prints
// one JSON value and ends the process. It is how an agent reads GitPulse or
// DevMap when the host's connection to them is down.
//
// Only a fixed table of owner-installed servers can be started, and only tools
// the server itself marks read-only (annotations.readOnlyHint, the JSON
// literal true) can be called. There is no flag that allows a writer: over MCP
// the host asks before each tool, and a shell command must not get around that.

// mcpServerSpec is one server vani mcp may start.
type mcpServerSpec struct {
	Name    string
	About   string
	resolve func() (binary string, args []string, err error)
	// env is added to the child's minimal environment. The fixed table sets
	// none; a test's fake server needs its switch.
	env []string
}

// gitpulseAppMCP is where GitPulse.app ships its MCP server.
const gitpulseAppMCP = "/Applications/GitPulse.app/Contents/MacOS/gitpulse-mcp"

// mcpStartTimeout bounds a server's start and handshake. Tests shorten it.
var mcpStartTimeout = mcpstdio.DefaultStartTimeout

// mcpLookPath resolves a server binary on PATH. Tests replace it.
var mcpLookPath = exec.LookPath

// mcpServers is the fixed table. No config key or environment variable adds to
// it: a server's readOnlyHint is the write boundary, so only binaries the owner
// installed are trusted to declare it. Tests replace the table.
var mcpServers = []mcpServerSpec{
	{"gitpulse", "GitPulse: worktrees, agent sessions, collisions, change context, ledger, task board (read tools only)", resolveGitPulseMCP, nil},
	{"devmap", "DevMap code graph: status, search, explore, impact, trace, dead symbols, affected tests", resolveDevMapMCP, nil},
}

func resolveGitPulseMCP() (string, []string, error) {
	if st, err := os.Stat(gitpulseAppMCP); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
		return gitpulseAppMCP, nil, nil
	}
	p, err := mcpLookPath("gitpulse-mcp")
	if err != nil {
		return "", nil, fmt.Errorf("gitpulse-mcp is neither at %s nor on PATH", gitpulseAppMCP)
	}
	return p, nil, nil
}

func resolveDevMapMCP() (string, []string, error) {
	p, err := mcpLookPath("devmap")
	if err != nil {
		return "", nil, errors.New("devmap is not on PATH")
	}
	return p, []string{"mcp"}, nil
}

// Bounds of vani mcp, beyond mcpstdio's own.
const (
	mcpMaxArgsBytes   = 1 << 20
	mcpDefaultTimeout = 30 * time.Second
	mcpMinTimeout     = time.Second
	mcpDescBytes      = 300
	mcpErrRunes       = 400
)

// Codes vani mcp's failures carry.
const (
	codeMCPUnknownServer = "mcp_unknown_server"
	codeMCPUnavailable   = "mcp_unavailable"
	codeMCPTimeout       = "mcp_timeout"
	codeMCPTooLarge      = "mcp_too_large"
	codeMCPMalformed     = "mcp_malformed"
	codeMCPUnknownTool   = "mcp_unknown_tool"
	codeMCPIncomplete    = "mcp_tools_incomplete"
	codeMCPWriteRefused  = "mcp_write_refused"
	codeMCPToolError     = "mcp_tool_error"
)

// mcpRefusal is a failure vani mcp decides itself, with its code.
type mcpRefusal struct{ code, msg string }

func (e *mcpRefusal) Error() string { return e.msg }

// mcpCode is the envelope code of a vani mcp failure.
func mcpCode(err error) string {
	var r *mcpRefusal
	var te *mcpstdio.ToolError
	switch {
	case errors.As(err, &r):
		return r.code
	case errors.Is(err, mcpstdio.ErrTimeout):
		return codeMCPTimeout
	case errors.Is(err, mcpstdio.ErrResponseTooLarge):
		return codeMCPTooLarge
	case errors.Is(err, mcpstdio.ErrMalformed):
		return codeMCPMalformed
	case errors.As(err, &te):
		return codeMCPToolError
	case errors.Is(err, mcpstdio.ErrServerGone):
		return codeMCPUnavailable
	default:
		return envelopeCode(err)
	}
}

// mcpFail writes err as the one {"error","code"} reply. The message is one
// line, bounded, with secret-looking values replaced: it can carry the tail of
// a server's stderr.
func (e *env) mcpFail(err error) error {
	if errors.As(err, new(usageError)) || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return e.writeReply(errorReply{redactSecrets(oneLine(err.Error(), mcpErrRunes)), mcpCode(err)}, err)
}

// cmdMCP is vani mcp servers|tools|call.
func (e *env) cmdMCP(args []string) error {
	if len(args) == 0 {
		return usagef("mcp: expected subcommand servers, tools or call")
	}
	switch args[0] {
	case "servers":
		return e.cmdMCPServers(args[1:])
	case "tools":
		return e.cmdMCPTools(args[1:])
	case "call":
		return e.cmdMCPCall(args[1:])
	}
	return usagef("mcp: unknown subcommand %q (want servers, tools or call)", args[0])
}

// parseInterleaved parses flags that may come before, between or after the
// positional arguments, and returns the positionals in order. "--" ends the
// flags; everything after it is positional.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := parsePositional(fs, args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if len(args) > len(rest) && args[len(args)-len(rest)-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func mcpServerByName(name string) (mcpServerSpec, error) {
	var names []string
	for _, s := range mcpServers {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return mcpServerSpec{}, &mcpRefusal{codeMCPUnknownServer, fmt.Sprintf("mcp: no server named %q (vani mcp servers lists them: %s)", oneLine(name, 60), strings.Join(names, ", "))}
}

type mcpServerRow struct {
	Name      string   `json:"name"`
	About     string   `json:"about"`
	Available bool     `json:"available"`
	Binary    string   `json:"binary,omitempty"`
	Args      []string `json:"args,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type mcpServersReply struct {
	Servers []mcpServerRow `json:"servers"`
}

func (e *env) cmdMCPServers(args []string) error {
	fs := e.newFlags("mcp servers")
	fs.Bool("json", true, "accepted for the caller's sake: the reply is always one JSON object")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: vani mcp servers [--json]

The servers vani mcp can start, and whether each one's binary is installed.
Nothing is started.
`)
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	out := mcpServersReply{Servers: []mcpServerRow{}}
	for _, s := range mcpServers {
		row := mcpServerRow{Name: s.Name, About: s.About}
		bin, a, err := s.resolve()
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Available, row.Binary, row.Args = true, bin, a
		}
		out.Servers = append(out.Servers, row)
	}
	return e.writeReply(out, nil)
}

// mcpTimeoutFlag declares --timeout and checks its range after parsing.
func mcpTimeoutFlag(fs *flag.FlagSet) *time.Duration {
	return fs.Duration("timeout", mcpDefaultTimeout, fmt.Sprintf("bound on the listing and on the call (%v to %v); starting the server has its own %v", mcpMinTimeout, mcpstdio.MaxCallTimeout, mcpstdio.DefaultStartTimeout))
}

func checkMCPTimeout(d time.Duration) error {
	if d < mcpMinTimeout || d > mcpstdio.MaxCallTimeout {
		return usagef("--timeout %v is outside %v to %v", d, mcpMinTimeout, mcpstdio.MaxCallTimeout)
	}
	return nil
}

// mcpSession resolves and starts the named server.
func mcpSession(ctx context.Context, name string, timeout time.Duration) (*mcpstdio.Session, string, error) {
	spec, err := mcpServerByName(name)
	if err != nil {
		return nil, "", err
	}
	bin, args, err := spec.resolve()
	if err != nil {
		return nil, "", &mcpRefusal{codeMCPUnavailable, "mcp " + name + ": " + err.Error()}
	}
	s, err := mcpstdio.Start(ctx, mcpstdio.Config{BinaryPath: bin, Args: args, Env: spec.env, StartTimeout: mcpStartTimeout, CallTimeout: timeout})
	if err != nil {
		var te *mcpstdio.ToolError
		if errors.As(err, &te) {
			// An initialize the server refused: it cannot be asked anything.
			return nil, bin, &mcpRefusal{codeMCPUnavailable, fmt.Sprintf("mcp %s: %v", name, err)}
		}
		return nil, bin, fmt.Errorf("mcp %s: %w", name, err)
	}
	return s, bin, nil
}

type mcpToolRow struct {
	Name        string          `json:"name"`
	ReadOnly    bool            `json:"read_only"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type mcpToolsReply struct {
	Server     string              `json:"server"`
	Binary     string              `json:"binary"`
	ServerInfo mcpstdio.ServerInfo `json:"server_info"`
	Tools      []mcpToolRow        `json:"tools"`
	Count      int                 `json:"count"`
	ReadOnly   int                 `json:"read_only"`
	// Truncated means the server had more tools than vani mcp reads: Tools is
	// the first of them, not all.
	Truncated bool `json:"truncated"`
}

func (e *env) cmdMCPTools(args []string) error {
	fs := e.newFlags("mcp tools")
	fs.Bool("json", true, "accepted for the caller's sake: the reply is always one JSON object")
	schema := fs.String("schema", "", "list only this tool, with its input schema")
	timeout := mcpTimeoutFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: vani mcp tools SERVER [--schema TOOL] [--timeout D] [--json]

The tools SERVER lists, each with read_only (the server's readOnlyHint): only
those vani mcp call will run. --schema TOOL replies with that tool alone and
its input schema.

`)
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("mcp tools: expected one SERVER (vani mcp servers lists them), got %d arguments", len(pos))
	}
	if err := checkMCPTimeout(*timeout); err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	s, bin, err := mcpSession(ctx, pos[0], *timeout)
	if err != nil {
		return e.mcpFail(err)
	}
	defer s.Close()
	list, err := s.ListTools(ctx)
	if err != nil {
		return e.mcpFail(fmt.Errorf("mcp %s: %w", pos[0], err))
	}
	out := mcpToolsReply{Server: pos[0], Binary: bin, ServerInfo: s.Info(), Tools: []mcpToolRow{}, Truncated: list.Truncated}
	for _, t := range list.Tools {
		if *schema != "" && t.Name != *schema {
			continue
		}
		row := mcpToolRow{Name: t.Name, ReadOnly: t.ReadOnly, Description: oneLine(t.Description, mcpDescBytes)}
		if *schema != "" && len(t.InputSchema) > 0 {
			row.InputSchema = json.RawMessage(t.InputSchema)
		}
		out.Tools = append(out.Tools, row)
		if t.ReadOnly {
			out.ReadOnly++
		}
	}
	out.Count = len(out.Tools)
	if *schema != "" && out.Count == 0 {
		return e.mcpFail(unknownToolErr(pos[0], *schema, list.Truncated))
	}
	return e.writeReply(out, nil)
}

func unknownToolErr(server, tool string, truncated bool) error {
	if truncated {
		return &mcpRefusal{codeMCPIncomplete, fmt.Sprintf("mcp %s: %q is not among the first tools the server listed, and the listing was cut short, so whether it exists is unknown", server, oneLine(tool, 80))}
	}
	return &mcpRefusal{codeMCPUnknownTool, fmt.Sprintf("mcp %s: no tool named %q (vani mcp tools %s lists them)", server, oneLine(tool, 80), server)}
}

type mcpCallReply struct {
	Server     string          `json:"server"`
	Tool       string          `json:"tool"`
	ResultKind string          `json:"result_kind"`
	Result     json.RawMessage `json:"result"`
}

// mcpInput is where --args-file - reads. Tests replace it.
var mcpInput io.Reader = os.Stdin

func (e *env) cmdMCPCall(args []string) error {
	fs := e.newFlags("mcp call")
	fs.Bool("json", true, "accepted for the caller's sake: the reply is always one JSON object")
	argsJSON := fs.String("args", "", "the tool's arguments: one JSON object (default {})")
	argsFile := fs.String("args-file", "", "read the arguments object from this file, or - for stdin")
	timeout := mcpTimeoutFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: vani mcp call SERVER TOOL [--args JSON | --args-file PATH|-] [--timeout D] [--json]

Start SERVER, call TOOL once, and print {"server","tool","result_kind","result"}:
result is the tool's payload as the server sent it. result_kind is structured
(structuredContent), text_json (one text block holding JSON), text (one text
block that is not JSON, as a string) or content (the whole content array).

Only a tool the server marks read-only runs; any other is refused with
mcp_write_refused before anything is sent to it, and no flag changes that.

`)
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("mcp call: expected SERVER and TOOL, got %d arguments", len(pos))
	}
	if err := checkMCPTimeout(*timeout); err != nil {
		return err
	}
	toolArgs, err := mcpCallArgs(*argsJSON, *argsFile)
	if err != nil {
		return err
	}
	server, tool := pos[0], pos[1]
	ctx, stop := signalContext()
	defer stop()
	s, _, err := mcpSession(ctx, server, *timeout)
	if err != nil {
		return e.mcpFail(err)
	}
	defer s.Close()
	list, err := s.ListTools(ctx)
	if err != nil {
		return e.mcpFail(fmt.Errorf("mcp %s: the tool list, read to check %s is read-only: %w", server, oneLine(tool, 80), err))
	}
	t, ok := list.Find(tool)
	if !ok {
		return e.mcpFail(unknownToolErr(server, tool, list.Truncated))
	}
	if !t.ReadOnly {
		return e.mcpFail(&mcpRefusal{codeMCPWriteRefused, fmt.Sprintf("mcp %s: %s is not marked read-only by the server, so vani mcp does not run it; use the MCP host, which asks before each tool", server, tool)})
	}
	res, err := s.CallTool(ctx, tool, toolArgs)
	if err != nil {
		return e.mcpFail(fmt.Errorf("mcp %s %s: %w", server, tool, err))
	}
	return e.writeReply(mcpCallReply{Server: server, Tool: tool, ResultKind: res.Kind, Result: json.RawMessage(res.Value)}, nil)
}

// mcpCallArgs reads and checks the arguments object: at most mcpMaxArgsBytes,
// one JSON object with no duplicate names. Nil means none were given.
func mcpCallArgs(inline, path string) (jsontext.Value, error) {
	var raw []byte
	switch {
	case inline != "" && path != "":
		return nil, usagef("mcp call: give --args or --args-file, not both")
	case inline != "":
		raw = []byte(inline)
	case path != "":
		var r io.Reader = mcpInput
		if path != "-" {
			f, err := os.Open(path)
			if err != nil {
				return nil, usagef("mcp call: --args-file: %v", err)
			}
			defer f.Close()
			r = f
		}
		b, err := io.ReadAll(io.LimitReader(r, mcpMaxArgsBytes+1))
		if err != nil {
			return nil, usagef("mcp call: read --args-file: %v", err)
		}
		raw = b
	default:
		return nil, nil
	}
	if len(raw) > mcpMaxArgsBytes {
		return nil, usagef("mcp call: the arguments are over %d bytes", mcpMaxArgsBytes)
	}
	v := jsontext.Value(raw)
	var obj map[string]jsontext.Value
	if v.Kind() != '{' || jsonv2.Unmarshal(v, &obj) != nil {
		return nil, usagef("mcp call: the arguments must be one JSON object with no name given twice, like {\"repo_path\":\"/abs/path\"}")
	}
	return v, nil
}
