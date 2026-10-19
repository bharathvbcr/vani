package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const reasonsTarget = "ML inference roles"

func reasonsReply(n int) string {
	cat := func(prefix string) string {
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf(`{"id":"%s_%d","label":"%s %d","description":"Applies when the posting shows %s %d."}`, prefix, i, prefix, i, prefix, i))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	return `{"fit":` + cat("fit") + `,"gap":` + cat("gap") + `,"check":` + cat("check") + `}`
}

func reasonsSetup(t *testing.T, p *draftLLM) (cfgPath, stateDir string) {
	t.Helper()
	cfgPath, stateDir = setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TargetSummary = reasonsTarget
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	old := reasonsProvider
	reasonsProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { reasonsProvider = old })
	return cfgPath, stateDir
}

func TestReasonsDraftWritesTheCatalogOnce(t *testing.T) {
	p := &draftLLM{text: reasonsReply(3)}
	_, stateDir := reasonsSetup(t, p)
	stdout, _ := mustCode(t, 0, "reasons", "draft")
	path := filepath.Join(stateDir, jdreasons.FileName)
	if !strings.Contains(stdout, path) || !strings.Contains(stdout, "3 fit, 3 gap, 3 check") {
		t.Fatalf("stdout %q", stdout)
	}
	c, err := jdreasons.Load(path, reasonsTarget)
	if err != nil || c == nil || len(c.Fit) != 3 {
		t.Fatalf("catalog %v err %v", c, err)
	}
	if len(p.reqs) != 1 || !strings.Contains(p.reqs[0].Messages[0].Text(), reasonsTarget) {
		t.Fatalf("requests %d", len(p.reqs))
	}

	// A second draft would replace a catalog the user may have edited.
	_, stderr := mustCode(t, 1, "reasons", "draft")
	if !strings.Contains(stderr, "--force") || len(p.reqs) != 1 {
		t.Fatalf("second draft: stderr %q, %d requests", stderr, len(p.reqs))
	}
	p.text = reasonsReply(5)
	mustCode(t, 0, "reasons", "draft", "--force")
	if c, _ := jdreasons.Load(path, reasonsTarget); c == nil || len(c.Fit) != 5 {
		t.Fatalf("--force did not replace: %v", c)
	}
	stdout, _ = mustCode(t, 0, "reasons", "check")
	if !strings.Contains(stdout, "ok") {
		t.Fatalf("check: %q", stdout)
	}
}

func TestReasonsDraftRefusesABadReplyAndWritesNothing(t *testing.T) {
	p := &draftLLM{text: "fit: serving; gap: CUDA"}
	_, stateDir := reasonsSetup(t, p)
	mustCode(t, 1, "reasons", "draft")
	if _, err := os.Stat(filepath.Join(stateDir, jdreasons.FileName)); !os.IsNotExist(err) {
		t.Fatalf("a refused draft left a file: %v", err)
	}
}

// A catalog drafted outside the API is imported: checked, keyed to the
// current target, provenance kept, and never over an existing one without
// --force. Nothing is spent.
func TestReasonsImport(t *testing.T) {
	p := &draftLLM{}
	_, stateDir := reasonsSetup(t, p)
	src := filepath.Join(t.TempDir(), "draft.json")
	body := strings.Replace(reasonsReply(2), `{"fit":`, `{"drafted_by":"a Claude Code session, 2026-10-18","fit":`, 1)
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _ := mustCode(t, 0, "reasons", "import", src)
	if !strings.Contains(stdout, "2 fit, 2 gap, 2 check") {
		t.Fatalf("stdout %q", stdout)
	}
	c, err := jdreasons.Load(filepath.Join(stateDir, jdreasons.FileName), reasonsTarget)
	if err != nil || c == nil || c.DraftedBy != "a Claude Code session, 2026-10-18" {
		t.Fatalf("catalog %+v err %v", c, err)
	}
	mustCode(t, 1, "reasons", "import", src)
	mustCode(t, 0, "reasons", "import", "--force", src)
	if stdout, _ := mustCode(t, 0, "reasons", "check"); !strings.Contains(stdout, "drafted by a Claude Code session") {
		t.Fatalf("check does not show provenance: %q", stdout)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"fit":[],"gap":[],"check":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 1, "reasons", "import", "--force", bad)
	if len(p.reqs) != 0 {
		t.Fatalf("import called the provider %d times", len(p.reqs))
	}
	mustCode(t, 2, "reasons", "import")
}

func TestReasonsCheckReportsAbsentAndStale(t *testing.T) {
	cfgPath, _ := reasonsSetup(t, &draftLLM{text: reasonsReply(2)})
	_, stderr := mustCode(t, 1, "reasons", "check")
	if !strings.Contains(stderr, "vani reasons draft") {
		t.Fatalf("absent: %q", stderr)
	}
	mustCode(t, 0, "reasons", "draft")
	cfg, _ := config.Load(cfgPath)
	cfg.TargetSummary = "frontend roles"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, stderr := mustCode(t, 1, "reasons", "check"); !strings.Contains(stderr, "different target summary") {
		t.Fatalf("stale: %q", stderr)
	}
}

// runnerFor builds the runner for cfg with a loopback doer so Jev builds
// without network; nothing is sent.
func runnerFor(t *testing.T, cfg config.Config, stateDir string) (*pipeline.Runner, string) {
	t.Helper()
	d, err := egress.NewLoopback(5*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	w := &wired{cfg: cfg, stateDir: stateDir, warn: &warn}
	r, err := w.runner(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return r, warn.String()
}

func reasonsConfig(t *testing.T) config.Config {
	t.Helper()
	clearKeys(t)
	t.Setenv("OPENROUTER_API_KEY", "test-not-a-real-key")
	cfg := config.Default()
	cfg.LLM.Provider = "" // no Claude: notes must not depend on it
	cfg.Decide.JevModel = "liquid/d1-ultra"
	cfg.TargetSummary = reasonsTarget
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeCatalog(t *testing.T, stateDir, target string) {
	t.Helper()
	c := jdreasons.Catalog{Version: jdreasons.Version, TargetSHA256: jdreasons.TargetHash(target),
		Fit:   []jdreasons.Item{{ID: "serving", Label: "Inference serving", Description: "The role builds model serving."}},
		Gap:   []jdreasons.Item{{ID: "cuda", Label: "CUDA kernels", Description: "The role requires CUDA kernel work."}},
		Check: []jdreasons.Item{{ID: "oncall", Label: "On-call load", Description: "The posting mentions on-call."}}}
	if err := jdreasons.Write(filepath.Join(stateDir, jdreasons.FileName), c, false); err != nil {
		t.Fatal(err)
	}
}

// With a catalog and Jev, notes are picked by Jev alone: no Claude, no
// shadow, one chain.
func TestRunnerPicksNotesWithJev(t *testing.T) {
	cfg := reasonsConfig(t)
	stateDir := t.TempDir()
	writeCatalog(t, stateDir, reasonsTarget)
	r, warn := runnerFor(t, cfg, stateDir)
	pk, ok := r.Explainer.(jdreasons.Picker)
	if !ok || r.SkipExplain {
		t.Fatalf("explainer %T skip %v\n%s", r.Explainer, r.SkipExplain, warn)
	}
	rt, ok := pk.D.(*decide.Router)
	if !ok || strings.Join(rt.Backends(), ",") != "jev" || rt.Shadow != nil {
		t.Fatalf("notes router %T %v", pk.D, rt)
	}
	if len(pk.Catalog.Fit) != 1 || pk.TargetSummary != reasonsTarget {
		t.Fatalf("picker %+v", pk)
	}
}

// End to end over the wire: one call, three questions, each judged by the
// catalog's own descriptions and its category's question, and the reply
// becomes the note.
func TestNotesAskJevWithTheCatalog(t *testing.T) {
	var body []byte
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"answers":{"fit":{"choice":"serving","confidence":0.88},"gap":{"choice":"none","confidence":0.7},"check":{"choice":"oncall","confidence":0.61}}}`)
	}))
	defer srv.Close()
	old := notesEndpoint
	notesEndpoint = srv.URL
	t.Cleanup(func() { notesEndpoint = old })

	cfg := reasonsConfig(t)
	stateDir := t.TempDir()
	writeCatalog(t, stateDir, reasonsTarget)
	r, _ := runnerFor(t, cfg, stateDir)
	rec := ledger.Record{Key: "gh:acme:1", Status: posting.Scored,
		Posting: posting.Posting{Key: "gh:acme:1", Title: "Inference Engineer", Description: "Build model serving. On-call rotation."}}
	note, err := r.Explainer.Explain(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	if note != "Fits: Inference serving (88%). Check: On-call load (61%)." || calls != 1 {
		t.Fatalf("note %q after %d calls", note, calls)
	}
	var sent struct {
		Model     string `json:"model"`
		Questions map[string]struct {
			Criteria     map[string]string `json:"criteria"`
			Instructions string            `json:"instructions"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	instr := jdreasons.Instructions()
	if sent.Model != "liquid/d1-ultra" || len(sent.Questions) != 3 {
		t.Fatalf("sent %s", body)
	}
	for slot, q := range sent.Questions {
		if q.Instructions != instr[slot] || q.Criteria[jdreasons.NoneID] == "" {
			t.Errorf("%s: %+v", slot, q)
		}
	}
	if sent.Questions["fit"].Criteria["serving"] != "The role builds model serving." {
		t.Errorf("fit criteria %v", sent.Questions["fit"].Criteria)
	}
}

// No catalog: notes are off, scored postings reach the digest without one,
// and the run says how to turn them on.
func TestRunnerSkipsNotesWithoutACatalog(t *testing.T) {
	r, warn := runnerFor(t, reasonsConfig(t), t.TempDir())
	if r.Explainer != nil || !r.SkipExplain {
		t.Fatalf("explainer %T skip %v", r.Explainer, r.SkipExplain)
	}
	if !strings.Contains(warn, "vani reasons draft") {
		t.Fatalf("warnings: %s", warn)
	}
}

// A catalog drafted for another target is not used.
func TestRunnerSkipsNotesWithAStaleCatalog(t *testing.T) {
	stateDir := t.TempDir()
	writeCatalog(t, stateDir, "frontend roles")
	r, warn := runnerFor(t, reasonsConfig(t), stateDir)
	if r.Explainer != nil || !r.SkipExplain {
		t.Fatalf("explainer %T skip %v", r.Explainer, r.SkipExplain)
	}
	if !strings.Contains(warn, "different target summary") {
		t.Fatalf("warnings: %s", warn)
	}
}

// A catalog but no Jev this run (no key in this shell): notes pause, the
// postings stay scored for a run that has Jev, rather than being marked
// explained with no note for good.
func TestRunnerPausesNotesWithoutJev(t *testing.T) {
	cfg := reasonsConfig(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg.LLM.Provider = "gemini" // keep a triage backend so the runner builds
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	stateDir := t.TempDir()
	writeCatalog(t, stateDir, reasonsTarget)
	r, warn := runnerFor(t, cfg, stateDir)
	if r.Explainer != nil || r.SkipExplain {
		t.Fatalf("explainer %T skip %v", r.Explainer, r.SkipExplain)
	}
	if !strings.Contains(warn, "notes paused") {
		t.Fatalf("warnings: %s", warn)
	}
}
