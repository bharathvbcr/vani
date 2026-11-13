package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// semanticAuditedState is auditedState with semantic matching on (a fake
// embedder) and the live scores warmed, as the owner's state has them.
func semanticAuditedState(t *testing.T) string {
	t.Helper()
	_, state := semanticSetup(t)
	seedAudit(t, state, 30, 6, 6)
	// seedAudit's one-line postings match thinly (unknown to the ranking);
	// these carry enough distinct lines for a known semantic part.
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range []string{
		"Run k8s clusters and golang services in production.\nBuild internal developer tooling and pipelines in Go.\nOperate Kubernetes for production traffic at scale.",
		"Own the data warehouse and SQL reporting.\nBuild Tableau dashboards for finance partners.\nAutomate Excel exports for the monthly close.",
		"Design kube operators in golang.\nOn-call for the platform that serves production traffic.\nImprove CI pipelines for internal developers.",
		"Plan marketing campaigns across channels.\nWrite copy for launches and the newsletter.\nTrack campaign spend against budget.",
	} {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("gh:r:%03d", i)), ATS: "gh", Company: "Acme", Title: fmt.Sprintf("Role %d", i), Description: d}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(strings.Repeat("1\n3\n2\ns\n", 4))
	mustCode(t, 0, "audit-sample", "--seed", "w1")
	mustCode(t, 0, "semantic", "warm")
	return state
}

// isolateTemp points the temporary directory at an empty one the test owns.
func isolateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func writeDraft(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "draft.txt")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func whatIfOf(t *testing.T, path string) summaryWhatIf {
	t.Helper()
	o, _ := mustCode(t, 0, "relevance", "--json", "--what-if-summary", path)
	var r relevanceReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.SummaryWhatIf == nil {
		t.Fatal("no what-if in the reply")
	}
	return *r.SummaryWhatIf
}

// treeDigest is the sha256 of every file under dir, by relative path.
func treeDigest(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The live summary given as the draft is the null test: the rescored live arm
// matches the scores the ranking reads, and no variant moves anything.
func TestSummaryWhatIfOfTheLiveSummaryMovesNothing(t *testing.T) {
	state := semanticAuditedState(t)
	tmp := isolateTemp(t)
	cfg, err := config.Load(filepath.Join(filepath.Dir(state), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	w := whatIfOf(t, writeDraft(t, "  "+cfg.TargetSummary+"\n"))
	if w.Error != "" {
		t.Fatal(w.Error)
	}
	s := w.Semantic
	if !s.Computed || s.Rescored == 0 || s.KnownBoth == 0 {
		t.Fatalf("semantic arm: %+v", s)
	}
	if s.NullCompared == 0 || s.NullDiffer != 0 {
		t.Errorf("null test: %+v", s)
	}
	if len(w.Added) != 0 || len(w.Removed) != 0 || w.LiveTerms != w.DraftTerms {
		t.Errorf("terms: +%v -%v (%d/%d)", w.Added, w.Removed, w.LiveTerms, w.DraftTerms)
	}
	if len(w.Variants) != 2 {
		t.Fatalf("%d variants", len(w.Variants))
	}
	for _, v := range w.Variants {
		if v.Changed != 0 || v.PoolChanged != 0 {
			t.Errorf("%s moved postings under the live summary: %+v", v.Name, v)
		}
		for _, a := range v.AUC {
			if a.OK && a.Base != a.Spare {
				t.Errorf("%s %s: AUC %v -> %v", v.Name, a.Mode, a.Base, a.Spare)
			}
		}
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// A different draft swaps the terms, rescores both arms, and leaves the state
// dir byte for byte as it was.
func TestSummaryWhatIfOfADraftWritesNothing(t *testing.T) {
	state := semanticAuditedState(t)
	tmp := isolateTemp(t)
	before := treeDigest(t, state)
	w := whatIfOf(t, writeDraft(t, "Data analyst roles in SQL, Tableau and Excel reporting"))
	if w.Error != "" || !w.Semantic.Computed {
		t.Fatalf("what-if: %+v", w)
	}
	if len(w.Added) == 0 || len(w.Removed) == 0 {
		t.Errorf("terms: +%v -%v", w.Added, w.Removed)
	}
	if w.Semantic.WantedTotal == 0 {
		t.Error("no posting answered yes was counted")
	}
	// The analyst draft reorders the rich postings through the semantic part.
	if len(w.Variants) != 2 || w.Variants[1].PoolChanged == 0 {
		t.Fatalf("the semantic variant moved nothing: %+v", w.Variants)
	}
	// The semantic variant is the keyword variant plus the draft's semantic
	// part: were the draft's scores not swapped in, the two would be equal.
	kwOnly, both := w.Variants[0], w.Variants[1]
	kwOnly.Name, both.Name = "", ""
	if reflect.DeepEqual(kwOnly, both) {
		t.Error("the semantic variant is the keyword variant: the draft's semantic scores were not used")
	}
	for _, v := range w.Variants {
		if len(v.Wanted) != w.Semantic.WantedTotal {
			t.Errorf("%s lists %d yes postings of %d", v.Name, len(v.Wanted), w.Semantic.WantedTotal)
		}
	}
	after := treeDigest(t, state)
	if len(after) != len(before) {
		t.Errorf("state files: %d before, %d after", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Errorf("%s changed", p)
		}
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	text, _ := mustCode(t, 0, "relevance", "--what-if-summary", writeDraft(t, "Data analyst roles in SQL"))
	for _, want := range []string{"## Summary what-if", "null test", "every posting answered yes", "cannot measure"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q", want)
		}
	}
}

// Without semantic matching, or with a model that cannot load, the semantic
// arm says why and the keyword part is still compared; it is never "no change".
func TestSummaryWhatIfSemanticArmNotRunSaysWhy(t *testing.T) {
	auditedState(t)
	w := whatIfOf(t, writeDraft(t, "Data analyst roles"))
	if w.Semantic.Computed || !strings.Contains(w.Semantic.Reason, "not configured") || len(w.Variants) != 1 {
		t.Errorf("unconfigured: %+v, %d variants", w.Semantic, len(w.Variants))
	}

	semanticAuditedState(t)
	tmp := isolateTemp(t)
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return nil, nil, errors.New("no model")
	}
	w = whatIfOf(t, writeDraft(t, "Data analyst roles"))
	if w.Semantic.Computed || !strings.Contains(w.Semantic.Reason, "could not be loaded") || len(w.Variants) != 1 {
		t.Errorf("model down: %+v", w.Semantic)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	text, _ := mustCode(t, 0, "relevance", "--what-if-summary", writeDraft(t, "Data analyst roles"))
	if !strings.Contains(text, "semantic part not computed") {
		t.Error("text report does not say the semantic part was not computed")
	}
}

func TestSummaryWhatIfRefusesABadDraft(t *testing.T) {
	auditedState(t)
	for name, path := range map[string]string{
		"missing":   filepath.Join(t.TempDir(), "nope.txt"),
		"empty":     writeDraft(t, " \n\t "),
		"oversized": writeDraft(t, strings.Repeat("platform ", 200)),
		"too big":   writeDraft(t, strings.Repeat("x", maxWhatIfSummaryFile+1)),
	} {
		if w := whatIfOf(t, path); w.Error == "" || len(w.Variants) != 0 {
			t.Errorf("%s: %+v", name, w)
		}
	}
	// With nothing judged there is no order to compare: an error, not a section.
	_, state := setup(t)
	mustCode(t, 0, "init")
	_ = state
	if _, e := mustCode(t, 1, "relevance", "--what-if-summary", writeDraft(t, "x")); !strings.Contains(e, "no judged") {
		t.Errorf("no labels: %s", e)
	}
}
