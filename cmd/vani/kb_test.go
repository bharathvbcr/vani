package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/Vani/internal/rkbexec"
)

// A synthetic bank: no real person, no real employer.
const kbBank = `{
  "_about": "synthetic",
  "headers": {"default": {"name": "Synthetic Person", "contact": ["person@example.invalid"]}},
  "entries": {
    "alpha-job": {"title": "Engineer", "org": "Acme", "dates": "JAN 2020 – Present", "stack": "Go", "bullets": ["b1"]},
    "beta-proj": {"title": "Widget", "stack": "Go, Rust", "bullets": ["b2"]}
  },
  "bullets": {
    "b1": {"text": "Cut latency by {{clmLat}}."},
    "b2": {"text": "Shipped a widget."}
  },
  "lines": {}
}`

// fakeRKB plays `rkb claims export --out FILE`.
type fakeRKB struct{ calls int }

func (f *fakeRKB) Run(_ context.Context, c rkbexec.Command, _ time.Duration) (rkbexec.Output, error) {
	f.calls++
	if len(c.Args) != 4 || c.Args[2] != "--out" {
		return rkbexec.Output{}, fmt.Errorf("unexpected rkb args %v", c.Args)
	}
	return rkbexec.Output{}, os.WriteFile(c.Args[3], []byte(`{"claims":{"clmLat":{"value":"42%"}}}`), 0o600)
}

// fakeDevPrism answers career_get_profile and career_upsert_block. Blocks in
// confirm get DevPrism's inputRequired answer instead of being accepted.
type fakeDevPrism struct {
	blocks  []json.RawMessage
	confirm map[string]bool
	upserts []string
	closed  bool
	// state, when set, is the requestState an inputRequired answer carries;
	// an upsert carrying a request_state is then accepted.
	state     string
	confirmed []string

	// calls is every tool name received, in order; payloads is the last block
	// bytes upserted per id.
	calls    []string
	payloads map[string][]byte
	// deletes is every block id DevPrism deleted. A block listing bullets or
	// facts is answered with inputRequired first, as tools_career.rs:1081-1115
	// does; the state is bound to the id.
	deletes []string
	// deleteRejects names ids whose confirming call is refused with -32002;
	// deleteDown names an id whose delete call fails as if DevPrism died;
	// afterDeleteAsk runs after DevPrism asked about id, so a change made
	// there is a change between the plan and the confirmation.
	deleteRejects  map[string]bool
	deleteDown     string
	afterDeleteAsk func(f *fakeDevPrism, id string)
}

// mutating reports whether any received call changed DevPrism.
func (f *fakeDevPrism) mutating() bool {
	for _, c := range f.calls {
		if c == "career_upsert_block" || c == "career_delete_block" {
			return true
		}
	}
	return false
}

func (f *fakeDevPrism) deleteBlock(args any) (json.RawMessage, error) {
	m, ok := args.(map[string]json.RawMessage)
	if !ok {
		return nil, fmt.Errorf("args %T", args)
	}
	var id string
	if err := json.Unmarshal(m["block_id"], &id); err != nil {
		return nil, err
	}
	if f.deleteDown == id {
		return nil, errors.New("devprism went away")
	}
	idx := -1
	var blk struct {
		Bullets []json.RawMessage `json:"bullets"`
		Facts   []json.RawMessage `json:"facts"`
	}
	for i, raw := range f.blocks {
		var h struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &h) == nil && h.ID == id {
			idx = i
			_ = json.Unmarshal(raw, &blk)
		}
	}
	if idx < 0 {
		return nil, &resume.ToolError{Code: -32602, Message: "Block '" + id + "' not found"}
	}
	remove := func() json.RawMessage {
		f.blocks = append(f.blocks[:idx:idx], f.blocks[idx+1:]...)
		f.deletes = append(f.deletes, id)
		return json.RawMessage(fmt.Sprintf(`{"success":true,"deletedBlockId":%q}`, id))
	}
	if m["request_state"] != nil {
		if f.deleteRejects[id] {
			return nil, &resume.ToolError{Code: -32002, Message: "requestState was not issued by this server, or has already been used"}
		}
		return remove(), nil
	}
	if len(blk.Bullets) == 0 && len(blk.Facts) == 0 {
		return remove(), nil
	}
	state := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"tool":"career_delete_block","blockId":%q,"title":"t","org":"o","timestamp":1,"__nonce":"n-%s"}`, id, id)))
	if f.afterDeleteAsk != nil {
		defer f.afterDeleteAsk(f, id)
	}
	return json.Marshal(map[string]any{"resultType": "inputRequired", "inputRequests": map[string]any{"confirm": map[string]any{"message": "Are you sure?"}}, "requestState": state})
}

func (f *fakeDevPrism) CallTool(_ context.Context, name string, args any) (json.RawMessage, error) {
	f.calls = append(f.calls, name)
	switch name {
	case "career_delete_block":
		return f.deleteBlock(args)
	case "career_get_profile":
		blocks := f.blocks
		if blocks == nil {
			blocks = []json.RawMessage{}
		}
		return json.Marshal(map[string]any{"blocks": blocks})
	case "career_upsert_block":
		m, ok := args.(map[string]json.RawMessage)
		if !ok {
			return nil, fmt.Errorf("args %T", args)
		}
		var b struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(m["block"], &b); err != nil {
			return nil, err
		}
		f.upserts = append(f.upserts, b.ID)
		if f.payloads == nil {
			f.payloads = map[string][]byte{}
		}
		f.payloads[b.ID] = append([]byte(nil), m["block"]...)
		if m["request_state"] != nil {
			f.confirmed = append(f.confirmed, b.ID)
			return json.Marshal(map[string]any{"success": true, "blockId": b.ID})
		}
		if f.confirm[b.ID] && f.state != "" {
			return json.Marshal(map[string]any{"resultType": "inputRequired", "inputRequests": map[string]any{"confirm": map[string]any{"message": "would drop bullets"}}, "requestState": f.state})
		}
		if f.confirm[b.ID] {
			return json.RawMessage(`{"resultType":"inputRequired","inputRequests":{"confirm":{"message":"would drop bullets"}}}`), nil
		}
		return json.Marshal(map[string]any{"success": true, "blockId": b.ID})
	}
	return nil, fmt.Errorf("unexpected tool %s", name)
}

func (f *fakeDevPrism) Close() error { f.closed = true; return nil }

func kbSetup(t *testing.T, devprism string) (bank, rkb string, fk *fakeRKB) {
	t.Helper()
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	if devprism != "" {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.DevPrism.Binary = devprism
		raw, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	bank = filepath.Join(dir, "bank.json")
	if err := os.WriteFile(bank, []byte(kbBank), 0o600); err != nil {
		t.Fatal(err)
	}
	fk = &fakeRKB{}
	old := rkbRunner
	rkbRunner = fk
	t.Cleanup(func() { rkbRunner = old })
	return bank, filepath.Join(dir, "rkb"), fk
}

func fakeStart(t *testing.T, f *fakeDevPrism) *int {
	t.Helper()
	starts := 0
	old := startKB
	startKB = func(context.Context, resume.Config) (kbClient, error) { starts++; return f, nil }
	t.Cleanup(func() { startKB = old })
	return &starts
}

func TestKBSyncDryRunStartsNothing(t *testing.T) {
	bank, rkb, fk := kbApprovedSimple(t, "")
	starts := fakeStart(t, &fakeDevPrism{})
	o, _ := mustCode(t, 0, "kb", "sync", "--dry-run", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(o, "live DevPrism state was not read") || !strings.Contains(o, "2 add") {
		t.Fatalf("dry run output %q", o)
	}
	if *starts != 0 {
		t.Fatal("a dry run must not start DevPrism")
	}
	if fk.calls != 1 {
		t.Fatalf("rkb ran %d times", fk.calls)
	}
	if strings.Contains(o, "person@example.invalid") || strings.Contains(o, "Synthetic Person") {
		t.Fatal("dry run printed header personal data")
	}
}

func TestKBSyncRefusals(t *testing.T) {
	bank, rkb, fk := kbSetup(t, "")
	starts := fakeStart(t, &fakeDevPrism{})
	defer func() {
		if fk.calls != 0 {
			t.Errorf("a refused sync ran rkb %d times", fk.calls)
		}
	}()
	// A real sync without devprism.binary fails before anything runs.
	_, e := mustCode(t, 1, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "devprism.binary is not configured") {
		t.Fatalf("stderr %q", e)
	}
	mustCode(t, 2, "kb", "sync", "--dry-run")                                      // no bank or rkb
	mustCode(t, 2, "kb", "sync", "--dry-run", "--bank", "bank.json", "--rkb", rkb) // relative
	mustCode(t, 2, "kb")
	mustCode(t, 2, "kb", "push")
	if *starts != 0 {
		t.Fatal("a refused sync started DevPrism")
	}
}

func TestKBSyncAppliesAndRemembers(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	f := &fakeDevPrism{confirm: map[string]bool{"beta-proj": true}}
	fakeStart(t, f)
	o, _ := mustCode(t, 0, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(o, "1 added") || !strings.Contains(o, "1 need confirmation") ||
		!strings.Contains(o, "needs_confirmation") || !strings.Contains(o, "would drop bullets") {
		t.Fatalf("sync output %q", o)
	}
	if !f.closed {
		t.Fatal("DevPrism client not closed")
	}

	// The accepted block is remembered; the unconfirmed one is retried, and
	// DevPrism now holds alpha-job so its unchanged hash sends nothing.
	f2 := &fakeDevPrism{confirm: map[string]bool{"beta-proj": true}, blocks: f.blocksFor(t, "alpha-job")}
	fakeStart(t, f2)
	o, _ = mustCode(t, 0, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if strings.Join(f2.upserts, ",") != "beta-proj" {
		t.Fatalf("second sync upserted %v, want only beta-proj; output %q", f2.upserts, o)
	}
}

// blocksFor returns minimal DevPrism blocks for ids, as career_get_profile
// would list them after an accepted upsert.
func (f *fakeDevPrism) blocksFor(t *testing.T, ids ...string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, id := range ids {
		out = append(out, json.RawMessage(fmt.Sprintf(`{"id":%q}`, id)))
	}
	return out
}

func TestKBSyncDevPrismStartFailure(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	old := startKB
	startKB = func(context.Context, resume.Config) (kbClient, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { startKB = old })
	_, e := mustCode(t, 1, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "devprism: boom") {
		t.Fatalf("stderr %q", e)
	}
}

// lockedAlpha is DevPrism's synthetic copy of alpha-job: its one bullet is
// locked with wording rkb would replace, so DevPrism asks before overwriting.
const lockedAlpha = `{"id":"alpha-job","kind":"work","title":"Engineer","org":"Acme","dateRange":{"start":"2020-01","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","facts":[],
  "bullets":[{"id":"alpha-job/b1","canonical":"synthetic old wording","variants":{},"metrics":[],"evidenceRefs":[],"locked":true}]}`

// lockedAlphaState is the requestState DevPrism would issue for it
// (tools_career.rs:945-952, protocol.rs:305-330).
var lockedAlphaState = base64.StdEncoding.EncodeToString([]byte(`{"tool":"career_upsert_block","blockId":"alpha-job","droppedBulletIds":[],"droppedFactIds":[],"modifiedLockedBullets":["alpha-job/b1"],"timestamp":1,"__nonce":"n1"}`))

func TestKBSyncConfirm(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	f := &fakeDevPrism{confirm: map[string]bool{"alpha-job": true}, state: lockedAlphaState, blocks: []json.RawMessage{json.RawMessage(lockedAlpha)}}
	fakeStart(t, f)
	o, _ := mustCode(t, 0, "kb", "sync", "--confirm", "--bank", bank, "--rkb", rkb)
	show := strings.Index(o, "confirming alpha-job; DevPrism will discard:\n  locked bullet rewritten, was alpha-job/b1: \"synthetic old wording\"\n")
	sum := strings.Index(o, "kb sync: 1 added, 0 updated")
	if show < 0 || sum < show || !strings.Contains(o, "1 confirmed, 0 confirmation refused") {
		t.Fatalf("sync output %q", o)
	}
	if strings.Join(f.confirmed, ",") != "alpha-job" {
		t.Fatalf("confirmed %v, want only alpha-job", f.confirmed)
	}
	// The confirmed block is remembered: the next run sends nothing.
	f2 := &fakeDevPrism{confirm: map[string]bool{"alpha-job": true}, state: lockedAlphaState, blocks: f.blocksFor(t, "alpha-job", "beta-proj")}
	fakeStart(t, f2)
	o, _ = mustCode(t, 0, "kb", "sync", "--confirm", "--bank", bank, "--rkb", rkb)
	if len(f2.upserts) != 0 || strings.Contains(o, "confirming") {
		t.Fatalf("second sync upserted %v; output %q", f2.upserts, o)
	}
}

// Without --confirm the run is unchanged: reported, skipped, nothing sent back.
func TestKBSyncWithoutConfirmSendsNoConfirmation(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	f := &fakeDevPrism{confirm: map[string]bool{"alpha-job": true}, state: lockedAlphaState, blocks: []json.RawMessage{json.RawMessage(lockedAlpha)}}
	fakeStart(t, f)
	o, _ := mustCode(t, 0, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if len(f.confirmed) != 0 || strings.Contains(o, "confirming") || !strings.Contains(o, "needs_confirmation") {
		t.Fatalf("confirmed %v; output %q", f.confirmed, o)
	}
}

// --dry-run never confirms: with --confirm it is refused before anything runs.
func TestKBSyncDryRunNeverConfirms(t *testing.T) {
	bank, rkb, fk := kbSetup(t, "/opt/devprism/devprism")
	starts := fakeStart(t, &fakeDevPrism{})
	_, e := mustCode(t, 2, "kb", "sync", "--dry-run", "--confirm", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "--confirm cannot be used with --dry-run") {
		t.Fatalf("stderr %q", e)
	}
	if *starts != 0 || fk.calls != 0 {
		t.Fatalf("started DevPrism %d times, rkb %d times", *starts, fk.calls)
	}
}
