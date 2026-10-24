package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// A synthetic bank for the approved-pool tests. Every reader passed b1, b2 and
// b4 on their exact wording; b3 cites a CAUTION claim; b5 was never reviewed.
//
//	alpha-job  b1 (approved)  b5 (unreviewed)   the selection lists both under alpha-job
//	beta-proj  b3 (CAUTION)                     listed with b3, so nothing in it may leave
//	gamma-proj b2 (approved)                    listed
//	delta-proj b4 (approved)                    in no selection
//	edu        no bullets, a degree             listed
func kbPoolBank() string {
	review := func(plain string) map[string]any {
		sum := sha1.Sum([]byte(plain))
		h := hex.EncodeToString(sum[:])[:12]
		pass := func(by string) []map[string]any {
			return []map[string]any{{"round": 1, "verdict": "pass", "text_hash": h, "by": by + "-r1"}, {"round": 2, "verdict": "pass", "text_hash": h, "by": by}}
		}
		return map[string]any{"hr": pass("hr"), "hm": pass("hm")}
	}
	bank := map[string]any{
		"headers": map[string]any{"default": map[string]any{"name": "Synthetic Person", "contact": []string{"person@example.invalid"}}},
		"entries": map[string]any{
			"alpha-job":  map[string]any{"title": "Engineer", "org": "Acme", "dates": "JAN 2020 – Present", "stack": "Go", "bullets": []string{"b1", "b5"}},
			"beta-proj":  map[string]any{"title": "Widget", "stack": "Go, Rust", "bullets": []string{"b3"}},
			"gamma-proj": map[string]any{"title": "Gadget", "stack": "Go", "bullets": []string{"b2"}},
			"delta-proj": map[string]any{"title": "Gizmo", "stack": "Go", "bullets": []string{"b4"}},
			"edu":        map[string]any{"title": "Master of Science in Things", "org": "Example University", "dates": "AUG 2018 – MAY 2020", "bullets": []string{}},
		},
		"bullets": map[string]any{
			"b1": map[string]any{"text": "Cut latency by {{clmLat}}.", "review": review("Cut latency by 42%.")},
			"b2": map[string]any{"text": "Shipped a gadget.", "review": review("Shipped a gadget.")},
			"b3": map[string]any{"text": "Reached {{clmRag}} recall.", "review": review("Reached 92% recall.")},
			"b4": map[string]any{"text": "Built a gizmo.", "review": review("Built a gizmo.")},
			"b5": map[string]any{"text": "Wrote a draft nobody read."},
		},
		"lines": map[string]any{},
	}
	raw, err := json.Marshal(bank)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

const kbPoolSelection = `{"sections":[
  {"title":"Experience","kind":"entries","entries":[{"entry":"alpha-job","bullets":["b1","b5"]}]},
  {"title":"Projects","kind":"entries","entries":[{"entry":"beta-proj","bullets":["b3"]},{"entry":"gamma-proj","bullets":["b2"]}]},
  {"title":"Education","kind":"entries","entries":[{"entry":"edu","bullets":[]}]}
]}`

// kbPoolClaims resolves clmLat and marks clmRag CAUTION.
const kbPoolClaims = `{"claims":{"clmLat":{"value":"42%"},"clmRag":{"value":"92%","caution":"re-measure"}}}`

// poolRKB plays `rkb claims export --out FILE` with a fixed claims document.
type poolRKB string

func (p poolRKB) Run(_ context.Context, c kbsync.Command) error {
	if len(c.Args) != 4 || c.Args[2] != "--out" {
		return fmt.Errorf("unexpected rkb args %v", c.Args)
	}
	return os.WriteFile(c.Args[3], []byte(p), 0o600)
}

// kbApproved is kbSetup plus the approved pool: the reviewed bank, an rkb that
// resolves its claims, and a selection in kb.selections. It returns the bank
// path and the selection path.
func kbApproved(t *testing.T, devprism string) (bank, rkb, selection string) {
	t.Helper()
	bank, rkb, selection, _ = kbApprove(t, devprism, kbPoolBank(), kbPoolClaims, kbPoolSelection)
	return bank, rkb, selection
}

// kbApprovedSimple approves kbSetup's own two-entry bank in full: alpha-job
// with b1 and beta-proj with b2, so the older sync tests keep their numbers.
func kbApprovedSimple(t *testing.T, devprism string) (bank, rkb string, fk *fakeRKB) {
	t.Helper()
	review := func(plain string) map[string]any {
		sum := sha1.Sum([]byte(plain))
		h := hex.EncodeToString(sum[:])[:12]
		pass := func(by string) []map[string]any {
			return []map[string]any{{"round": 1, "verdict": "pass", "text_hash": h, "by": by + "-r1"}, {"round": 2, "verdict": "pass", "text_hash": h, "by": by}}
		}
		return map[string]any{"hr": pass("hr"), "hm": pass("hm")}
	}
	raw, err := json.Marshal(map[string]any{
		"entries": map[string]any{
			"alpha-job": map[string]any{"title": "Engineer", "org": "Acme", "dates": "JAN 2020 – Present", "stack": "Go", "bullets": []string{"b1"}},
			"beta-proj": map[string]any{"title": "Widget", "stack": "Go, Rust", "bullets": []string{"b2"}},
		},
		"bullets": map[string]any{
			"b1": map[string]any{"text": "Cut latency by {{clmLat}}.", "review": review("Cut latency by 42%.")},
			"b2": map[string]any{"text": "Shipped a widget.", "review": review("Shipped a widget.")},
		},
		"lines": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	bank, rkb, _, fk = kbApprove(t, devprism, string(raw), "",
		`{"sections":[{"title":"All","kind":"entries","entries":[{"entry":"alpha-job","bullets":["b1"]},{"entry":"beta-proj","bullets":["b2"]}]}]}`)
	return bank, rkb, fk
}

// kbApprove is kbSetup plus an approved pool: the bank text, a selection in
// kb.selections, and (when claims is not empty) an rkb that answers with those
// claims instead of kbSetup's fake.
func kbApprove(t *testing.T, devprism, bankJSON, claims, selectionJSON string) (bank, rkb, selection string, fk *fakeRKB) {
	t.Helper()
	bank, rkb, fk = kbSetup(t, devprism)
	if err := os.WriteFile(bank, []byte(bankJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if claims != "" {
		old := rkbRunner
		rkbRunner = poolRKB(claims)
		t.Cleanup(func() { rkbRunner = old })
	}
	selection = filepath.Join(filepath.Dir(bank), "selection.json")
	if err := os.WriteFile(selection, []byte(selectionJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KB.Bank, cfg.KB.RKB, cfg.KB.Selections = bank, rkb, []string{selection}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return bank, rkb, selection, fk
}

// Before the approved pool, a sync sent every bank entry: an entry no selection
// lists, an entry whose only bullet cites a CAUTION claim, and the unreviewed
// bullet of an entry that was listed.
func TestKBSyncSendsOnlyTheApprovedPool(t *testing.T) {
	bank, rkb, _ := kbApproved(t, "/opt/devprism/devprism")
	f := &fakeDevPrism{}
	fakeStart(t, f)
	o, _ := mustCode(t, 0, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if got := strings.Join(f.upserts, ","); got != "alpha-job,edu,gamma-proj" {
		t.Fatalf("upserted %q, want alpha-job,edu,gamma-proj (delta-proj no selection lists, beta-proj holds only a CAUTION bullet); output %q", got, o)
	}
	var alpha struct {
		Bullets []struct {
			ID string `json:"id"`
		} `json:"bullets"`
	}
	if err := json.Unmarshal(f.payloads["alpha-job"], &alpha); err != nil {
		t.Fatal(err)
	}
	if len(alpha.Bullets) != 1 || alpha.Bullets[0].ID != "alpha-job/b1" {
		t.Fatalf("alpha-job carried %+v, want only alpha-job/b1 (b5 was never reviewed)", alpha.Bullets)
	}
	for id, raw := range f.payloads {
		if strings.Contains(string(raw), "92%") || strings.Contains(string(raw), "draft nobody read") || strings.Contains(string(raw), "gizmo") {
			t.Fatalf("%s payload carries withheld content: %s", id, raw)
		}
	}
	if strings.Contains(o, "CAUTION") {
		t.Fatalf("an eligible-only sync must not report CAUTION claims in use: %q", o)
	}
}

// With no selection the owner has said nothing is in use, so nothing is sent:
// never a fall back to the whole bank.
func TestKBSyncRefusesWithoutSelections(t *testing.T) {
	bank, rkb, fk := kbSetup(t, "/opt/devprism/devprism")
	starts := fakeStart(t, &fakeDevPrism{})
	for _, args := range [][]string{
		{"kb", "sync", "--bank", bank, "--rkb", rkb},
		{"kb", "sync", "--dry-run", "--bank", bank, "--rkb", rkb},
		{"kb", "sync", "--replace", "--bank", bank, "--rkb", rkb},
	} {
		_, e := mustCode(t, 2, args...)
		if !strings.Contains(e, "kb.selections") {
			t.Fatalf("%v: stderr %q must name kb.selections", args, e)
		}
	}
	if *starts != 0 || fk.calls != 0 {
		t.Fatalf("a refused sync started DevPrism %d times and ran rkb %d times", *starts, fk.calls)
	}
}
