package page

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxStdout is the largest document one page may produce.
	MaxStdout = 16 << 20
	// MaxRestarts is how many times MCPBrowser starts lightpanda again, after
	// a process died or was killed, before the next Reset. Past it every Load
	// fails at once: a page that crashes the browser must not respawn it in a
	// loop.
	MaxRestarts = 3

	// maxStderr bounds the stderr kept for diagnostics.
	maxStderr = 64 << 10
	// stderrInError is how much of stderr, or of a lightpanda message, an
	// error carries.
	stderrInError = 300
	// gotoTimeoutMs bounds the wait for a page's first response. It is
	// lightpanda fetch's --wait-ms default, so a page that dumped an empty
	// document under fetch fails the same way here; it is also shorter than
	// --http-timeout, so a missing first response always comes back as
	// navTimeoutCode and a stalled transfer as a failed navigation (both
	// measured 2026-10-19, lightpanda 1.1.0-nightly.10455).
	gotoTimeoutMs = 5000
	// navTimeoutCode is the JSON-RPC error lightpanda's goto returns when no
	// response arrived before its timeout ("NavigationTimeout").
	navTimeoutCode = -32002
	// maxLine bounds one JSON-RPC line. html's reply carries the document
	// JSON-escaped, which at most doubles quotes and backslashes; the rest is
	// envelope. A longer line fails the load rather than being cut.
	maxLine = 2*MaxStdout + 64<<10
	// maxUnread bounds lines read but not yet asked for.
	maxUnread = 16
	// stopWait is how long a process gets to exit after its stdin closes,
	// and again after it is killed.
	stopWait = 2 * time.Second
)

var (
	// ErrOutputTooLarge means a page's document is larger than MaxStdout. The
	// document is never returned truncated: a cut page could read as a
	// smaller, valid one.
	ErrOutputTooLarge = errors.New("page: lightpanda output exceeds the size limit")
	// ErrHTTPStatus is a page that answered with an HTTP status of 400 or
	// more; its body is an error page, not the posting.
	ErrHTTPStatus = errors.New("page: the page answered with an HTTP error status")
	// ErrBrowserGone means the lightpanda process exited, was killed, or
	// broke the protocol.
	ErrBrowserGone = errors.New("page: lightpanda exited or broke the protocol")
	// ErrBrowserClosed is a Load after Close.
	ErrBrowserClosed = errors.New("page: the browser is closed")

	errLoadTimeout = errors.New("page: load timeout")
)

// Browser loads pages in a lightpanda process that outlives one page, so its
// robots.txt store is shared by every page it loads.
type Browser interface {
	// Load navigates to rawURL, which the caller has validated, and returns
	// the serialized DOM. A non-nil error means nothing read for that page is
	// trusted.
	Load(ctx context.Context, rawURL string) ([]byte, error)
	// Reset ends the current process, if any. The next Load starts a fresh
	// one, with a new robots.txt store, cookie jar and restart budget. A Load
	// in flight when Reset is called fails.
	Reset() error
}

// MCPBrowser drives one `lightpanda mcp` process over stdio (JSON-RPC, one
// message per line). The process starts on the first Load, starts again on
// the next Load after it dies or is killed (at most MaxRestarts times between
// Resets), and ends on Reset or Close. Its flags are fixed per process.
// Lightpanda is AGPL-licensed; it is only ever executed, never linked.
//
// The mcp server answers one request at a time, so Loads are serialized. A
// page cannot be abandoned inside the process (it ignores
// notifications/cancelled), so a Load whose context ends kills the process,
// as an expired `lightpanda fetch` was killed before.
type MCPBrowser struct {
	binary      string
	flags       []string
	loadTimeout time.Duration

	sem chan struct{} // held by the one Load in flight

	mu       sync.Mutex
	proc     *mcpProc
	started  bool // a process was started since the last Reset
	restarts int  // processes started after the first, since the last Reset
	closed   bool
}

// NewMCPBrowser returns a browser that runs binary (an absolute path) as
// `binary mcp flags...`. loadTimeout bounds one Load, the process start
// included. No process starts until the first Load.
func NewMCPBrowser(binary string, flags []string, loadTimeout time.Duration) (*MCPBrowser, error) {
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("page: lightpanda binary %q is not an absolute path", binary)
	}
	if loadTimeout <= 0 {
		return nil, errors.New("page: MCPBrowser needs a positive load timeout")
	}
	return &MCPBrowser{binary: binary, flags: slices.Clone(flags), loadTimeout: loadTimeout, sem: make(chan struct{}, 1)}, nil
}

// Load navigates the process's page to rawURL and returns its HTML. The
// navigation must succeed with an HTTP status below 400 before the document
// is read: after a failed navigation lightpanda's page holds its own error
// document, which must never be taken for the posting.
func (b *MCPBrowser) Load(ctx context.Context, rawURL string) ([]byte, error) {
	select {
	case b.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-b.sem }()
	ctx, cancel := context.WithTimeoutCause(ctx, b.loadTimeout, errLoadTimeout)
	defer cancel()

	p, err := b.process(ctx)
	if err != nil {
		return nil, b.procError(ctx, nil, err)
	}
	doc, err := load(ctx, p, rawURL)
	var pe pageError
	if errors.As(err, &pe) {
		return nil, pe.err // the page failed; the process is fine
	}
	if err != nil {
		b.discard(p)
		return nil, b.procError(ctx, p, err)
	}
	return doc, nil
}

// Reset ends the current process; see Browser.
func (b *MCPBrowser) Reset() error {
	b.mu.Lock()
	p := b.proc
	b.proc, b.started, b.restarts = nil, false, 0
	b.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.stop(true)
}

// Close ends the process and refuses every later Load. Closing twice is
// harmless.
func (b *MCPBrowser) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return b.Reset()
}

// process returns the live process, starting one if there is none.
func (b *MCPBrowser) process(ctx context.Context) (*mcpProc, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrBrowserClosed
	}
	if p := b.proc; p != nil {
		if !p.dead() {
			b.mu.Unlock()
			return p, nil
		}
		b.proc = nil
		b.mu.Unlock()
		_ = p.stop(false) // it has exited; this only removes its directory
		b.mu.Lock()
	}
	if b.started {
		if b.restarts >= MaxRestarts {
			b.mu.Unlock()
			return nil, fmt.Errorf("%w: restarted %d times already; not starting it again", ErrBrowserGone, MaxRestarts)
		}
		b.restarts++
	}
	b.started = true
	b.mu.Unlock()

	p, err := startMCP(ctx, b.binary, b.flags)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		_ = p.stop(false)
		return nil, ErrBrowserClosed
	}
	b.proc = p
	b.mu.Unlock()
	return p, nil
}

// discard kills p and forgets it, so the next Load starts afresh.
func (b *MCPBrowser) discard(p *mcpProc) {
	b.mu.Lock()
	if b.proc == p {
		b.proc = nil
	}
	b.mu.Unlock()
	_ = p.stop(false)
}

// procError words a failure that cost the process: its own timeout, the
// caller's context, or the process's death with the end of its stderr.
func (b *MCPBrowser) procError(ctx context.Context, p *mcpProc, err error) error {
	switch {
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), errLoadTimeout):
		return fmt.Errorf("page: lightpanda timed out after %s", b.loadTimeout)
	case ctx.Err() != nil:
		return ctx.Err()
	case p != nil:
		return fmt.Errorf("%w%s", err, tail(p.stderr.bytes()))
	}
	return err
}

// pageError is a page that failed while the process stayed usable.
type pageError struct{ err error }

func (e pageError) Error() string { return e.err.Error() }

// load is goto, then html, on a live process. A pageError leaves the process
// in use; any other error means it can no longer be trusted.
func load(ctx context.Context, p *mcpProc, rawURL string) ([]byte, error) {
	res, rerr, err := p.call(ctx, "tools/call", toolCall{Name: "goto", Arguments: map[string]any{"url": rawURL, "timeout": gotoTimeoutMs}})
	if err != nil {
		return nil, err
	}
	if rerr != nil {
		if rerr.Code == navTimeoutCode {
			return nil, pageError{fmt.Errorf("%w: %s", ErrEmptyDocument, rerr)}
		}
		return nil, pageError{fmt.Errorf("page: lightpanda goto: %s", rerr)}
	}
	var nav toolResult
	if err := json.Unmarshal(res, &nav); err != nil {
		return nil, fmt.Errorf("%w: goto's reply is not a tool result", ErrBrowserGone)
	}
	if nav.IsError {
		return nil, pageError{fmt.Errorf("page: lightpanda: %s", oneLine(nav.text(), stderrInError))}
	}
	status := 0
	if nav.Structured != nil {
		status = nav.Structured.HTTPStatus
	}
	switch {
	case status >= 400:
		return nil, pageError{fmt.Errorf("%w %d", ErrHTTPStatus, status)}
	case status <= 0:
		return nil, pageError{errors.New("page: lightpanda reported no HTTP status for the page")}
	}

	// No url (html would navigate again, and prefixes a warning to a 4xx/5xx
	// body) and no maxBytes (it truncates).
	res, rerr, err = p.call(ctx, "tools/call", toolCall{Name: "html", Arguments: map[string]any{}})
	if err != nil {
		return nil, err
	}
	if rerr != nil {
		return nil, pageError{fmt.Errorf("page: lightpanda html: %s", rerr)}
	}
	var doc toolResult
	if err := json.Unmarshal(res, &doc); err != nil {
		return nil, fmt.Errorf("%w: html's reply is not a tool result", ErrBrowserGone)
	}
	if doc.IsError {
		return nil, pageError{fmt.Errorf("page: lightpanda html: %s", oneLine(doc.text(), stderrInError))}
	}
	if len(doc.Content) != 1 || doc.Content[0].Type != "text" {
		return nil, pageError{fmt.Errorf("page: lightpanda html returned %d content items, want one text", len(doc.Content))}
	}
	if len(doc.Content[0].Text) > MaxStdout {
		return nil, pageError{ErrOutputTooLarge}
	}
	return []byte(doc.Content[0].Text), nil
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Structured *struct {
		HTTPStatus int `json:"httpStatus"`
	} `json:"structuredContent"`
	IsError bool `json:"isError"`
}

func (r toolResult) text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, " ")
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) String() string {
	return fmt.Sprintf("%s (code %d)", oneLine(e.Message, stderrInError), e.Code)
}

// mcpProc is one lightpanda mcp process and the reader of its stdout.
type mcpProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	dir    string // its working directory, removed on stop
	stderr capBuffer
	nextID int64 // guarded by MCPBrowser.sem

	lines  chan []byte   // reply lines in order; closed when reading stops
	exited chan struct{} // closed once the process has been waited for

	errMu   sync.Mutex
	readErr error // why reading stopped
}

// startMCP starts the process in a fresh empty directory with a minimal
// environment and completes the MCP handshake within ctx.
func startMCP(ctx context.Context, binary string, flags []string) (*mcpProc, error) {
	dir, err := os.MkdirTemp("", "vani-lightpanda-")
	if err != nil {
		return nil, fmt.Errorf("page: lightpanda working directory: %w", err)
	}
	cmd := exec.Command(binary, append([]string{"mcp"}, flags...)...)
	cmd.Env = minimalEnv()
	cmd.Dir = dir
	cmd.WaitDelay = stopWait
	p := &mcpProc{cmd: cmd, dir: dir, lines: make(chan []byte, maxUnread), exited: make(chan struct{})}
	p.stderr.limit = maxStderr
	cmd.Stderr = &p.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("page: lightpanda stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("page: lightpanda stdout: %w", err)
	}
	p.stdin = stdin
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("page: starting lightpanda: %w", err)
	}
	go p.read(stdout)

	_, rerr, err := p.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "vani", "version": "0.1"},
	})
	if err == nil && rerr != nil {
		err = fmt.Errorf("%w: initialize refused: %s", ErrBrowserGone, rerr)
	}
	if err == nil {
		err = p.send(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	}
	if err != nil {
		_ = p.stop(false)
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w%s", err, tail(p.stderr.bytes()))
	}
	return p, nil
}

// read delivers stdout's lines until EOF, an over-long line or more unread
// lines than maxUnread; then the process is killed, since its stream can no
// longer be trusted, and waited for.
func (p *mcpProc) read(stdout io.Reader) {
	br := bufio.NewReaderSize(stdout, 64<<10)
	var err error
	for {
		var line []byte
		if line, err = readLine(br, maxLine); err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		select {
		case p.lines <- line:
			continue
		default:
			err = fmt.Errorf("%w: more than %d unread lines", ErrBrowserGone, maxUnread)
		}
		break
	}
	p.errMu.Lock()
	p.readErr = err
	p.errMu.Unlock()
	close(p.lines)
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	close(p.exited)
}

// readLine reads one '\n'-terminated line of at most limit bytes, without the
// newline. A longer line is ErrOutputTooLarge; EOF, with or without a
// partial line, is ErrBrowserGone.
func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > limit+1 {
			return nil, fmt.Errorf("%w: a reply line is over %d bytes", ErrOutputTooLarge, limit)
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case len(line) > 0:
			return nil, fmt.Errorf("%w: stdout ended inside a reply", ErrBrowserGone)
		default:
			return nil, fmt.Errorf("%w: stdout closed", ErrBrowserGone)
		}
	}
}

func (p *mcpProc) dead() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// send writes one message line, bounded by ctx. A process that stops reading
// stdin fills the pipe, and a request longer than the pipe buffer (a page's
// href has no length limit) would then block the write past the load's
// timeout. A write still blocked when ctx ends leaves part of a line on the
// wire, so the process is killed, which ends the write; the caller discards it.
func (p *mcpProc) send(ctx context.Context, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() {
		_, err := p.stdin.Write(append(b, '\n'))
		written <- err
	}()
	select {
	case err = <-written:
	case <-ctx.Done():
		select {
		case err = <-written: // finished as ctx ended: the line is whole
		default:
			_ = p.cmd.Process.Kill()
			_ = p.stdin.Close()
			select {
			case <-written:
			case <-time.After(stopWait):
			}
			return ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("%w: writing a request: %v", ErrBrowserGone, err)
	}
	return nil
}

// call sends one request and waits for the reply with its id. A JSON-RPC
// error comes back as rerr; err is a failure of the process or of ctx.
func (p *mcpProc) call(ctx context.Context, method string, params any) (json.RawMessage, *rpcError, error) {
	p.nextID++
	id := strconv.FormatInt(p.nextID, 10)
	if err := p.send(ctx, map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": method, "params": params}); err != nil {
		return nil, nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case line, ok := <-p.lines:
			if !ok {
				p.errMu.Lock()
				err := p.readErr
				p.errMu.Unlock()
				return nil, nil, err
			}
			var r struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *rpcError       `json:"error"`
			}
			if err := json.Unmarshal(line, &r); err != nil {
				return nil, nil, fmt.Errorf("%w: a reply line is not JSON", ErrBrowserGone)
			}
			if string(r.ID) != id {
				continue // a notification, or a reply to nothing this call asked
			}
			if r.Error != nil {
				return nil, r.Error, nil
			}
			if len(r.Result) == 0 {
				return nil, nil, fmt.Errorf("%w: a reply has neither result nor error", ErrBrowserGone)
			}
			return r.Result, nil, nil
		}
	}
}

// stop ends the process: gracefully closes its stdin and waits stopWait
// before killing it, or kills it at once, and removes its directory.
func (p *mcpProc) stop(graceful bool) error {
	_ = p.stdin.Close()
	if graceful {
		select {
		case <-p.exited:
		case <-time.After(stopWait):
		}
	}
	if !p.dead() {
		_ = p.cmd.Process.Kill()
		select {
		case <-p.exited:
		case <-time.After(stopWait):
			return fmt.Errorf("page: lightpanda (pid %d) did not exit after a kill", p.cmd.Process.Pid)
		}
	}
	return os.RemoveAll(p.dir)
}

func minimalEnv() []string {
	env := []string{"LIGHTPANDA_DISABLE_TELEMETRY=1"}
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// tail renders the end of stderr for an error message: bounded, one line, no
// control characters.
func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > stderrInError {
		s = s[len(s)-stderrInError:]
	}
	s = oneLine(s, stderrInError)
	if s == "" {
		return ""
	}
	return ": " + s
}

// oneLine is s cut to its first n bytes with control characters blanked.
func oneLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// capBuffer keeps the newest limit bytes written to it: the end of stderr
// says why a process failed. It is a diagnostic, not data, so writes never
// fail.
type capBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) <= c.limit-c.buf.Len() {
		return c.buf.Write(p)
	}
	keep := append(c.buf.Bytes(), p...)
	if len(keep) > c.limit {
		keep = keep[len(keep)-c.limit:]
	}
	c.buf = *bytes.NewBuffer(append([]byte(nil), keep...))
	return len(p), nil
}

func (c *capBuffer) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes())
}
