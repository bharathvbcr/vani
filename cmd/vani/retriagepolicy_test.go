package main

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// seedDrops writes one posting dropped by each route the policy reopen tells
// apart: the configured decision model's calibrated staff_plus reading, the
// same reading under the model's old backend name, a title drop and an
// exclusion. The default config has no drop rules and names d1-ultra, so the
// first and third no longer drop, the second cannot be judged, and the
// exclusion is ApplyExclusions' business. The readings carry the model id a
// run under cfg records, and the board (gh:d) is put on cfg's watchlist, as a
// real drop's is.
func seedDrops(t *testing.T, cfgPath, stateDir string) (model, oldName, title, excluded posting.Key) {
	t.Helper()
	watchGreenhouse(t, cfgPath, "d")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	reading := func(backend string) map[string]string {
		return map[string]string{
			"triage.backend": backend, "triage.seniority": "staff_plus", "triage.seniority.reading": "answered",
			"triage.seniority.backend": backend, "triage.seniority.p": "0.9500",
			"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered", "triage.role_fit.backend": backend,
			"triage.role_fit.p": "0.9100", pipeline.AnnTriageModel: triageModelID(cfg),
		}
	}
	add := func(id string, ann map[string]string) posting.Key {
		p := posting.Posting{Key: posting.Key("gh:d:" + id), ATS: "gh", Company: "Acme", Title: "Staff Engineer " + id,
			Location: "Austin, TX", Description: "Build distributed systems."}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Dropped, Posting: &p,
			Annotations: ann, Reason: "dropped"}); err != nil {
			t.Fatal(err)
		}
		return p.Key
	}
	model = add("model", reading(cfg.Decide.DecisionName()))
	oldName = add("jev", reading("jev"))
	title = add("title", map[string]string{"triage.backend": "title", "triage.seniority": "staff_plus", "triage.seniority.reading": "answered"})
	excluded = add("exclude", map[string]string{"triage.backend": "exclude", "exclude.matched": "excluded: company is \"Acme\""})
	return
}

func TestRetriagePolicyDropsCommand(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	model, oldName, title, excluded := seedDrops(t, cfgPath, stateDir)

	// The owner hid the model-dropped role: a mark is the owner's, kept in
	// state marks/, and the reopen neither reads nor changes it.
	mustCode(t, 0, "jobs", "mark", string(model), "hidden")
	marksBefore := readTree(t, filepath.Join(stateDir, "marks"))
	if len(marksBefore) == 0 {
		t.Fatal("setup: the mark wrote nothing under marks/")
	}

	// Dry run, while the daemon holds the ledger: reported, nothing changed.
	held, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	o, e := mustCode(t, 0, "retriage", "--dry-run", "--policy-drops")
	for _, want := range []string{
		"2 posting(s) would be let back (dropped on a rule the current drop policy no longer drops)",
		"waiting for their board to list them again: 2",
		"title drops, back to live for the backends to read: 1",
		"model drops, back on their stored reading (no model asked): 1",
		"model drops not judged, left dropped: 1",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("dry run lacks %q:\n%s\n%s", want, o, e)
		}
	}
	o, _ = mustCode(t, 0, "--json", "retriage", "--dry-run", "--policy-drops")
	var rt retriageRunReply
	decodeOne(t, o, &rt)
	if !rt.DryRun || !rt.PolicyDrops || rt.TitleDrops || rt.Returned != 2 || rt.TitleReturned != 1 || rt.ModelReturned != 1 ||
		rt.Unjudged == nil || *rt.Unjudged != 1 || rt.Stale == nil || *rt.Stale != 0 || rt.Unreadable == nil || *rt.Unreadable != 0 ||
		!slices.Contains(rt.Keys, model) || !slices.Contains(rt.Keys, title) {
		t.Fatalf("json dry run %+v", rt)
	}
	for _, r := range held.All() {
		if r.Status != posting.Dropped {
			t.Fatalf("%s is %s after a dry run", r.Key, r.Status)
		}
	}
	held.Close()

	mustCode(t, 2, "retriage", "--title-drops", "--policy-drops")

	// Their board (gh:d) is watched: both wait, still dropped, for its next
	// listing, which lets them back.
	mustCode(t, 0, "retriage", "--policy-drops")
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, r := range l.All() {
		if r.Status != posting.Dropped {
			t.Fatalf("%s is %s before its board listed it", r.Key, r.Status)
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	w := &wired{cfg: cfg, stateDir: stateDir, led: l, warn: io.Discard}
	rec := pipeline.NewReconciler(l)
	finish := w.reopenOnSighting(rec, jobsT0)
	listed := []posting.Posting{ledger.Full(l, mustGet(t, l, model)).Posting, ledger.Full(l, mustGet(t, l, title)).Posting}
	if _, err := rec.Reconcile(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "d", Company: "Acme"}, listed, jobsT0); err != nil {
		t.Fatal(err)
	}
	finish()
	want := map[posting.Key]posting.Status{model: posting.Triaged, oldName: posting.Dropped, title: posting.Live, excluded: posting.Dropped}
	for k, st := range want {
		r, ok := l.Get(k)
		if !ok || r.Status != st {
			t.Errorf("%s is %s, want %s", k, r.Status, st)
		}
	}
	if r, _ := l.Get(model); r.Annotations["triage.seniority.p"] != "0.9500" || r.Posting.Description == "" {
		t.Errorf("the model drop lost its reading or body: %v", r.Annotations)
	}
	if after := readTree(t, filepath.Join(stateDir, "marks")); !maps.Equal(marksBefore, after) {
		t.Errorf("marks changed:\nbefore %v\nafter  %v", marksBefore, after)
	}
}

// readTree returns every regular file under dir by relative path.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
