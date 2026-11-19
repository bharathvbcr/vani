// Package mcpfake is a scripted stdio MCP server for tests: a test binary
// re-executes itself with EnvVar set and calls Run from TestMain (or a
// helper test), so internal/mcpstdio and cmd/vani exercise a real child
// process. Every reply is preceded by noise a real server may send: a
// notification, a server request, a stale response, a line that is not JSON.
//
// The tool name selects a tool's behaviour; EnvMode selects a server-wide one.
// EnvLog names a file every received line is appended to, so a test can see
// what was (and was not) sent.
package mcpfake

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"strings"
	"time"
)

// Environment the fake reads.
const (
	EnvVar  = "VANI_MCPFAKE"
	EnvMode = "VANI_MCPFAKE_MODE"
	EnvLog  = "VANI_MCPFAKE_LOG"
)

// Modes.
const (
	ModeInitError    = "init_error"    // initialize answers a JSON-RPC error
	ModeInitHang     = "init_hang"     // initialize is never answered
	ModeExitEarly    = "exit_early"    // exit before reading anything
	ModeListHang     = "list_hang"     // tools/list is never answered
	ModeEmptyTools   = "empty_tools"   // tools/list has no tools
	ModePaged        = "paged"         // the tools come in two pages
	ModePagedLoop    = "paged_loop"    // every page names the same next cursor
	ModeEndlessPages = "endless_pages" // every page names a new next cursor
	ModeDupTools     = "dup_tools"     // a tool name appears twice
	ModeBadList      = "bad_list"      // tools/list result is an array
)

// Tools the fake lists, by behaviour. All are read-only unless named otherwise.
var readOnly = []string{
	"read_struct", "read_text_json", "read_text", "read_multi", "read_iserror",
	"read_rpcerror", "read_hang", "read_big", "read_die", "read_stderr",
	"read_env", "read_echo", "read_empty", "read_stopread", "read_null_result",
}

// Run serves stdin until it closes, then exits the process.
func Run() {
	mode := os.Getenv(EnvMode)
	if mode == ModeExitEarly {
		fmt.Fprintln(os.Stderr, "fake: exiting before the handshake")
		os.Exit(7)
	}
	var logf *os.File
	if p := os.Getenv(EnvLog); p != "" {
		logf, _ = os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	}
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		// Deterministic: v2 does not sort map keys, and a test that reads a
		// reply's text must see the same text every run.
		b, _ := json.Marshal(v, json.Deterministic(true))
		out.Write(append(b, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	page := 0
	for sc.Scan() {
		line := sc.Bytes()
		if logf != nil {
			logf.Write(append(append([]byte{}, line...), '\n'))
		}
		var req struct {
			ID     jsontext.Value `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments jsontext.Value `json:"arguments"`
				Cursor    string          `json:"cursor"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 {
			continue // a notification: no reply
		}
		id := req.ID
		reply := func(result any) {
			out.WriteString("not json at all\n")
			send(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{}})
			send(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "ping"})
			send(map[string]any{"jsonrpc": "2.0", "id": 9999, "result": map[string]any{"stale": true}})
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		}
		rpcErr := func(code int, msg string) {
			send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
		}
		switch req.Method {
		case "initialize":
			switch mode {
			case ModeInitError:
				rpcErr(-32601, "Method not found: initialize")
			case ModeInitHang:
			default:
				reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "mcpfake", "version": "1.0"}, "capabilities": map[string]any{"tools": map[string]any{}}})
			}
		case "tools/list":
			switch mode {
			case ModeListHang:
				continue
			case ModeEmptyTools:
				reply(map[string]any{"tools": []any{}})
			case ModeBadList:
				reply([]any{1, 2})
			case ModePaged:
				if req.Params.Cursor == "" {
					reply(map[string]any{"tools": tools()[:3], "nextCursor": "p2"})
				} else {
					reply(map[string]any{"tools": tools()[3:]})
				}
			case ModePagedLoop:
				reply(map[string]any{"tools": []any{}, "nextCursor": "same"})
			case ModeEndlessPages:
				page++
				reply(map[string]any{"tools": []any{tool(fmt.Sprintf("read_page_%d", page), true)}, "nextCursor": fmt.Sprintf("c%d", page)})
			case ModeDupTools:
				reply(map[string]any{"tools": append(tools(), tool("read_struct", false))})
			default:
				reply(map[string]any{"tools": tools()})
			}
		case "tools/call":
			callTool(req.Params.Name, req.Params.Arguments, reply, rpcErr)
		default:
			rpcErr(-32601, "Method not found")
		}
	}
	os.Exit(0)
}

func tool(name string, ro any) map[string]any {
	t := map[string]any{"name": name, "description": "fake " + name, "inputSchema": map[string]any{"type": "object"}}
	if ro != nil {
		t["annotations"] = map[string]any{"readOnlyHint": ro, "destructiveHint": false}
	}
	return t
}

func tools() []any {
	var out []any
	for _, n := range readOnly {
		out = append(out, tool(n, true))
	}
	out = append(out,
		tool("writer", false),
		tool("no_annotations", nil),
		tool("string_hint", "true"),
		map[string]any{"name": "hint_missing", "annotations": map[string]any{"destructiveHint": false}},
	)
	return out
}

func callTool(name string, args jsontext.Value, reply func(any), rpcErr func(int, string)) {
	text := func(s string) map[string]any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}
	}
	switch name {
	case "read_struct":
		reply(map[string]any{
			"structuredContent": map[string]any{"facets": map[string]any{"a": map[string]any{"ok": false, "error": "scan shed"}, "b": map[string]any{"ok": true}}},
			"content":           []any{map[string]any{"type": "text", "text": "summary"}},
		})
	case "read_text_json":
		reply(text(`{"agents":{"ok":false,"error":"unserved"},"n":1.50}`))
	case "read_text":
		reply(text("plain words, not JSON"))
	case "read_multi":
		reply(map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "first"},
			map[string]any{"type": "text", "text": "second"},
		}})
	case "read_iserror":
		reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": strings.Repeat("boom ", 400)}}})
	case "read_rpcerror":
		rpcErr(-32602, "Invalid params: repo_path is required")
	case "read_hang":
	case "read_big":
		reply(map[string]any{"structuredContent": map[string]any{"blob": strings.Repeat("A", 2<<20)}})
	case "read_die":
		os.Exit(3)
	case "read_stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 20000)+" Authorization: Bearer abcdefghijklmnop0123 sk-abcdefghijklmnopqrstuvwx TAILMARK")
		os.Exit(4)
	case "read_env":
		var keys []string
		for _, kv := range os.Environ() {
			keys = append(keys, strings.SplitN(kv, "=", 2)[0])
		}
		reply(map[string]any{"structuredContent": map[string]any{"env": keys}})
	case "read_echo":
		reply(map[string]any{"structuredContent": map[string]any{"args": args}})
	case "read_empty":
		reply(map[string]any{})
	case "read_null_result":
		reply(nil)
	case "read_stopread":
		// Answer, then never read stdin again: the pipe fills and the next
		// large write cannot finish.
		reply(text("{}"))
		time.Sleep(time.Minute)
		os.Exit(5)
	default:
		reply(text(`{"called":true}`))
	}
}
