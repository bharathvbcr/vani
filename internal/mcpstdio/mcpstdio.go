// Package mcpstdio is a client for one Model Context Protocol server process
// that speaks newline-delimited JSON-RPC on its stdin and stdout: a session
// starts the binary, does the initialize handshake, lists tools and calls them,
// and ends the process on Close.
//
// It is what `vani mcp` uses to ask a local server (gitpulse-mcp, devmap mcp)
// directly, without the MCP connection of an agent host. Every wait, line and
// listing is bounded; the child gets the minimal environment (childenv); a
// failure is never mistaken for a payload, and a payload is handed back as the
// bytes the server sent, never decoded and re-encoded.
//
// internal/resume (DevPrism) and internal/sources/page (lightpanda) carry their
// own stdio clients with the same transport shape; folding them onto this
// package is docs/FOLLOWUPS.md's "One stdio MCP transport".
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/childenv"
)

// Defaults and ceilings. All are bounds, not targets.
const (
	DefaultStartTimeout = 15 * time.Second
	MaxStartTimeout     = 60 * time.Second
	DefaultCallTimeout  = 30 * time.Second
	MaxCallTimeout      = 120 * time.Second
	// DefaultMaxLineBytes bounds one JSON-RPC line from the server.
	DefaultMaxLineBytes = 8 << 20
	// MaxToolPages bounds how many tools/list pages one listing follows.
	MaxToolPages = 10
	// MaxToolListBytes bounds the pages of one listing together.
	MaxToolListBytes = 8 << 20
	// ProtocolVersion is the version the client offers at initialize.
	ProtocolVersion = "2025-06-18"

	stderrRingBytes = 8 << 10
	errMessageBytes = 512
	closeGrace      = 2 * time.Second
)

var (
	// ErrServerGone means the process exited, closed its pipes, or was closed.
	ErrServerGone = errors.New("mcpstdio: server process is gone")
	// ErrResponseTooLarge means one line, or one listing, exceeded its bound.
	ErrResponseTooLarge = errors.New("mcpstdio: response exceeds size limit")
	// ErrTimeout means a start or a call exceeded its deadline.
	ErrTimeout = errors.New("mcpstdio: timed out")
	// ErrMalformed means the server answered with a shape this client cannot map.
	ErrMalformed = errors.New("mcpstdio: malformed server output")
)

// ToolError is a failure the server reported: a JSON-RPC error object (Code
// set) or a tool result with isError true (Code 0). Message is bounded.
type ToolError struct {
	Code    int64
	Message string
}

func (e *ToolError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("mcpstdio: server error %d: %s", e.Code, e.Message)
	}
	return "mcpstdio: tool error: " + e.Message
}

// Config configures Start. Zero values take the documented defaults.
type Config struct {
	BinaryPath   string
	Args         []string      // passed as is; no default
	StartTimeout time.Duration // default 15s, capped at 60s
	CallTimeout  time.Duration // default 30s, capped at 120s
	MaxLineBytes int           // default 8 MiB
	ClientName   string        // initialize's clientInfo.name; default "vani"
	// Env adds explicit KEY=VALUE entries on top of the minimal PATH, HOME,
	// TMPDIR environment. The process never inherits anything else.
	Env []string
}

func (c Config) withDefaults() Config {
	c.StartTimeout = clamp(c.StartTimeout, DefaultStartTimeout, MaxStartTimeout)
	c.CallTimeout = clamp(c.CallTimeout, DefaultCallTimeout, MaxCallTimeout)
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = DefaultMaxLineBytes
	}
	if c.ClientName == "" {
		c.ClientName = "vani"
	}
	return c
}

func clamp(d, def, max time.Duration) time.Duration {
	switch {
	case d <= 0:
		return def
	case d > max:
		return max
	}
	return d
}

// ServerInfo is what the server said about itself at initialize.
type ServerInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol string `json:"protocol_version"`
}

// Tool is one tool a server lists.
type Tool struct {
	Name        string
	Description string
	InputSchema jsontext.Value
	// ReadOnly is true only when the tool's annotations carry readOnlyHint as
	// the JSON literal true. Missing annotations, a missing hint, or any other
	// value (the string "true" included) is not read-only.
	ReadOnly bool
}

// ToolList is one listing. Truncated means the server had more pages than
// MaxToolPages: Tools is then a prefix of the server's list, not all of it.
type ToolList struct {
	Tools     []Tool
	Truncated bool
}

// Find returns the tool named name.
func (l ToolList) Find(name string) (Tool, bool) {
	for _, t := range l.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Result kinds: how the server shaped a tool's payload.
const (
	// KindStructured is the result's structuredContent.
	KindStructured = "structured"
	// KindTextJSON is a single text block whose text is one JSON value.
	KindTextJSON = "text_json"
	// KindText is a single text block that is not JSON, as a JSON string.
	KindText = "text"
	// KindContent is the content array as sent: more than one block, or a
	// block that is not text. Nothing in it is dropped.
	KindContent = "content"
)

// Result is a tool's payload, as the bytes the server sent it.
type Result struct {
	Kind  string
	Value jsontext.Value
}

// message is one inbound line, or a reader-side failure.
type message struct {
	raw []byte
	err error
}

// proc is the running server and its reader goroutine.
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	msgs   chan message
	done   chan struct{} // closed when stdout ends
	waited chan struct{} // closed once cmd.Wait returned
	quit   chan struct{} // closed by terminate to release the reader
	once   sync.Once
	stderr *ringBuffer
	cancel context.CancelFunc
}

// Session is one started server. Calls are serialised: one stdio channel.
type Session struct {
	cfg    Config
	mu     sync.Mutex
	p      *proc
	nextID int64
	info   ServerInfo
}

// Start spawns the server and performs the handshake. A server that answers
// initialize with an error, or not at all within StartTimeout, is refused.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.BinaryPath == "" {
		return nil, errors.New("mcpstdio: Config.BinaryPath is required")
	}
	s := &Session{cfg: cfg.withDefaults()}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.spawn()
	if err != nil {
		return nil, err
	}
	s.p = p
	hctx, cancel := context.WithTimeout(ctx, s.cfg.StartTimeout)
	defer cancel()
	if err := s.handshakeLocked(hctx); err != nil {
		p.terminate()
		s.p = nil
		return nil, err
	}
	return s, nil
}

// Info is what the server said about itself at initialize.
func (s *Session) Info() ServerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Stderr returns the bounded tail of the server's stderr.
func (s *Session) Stderr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return ""
	}
	return s.p.stderr.String()
}

// Close ends the process: stdin is closed, then after a bounded wait it is
// killed. Safe to call more than once.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p != nil {
		s.p.terminate()
		s.p = nil
	}
	return nil
}

func (s *Session) spawn() (*proc, error) {
	pctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(pctx, s.cfg.BinaryPath, s.cfg.Args...)
	cmd.Env = childenv.Minimal(s.cfg.Env...)
	cmd.WaitDelay = closeGrace
	ring := &ringBuffer{max: stderrRingBytes}
	cmd.Stderr = ring
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcpstdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcpstdio: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%w: start %s: %v", ErrServerGone, s.cfg.BinaryPath, err)
	}
	p := &proc{
		cmd: cmd, stdin: stdin, stderr: ring, cancel: cancel,
		msgs: make(chan message), done: make(chan struct{}), waited: make(chan struct{}), quit: make(chan struct{}),
	}
	go p.readLoop(bufio.NewReaderSize(stdout, 64<<10), s.cfg.MaxLineBytes)
	go func() {
		<-p.done // stdout must be drained before Wait closes the pipe
		_ = cmd.Wait()
		close(p.waited)
	}()
	return p, nil
}

// readLoop reads newline-delimited messages, never holding more than max
// bytes of one line. An oversized line is reported and its remainder dropped,
// so the stream stays in step.
func (p *proc) readLoop(r *bufio.Reader, max int) {
	defer close(p.done)
	for {
		var line []byte
		tooBig := false
		for {
			chunk, isPrefix, err := r.ReadLine()
			if err != nil {
				return
			}
			if !tooBig {
				if len(line)+len(chunk) > max {
					tooBig = true
					line = nil
				} else {
					line = append(line, chunk...)
				}
			}
			if !isPrefix {
				break
			}
		}
		m := message{raw: line}
		if tooBig {
			m = message{err: ErrResponseTooLarge}
		} else if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		select {
		case p.msgs <- m:
		case <-p.quit:
			return
		}
	}
}

// terminate closes stdin (a stdio server exits on EOF), waits a bounded time,
// then kills the process.
func (p *proc) terminate() {
	p.once.Do(func() { close(p.quit) })
	_ = p.stdin.Close()
	select {
	case <-p.waited:
	case <-time.After(closeGrace):
		p.cancel()
		select {
		case <-p.waited:
		case <-time.After(closeGrace):
		}
	}
	p.cancel()
}

func (s *Session) handshakeLocked(ctx context.Context) error {
	res, err := s.rpcLocked(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": s.cfg.ClientName, "version": "0"},
	})
	if err != nil {
		return fmt.Errorf("mcpstdio: initialize: %w", err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if res.Kind() != '{' || json.Unmarshal(res, &init) != nil {
		return fmt.Errorf("%w: initialize result is not an object", ErrMalformed)
	}
	s.info = ServerInfo{Name: bound(init.ServerInfo.Name), Version: bound(init.ServerInfo.Version), Protocol: bound(init.ProtocolVersion)}
	return s.writeLocked(ctx, "notifications/initialized", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}

// ListTools lists the server's tools, following nextCursor for at most
// MaxToolPages pages. The whole listing shares one CallTimeout.
func (s *Session) ListTools(ctx context.Context) (ToolList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	var out ToolList
	seen := map[string]bool{}
	names := map[string]bool{}
	total := 0
	cursor := ""
	for page := 0; ; page++ {
		if page == MaxToolPages {
			out.Truncated = true
			return out, nil
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := s.rpcLocked(ctx, "tools/list", params)
		if err != nil {
			return ToolList{}, fmt.Errorf("mcpstdio: tools/list: %w", err)
		}
		if total += len(res); total > MaxToolListBytes {
			return ToolList{}, fmt.Errorf("%w: tools/list over %d bytes", ErrResponseTooLarge, MaxToolListBytes)
		}
		var pg struct {
			Tools []struct {
				Name        string                    `json:"name"`
				Description string                    `json:"description"`
				InputSchema jsontext.Value            `json:"inputSchema"`
				Annotations map[string]jsontext.Value `json:"annotations"`
			} `json:"tools"`
			NextCursor *string `json:"nextCursor"`
		}
		if res.Kind() != '{' || json.Unmarshal(res, &pg) != nil {
			return ToolList{}, fmt.Errorf("%w: tools/list page %d", ErrMalformed, page+1)
		}
		for _, t := range pg.Tools {
			if t.Name == "" || names[t.Name] {
				return ToolList{}, fmt.Errorf("%w: tools/list names a tool %q twice or not at all", ErrMalformed, bound(t.Name))
			}
			names[t.Name] = true
			ro := false
			if h, ok := t.Annotations["readOnlyHint"]; ok {
				ro = string(bytes.TrimSpace(h)) == "true"
			}
			out.Tools = append(out.Tools, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, ReadOnly: ro})
		}
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return out, nil
		}
		if seen[*pg.NextCursor] {
			return ToolList{}, fmt.Errorf("%w: tools/list repeats a cursor", ErrMalformed)
		}
		seen[*pg.NextCursor] = true
		cursor = *pg.NextCursor
	}
}

// CallTool runs tools/call with args (a JSON object; nil sends {}) and returns
// the tool's payload. isError and JSON-RPC errors are *ToolError; a result with
// neither structuredContent nor content is ErrMalformed.
func (s *Session) CallTool(ctx context.Context, name string, args jsontext.Value) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	if len(args) == 0 {
		args = jsontext.Value("{}")
	}
	res, err := s.rpcLocked(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return Result{}, err
	}
	return unwrapResult(res)
}

func unwrapResult(res jsontext.Value) (Result, error) {
	var w struct {
		IsError           bool           `json:"isError"`
		StructuredContent jsontext.Value `json:"structuredContent"`
		Content           jsontext.Value `json:"content"`
	}
	if res.Kind() != '{' || json.Unmarshal(res, &w, jsontext.AllowInvalidUTF8(true)) != nil {
		return Result{}, fmt.Errorf("%w: tool result is not an object", ErrMalformed)
	}
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var blocks []block
	if len(w.Content) > 0 && w.Content.Kind() != 'n' {
		if w.Content.Kind() != '[' || json.Unmarshal(w.Content, &blocks, jsontext.AllowInvalidUTF8(true)) != nil {
			return Result{}, fmt.Errorf("%w: tool result content is not an array of blocks", ErrMalformed)
		}
	}
	if w.IsError {
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		msg := strings.Join(texts, " ")
		if msg == "" {
			msg = "the tool reported an error and said nothing more"
		}
		return Result{}, &ToolError{Message: bound(msg)}
	}
	if len(w.StructuredContent) > 0 && w.StructuredContent.Kind() != 'n' {
		return Result{Kind: KindStructured, Value: w.StructuredContent}, nil
	}
	switch {
	case len(blocks) == 0:
		return Result{}, fmt.Errorf("%w: tool result has neither structuredContent nor content", ErrMalformed)
	case len(blocks) == 1 && blocks[0].Type == "text":
		text := jsontext.Value(blocks[0].Text)
		if len(bytes.TrimSpace(text)) > 0 && text.IsValid() {
			return Result{Kind: KindTextJSON, Value: text}, nil
		}
		b, err := json.Marshal(blocks[0].Text, jsontext.AllowInvalidUTF8(true))
		if err != nil {
			return Result{}, fmt.Errorf("%w: text block: %v", ErrMalformed, err)
		}
		return Result{Kind: KindText, Value: b}, nil
	default:
		return Result{Kind: KindContent, Value: w.Content}, nil
	}
}

// writeLocked sends one message line. The pipe write runs in its own goroutine
// bounded by ctx (by CallTimeout when ctx has none): a server that stops
// reading stdin fills the pipe, and a bare Write would then block forever
// holding s.mu. A write still blocked when ctx ends may have put part of the
// line on the wire, so the channel is unusable and the process is killed; later
// calls see ErrServerGone.
func (s *Session) writeLocked(ctx context.Context, method string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mcpstdio: encode %s: %w", method, err)
	}
	if s.p == nil {
		return ErrServerGone
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.CallTimeout)
		defer cancel()
	}
	if ctx.Err() != nil {
		return ctxErr(ctx, method)
	}
	p := s.p
	written := make(chan error, 1)
	go func() {
		_, err := p.stdin.Write(append(b, '\n'))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return s.goneLocked()
		}
		return nil
	case <-ctx.Done():
	}
	select {
	case err := <-written: // finished as ctx ended: the line is whole
		if err != nil {
			return s.goneLocked()
		}
		return nil
	default:
	}
	p.cancel()
	p.terminate()
	s.p = nil
	select {
	case <-written:
	case <-time.After(closeGrace):
	}
	return ctxErr(ctx, method)
}

// ctxErr maps an ended context: a deadline is ErrTimeout, a cancellation is
// returned as is.
func ctxErr(ctx context.Context, method string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrTimeout, method)
	}
	return ctx.Err()
}

func (s *Session) goneLocked() error {
	if s.p == nil {
		return ErrServerGone
	}
	if tail := s.p.stderr.String(); tail != "" {
		return fmt.Errorf("%w: stderr: %s", ErrServerGone, boundTail(tail))
	}
	return ErrServerGone
}

type rpcResponse struct {
	ID     jsontext.Value `json:"id"`
	Method string         `json:"method"`
	Result jsontext.Value `json:"result"`
	Error  *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpcLocked sends one request and waits for the response with its id.
// Notifications, server requests, lines that are not JSON-RPC, and responses
// to other ids are skipped. A response with neither result nor error is
// ErrMalformed.
func (s *Session) rpcLocked(ctx context.Context, method string, params any) (jsontext.Value, error) {
	if s.p == nil {
		return nil, ErrServerGone
	}
	s.nextID++
	want := strconv.FormatInt(s.nextID, 10)
	if err := s.writeLocked(ctx, method, map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		select {
		case m := <-s.p.msgs:
			if m.err != nil {
				return nil, m.err
			}
			var r rpcResponse
			if json.Unmarshal(m.raw, &r, jsontext.AllowInvalidUTF8(true)) != nil || r.Method != "" || string(bytes.TrimSpace(r.ID)) != want {
				continue
			}
			if r.Error != nil {
				return nil, &ToolError{Code: r.Error.Code, Message: bound(r.Error.Message)}
			}
			if len(r.Result) == 0 {
				return nil, fmt.Errorf("%w: %s response has neither result nor error", ErrMalformed, method)
			}
			return r.Result, nil
		case <-s.p.done:
			return nil, s.goneLocked()
		case <-ctx.Done():
			return nil, ctxErr(ctx, method)
		}
	}
}

// bound cuts s to errMessageBytes on a rune boundary.
func bound(s string) string {
	if len(s) <= errMessageBytes {
		return s
	}
	cut := errMessageBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "...[truncated]"
}

// boundTail keeps the last errMessageBytes of s on a rune boundary: the end of
// a server's stderr is where its reason for dying is.
func boundTail(s string) string {
	if len(s) <= errMessageBytes {
		return s
	}
	cut := len(s) - errMessageBytes
	for cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut++
	}
	return "[truncated]..." + s[cut:]
}

// ringBuffer keeps the last max bytes written to it.
type ringBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.max:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(bytes.TrimSpace(r.buf))
}
