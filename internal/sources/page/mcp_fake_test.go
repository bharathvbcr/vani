package page

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for lightpanda: run as
// `<test binary> mcp --fake-pages=P --fake-log=L ...`, it is fakeMCP.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(fakeMCP(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// fakePage scripts what the fake does for one URL.
type fakePage struct {
	Status   int    `json:"status,omitempty"`   // 0 means 200
	NoStatus bool   `json:"noStatus,omitempty"` // goto omits structuredContent
	HTML     string `json:"html,omitempty"`
	HTMLSize int    `json:"htmlSize,omitempty"` // html is this many 'x' bytes
	NavFail  string `json:"navFail,omitempty"`  // goto isError "navigation failed: X"
	RPCCode  int    `json:"rpcCode,omitempty"`  // goto answers a JSON-RPC error
	HTMLFail string `json:"htmlFail,omitempty"` // html isError
	Hang     bool   `json:"hang,omitempty"`     // goto never answers
	Crash    bool   `json:"crash,omitempty"`    // exit during goto
	Junk     bool   `json:"junk,omitempty"`     // html's reply is not JSON
	LongLine int    `json:"longLine,omitempty"` // html's reply is a line of this many bytes
	Partial  bool   `json:"partial,omitempty"`  // half of html's reply, then exit
	Chatter  int    `json:"chatter,omitempty"`  // notifications and stale replies before goto's reply
	Flood    int    `json:"flood,omitempty"`    // unsolicited lines after html's reply
	Stderr   int    `json:"stderr,omitempty"`   // bytes of stderr written before a crash
	DelayMs  int    `json:"delayMs,omitempty"`  // goto takes this long
}

// fakeEvent is one line of the fake's log.
type fakeEvent struct {
	PID       int             `json:"pid"`
	Ev        string          `json:"ev"` // start, req, eof
	Args      []string        `json:"args,omitempty"`
	Env       []string        `json:"env,omitempty"`
	Cwd       string          `json:"cwd,omitempty"`
	Method    string          `json:"method,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func fakeMCP(args []string) int {
	var pagesPath, logPath, initMode string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--fake-pages="):
			pagesPath = strings.TrimPrefix(a, "--fake-pages=")
		case strings.HasPrefix(a, "--fake-log="):
			logPath = strings.TrimPrefix(a, "--fake-log=")
		case strings.HasPrefix(a, "--fake-init="):
			initMode = strings.TrimPrefix(a, "--fake-init=")
		}
	}
	pages := map[string]fakePage{}
	if b, err := os.ReadFile(pagesPath); err == nil {
		_ = json.Unmarshal(b, &pages)
	}
	logf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	defer logf.Close()
	pid := os.Getpid()
	logEv := func(e fakeEvent) {
		e.PID = pid
		b, _ := json.Marshal(e)
		_, _ = logf.Write(append(b, '\n'))
	}
	cwd, _ := os.Getwd()
	logEv(fakeEvent{Ev: "start", Args: append([]string{"mcp"}, args...), Env: os.Environ(), Cwd: cwd})

	out := bufio.NewWriter(os.Stdout)
	reply := func(id json.RawMessage, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	rpcErr := func(id json.RawMessage, code int, msg string) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	raw := func(s string) {
		_, _ = out.WriteString(s)
		_ = out.Flush()
	}
	text := func(s string, isErr bool) map[string]any {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}, "isError": isErr}
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	var current fakePage
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return 3
		}
		logEv(fakeEvent{Ev: "req", Method: req.Method, Tool: req.Params.Name, Arguments: req.Params.Arguments})
		switch req.Method {
		case "initialize":
			switch initMode {
			case "fail":
				rpcErr(req.ID, -32600, "no")
			case "hang":
				time.Sleep(time.Hour)
			case "stopread":
				// Answer, then never read stdin again: a request larger
				// than the pipe buffer can then not be written.
				reply(req.ID, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
				time.Sleep(time.Hour)
			case "crash":
				fmt.Fprint(os.Stderr, "fake init crash")
				return 4
			default:
				reply(req.ID, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
			}
			continue
		case "tools/call":
		default:
			continue // notifications
		}
		switch req.Params.Name {
		case "goto":
			var a struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &a)
			p, ok := pages[a.URL]
			if !ok {
				p = fakePage{NavFail: "UnknownHost"}
			}
			current = p
			if p.DelayMs > 0 {
				time.Sleep(time.Duration(p.DelayMs) * time.Millisecond)
			}
			for i := range p.Chatter {
				raw(`{"jsonrpc":"2.0","method":"notifications/message","params":{"n":` + fmt.Sprint(i) + "}}\n")
				raw(`{"jsonrpc":"2.0","id":999999,"result":{"stale":true}}` + "\n")
			}
			switch {
			case p.Hang:
				time.Sleep(time.Hour)
			case p.Crash:
				fmt.Fprint(os.Stderr, strings.Repeat("e", p.Stderr)+"fake crash marker")
				return 5
			case p.RPCCode != 0:
				rpcErr(req.ID, p.RPCCode, "NavigationTimeout: no response arrived before the timeout")
			case p.NavFail != "":
				reply(req.ID, text("navigation failed: "+p.NavFail, true))
			default:
				res := text("Navigated.", false)
				if !p.NoStatus {
					status := p.Status
					if status == 0 {
						status = 200
					}
					res["structuredContent"] = map[string]any{"url": a.URL, "httpStatus": status}
				}
				reply(req.ID, res)
			}
		case "html":
			p := current
			switch {
			case p.Junk:
				raw("this is not json\n")
			case p.LongLine > 0:
				raw(strings.Repeat("a", p.LongLine) + "\n")
			case p.Partial:
				raw(`{"jsonrpc":"2.0","id":`)
				return 6
			case p.HTMLFail != "":
				reply(req.ID, text(p.HTMLFail, true))
			case p.HTMLSize > 0:
				reply(req.ID, text(strings.Repeat("x", p.HTMLSize), false))
			default:
				reply(req.ID, text(p.HTML, false))
			}
			for range p.Flood {
				raw(`{"jsonrpc":"2.0","method":"notifications/message"}` + "\n")
			}
		}
	}
	logEv(fakeEvent{Ev: "eof"})
	return 0
}
