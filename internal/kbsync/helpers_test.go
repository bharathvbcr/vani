package kbsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic fixtures only: nothing here comes from the real bank.
const fixtureBank = `{
  "_about": "synthetic",
  "headers": {"default": {"name": "Synthetic Person", "contact": ["person@example.invalid"]}},
  "entries": {
    "gamma-edu": {"title": "Master of Science in Things", "org": "Example University", "dates": "AUG 2018 – MAY 2020", "bullets": []},
    "alpha-job": {"title": "Engineer", "org": "Acme", "dates": "JAN 2020 – Present", "stack": "Go", "bullets": ["b1", "b2"]},
    "beta-proj": {"title": "Widget", "stack": "Go, Rust", "bullets": ["b3"]}
  },
  "bullets": {
    "b1": {"text": "Cut latency by {{clmLat}} using **caching**.", "sources": ["x"]},
    "b2": {"text": "Wrote [docs](https://example.invalid/d) for it."},
    "b3": {"text": "Handled {{clmQps}} requests."},
    "unattached": {"text": "nobody lists me"}
  },
  "lines": {}
}`

var fixtureClaims = Claims{
	Values:  map[string]string{"clmLat": "42%", "clmQps": "1.2M/s"},
	Caution: map[string]bool{},
}

var fixedNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func mustBank(t *testing.T, src string) Bank {
	t.Helper()
	b, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return b
}

func fixtureResolved(t *testing.T) Resolved {
	t.Helper()
	return ResolveBank(mustBank(t, fixtureBank), fixtureClaims)
}

// fakeDevPrism emulates the parts of DevPrism's MCP career tools the sync
// touches, including the overwrite gate (tools_career.rs:103-135, 879-960).
type fakeDevPrism struct {
	mu     sync.Mutex
	blocks map[string]map[string]any
	calls  []fakeCall
	// failOn makes the Nth upsert call (1-based) return an error.
	failOn int
	// weirdAnswer, when set, replaces the upsert answer.
	weirdAnswer json.RawMessage
}

type fakeCall struct {
	Tool string
	Args []byte // json.Marshal(args), as the client would send
}

func newFake() *fakeDevPrism { return &fakeDevPrism{blocks: map[string]map[string]any{}} }

func (f *fakeDevPrism) seed(t *testing.T, raw string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	f.blocks[m["id"].(string)] = m
}

func (f *fakeDevPrism) upserts() []fakeCall {
	var out []fakeCall
	for _, c := range f.calls {
		if c.Tool == "career_upsert_block" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeDevPrism) CallTool(_ context.Context, name string, args any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	f.calls = append(f.calls, fakeCall{Tool: name, Args: raw})
	switch name {
	case "career_get_profile":
		list := []any{}
		for _, id := range sortedKeys(f.blocks) {
			list = append(list, f.blocks[id])
		}
		return json.Marshal(map[string]any{"blocks": list, "personas": []any{}, "totalBlocks": len(list)})
	case "career_upsert_block":
		if f.failOn > 0 && len(f.upserts()) == f.failOn {
			return nil, errors.New("simulated transport failure")
		}
		if f.weirdAnswer != nil {
			return f.weirdAnswer, nil
		}
		var in struct {
			Block         map[string]any `json:"block"`
			RequestState  string         `json:"request_state"`
			InputResponse any            `json:"input_responses"`
		}
		if err := json.Unmarshal(raw, &in); err != nil || in.Block == nil {
			return nil, errors.New("fake: bad arguments")
		}
		id, _ := in.Block["id"].(string)
		if prior, ok := f.blocks[id]; ok && overwriteLoses(prior, in.Block) {
			return json.RawMessage(`{"resultType":"inputRequired","inputRequests":{"confirm":{"kind":"confirmation","message":"Overwriting block will permanently discard 1 locked bullet(s) rewritten. Continue?","schema":{"type":"boolean"}}},"requestState":"c3RhdGU="}`), nil
		}
		_, existed := f.blocks[id]
		f.blocks[id] = in.Block
		return json.Marshal(map[string]any{"success": true, "blockId": id, "created": !existed})
	}
	return nil, errors.New("fake: unknown tool " + name)
}

// overwriteLoses mirrors overwrite_loss: dropped bullet or fact ids, or a
// locked bullet whose text changed or whose lock was cleared.
func overwriteLoses(prior, next map[string]any) bool {
	nb := map[string]map[string]any{}
	for _, b := range asList(next["bullets"]) {
		nb[b["id"].(string)] = b
	}
	for _, b := range asList(prior["bullets"]) {
		in, ok := nb[b["id"].(string)]
		if !ok {
			return true
		}
		if locked, _ := b["locked"].(bool); locked {
			if in["canonical"] != b["canonical"] {
				return true
			}
			if l, _ := in["locked"].(bool); !l {
				return true
			}
		}
	}
	nf := map[string]bool{}
	for _, f := range asList(next["facts"]) {
		nf[f["id"].(string)] = true
	}
	for _, f := range asList(prior["facts"]) {
		if !nf[f["id"].(string)] {
			return true
		}
	}
	return false
}

func asList(v any) []map[string]any {
	var out []map[string]any
	l, _ := v.([]any)
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// fakeRunner plays rkb: it writes the claims file named by --out.
type fakeRunner struct {
	cmd     Command
	content []byte
	err     error
	noFile  bool
}

func (r *fakeRunner) Run(_ context.Context, c Command) error {
	r.cmd = c
	if r.err != nil {
		return r.err
	}
	if r.noFile {
		return nil
	}
	for i, a := range c.Args {
		if a == "--out" && i+1 < len(c.Args) {
			return os.WriteFile(c.Args[i+1], r.content, 0o600)
		}
	}
	return errors.New("fake: no --out")
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func jsonEq(t *testing.T, got, want []byte) {
	t.Helper()
	var a, b bytes.Buffer
	if err := json.Indent(&a, got, "", "  "); err != nil {
		t.Fatalf("got is not JSON: %v\n%s", err, got)
	}
	if err := json.Indent(&b, want, "", "  "); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if strings.TrimSpace(a.String()) != strings.TrimSpace(b.String()) {
		t.Fatalf("JSON differs\n--- got ---\n%s\n--- want ---\n%s", a.String(), b.String())
	}
}
