package mcphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }

const endpoint = "https://mcp.example.test/mcp"

// server is a fake MCP endpoint: initialize, the initialized notification and
// tools/call, each answered by a hook the test can replace.
type server struct {
	initReply   func(id string) srctest.Reply
	notifyReply srctest.Reply
	callReply   func(id string, req map[string]any) srctest.Reply
	seen        []srctest.Request
}

func (s *server) doer() *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method != http.MethodPost || r.URL != endpoint {
			return srctest.Reply{}, false
		}
		s.seen = append(s.seen, r)
		var msg map[string]any
		if err := json.Unmarshal(r.Body, &msg); err != nil {
			return srctest.Reply{Status: 400}, true
		}
		id := ""
		if v, ok := msg["id"]; ok {
			b, _ := json.Marshal(v)
			id = string(b)
		}
		switch msg["method"] {
		case "initialize":
			if s.initReply != nil {
				return s.initReply(id), true
			}
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"t","version":"1"}}}`), true
		case "notifications/initialized":
			if s.notifyReply.Status != 0 {
				return s.notifyReply, true
			}
			return srctest.Reply{Status: http.StatusAccepted}, true
		case "tools/call":
			return s.callReply(id, msg), true
		}
		return srctest.Reply{}, false
	})
}

func jsonReply(body string) srctest.Reply {
	return srctest.Reply{Status: 200, Body: []byte(body), Header: http.Header{"Content-Type": {"application/json"}}}
}

func sseReply(body string) srctest.Reply {
	return srctest.Reply{Status: 200, Body: []byte(body), Header: http.Header{"Content-Type": {"text/event-stream"}}}
}

func okResult(id, sc string) srctest.Reply {
	return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[{"type":"text","text":"x"}],"structuredContent":` + sc + `,"isError":false}}`)
}

type out struct {
	Rows []string `json:"rows"`
}

func open(t *testing.T, s *server) *Client {
	t.Helper()
	c, err := New(s.doer(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c
}

func TestCallJSONReply(t *testing.T) {
	s := &server{callReply: func(id string, req map[string]any) srctest.Reply {
		p := req["params"].(map[string]any)
		if p["name"] != "search_jobs" || p["arguments"].(map[string]any)["keyword"] != "go" {
			return srctest.Reply{Status: 400}
		}
		return okResult(id, `{"rows":["a","b"]}`)
	}}
	c := open(t, s)
	var o out
	if err := c.Call(context.Background(), "search_jobs", map[string]any{"keyword": "go"}, &o); err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.Rows, ",") != "a,b" {
		t.Fatalf("rows = %v", o.Rows)
	}
	// initialize, initialized, tools/call; the later two name the revision.
	if len(s.seen) != 3 {
		t.Fatalf("requests = %d, want 3", len(s.seen))
	}
	for _, r := range s.seen[1:] {
		if r.Header.Get(versionHeader) != ProtocolVersion {
			t.Fatalf("%s header = %q", versionHeader, r.Header.Get(versionHeader))
		}
		if r.Header.Get(sessionHeader) != "" {
			t.Fatal("a session id was sent that the server never assigned")
		}
	}
	for _, r := range s.seen {
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || !strings.Contains(r.Header.Get("Accept"), "application/json") {
			t.Fatalf("Accept = %q", r.Header.Get("Accept"))
		}
	}
}

func TestCallEventStreamSkipsOtherMessages(t *testing.T) {
	s := &server{callReply: func(id string, _ map[string]any) srctest.Reply {
		// A comment, a server notification, a response to another id, then
		// ours split over two data lines, with CRLF endings.
		body := ": keepalive\r\n\r\n" +
			"event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\r\n\r\n" +
			"event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":999,\"result\":{\"structuredContent\":{\"rows\":[\"wrong\"]}}}\r\n\r\n" +
			"event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + id + ",\r\ndata: \"result\":{\"structuredContent\":{\"rows\":[\"ok\"]},\"isError\":false}}\r\n\r\n"
		return sseReply(body)
	}}
	s.initReply = func(id string) srctest.Reply {
		return sseReply("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + id + ",\"result\":{\"protocolVersion\":\"2025-06-18\"}}\n\n")
	}
	c := open(t, s)
	var o out
	if err := c.Call(context.Background(), "t", map[string]any{}, &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Rows) != 1 || o.Rows[0] != "ok" {
		t.Fatalf("rows = %v", o.Rows)
	}
}

func TestSessionIDIsKeptAndSent(t *testing.T) {
	s := &server{callReply: func(id string, _ map[string]any) srctest.Reply { return okResult(id, `{"rows":[]}`) }}
	s.initReply = func(id string) srctest.Reply {
		r := jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"protocolVersion":"2025-03-26"}}`)
		r.Header.Set(sessionHeader, "abc-123")
		return r
	}
	c := open(t, s)
	var o out
	if err := c.Call(context.Background(), "t", nil, &o); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.seen[1:] {
		if r.Header.Get(sessionHeader) != "abc-123" {
			t.Fatalf("session header = %q", r.Header.Get(sessionHeader))
		}
		if r.Header.Get(versionHeader) != "2025-03-26" {
			t.Fatalf("the negotiated revision was not sent: %q", r.Header.Get(versionHeader))
		}
	}
}

func TestBadHandshakes(t *testing.T) {
	cases := []struct {
		name   string
		init   func(id string) srctest.Reply
		notify srctest.Reply
		want   error
		substr string
	}{
		{name: "init 404", init: func(string) srctest.Reply { return srctest.Reply{Status: 404} }, want: ErrEndpointGone},
		{name: "init 202", init: func(string) srctest.Reply { return srctest.Reply{Status: 202} }, want: ErrProtocol},
		{name: "init 429", init: func(string) srctest.Reply { return srctest.Reply{Status: 429} }, substr: "429"},
		{name: "init 500", init: func(string) srctest.Reply { return srctest.Reply{Status: 500} }, substr: "500"},
		{name: "init not json", init: func(string) srctest.Reply { return jsonReply("<html>") }, want: ErrProtocol},
		{name: "init no version", init: func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{}}`)
		}, want: ErrProtocol},
		{name: "init wrong id", init: func(string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":77,"result":{"protocolVersion":"2025-06-18"}}`)
		}, want: ErrProtocol},
		{name: "init rpc error", init: func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32602,"message":"bad\u0007 version"}}`)
		}, substr: "error -32602: bad version"},
		{name: "bad session id", init: func(id string) srctest.Reply {
			r := jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"protocolVersion":"2025-06-18"}}`)
			r.Header.Set(sessionHeader, "has space")
			return r
		}, want: ErrProtocol},
		{name: "oversized init", init: func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"protocolVersion":"2025-06-18","pad":"` + strings.Repeat("x", maxInitReply) + `"}}`)
		}, want: srcutil.ErrBodyTooLarge},
		{name: "notify refused", notify: srctest.Reply{Status: 400}, substr: "400"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{initReply: tc.init, notifyReply: tc.notify}
			c, err := New(s.doer(), endpoint)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Open(context.Background())
			if err == nil {
				t.Fatal("Open succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.substr != "" && !strings.Contains(err.Error(), tc.substr) {
				t.Fatalf("err = %v, want it to say %q", err, tc.substr)
			}
			if c.Call(context.Background(), "t", nil, &out{}) == nil {
				t.Fatal("Call succeeded on a client whose Open failed")
			}
		})
	}
}

func TestBadCallReplies(t *testing.T) {
	cases := []struct {
		name  string
		reply func(id string) srctest.Reply
		check func(t *testing.T, err error)
	}{
		{"tool error", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[{"type":"text","text":"Job not found:\n\u0000 x"}],"isError":true}}`)
		}, func(t *testing.T, err error) {
			var te *ToolError
			if !errors.As(err, &te) || te.Tool != "t" || te.Text != "Job not found: x" {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"tool error without text", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[],"isError":true}}`)
		}, func(t *testing.T, err error) {
			var te *ToolError
			if !errors.As(err, &te) || te.Text != "(no message)" {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"rpc error", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32601,"message":"no such tool"}}`)
		}, func(t *testing.T, err error) {
			var re *RPCError
			if !errors.As(err, &re) || re.Code != -32601 {
				t.Fatalf("err = %#v", err)
			}
		}},
		{"no structuredContent", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[{"type":"text","text":"{\"rows\":[]}"}]}}`)
		}, isErr(ErrProtocol)},
		{"structuredContent null", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"structuredContent":null}}`)
		}, isErr(ErrProtocol)},
		{"structuredContent wrong type", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"structuredContent":{"rows":"notalist"}}}`)
		}, isErr(ErrProtocol)},
		{"batch", func(id string) srctest.Reply {
			return jsonReply(`[{"jsonrpc":"2.0","id":` + id + `,"result":{"structuredContent":{}}}]`)
		}, isErr(ErrProtocol)},
		{"empty body", func(string) srctest.Reply { return jsonReply("") }, isErr(ErrProtocol)},
		{"html", func(string) srctest.Reply { return jsonReply("<!doctype html><p>blocked</p>") }, isErr(ErrProtocol)},
		{"stream without our id", func(string) srctest.Reply {
			return sseReply("data: {\"jsonrpc\":\"2.0\",\"id\":424242,\"result\":{}}\n\n")
		}, isErr(ErrProtocol)},
		{"stream with non-object data", func(string) srctest.Reply { return sseReply("data: [1,2]\n\n") }, isErr(ErrProtocol)},
		{"response with neither", func(id string) srctest.Reply { return jsonReply(`{"jsonrpc":"2.0","id":` + id + `}`) }, isErr(ErrProtocol)},
		{"only a server request reusing our id", func(id string) srctest.Reply {
			return sseReply("data: {\"jsonrpc\":\"2.0\",\"id\":" + id + ",\"method\":\"ping\"}\n\n")
		}, isErr(ErrProtocol)},
		{"not found", func(string) srctest.Reply { return srctest.Reply{Status: 404} }, func(t *testing.T, err error) {
			if !errors.Is(err, ErrEndpointGone) || errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Fatalf("err = %v, want ErrEndpointGone and not ErrBoardNotFound", err)
			}
		}},
		{"rate limited", func(string) srctest.Reply { return srctest.Reply{Status: 429} }, func(t *testing.T, err error) {
			var rl *srcutil.RateLimitedError
			if !errors.As(err, &rl) {
				t.Fatalf("err = %v, want a RateLimitedError", err)
			}
		}},
		{"server error", func(string) srctest.Reply { return srctest.Reply{Status: 500} }, func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "500") {
				t.Fatalf("err = %v", err)
			}
		}},
		{"transport failure", func(string) srctest.Reply { return srctest.Reply{Err: errors.New("connection reset")} }, func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "connection reset") {
				t.Fatalf("err = %v", err)
			}
		}},
		{"oversized", func(id string) srctest.Reply {
			return jsonReply(`{"jsonrpc":"2.0","id":` + id + `,"result":{"structuredContent":{"rows":["` + strings.Repeat("x", MaxReply) + `"]}}}`)
		}, isErr(srcutil.ErrBodyTooLarge)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{callReply: func(id string, _ map[string]any) srctest.Reply { return tc.reply(id) }}
			c := open(t, s)
			var o out
			err := c.Call(context.Background(), "t", map[string]any{}, &o)
			if err == nil {
				t.Fatal("Call succeeded")
			}
			tc.check(t, err)
		})
	}
}

func isErr(want error) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}
}

func TestEndpointMustBeHTTPS(t *testing.T) {
	d := srctest.New(nil)
	for _, u := range []string{"", "http://mcp.example.test/mcp", "https://user:pw@mcp.example.test/mcp", "https:///mcp",
		"https://mcp.example.test/mcp?x=1", "https://mcp.example.test/mcp#f", "mcp.example.test/mcp"} {
		if _, err := New(d, u); !errors.Is(err, ErrEndpoint) {
			t.Errorf("New(%q) err = %v, want ErrEndpoint", u, err)
		}
	}
	if _, err := New(nil, endpoint); err == nil {
		t.Error("New with a nil doer succeeded")
	}
	if len(d.URLs()) != 0 {
		t.Fatal("New sent a request")
	}
}

func TestOpenTwiceAndCallBeforeOpen(t *testing.T) {
	s := &server{callReply: func(id string, _ map[string]any) srctest.Reply { return okResult(id, `{}`) }}
	c, err := New(s.doer(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), "t", nil, &out{}); err == nil {
		t.Fatal("Call before Open succeeded")
	}
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(context.Background()); err == nil {
		t.Fatal("second Open succeeded")
	}
}

func TestCanceledContextSendsNothing(t *testing.T) {
	s := &server{callReply: func(id string, _ map[string]any) srctest.Reply { return okResult(id, `{}`) }}
	c := open(t, s)
	n := len(s.seen)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Call(ctx, "t", nil, &out{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(s.seen) != n {
		t.Fatal("a canceled call was sent")
	}
}

func TestRequestIDsIncrease(t *testing.T) {
	var ids []string
	s := &server{callReply: func(id string, _ map[string]any) srctest.Reply {
		ids = append(ids, id)
		return okResult(id, `{}`)
	}}
	c := open(t, s)
	for range 3 {
		if err := c.Call(context.Background(), "t", nil, &out{}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(ids, ",") != "2,3,4" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestClean(t *testing.T) {
	long := strings.Repeat("é", 400)
	cases := map[string]string{
		"a\nb\tc":              "a b c",
		"  lead  and  trail  ": "lead and trail",
		"bidi‮override":        "bidi override",
		"bad\xffutf8":          "bad?utf8",
		"zero​width\u0000nl":   "zero width nl",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	got := Clean(long)
	if len(got) > maxErrText+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("Clean(long) = %d bytes, %q...", len(got), got[:10])
	}
	if !bytes.Equal([]byte(Clean("")), nil) && Clean("") != "" {
		t.Fatal("Clean of empty")
	}
}

// A server's request that happens to reuse the client's id (a ping) is not the
// response: it is skipped, and the response after it is read.
func TestServerRequestWithOurIDIsSkipped(t *testing.T) {
	s := &server{callReply: func(id string, _ map[string]any) srctest.Reply {
		return sseReply("data: {\"jsonrpc\":\"2.0\",\"id\":" + id + ",\"method\":\"ping\"}\n\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":" + id + ",\"result\":{\"structuredContent\":{\"rows\":[\"ok\"]}}}\n\n")
	}}
	c := open(t, s)
	var o out
	if err := c.Call(context.Background(), "t", nil, &o); err != nil || len(o.Rows) != 1 {
		t.Fatalf("rows %v, err %v", o.Rows, err)
	}
}
