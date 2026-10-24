package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// eligibleTestClaims is the fake rkb's export for synthetic banks with no claim
// tokens (ParseClaims refuses an empty export).
const eligibleTestClaims = `{"claims":{"clmX":{"value":"1"}}}`

// wireEligibleKB makes a synthetic bank usable where the whole bank used to
// be read: every bullet gets a pass from both readers on its current wording,
// one selection lists every entry's bullets, and rkb is played by a fake. It
// rewrites the bank at bankPath and sets kb.rkb and kb.selections on cfg (the
// caller saves it).
func wireEligibleKB(t *testing.T, cfg *config.Config, bankPath, bankJSON string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(bankJSON), &doc); err != nil {
		t.Fatal(err)
	}
	bullets, _ := doc["bullets"].(map[string]any)
	for _, v := range bullets {
		b := v.(map[string]any)
		text, _ := b["text"].(string)
		b["review"] = reviewedBullet(text, text)["review"]
	}
	var listed []any
	entries, _ := doc["entries"].(map[string]any)
	for id, v := range entries {
		listed = append(listed, map[string]any{"entry": id, "bullets": v.(map[string]any)["bullets"]})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bankPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sel, err := json.Marshal(map[string]any{"sections": []any{map[string]any{"kind": "entries", "entries": listed}}})
	if err != nil {
		t.Fatal(err)
	}
	selPath := filepath.Join(t.TempDir(), "resume.json")
	if err := os.WriteFile(selPath, sel, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.KB.RKB = filepath.Join(t.TempDir(), "rkb")
	cfg.KB.Selections = []string{selPath}
	old := rkbRunner
	rkbRunner = claimsRKB{eligibleTestClaims}
	t.Cleanup(func() { rkbRunner = old })
}

func eligibleCfg(t *testing.T, kb *draftKB) config.Config {
	t.Helper()
	draftSetupKB(t, "", &draftLLM{}, kb)
	cfg, err := config.Load(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// chunkText is every chunk label and text of a profile, for "is this in it".
func chunkText(p semantic.Profile) string {
	var b strings.Builder
	for _, c := range p.Chunks {
		b.WriteString(c.Label + "\n" + c.Text + "\n")
	}
	return b.String()
}

// The profile that is embedded and digested for kbmatch's questions to a model
// holds only eligible bullets: not one the selections omit, one citing a CAUTION
// claim, or one no reader reviewed, and none of their entries' titles either.
func TestSemanticProfileHoldsOnlyEligibleBullets(t *testing.T) {
	cfg := eligibleCfg(t, eligibilityKB())
	p, err := semanticProfile(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := chunkText(p)
	if !strings.Contains(text, "serving path") || !strings.Contains(text, "Serving Engineer") {
		t.Errorf("the eligible bullet is missing from the profile: %q", text)
	}
	for _, banned := range []string{"zzunselected", "zzcaution", "zzunreviewed", "Scheduler Engineer", "Retrieval Engineer", "Compiler Engineer"} {
		if strings.Contains(text, banned) {
			t.Errorf("%q reached the profile although it is not eligible", banned)
		}
	}
	d, err := kbmatch.DigestOf(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"zzunselected", "zzcaution", "zzunreviewed", "Scheduler Engineer"} {
		if strings.Contains(d.Text, banned) || strings.Contains(d.Compact, banned) {
			t.Errorf("%q reached the kbmatch digest", banned)
		}
	}
	if !strings.Contains(d.Text, "serving path") {
		t.Errorf("digest = %q", d.Text)
	}
}

// Narrowing the bank changes the profile hash, which is what discards the score
// cache and the stored kbmatch answers made against the whole bank.
func TestEligibleProfileHashDiffersFromTheWholeBank(t *testing.T) {
	cfg := eligibleCfg(t, eligibilityKB())
	p, err := semanticProfile(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := kbsync.Load(cfg.KB.Bank)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := semantic.ProfileFromBank(b, cfg.TargetSummary)
	if err != nil {
		t.Fatal(err)
	}
	if p.Hash() == whole.Hash() || len(p.Chunks) >= len(whole.Chunks) {
		t.Errorf("eligible profile has %d chunks, whole bank %d; hashes equal: %v", len(p.Chunks), len(whole.Chunks), p.Hash() == whole.Hash())
	}
}

// With nothing to say what is current, or nothing eligible, there is no
// profile: matching reads "not run" rather than ranking by retired wording.
func TestSemanticProfileFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		kbEdit  func(*draftKB)
		cfgEdit func(*config.Config)
		want    string
	}{
		{"no selections", nil, func(c *config.Config) { c.KB.Selections = nil }, "kb.selections"},
		{"no rkb", nil, func(c *config.Config) { c.KB.RKB = "" }, "kb.rkb"},
		{"nothing eligible", func(kb *draftKB) { kb.selections = []string{selectionOf("e-unrev", "unrev")} }, nil, "0 eligible"},
		{"unreadable selection", func(kb *draftKB) { kb.selections = []string{"{"} }, nil, "selection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kb := eligibilityKB()
			if c.kbEdit != nil {
				c.kbEdit(kb)
			}
			cfg := eligibleCfg(t, kb)
			if c.cfgEdit != nil {
				c.cfgEdit(&cfg)
			}
			_, err := semanticProfile(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, c.want)
			}
			if c.name == "nothing eligible" && !errors.Is(err, semantic.ErrEmpty) {
				t.Errorf("err = %v, want ErrEmpty", err)
			}
		})
	}
}

// The ranking views read the profile to find their cache: with no selections
// they report matching off and name why, and the warm does not run.
func TestSemanticMatchingIsOffWithoutSelections(t *testing.T) {
	_, state := semanticSetup(t)
	cfg, err := config.Load(os.Getenv("VANI_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.KB.Selections = nil
	m, warning := semanticLookup(cfg, state)
	if m != nil || !strings.Contains(warning, "kb.selections") {
		t.Errorf("lookup = %v, warning %q", m, warning)
	}
	rep := warmSemanticDocs(context.Background(), cfg, state, []semantic.Doc{{Key: "k", Title: "t", Description: "d"}}, 5)
	if rep.Ran || !strings.Contains(rep.Note, "semantic matching not run") || !strings.Contains(rep.Note, "kb.selections") {
		t.Errorf("report = %+v", rep)
	}
}
