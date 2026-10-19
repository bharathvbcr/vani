package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/resume"
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

func (f *fakeRKB) Run(_ context.Context, c kbsync.Command) error {
	f.calls++
	if len(c.Args) != 4 || c.Args[2] != "--out" {
		return fmt.Errorf("unexpected rkb args %v", c.Args)
	}
	return os.WriteFile(c.Args[3], []byte(`{"claims":{"clmLat":{"value":"42%"}}}`), 0o600)
}

// fakeDevPrism answers career_get_profile and career_upsert_block. Blocks in
// confirm get DevPrism's inputRequired answer instead of being accepted.
type fakeDevPrism struct {
	blocks  []json.RawMessage
	confirm map[string]bool
	upserts []string
	closed  bool
}

func (f *fakeDevPrism) CallTool(_ context.Context, name string, args any) (json.RawMessage, error) {
	switch name {
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
	bank, rkb, fk := kbSetup(t, "")
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
	bank, rkb, _ := kbSetup(t, "/opt/devprism/devprism")
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
	bank, rkb, _ := kbSetup(t, "/opt/devprism/devprism")
	old := startKB
	startKB = func(context.Context, resume.Config) (kbClient, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { startKB = old })
	_, e := mustCode(t, 1, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(e, "devprism: boom") {
		t.Fatalf("stderr %q", e)
	}
}
