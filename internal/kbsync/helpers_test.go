package kbsync

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/resume"
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

	// DevPrism's elicitation store (elicitation.rs:87-167): single-use nonces
	// bound to tool and subject, held in the server's memory.
	pending  map[string]string // nonce -> subject
	nonces   int
	issued   []string // every requestState handed out, in order
	blockArg [][]byte // the exact "block" bytes of every upsert
	// beforeConfirm runs, with the lock held, when an upsert arrives carrying
	// a request_state, before it is checked: a change made here is a change
	// DevPrism sees between the two calls.
	beforeConfirm func(f *fakeDevPrism)
	// beforeProfile runs, with the lock held, before each career_get_profile
	// answer after the first profileHookAfter.
	beforeProfile    func(f *fakeDevPrism)
	profileHookAfter int
	profiles         int
	// confirmErr, when set, is returned for an upsert carrying a request_state.
	confirmErr error
}

// forget empties the elicitation store, as a DevPrism restart would.
func (f *fakeDevPrism) forget() { f.pending = map[string]string{} }

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
		f.profiles++
		if f.beforeProfile != nil && f.profiles > f.profileHookAfter {
			f.beforeProfile(f)
		}
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
			Block          json.RawMessage `json:"block"`
			RequestState   *string         `json:"request_state"`
			InputResponses struct {
				Confirm *bool `json:"confirm"`
			} `json:"input_responses"`
		}
		var blk map[string]any
		if err := json.Unmarshal(raw, &in); err != nil || json.Unmarshal(in.Block, &blk) != nil || blk == nil {
			return nil, errors.New("fake: bad arguments")
		}
		f.blockArg = append(f.blockArg, append([]byte(nil), in.Block...))
		if in.RequestState != nil {
			if f.beforeConfirm != nil {
				f.beforeConfirm(f)
			}
			if f.confirmErr != nil {
				return nil, f.confirmErr
			}
		}
		id, _ := blk["id"].(string)
		if prior, ok := f.blocks[id]; ok {
			if loss := overwriteLoss(prior, blk); loss.destructive() {
				// The subject is the block id and a digest of the exact
				// payload (tools_career.rs:144-148). serde_json re-serialises
				// the parsed value; json.Marshal of the parsed map is the
				// same kind of canonical form.
				canon, _ := json.Marshal(blk)
				subject := fmt.Sprintf("%s:%x", id, sha1.Sum(canon))
				if in.RequestState == nil {
					return f.issue(subject, id, loss)
				}
				if err := f.consume(*in.RequestState, subject); err != nil {
					return nil, err
				}
				if in.InputResponses.Confirm == nil || !*in.InputResponses.Confirm {
					return json.RawMessage(`{"success":false,"cancelled":true,"message":"Block overwrite was cancelled by the user"}`), nil
				}
			}
		}
		_, existed := f.blocks[id]
		f.blocks[id] = blk
		return json.Marshal(map[string]any{"success": true, "blockId": id, "created": !existed})
	}
	return nil, errors.New("fake: unknown tool " + name)
}

// issue answers inputRequired with a requestState shaped as DevPrism's
// (tools_career.rs:926-959, protocol.rs:305-330).
func (f *fakeDevPrism) issue(subject, id string, loss fakeLoss) (json.RawMessage, error) {
	if f.pending == nil {
		f.pending = map[string]string{}
	}
	f.nonces++
	nonce := fmt.Sprintf("nonce-%d", f.nonces)
	f.pending[nonce] = subject
	st, err := json.Marshal(map[string]any{
		"tool": "career_upsert_block", "blockId": id,
		"droppedBulletIds": loss.bullets, "droppedFactIds": loss.facts, "modifiedLockedBullets": loss.locked,
		"timestamp": 1, "__nonce": nonce,
	})
	if err != nil {
		return nil, err
	}
	state := base64.StdEncoding.EncodeToString(st)
	f.issued = append(f.issued, state)
	return json.Marshal(map[string]any{
		"resultType": "inputRequired",
		"inputRequests": map[string]any{"confirm": map[string]any{
			"type":    "confirmation",
			"message": "Overwriting block 'E' at 'A' will permanently discard " + loss.describe() + ". Continue?",
			"schema":  map[string]any{"type": "boolean"},
		}},
		"requestState": state,
	})
}

// consume checks a returned requestState as tools_career.rs:886-911 and
// elicitation.rs:145-166 do, answering a refusal with error -32002.
func (f *fakeDevPrism) consume(state, subject string) error {
	refuse := func(msg string) error { return &resume.ToolError{Code: -32002, Message: msg} }
	raw, err := base64.StdEncoding.DecodeString(state)
	if err != nil {
		return refuse("Invalid base64 in requestState")
	}
	var st struct {
		Nonce *string `json:"__nonce"`
	}
	if json.Unmarshal(raw, &st) != nil {
		return refuse("Invalid JSON in requestState")
	}
	if st.Nonce == nil {
		return refuse("requestState is not bound to a server-issued confirmation")
	}
	want, ok := f.pending[*st.Nonce]
	delete(f.pending, *st.Nonce)
	switch {
	case !ok:
		return refuse("requestState was not issued by this server, or has already been used")
	case want != subject:
		return refuse("requestState was issued for a different subject")
	}
	return nil
}

// fakeLoss mirrors OverwriteLoss (tools_career.rs:69-101).
type fakeLoss struct{ bullets, facts, locked []string }

func (l fakeLoss) destructive() bool { return len(l.bullets)+len(l.facts)+len(l.locked) > 0 }

func (l fakeLoss) describe() string {
	var parts []string
	if len(l.bullets) > 0 {
		parts = append(parts, fmt.Sprintf("%d bullet(s)", len(l.bullets)))
	}
	if len(l.facts) > 0 {
		parts = append(parts, fmt.Sprintf("%d fact(s)", len(l.facts)))
	}
	if len(l.locked) > 0 {
		parts = append(parts, fmt.Sprintf("%d locked bullet(s) rewritten", len(l.locked)))
	}
	return strings.Join(parts, ", ")
}

// overwriteLoss mirrors overwrite_loss (tools_career.rs:103-135): dropped
// bullet or fact ids, and locked bullets whose text changed or whose lock was
// cleared.
func overwriteLoss(prior, next map[string]any) fakeLoss {
	l := fakeLoss{bullets: []string{}, facts: []string{}, locked: []string{}}
	nb := map[string]map[string]any{}
	for _, b := range asList(next["bullets"]) {
		nb[b["id"].(string)] = b
	}
	for _, b := range asList(prior["bullets"]) {
		id := b["id"].(string)
		in, ok := nb[id]
		if !ok {
			l.bullets = append(l.bullets, id)
			continue
		}
		if locked, _ := b["locked"].(bool); locked {
			if lk, _ := in["locked"].(bool); in["canonical"] != b["canonical"] || !lk {
				l.locked = append(l.locked, id)
			}
		}
	}
	nf := map[string]bool{}
	for _, x := range asList(next["facts"]) {
		nf[x["id"].(string)] = true
	}
	for _, x := range asList(prior["facts"]) {
		if !nf[x["id"].(string)] {
			l.facts = append(l.facts, x["id"].(string))
		}
	}
	return l
}

func overwriteLoses(prior, next map[string]any) bool { return overwriteLoss(prior, next).destructive() }

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
