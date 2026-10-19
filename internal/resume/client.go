package resume

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Framing: DevPrism's stdio transport is newline-delimited JSON, one JSON-RPC
// message per line in each direction (src-tauri/src/mcp/transport_stdio.rs:3-4,
// read_until(b'\n') at :46 and writeln! at :194). There are no Content-Length
// headers. The server is stateless MCP 2026-07-28 and has no `initialize`
// method (mcp/protocol.rs:4, mcp/server.rs:226 answers -32601), so the
// handshake tolerates that one answer and nothing else.

// Defaults and ceilings. All are bounds, not targets.
const (
	DefaultCallTimeout      = 30 * time.Second
	MaxCallTimeout          = 120 * time.Second
	DefaultStartTimeout     = 15 * time.Second
	MaxStartTimeout         = 60 * time.Second
	DefaultMaxResponseBytes = 8 << 20
	DefaultMaxRestarts      = 3
	// PageBudget is pinned: the user's resumes are exactly two pages.
	PageBudget = 2

	stderrRingBytes   = 8 << 10
	errMessageBytes   = 512
	closeGrace        = 2 * time.Second
	protocolVersion   = "2026-07-28"
	methodNotFoundRPC = -32601
	maxToolListBytes  = 1 << 20
)

var (
	// ErrToolsChanged means the server's tool names/input schemas hash differs
	// from Config.PinnedToolsHash.
	ErrToolsChanged = errors.New("resume: DevPrism tool set changed")
	// ErrServerGone means the DevPrism process exited or closed its pipes.
	ErrServerGone = errors.New("resume: DevPrism process is gone")
	// ErrResponseTooLarge means one response line exceeded MaxResponseBytes.
	ErrResponseTooLarge = errors.New("resume: response exceeds size limit")
	// ErrCallTimeout means a call exceeded its deadline.
	ErrCallTimeout = errors.New("resume: call timed out")
	// ErrRestartLimit means Restart was called more than MaxRestarts times.
	ErrRestartLimit = errors.New("resume: restart limit reached")
	// ErrMalformed means DevPrism answered with a shape this client cannot map.
	ErrMalformed = errors.New("resume: malformed DevPrism output")
)

// ToolError is a tool-level failure: a JSON-RPC error object or an MCP result
// with isError true. Message is bounded.
type ToolError struct {
	Code    int64
	Message string
}

func (e *ToolError) Error() string {
	return "resume: DevPrism tool error: " + e.Message
}

// Config configures Start. Zero values take the documented defaults.
type Config struct {
	BinaryPath       string
	Args             []string      // default ["--mcp"]
	CallTimeout      time.Duration // default 30s, capped at 120s
	StartTimeout     time.Duration // default 15s, capped at 60s
	MaxResponseBytes int           // default 8 MiB
	MaxRestarts      int           // default 3
	PinnedToolsHash  string        // optional; mismatch fails Start with ErrToolsChanged
	// Env adds explicit KEY=VALUE entries on top of the minimal PATH, HOME,
	// TMPDIR environment. The process never inherits anything else.
	Env []string
}

func (c Config) withDefaults() Config {
	if len(c.Args) == 0 {
		c.Args = []string{"--mcp"}
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = DefaultCallTimeout
	}
	if c.CallTimeout > MaxCallTimeout {
		c.CallTimeout = MaxCallTimeout
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = DefaultStartTimeout
	}
	if c.StartTimeout > MaxStartTimeout {
		c.StartTimeout = MaxStartTimeout
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if c.MaxRestarts <= 0 {
		c.MaxRestarts = DefaultMaxRestarts
	}
	return c
}

// message is one inbound line, or a reader-side failure.
type message struct {
	raw []byte
	err error // ErrResponseTooLarge, or a read error
}

// proc is one running DevPrism process and its reader goroutine.
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	msgs   chan message
	done   chan struct{} // closed when stdout reaches EOF/error
	waited chan struct{} // closed once cmd.Wait returned
	quit   chan struct{} // closed by terminate to release the reader
	once   sync.Once
	stderr *ringBuffer
	cancel context.CancelFunc
}

// Client is a DevPrism MCP stdio client. It implements Scorer.
type Client struct {
	cfg Config

	mu       sync.Mutex // serialises calls: one stdio channel
	p        *proc
	nextID   int64
	hash     string
	restarts int
	closed   bool
}

var _ Scorer = (*Client)(nil)

// Start spawns DevPrism and performs the handshake and tool-list hash check.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.BinaryPath == "" {
		return nil, errors.New("resume: Config.BinaryPath is required")
	}
	c := &Client{cfg: cfg.withDefaults()}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startLocked(ctx, c.cfg.PinnedToolsHash); err != nil {
		return nil, err
	}
	return c, nil
}

// ToolsHash is the sha256 (hex) over the sorted tool names and input schemas
// seen at the last handshake. Pin it with Config.PinnedToolsHash.
func (c *Client) ToolsHash() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hash
}

// Stderr returns the bounded tail of the process's stderr.
func (c *Client) Stderr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return ""
	}
	return c.p.stderr.String()
}

func minimalEnv(extra []string) []string {
	env := make([]string, 0, 3+len(extra))
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}

func (c *Client) spawn() (*proc, error) {
	pctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(pctx, c.cfg.BinaryPath, c.cfg.Args...)
	cmd.Env = minimalEnv(c.cfg.Env)
	cmd.WaitDelay = closeGrace
	ring := &ringBuffer{max: stderrRingBytes}
	cmd.Stderr = ring
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("resume: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("resume: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("resume: start %s: %w", c.cfg.BinaryPath, err)
	}
	p := &proc{
		cmd: cmd, stdin: stdin, stderr: ring, cancel: cancel,
		msgs: make(chan message), done: make(chan struct{}), waited: make(chan struct{}), quit: make(chan struct{}),
	}
	go p.readLoop(bufio.NewReaderSize(stdout, 64<<10), c.cfg.MaxResponseBytes)
	go func() {
		<-p.done // stdout must be drained before Wait closes the pipe
		_ = cmd.Wait()
		close(p.waited)
	}()
	return p, nil
}

// readLoop reads newline-delimited messages, never holding more than max
// bytes of one line. An oversized line is reported and its remainder dropped.
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

// terminate closes stdin (DevPrism exits on EOF), waits a bounded time, then
// kills the process.
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

func (c *Client) startLocked(ctx context.Context, pin string) error {
	p, err := c.spawn()
	if err != nil {
		return err
	}
	hctx, cancel := context.WithTimeout(ctx, c.cfg.StartTimeout)
	defer cancel()
	c.p = p
	hash, err := c.handshakeLocked(hctx)
	if err != nil {
		p.terminate()
		c.p = nil
		return err
	}
	if want := pin; want != "" && want != hash {
		p.terminate()
		c.p = nil
		return fmt.Errorf("%w: pinned %s, server reports %s", ErrToolsChanged, want, hash)
	}
	c.hash = hash
	return nil
}

func (c *Client) handshakeLocked(ctx context.Context) (string, error) {
	_, err := c.rpcLocked(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "vani", "version": "0"},
	})
	var te *ToolError
	if err != nil && !(errors.As(err, &te) && te.Code == methodNotFoundRPC) {
		return "", fmt.Errorf("resume: initialize: %w", err)
	}
	if err := c.notifyLocked(ctx, "notifications/initialized"); err != nil {
		return "", err
	}
	res, err := c.rpcLocked(ctx, "tools/list", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("resume: tools/list: %w", err)
	}
	return hashTools(res)
}

// hashTools hashes sorted tool names with their canonicalised input schemas.
// Descriptions are excluded: wording changes are not interface changes.
func hashTools(res json.RawMessage) (string, error) {
	var out struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if len(res) > maxToolListBytes {
		return "", fmt.Errorf("%w: tools/list too large", ErrMalformed)
	}
	if err := json.Unmarshal(res, &out); err != nil || len(out.Tools) == 0 {
		return "", fmt.Errorf("%w: tools/list", ErrMalformed)
	}
	sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
	h := sha256.New()
	for _, t := range out.Tools {
		var schema any
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return "", fmt.Errorf("%w: schema of %q", ErrMalformed, t.Name)
			}
		}
		canon, _ := json.Marshal(schema) // map keys marshal sorted
		fmt.Fprintf(h, "%d:%s\n%d:%s\n", len(t.Name), t.Name, len(canon), canon)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeLocked sends one message line. The pipe write runs in its own goroutine
// and is bounded by ctx (by CallTimeout when ctx has none): a DevPrism that
// stops reading stdin fills the pipe, and a bare Write would then block forever
// while c.mu is held. A write still blocked when ctx ends may have put part of
// the line on the wire, so the channel is unusable and the process is killed,
// which also ends the write; later calls see ErrServerGone until Restart.
func (c *Client) writeLocked(ctx context.Context, method string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if c.p == nil {
		return c.goneLocked()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.CallTimeout)
		defer cancel()
	}
	if ctx.Err() != nil {
		return ctxErr(ctx, method)
	}
	p := c.p
	written := make(chan error, 1)
	go func() {
		_, err := p.stdin.Write(append(b, '\n'))
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return c.goneLocked()
		}
		return nil
	case <-ctx.Done():
	}
	select {
	case err := <-written: // finished as ctx ended: the line is whole
		if err != nil {
			return c.goneLocked()
		}
		return nil
	default:
	}
	p.cancel()
	p.terminate()
	c.p = nil
	select {
	case <-written:
	case <-time.After(closeGrace):
	}
	return ctxErr(ctx, method)
}

func (c *Client) notifyLocked(ctx context.Context, method string) error {
	return c.writeLocked(ctx, method, map[string]any{"jsonrpc": "2.0", "method": method})
}

// ctxErr maps an ended context to the client's error: a deadline is
// ErrCallTimeout, a cancellation is returned as is.
func ctxErr(ctx context.Context, method string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrCallTimeout, method)
	}
	return ctx.Err()
}

func (c *Client) goneLocked() error {
	if c.p == nil {
		return ErrServerGone
	}
	if tail := c.p.stderr.String(); tail != "" {
		return fmt.Errorf("%w: stderr: %s", ErrServerGone, tail)
	}
	return ErrServerGone
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpcLocked sends one request and waits for the response with the same id.
// Notifications, server requests and responses to other ids are ignored.
func (c *Client) rpcLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.closed {
		return nil, ErrServerGone
	}
	if c.p == nil {
		return nil, ErrServerGone
	}
	c.nextID++
	id := c.nextID
	want := strconv.FormatInt(id, 10)
	if err := c.writeLocked(ctx, method, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		select {
		case m := <-c.p.msgs:
			if m.err != nil {
				return nil, m.err
			}
			var r rpcResponse
			if json.Unmarshal(m.raw, &r) != nil || r.Method != "" || string(bytes.TrimSpace(r.ID)) != want {
				continue
			}
			if r.Error != nil {
				return nil, &ToolError{Code: r.Error.Code, Message: bound(r.Error.Message)}
			}
			return r.Result, nil
		case <-c.p.done:
			return nil, c.goneLocked()
		case <-ctx.Done():
			return nil, ctxErr(ctx, method)
		}
	}
}

func bound(s string) string {
	if len(s) <= errMessageBytes {
		return s
	}
	cut := errMessageBytes
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...[truncated]"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// CallTool runs tools/call and returns the tool's JSON payload. DevPrism
// returns the payload bare as the JSON-RPC result (mcp/server.rs:116-123,
// plugins/resume_synthesis.rs:42); structuredContent and content[0].text
// (MCP-standard wrapping) and isError are handled too, so a future server
// that wraps results keeps working. Failures are never mistaken for payloads.
func (c *Client) CallTool(ctx context.Context, name string, args any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
	defer cancel()
	res, err := c.rpcLocked(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	return unwrapToolResult(res)
}

func unwrapToolResult(res json.RawMessage) (json.RawMessage, error) {
	var w struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &w); err != nil {
		return nil, fmt.Errorf("%w: tool result is not an object", ErrMalformed)
	}
	if w.IsError {
		msg := "tool reported an error"
		if len(w.Content) > 0 && w.Content[0].Text != "" {
			msg = w.Content[0].Text
		}
		return nil, &ToolError{Message: bound(msg)}
	}
	if len(w.StructuredContent) > 0 && string(w.StructuredContent) != "null" {
		return w.StructuredContent, nil
	}
	if len(w.Content) > 0 {
		var probe any
		if json.Unmarshal([]byte(w.Content[0].Text), &probe) == nil {
			return json.RawMessage(w.Content[0].Text), nil
		}
	}
	return res, nil
}

// Restart replaces a dead (or live) process with a fresh one, repeating the
// handshake and hash check. At most MaxRestarts restarts are allowed over the
// Client's life.
func (c *Client) Restart(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrServerGone
	}
	if c.restarts >= c.cfg.MaxRestarts {
		return ErrRestartLimit
	}
	c.restarts++
	if c.p != nil {
		c.p.terminate()
		c.p = nil
	}
	pin := c.cfg.PinnedToolsHash
	if pin == "" {
		pin = c.hash // a restart must see the same tool set
	}
	return c.startLocked(ctx, pin)
}

// Close terminates the process: stdin is closed, then after a bounded wait
// the process is killed. Safe to call more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.p != nil {
		c.p.terminate()
		c.p = nil
	}
	return nil
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
