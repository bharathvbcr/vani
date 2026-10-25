package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/rkbexec"
)

type draftStream struct {
	text string
	done bool
}

func (s *draftStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *draftStream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}}, nil
}
func (s *draftStream) Close() error { return nil }

type draftLLM struct {
	text string
	err  error
	reqs []llm.Request
}

func (p *draftLLM) Name() string { return "fake" }
func (p *draftLLM) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *draftLLM) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return nil, p.err
	}
	return &draftStream{text: p.text}, nil
}

// draftKB is a synthetic rkb bank, its claims export and the selection files
// the config lists. A nil *draftKB leaves the knowledge base unconfigured.
type draftKB struct {
	entries    map[string]any
	bullets    map[string]any
	claims     string
	selections []string // JSON documents, one file each
}

// claimsRKB plays `rkb claims export --out FILE` with the claims it was given.
type claimsRKB struct{ claims string }

func (c claimsRKB) Run(_ context.Context, cmd rkbexec.Command, _ time.Duration) (rkbexec.Output, error) {
	if len(cmd.Args) != 4 || cmd.Args[2] != "--out" {
		return rkbexec.Output{}, fmt.Errorf("unexpected rkb args %v", cmd.Args)
	}
	return rkbexec.Output{}, os.WriteFile(cmd.Args[3], []byte(c.claims), 0o600)
}

// reviewedBullet is a bullet both readers passed on exactly this wording; plain
// is the wording after claim tokens resolve (rkb hashes that).
func reviewedBullet(text, plain string) map[string]any {
	sum := sha1.Sum([]byte(plain))
	h := hex.EncodeToString(sum[:])[:12]
	pass := func(by string) []map[string]any {
		return []map[string]any{{"round": 1, "verdict": "pass", "text_hash": h, "by": by + "-r1"}, {"round": 2, "verdict": "pass", "text_hash": h, "by": by + "-r2"}}
	}
	return map[string]any{"text": text, "review": map[string]any{"hr": pass("hr"), "hm": pass("hm")}}
}

func draftEntry(title, org string, bullets ...string) map[string]any {
	return map[string]any{"title": title, "org": org, "dates": "JAN 2020 – Present", "stack": "Go", "bullets": bullets}
}

func selectionOf(entry string, bullets ...string) string {
	raw, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"kind": "entries", "entries": []any{map[string]any{"entry": entry, "bullets": bullets}}}}})
	return string(raw)
}

// defaultDraftKB is the one-entry bank most draft tests use: one reviewed
// bullet the owner's selection lists.
func defaultDraftKB() *draftKB {
	return &draftKB{
		entries:    map[string]any{"alpha-job": draftEntry("Engineer", "Acme", "b1")},
		bullets:    map[string]any{"b1": reviewedBullet("Cut latency by {{clmLat}}.", "Cut latency by 42%.")},
		claims:     `{"claims":{"clmLat":{"value":"42%"}}}`,
		selections: []string{selectionOf("alpha-job", "b1")},
	}
}

// draftSetup writes a config (with the default synthetic knowledge base when
// withKB) and replaces the provider and stdin seams.
func draftSetup(t *testing.T, input string, p *draftLLM, withKB bool) {
	t.Helper()
	var kb *draftKB
	if withKB {
		kb = defaultDraftKB()
	}
	draftSetupKB(t, input, p, kb)
}

func draftSetupKB(t *testing.T, input string, p *draftLLM, kb *draftKB) {
	t.Helper()
	bank, rkb, _ := kbSetup(t, "")
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if kb != nil {
		raw, err := json.Marshal(map[string]any{"entries": kb.entries, "bullets": kb.bullets})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bank, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.KB.Bank, cfg.KB.RKB = bank, rkb
		old := rkbRunner
		rkbRunner = claimsRKB{kb.claims}
		t.Cleanup(func() { rkbRunner = old })
		dir := t.TempDir()
		for i, doc := range kb.selections {
			path := filepath.Join(dir, fmt.Sprintf("resume%d.json", i))
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.KB.Selections = append(cfg.KB.Selections, path)
		}
	}
	cfg.TargetSummary = "ML inference roles"
	cfg.LLM = testLLM
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	oldIn, oldP := draftInput, draftProvider
	draftInput = strings.NewReader(input)
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftInput, draftProvider = oldIn, oldP })
}

func draftOut(t *testing.T, stdout string) draftReply {
	t.Helper()
	var r draftReply
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not one JSON reply: %q (%v)", stdout, err)
	}
	return r
}

const goodDraftReq = `{"question":"Why do you want to cut latency here?","job":{"company":"Modal","title":"Engineer","description":"Latency matters."},"max_words":120}`

func TestDraftUsesKnowledgeBaseWithoutPersonalData(t *testing.T) {
	p := &draftLLM{text: "I cut latency by 42% at Acme."}
	draftSetup(t, goodDraftReq, p, true)
	stdout, _ := mustCode(t, 0, "draft")
	r := draftOut(t, stdout)
	if r.Draft != "I cut latency by 42% at Acme." || r.Error != "" || r.EvidenceItems != 1 {
		t.Fatalf("reply = %+v", r)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("%d provider calls", len(p.reqs))
	}
	msg := p.reqs[0].Messages[0].Text()
	if !strings.Contains(msg, "Cut latency by 42%.") {
		t.Errorf("resolved evidence missing from the prompt: %q", msg)
	}
	for _, personal := range []string{"Synthetic Person", "person@example.invalid"} {
		if strings.Contains(msg, personal) || strings.Contains(p.reqs[0].System, personal) {
			t.Errorf("%q reached the prompt", personal)
		}
	}
}

func TestDraftWithoutKnowledgeBaseStillDrafts(t *testing.T) {
	p := &draftLLM{text: "A draft."}
	draftSetup(t, goodDraftReq, p, false)
	stdout, stderr := mustCode(t, 0, "draft")
	if r := draftOut(t, stdout); r.Draft != "A draft." || r.EvidenceItems != 0 {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(stderr, "without knowledge-base evidence") {
		t.Errorf("stderr = %q", stderr)
	}
}

// The stdin cap fits the longest posting the draft package reads, in the
// worst UTF-8 case, so a long posting is sectioned rather than refused.
func TestDraftAcceptsTheLongestPostingDraftReads(t *testing.T) {
	desc := "About the role\n" + strings.Repeat("é", draft.MaxPostingRunes-20)
	raw, err := json.Marshal(draft.Request{Question: "Why Acme?", Job: draft.Job{Description: desc}})
	if err != nil {
		t.Fatal(err)
	}
	if draft.MaxPostingRunes*4+4096 > maxDraftInput {
		t.Errorf("maxDraftInput %d cannot carry %d runes of posting", maxDraftInput, draft.MaxPostingRunes)
	}
	p := &draftLLM{text: "A draft."}
	draftSetup(t, string(raw), p, false)
	stdout, _ := mustCode(t, 0, "draft")
	if r := draftOut(t, stdout); r.Draft != "A draft." || len(p.reqs) != 1 {
		t.Errorf("reply = %+v, provider calls %d", r, len(p.reqs))
	}
}

func TestDraftRefusalsAreJSONWithCodes(t *testing.T) {
	cases := []struct {
		name, input, code string
		p                 *draftLLM
	}{
		{"not json", "{", "bad_request", &draftLLM{}},
		{"unknown field", `{"question":"Why?","ssn":"1"}`, "bad_request", &draftLLM{}},
		{"empty question", `{"question":" "}`, "bad_request", &draftLLM{}},
		{"oversized", `{"question":"` + strings.Repeat("q", maxDraftInput) + `"}`, "bad_request", &draftLLM{}},
		{"budget", goodDraftReq, "budget_exhausted", &draftLLM{err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}},
		{"provider", goodDraftReq, "provider", &draftLLM{err: fmt.Errorf("503")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			draftSetup(t, c.input, c.p, true)
			stdout, stderr := mustCode(t, 1, "draft")
			r := draftOut(t, stdout)
			if r.Code != c.code || r.Error == "" || r.Draft != "" {
				t.Errorf("reply = %+v", r)
			}
			if strings.Contains(stderr, "usage") {
				t.Errorf("a refusal printed usage: %q", stderr)
			}
			if c.code == "bad_request" && len(c.p.reqs) != 0 {
				t.Error("a bad request reached the provider")
			}
		})
	}
}

func TestDraftNeedsAProvider(t *testing.T) {
	p := &draftLLM{text: "x"}
	draftSetup(t, goodDraftReq, p, true)
	cfgPath := os.Getenv("VANI_CONFIG")
	cfg, _ := config.Load(cfgPath)
	cfg.LLM.Provider, cfg.LLM.Model = "", ""
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustCode(t, 1, "draft")
	if r := draftOut(t, stdout); r.Code != "config" {
		t.Errorf("reply = %+v", r)
	}
	if len(p.reqs) != 0 {
		t.Error("provider called without a configured llm")
	}
}

func TestDraftBadBankIsAKBError(t *testing.T) {
	p := &draftLLM{text: "x"}
	draftSetup(t, goodDraftReq, p, true)
	cfg, _ := config.Load(os.Getenv("VANI_CONFIG"))
	if err := os.WriteFile(cfg.KB.Bank, []byte(`{"entries":{"x":null}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustCode(t, 1, "draft")
	if r := draftOut(t, stdout); r.Code != "kb" {
		t.Errorf("reply = %+v", r)
	}
}

// latencyReq is not a motivation question, so every bullet that shares a word
// with it competes for the evidence slots.
const latencyReq = `{"question":"Describe a project where you reduced latency.","job":{"company":"Modal","title":"Engineer","description":"Latency matters."},"max_words":120}`

// eligibilityKB has one bullet of each kind the owner's rules separate, all
// matching the request's words so word matching alone would pick them.
func eligibilityKB() *draftKB {
	return &draftKB{
		entries: map[string]any{
			"e-ok":      draftEntry("Serving Engineer", "Acme", "ok"),
			"e-unsel":   draftEntry("Scheduler Engineer", "Beta", "unsel"),
			"e-caution": draftEntry("Retrieval Engineer", "Gamma", "caution"),
			"e-unrev":   draftEntry("Compiler Engineer", "Delta", "unrev"),
		},
		bullets: map[string]any{
			"ok":      reviewedBullet("Cut p50 latency by {{clmLat}} on the serving path.", "Cut p50 latency by 42% on the serving path."),
			"unsel":   reviewedBullet("Reduced tail latency with a retired scheduler zzunselected.", "Reduced tail latency with a retired scheduler zzunselected."),
			"caution": reviewedBullet("Raised retrieval latency recall to {{clmRag}} zzcaution.", "Raised retrieval latency recall to 92% zzcaution."),
			"unrev":   map[string]any{"text": "Profiled latency hotspots zzunreviewed in compilers."},
		},
		claims: `{"claims":{"clmLat":{"value":"42%"},"clmRag":{"value":"92%","caution":"DO NOT USE"}}}`,
		selections: []string{
			selectionOf("e-ok", "ok"),
			selectionOf("e-caution", "caution"),
			selectionOf("e-unrev", "unrev"),
		},
	}
}

// A bullet reaches the model only if a current selection lists it and it passes
// the owner's rules: not listed, citing a CAUTION claim, or never reviewed are
// all kept out.
func TestDraftSendsOnlyEligibleBullets(t *testing.T) {
	p := &draftLLM{text: "I cut latency by 42%."}
	draftSetupKB(t, latencyReq, p, eligibilityKB())
	stdout, _ := mustCode(t, 0, "draft")
	r := draftOut(t, stdout)
	if r.Error != "" || r.EvidenceItems != 1 {
		t.Fatalf("reply = %+v", r)
	}
	msg := p.reqs[0].Messages[0].Text()
	if !strings.Contains(msg, "Cut p50 latency by 42% on the serving path.") {
		t.Errorf("the eligible bullet is missing from the prompt: %q", msg)
	}
	for _, banned := range []string{"zzunselected", "zzcaution", "92%", "zzunreviewed", "Scheduler Engineer"} {
		if strings.Contains(msg, banned) {
			t.Errorf("%q reached the prompt although its bullet is not eligible", banned)
		}
	}
}

// Naming a CAUTION claim in caution_ok clears it, as in rkb's final gate.
func TestDraftSendsACautionBulletTheOwnerCleared(t *testing.T) {
	kb := eligibilityKB()
	kb.bullets["caution"].(map[string]any)["caution_ok"] = map[string]string{"clmRag": "re-measured 2026-10-22"}
	p := &draftLLM{text: "I raised recall to 92%."}
	draftSetupKB(t, latencyReq, p, kb)
	mustCode(t, 0, "draft")
	if msg := p.reqs[0].Messages[0].Text(); !strings.Contains(msg, "zzcaution") {
		t.Errorf("a cleared CAUTION bullet was withheld: %q", msg)
	}
}

// A knowledge base without the owner's current resumes is refused, not read
// as "the whole bank".
func TestDraftRefusesAKnowledgeBaseWithoutSelections(t *testing.T) {
	p := &draftLLM{text: "x"}
	kb := defaultDraftKB()
	kb.selections = nil
	draftSetupKB(t, latencyReq, p, kb)
	stdout, _ := mustCode(t, 1, "draft")
	r := draftOut(t, stdout)
	if r.Code != "kb" || !strings.Contains(r.Error, "kb.selections") || r.Draft != "" {
		t.Errorf("reply = %+v", r)
	}
	if len(p.reqs) != 0 {
		t.Error("the provider was called with no selections")
	}
}

func TestDraftRefusesABadSelectionFile(t *testing.T) {
	for name, doc := range map[string]string{"not json": "{", "no sections": `{"sections":[]}`, "bad id": selectionOf("a b", "b1")} {
		t.Run(name, func(t *testing.T) {
			p := &draftLLM{text: "x"}
			kb := defaultDraftKB()
			kb.selections = []string{doc}
			draftSetupKB(t, latencyReq, p, kb)
			stdout, _ := mustCode(t, 1, "draft")
			if r := draftOut(t, stdout); r.Code != "kb" {
				t.Errorf("reply = %+v", r)
			}
			if len(p.reqs) != 0 {
				t.Error("the provider was called with an unreadable selection")
			}
		})
	}
}

// Selections that list nothing eligible still draft (from the posting alone),
// and say so on stderr rather than quietly sending no evidence.
func TestDraftWithNoEligibleBulletsSaysSo(t *testing.T) {
	p := &draftLLM{text: "A draft."}
	kb := eligibilityKB()
	kb.selections = []string{selectionOf("e-unrev", "unrev")}
	draftSetupKB(t, latencyReq, p, kb)
	stdout, stderr := mustCode(t, 0, "draft")
	if r := draftOut(t, stdout); r.EvidenceItems != 0 {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(stderr, "0 eligible") {
		t.Errorf("stderr = %q", stderr)
	}
}
