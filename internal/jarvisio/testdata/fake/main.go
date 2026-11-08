// Command fake speaks one JSON object per line, the way jarvis serve does,
// so the client can be tested without the Jarvis module. The mode file
// <root>/fake-mode, where root is --root, selects the script.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	root := "."
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--root" {
			root = os.Args[i+1]
		}
	}
	mode, _ := os.ReadFile(filepath.Join(root, "fake-mode"))
	script := strings.TrimSpace(string(mode))
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 1<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     string          `json:"id"`
			Op     string          `json:"op"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			_ = out.Encode(map[string]any{"id": "", "ok": false, "error": map[string]string{"code": "E_BAD_REQUEST", "message": "bad json"}})
			continue
		}
		switch script {
		case "down":
			fmt.Fprintln(os.Stderr, "jarvis: down")
			os.Exit(1)
		case "silent":
			return
		case "sleep":
			time.Sleep(time.Hour)
			return
		case "bad-request":
			if req.Op == "hello" {
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"protocol": 1}})
				continue
			}
			_ = out.Encode(map[string]any{"id": req.ID, "ok": false, "error": map[string]string{"code": "E_BAD_REQUEST", "message": "unknown field"}})
		case "bank-missing":
			if req.Op == "hello" {
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"protocol": 1}})
				continue
			}
			if req.Op == "jarvis.inputs.put" {
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"stored": true}})
				continue
			}
			_ = out.Encode(map[string]any{"id": req.ID, "ok": false, "error": map[string]string{
				"code": "E_JARVIS", "message": "inputs read failed: bank executable is missing",
			}})
		case "echo-secret":
			// A misbehaving serve that puts the submitted text into the error.
			var p struct {
				Value struct {
					Text string `json:"text"`
				} `json:"value"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if req.Op == "hello" {
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"protocol": 1}})
				continue
			}
			_ = out.Encode(map[string]any{"id": req.ID, "ok": false, "error": map[string]string{
				"code": "E_JARVIS", "message": "refused " + p.Value.Text,
			}})
		default:
			if req.Op == "hello" {
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"protocol": 1}})
				continue
			}
			switch req.Op {
			case "jarvis.inputs.put":
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"stored": true}})
			case "jarvis.inputs.list-keys":
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"keys": []string{"email"}}})
			case "jarvis.inputs.get":
				_ = out.Encode(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{
					"key": "email", "value": map[string]string{"type": "string", "text": "stored-example"},
				}})
			default:
				_ = out.Encode(map[string]any{"id": req.ID, "ok": false, "error": map[string]string{"code": "E_JARVIS", "message": "unknown op"}})
			}
		}
	}
}
