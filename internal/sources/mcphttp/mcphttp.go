// Package mcphttp is a client for one remote Model Context Protocol server
// that speaks the Streamable HTTP transport (spec 2025-06-18): each JSON-RPC
// message is a POST to one endpoint, and the server answers with one JSON
// message or with a text/event-stream whose events carry it. Job boards that
// publish their search as a keyless MCP server (Dice, ZipRecruiter) are read
// through it, from Vani's own code rather than through a chat assistant.
//
// Every request goes through an egress.Doer and srcutil's one request loop
// (srcutil.PostBodyLimit, srcutil.PostExchange), so it shares the adapters'
// status, body-cap, retry, Retry-After and per-host throttle rules. The
// handshake reads the initialize reply's headers (a server may assign an
// Mcp-Session-Id) and takes the notification's 202.
//
// Only tools/call is made after the handshake; the client never answers a
// server's request, never opens the optional GET stream, and never sends
// sampling, elicitation or roots capabilities, so a server cannot ask it for
// anything. A tool's result must carry structuredContent (both servers above
// declare an outputSchema); text a server returns is untrusted and reaches an
// error only bounded and stripped of control characters.
package mcphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// ProtocolVersion is the MCP revision this client speaks.
	ProtocolVersion = "2025-06-18"
	// MaxReply bounds one reply body. A Dice page of 100 rows was about
	// 106 KB on 2026-11-24; 8 MiB leaves room and still refuses a runaway.
	MaxReply = 8 << 20
	// maxInitReply bounds the initialize reply, which is a few hundred bytes.
	maxInitReply = 256 << 10
	// maxErrText bounds server text quoted in an error.
	maxErrText = 300
	// accept is what a Streamable HTTP client must accept.
	accept = "application/json, text/event-stream"
	// sessionHeader is the session id a server may assign at initialize.
	sessionHeader = "Mcp-Session-Id"
	// versionHeader names the negotiated revision on every later request.
	versionHeader = "MCP-Protocol-Version"
	// maxSessionID bounds a session id; the spec allows visible ASCII only.
	maxSessionID = 512
)

var (
	// ErrProtocol is a reply that is not the JSON-RPC message the call asked
	// for: not JSON, no response with the request's id, a batch, a result with
	// neither structuredContent nor an error, or a bad session id.
	ErrProtocol = errors.New("mcphttp: unexpected reply")
	// ErrEndpointGone is a 404 from the endpoint: the server moved, or no
	// longer knows the session. It is not srcutil.ErrBoardNotFound.
	ErrEndpointGone = errors.New("mcphttp: the MCP endpoint answered 404")
	// ErrEndpoint is an endpoint that is not an absolute https URL.
	ErrEndpoint = errors.New("mcphttp: endpoint must be an absolute https URL with no userinfo or fragment")
)

// ToolError is a tools/call the server answered with isError: the tool ran
// and failed (a validation error, an upstream status, "Job not found").
type ToolError struct {
	Tool string
	// Text is the server's message, bounded and stripped of control
	// characters. It is untrusted.
	Text string
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("mcphttp: tool %s failed: %s", e.Tool, e.Text)
}

// RPCError is a JSON-RPC error response: the server refused the call itself
// (unknown tool, malformed params).
type RPCError struct {
	Method  string
	Code    int
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcphttp: %s: error %d: %s", e.Method, e.Code, e.Message)
}

// Client is one server's session. It is not safe for concurrent use: an
// adapter's List makes its calls one after another.
type Client struct {
	d        egress.Doer
	endpoint string
	session  string
	version  string
	nextID   int64
	open     bool
}

// New returns a client for endpoint, which must be an absolute https URL.
// Nothing is sent until Open.
func New(d egress.Doer, endpoint string) (*Client, error) {
	if d == nil {
		return nil, errors.New("mcphttp: no http doer configured")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, ErrEndpoint
	}
	return &Client{d: d, endpoint: u.String()}, nil
}

// Open runs the initialize handshake: initialize, then the initialized
// notification. It keeps the session id and protocol revision the server
// names for every later request. Calling it twice is an error.
func (c *Client) Open(ctx context.Context) error {
	if c.open {
		return errors.New("mcphttp: already open")
	}
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "vani", "version": "0.1"},
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	hdr, err := c.initialize(ctx, params, &res)
	if err != nil {
		return err
	}
	if res.ProtocolVersion == "" {
		return fmt.Errorf("%w: initialize named no protocolVersion", ErrProtocol)
	}
	if !cleanToken(res.ProtocolVersion, 32) {
		return fmt.Errorf("%w: initialize named a malformed protocolVersion", ErrProtocol)
	}
	c.version = res.ProtocolVersion
	if sid := hdr.Get(sessionHeader); sid != "" {
		if !cleanToken(sid, maxSessionID) {
			return fmt.Errorf("%w: malformed %s", ErrProtocol, sessionHeader)
		}
		c.session = sid
	}
	if err := c.notifyInitialized(ctx); err != nil {
		return err
	}
	c.open = true
	return nil
}

// Call runs tools/call for tool with args and decodes the result's
// structuredContent into out. A tool that reports isError is a *ToolError; a
// JSON-RPC error is an *RPCError; an HTTP failure is srcutil's error (a 404
// is srcutil.ErrBoardNotFound, a 429 a *srcutil.RateLimitedError).
func (c *Client) Call(ctx context.Context, tool string, args any, out any) error {
	if !c.open {
		return errors.New("mcphttp: Call before Open")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, body, err := c.request("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return err
	}
	raw, err := srcutil.PostBodyLimit(ctx, c.d, c.endpoint, body, c.headers(), accept, MaxReply)
	if err != nil {
		return fmt.Errorf("mcphttp: tools/call %s: %w", tool, gone(err))
	}
	result, err := response(raw, id, "tools/call")
	if err != nil {
		return err
	}
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return fmt.Errorf("%w: tools/call %s: result: %v", ErrProtocol, tool, err)
	}
	if r.IsError {
		var text string
		for _, ct := range r.Content {
			if ct.Type == "text" && ct.Text != "" {
				text = ct.Text
				break
			}
		}
		if text == "" {
			text = "(no message)"
		}
		return &ToolError{Tool: tool, Text: Clean(text)}
	}
	sc := bytes.TrimSpace(r.StructuredContent)
	if len(sc) == 0 || bytes.Equal(sc, []byte("null")) || sc[0] != '{' {
		return fmt.Errorf("%w: tools/call %s: no structuredContent object", ErrProtocol, tool)
	}
	if err := json.Unmarshal(sc, out); err != nil {
		return fmt.Errorf("%w: tools/call %s: structuredContent: %v", ErrProtocol, tool, err)
	}
	return nil
}

// request encodes one JSON-RPC request and returns its id.
func (c *Client) request(method string, params any) (json.RawMessage, []byte, error) {
	c.nextID++
	id := json.RawMessage(fmt.Sprintf("%d", c.nextID))
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, nil, fmt.Errorf("mcphttp: encode %s: %w", method, err)
	}
	return id, body, nil
}

// headers are a request's headers after the handshake.
func (c *Client) headers() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if c.version != "" {
		h.Set(versionHeader, c.version)
	}
	if c.session != "" {
		h.Set(sessionHeader, c.session)
	}
	return h
}

// initialize sends the initialize request and returns the reply's headers,
// where a server names its session id.
func (c *Client) initialize(ctx context.Context, params any, out any) (http.Header, error) {
	id, body, err := c.request("initialize", params)
	if err != nil {
		return nil, err
	}
	x, err := srcutil.PostExchange(ctx, c.d, c.endpoint, body, c.headers(), accept, maxInitReply)
	if err != nil {
		return nil, fmt.Errorf("mcphttp: initialize: %w", gone(err))
	}
	if x.Status != http.StatusOK {
		return nil, fmt.Errorf("%w: initialize answered %d, not 200", ErrProtocol, x.Status)
	}
	result, err := response(x.Body, id, "initialize")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(result, out); err != nil {
		return nil, fmt.Errorf("%w: initialize result: %v", ErrProtocol, err)
	}
	return x.Header, nil
}

// notifyInitialized sends the initialized notification. The spec answers a
// notification with 202 Accepted; 200 and 204 are taken as the same.
func (c *Client) notifyInitialized(ctx context.Context) error {
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if _, err := srcutil.PostExchange(ctx, c.d, c.endpoint, body, c.headers(), accept, maxInitReply); err != nil {
		return fmt.Errorf("mcphttp: notifications/initialized: %w", gone(err))
	}
	return nil
}

// gone rewrites srcutil's 404, which an adapter reads as "this board does not
// exist", into ErrEndpointGone: an MCP endpoint that answers 404 has moved or
// dropped the session, which says nothing about the board's postings.
func gone(err error) error {
	if errors.Is(err, srcutil.ErrBoardNotFound) {
		return fmt.Errorf("%w (%s)", ErrEndpointGone, Clean(err.Error()))
	}
	return err
}

// response finds the JSON-RPC response to id in raw, a JSON body or an
// event stream, and returns its result. Messages with another id or none (a
// server's notification or request) are skipped.
func response(raw []byte, id json.RawMessage, method string) (json.RawMessage, error) {
	msgs, err := messages(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrProtocol, method, err)
	}
	for _, m := range msgs {
		var r struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(m, &r); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrProtocol, method, err)
		}
		if !bytes.Equal(bytes.TrimSpace(r.ID), id) {
			continue
		}
		if r.Error != nil {
			return nil, &RPCError{Method: method, Code: r.Error.Code, Message: Clean(r.Error.Message)}
		}
		if len(bytes.TrimSpace(r.Result)) == 0 {
			return nil, fmt.Errorf("%w: %s: response has neither result nor error", ErrProtocol, method)
		}
		return r.Result, nil
	}
	return nil, fmt.Errorf("%w: %s: no response with id %s", ErrProtocol, method, id)
}

// messages splits a reply into its JSON-RPC messages: the body itself when it
// is a JSON object, else the data of each event of an event stream (data
// lines joined by newlines, events ended by a blank line). A JSON array (a
// batch) is refused: this client never sends one.
func messages(raw []byte) ([][]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("empty reply")
	}
	switch trimmed[0] {
	case '{':
		return [][]byte{trimmed}, nil
	case '[':
		return nil, errors.New("batch reply")
	}
	var out [][]byte
	var data []byte
	inData := false
	flush := func() {
		if inData {
			out = append(out, data)
		}
		data, inData = nil, false
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			flush()
			continue
		}
		if line[0] == ':' { // comment
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if string(field) != "data" {
			continue // event, id, retry: nothing this client uses
		}
		if inData {
			data = append(data, '\n')
		}
		data = append(data, value...)
		inData = true
	}
	flush()
	var msgs [][]byte
	for _, m := range out {
		m = bytes.TrimSpace(m)
		if len(m) == 0 {
			continue
		}
		if m[0] != '{' {
			return nil, errors.New("event data is not a JSON object")
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		return nil, errors.New("reply is neither JSON nor an event stream with data")
	}
	return msgs, nil
}

// Clean bounds untrusted server text for an error or log line: valid UTF-8,
// control and format characters as spaces, runs of space folded, at most
// maxErrText bytes.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "?")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		if b.Len()+utf8.RuneLen(r) > maxErrText {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// cleanToken reports whether s is 1..max visible ASCII characters (0x21-0x7E),
// the spec's rule for a session id.
func cleanToken(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
