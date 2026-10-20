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
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// bankMarker is in the synthetic bank's org and in a bullet, and postMarker is
// in a posting: neither may reach a log line, the run record or an error.
const (
	bankMarker = "ZORBLAXMARKER"
	postMarker = "QUUXPOSTMARKER"
)

// The synthetic bank: org and bullets carry bankMarker.
const semBank = `{
  "entries": {
    "e1": {"title": "Platform Engineer", "org": "` + bankMarker + ` Industries", "dates": "2019 - 2022", "location": "Quuxville", "stack": "Go, Kubernetes",
           "bullets": ["b1", "b2"]}
  },
  "bullets": {
    "b1": {"text": "Operated Kubernetes clusters serving production traffic for ` + bankMarker + `"},
    "b2": {"text": "Built Go services for internal developer tooling and pipelines"}
  }
}`

var semSynonyms = [][]string{{"kubernetes", "k8s", "kube"}, {"golang", "go"}}

// semanticSetup writes a config with semantic matching on, a synthetic bank,
// and a fake embedder in place of Tessl. It returns the state dir.
func semanticSetup(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	cfgPath, stateDir = setup(t)
	fixedNow(t)
	bank := filepath.Join(t.TempDir(), "bank.json")
	if err := os.WriteFile(bank, []byte(semBank), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.TargetSummary = "Senior platform engineer building Kubernetes and Go infrastructure services"
	cfg.KB.Bank = bank
	cfg.Semantic.Snapshot = filepath.Join(t.TempDir(), "snapshot")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	old, oldTimeout := openEmbedder, semanticWarmTimeout
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return semantic.NewFakeEmbedder(256, semSynonyms), func() error { return nil }, nil
	}
	t.Cleanup(func() { openEmbedder, semanticWarmTimeout = old, oldTimeout })
	return cfgPath, stateDir
}

func daysAgo(n int) time.Time { return jobsT0.Add(-time.Duration(n) * 24 * time.Hour) }

// A posting that says k8s and golang matches the bank's Kubernetes and Go
// work by meaning; a keyword-free one does not. After a warm, `vani jobs`
// carries the scores and the ranking uses them.
func TestSemanticWarmThenJobsRankBySynonymMatch(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state,
		// Sorts first by key, so a tie would put it first.
		insightSeed{key: "gh:a:1-sales", company: "Acme", title: "Account Executive", status: posting.Explained,
			desc: "Sell enterprise subscriptions, manage quota and territory accounts", posted: daysAgo(3), seenAgo: time.Hour},
		insightSeed{key: "gh:a:2-infra", company: "Acme", title: "Infrastructure Engineer", status: posting.Explained,
			desc: "Run k8s clusters and golang services in production", posted: daysAgo(3), seenAgo: time.Hour},
	)

	before := listJSON(t, "--sort", "priority")
	for _, j := range before.Jobs {
		if j.Semantic != nil {
			t.Fatalf("%s has a semantic match before any warm", j.Key)
		}
	}
	if !strings.Contains(before.Insight.Warning, "vani semantic warm") {
		t.Errorf("unwarmed listing should say so, warning %q", before.Insight.Warning)
	}

	out, _ := mustCode(t, 0, "semantic", "warm")
	if !strings.Contains(out, "scored 2") {
		t.Fatalf("warm output %q", out)
	}
	// A second warm finds both cached and embeds nothing.
	if out, _ = mustCode(t, 0, "semantic", "warm"); !strings.Contains(out, "cached 2") || !strings.Contains(out, "scored 0") {
		t.Fatalf("second warm output %q", out)
	}

	after := listJSON(t, "--sort", "priority")
	infra, sales := findJob(t, after, "gh:a:2-infra"), findJob(t, after, "gh:a:1-sales")
	if infra.Semantic == nil || sales.Semantic == nil {
		t.Fatalf("scores missing: infra %+v sales %+v", infra.Semantic, sales.Semantic)
	}
	if !(infra.Semantic.Score > sales.Semantic.Score) {
		t.Errorf("synonym match %.3f should beat unrelated %.3f", infra.Semantic.Score, sales.Semantic.Score)
	}
	if len(infra.Semantic.Evidence) == 0 {
		t.Error("no evidence on the matching posting")
	}
	// The score itself moved with the match: the synonym posting gained more
	// priority from being warmed than the unrelated one.
	b1, b2 := findJob(t, before, "gh:a:2-infra"), findJob(t, before, "gh:a:1-sales")
	if gi, gs := infra.Priority-b1.Priority, sales.Priority-b2.Priority; !(gi > gs) {
		t.Errorf("priority gain from the match: infra %+.2f, unrelated %+.2f", gi, gs)
	}
	if after.Jobs[0].Key != "gh:a:2-infra" {
		t.Errorf("order %s: the semantic match should lead", keys(after))
	}
	if after.Insight.Warning != "" {
		t.Errorf("warning %q on a warmed listing", after.Insight.Warning)
	}
}

// A model that cannot load is reported as "not run", the exit is not a
// success, and the jobs list carries no semantic field and says why.
func TestSemanticUnavailableIsNotRunNeverSuccess(t *testing.T) {
	_, state := semanticSetup(t)
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return nil, nil, fmt.Errorf("%w: this vani was built without -tags tessl", semantic.ErrUnavailable)
	}
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s", posted: daysAgo(2), seenAgo: time.Hour})

	_, stderr := mustCode(t, 1, "semantic", "warm")
	if !strings.Contains(stderr, "semantic matching not run") {
		t.Errorf("stderr %q", stderr)
	}
	l := listJSON(t)
	if l.Jobs[0].Semantic != nil {
		t.Error("a semantic field without a score")
	}
	if l.Insight.Warning == "" {
		t.Error("no warning on the listing")
	}
}

// The daily run still succeeds when the model is unavailable, and records that
// semantic matching was not run; with a model it records the counts.
func TestDailyRunWarmsAndNeverFailsOnSemantic(t *testing.T) {
	for _, tc := range []struct {
		name      string
		embedder  func(context.Context, config.Config) (semantic.Embedder, func() error, error)
		wantRan   bool
		wantScore int
		wantNote  string
	}{
		{name: "unavailable", wantRan: false, wantNote: "semantic matching not run",
			embedder: func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
				return nil, nil, fmt.Errorf("%w: no snapshot", semantic.ErrUnavailable)
			}},
		{name: "available", wantRan: true, wantScore: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, state := semanticSetup(t)
			if tc.embedder != nil {
				openEmbedder = tc.embedder
			}
			l := fixture() // two kept postings: gh:a:1 explained, gh:a:2 unsure
			wire(t, l, &fakeNotifier{}, nil)
			mustCode(t, 0, "run-once")
			r, err := readRunRecord(state)
			if err != nil || r == nil || r.Outcome != outcomeOK {
				t.Fatalf("record %+v %v", r, err)
			}
			if r.Semantic == nil {
				t.Fatal("no semantic report on the run record")
			}
			s := *r.Semantic
			if s.Ran != tc.wantRan || s.Scored != tc.wantScore || !strings.Contains(s.Note, tc.wantNote) || (tc.wantNote == "" && s.Note != "") {
				t.Errorf("report %+v", s)
			}
			if tc.wantNote != "" && !s.Unavailable {
				t.Errorf("unavailable not flagged: %+v", s)
			}
			if st := statusOf(t); st.LastRun.Semantic == nil || st.LastRun.Semantic.Ran != tc.wantRan {
				t.Errorf("status last run semantic: %+v", st.LastRun.Semantic)
			}
		})
	}
}

// With the feature off the record carries no semantic report at all.
func TestDailyRunWithSemanticOffRecordsNothing(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	mustCode(t, 0, "run-once")
	if r, _ := readRunRecord(state); r == nil || r.Semantic != nil {
		t.Errorf("record %+v", r)
	}
}

// A cache file that cannot be read is a warning on the listing, not a crash;
// a warm starts the cache again.
func TestSemanticCorruptCache(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s and golang", posted: daysAgo(2), seenAgo: time.Hour})
	mustCode(t, 0, "semantic", "warm")
	scores := filepath.Join(state, "semantic", "semantic-scores.json")
	if err := os.WriteFile(scores, []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}

	l := listJSON(t)
	if l.Jobs[0].Semantic != nil || !strings.Contains(l.Insight.Warning, "score cache could not be read") {
		t.Errorf("semantic %+v, warning %q", l.Jobs[0].Semantic, l.Insight.Warning)
	}

	out, _ := mustCode(t, 0, "semantic", "warm")
	if !strings.Contains(out, "scored 1") || !strings.Contains(out, "started again") {
		t.Errorf("warm after corruption: %q", out)
	}
	if l = listJSON(t); l.Jobs[0].Semantic == nil || l.Insight.Warning != "" {
		t.Errorf("after rebuild: semantic %+v warning %q", l.Jobs[0].Semantic, l.Insight.Warning)
	}
}

// --max bounds the postings embedded, and the freshest go first whatever the
// ledger order.
func TestSemanticWarmIsBoundedAndFreshestFirst(t *testing.T) {
	_, state := semanticSetup(t)
	var seeds []insightSeed
	// The oldest sorts first by key; the freshest sorts last.
	for i, age := range []int{40, 20, 10, 2} {
		seeds = append(seeds, insightSeed{key: fmt.Sprintf("gh:a:%d", i), company: "Acme", title: "Engineer " + fmt.Sprint(i),
			desc: "Run k8s services " + fmt.Sprint(i), status: posting.Explained, posted: daysAgo(age), seenAgo: time.Hour})
	}
	seedInsight(t, state, seeds...)

	out, _ := mustCode(t, 0, "semantic", "warm", "--max", "2")
	if !strings.Contains(out, "scored 2") || !strings.Contains(out, "skipped 2") {
		t.Fatalf("warm output %q", out)
	}
	l := listJSON(t, "--max-age", "0")
	have := map[string]bool{}
	for _, j := range l.Jobs {
		have[string(j.Key)] = j.Semantic != nil
	}
	if !have["gh:a:3"] || !have["gh:a:2"] || have["gh:a:1"] || have["gh:a:0"] {
		t.Errorf("scored %v: want only the two freshest (gh:a:3, gh:a:2)", have)
	}
	mustCode(t, 2, "semantic", "warm", "--max", "999999")
}

// A context that ends mid-warm returns promptly with a canceled report, even
// when the embedder ignores the context.
func TestSemanticWarmTimeoutMidWarm(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	semanticWarmTimeout = 100 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return stuckEmbedder{release}, func() error { return nil }, nil
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	recs := []ledger.Record{{Key: "gh:a:1", Status: posting.Explained, FirstSeen: jobsT0,
		Posting: posting.Posting{Title: "Infra", Description: "Run k8s clusters"}}}
	start := time.Now()
	rep := warmSemantic(context.Background(), cfg, state, recs, nil, 10)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("warm held for %v past its context", d)
	}
	if !rep.Canceled || rep.Scored != 0 || !strings.Contains(rep.Note, "stopped early") {
		t.Errorf("report %+v", rep)
	}
}

type stuckEmbedder struct{ release chan struct{} }

func (stuckEmbedder) Model() string { return "stuck/dim=1" }
func (s stuckEmbedder) Embed(ctx context.Context, _ []string, _ semantic.Kind) ([][]float32, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return nil, errors.New("stuck")
}

// Neither the bank's text nor a posting's text appears in anything the warm
// writes down: stdout, stderr, the run record or the status.
func TestSemanticWritesNoPersonalOrPostingText(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s and golang. " + postMarker, posted: daysAgo(2), seenAgo: time.Hour})

	var outputs []string
	o, e := mustCode(t, 0, "semantic", "warm")
	outputs = append(outputs, o, e)
	o, e = mustCode(t, 0, "semantic", "status")
	outputs = append(outputs, o, e)

	// A daily run on the same state, against a ledger holding the marker.
	l := fixture()
	for _, r := range l.recs {
		r.Posting.Description = "Run k8s " + postMarker
	}
	wire(t, l, &fakeNotifier{}, nil)
	_, e = mustCode(t, 0, "run-once")
	outputs = append(outputs, e)
	raw, err := os.ReadFile(filepath.Join(state, "runs", "last.json"))
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(raw))
	// An unavailable run, whose reasons come from errors.
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return nil, nil, fmt.Errorf("%w: refused", semantic.ErrUnavailable)
	}
	_, e = mustCode(t, 1, "semantic", "warm")
	outputs = append(outputs, e)

	for i, s := range outputs {
		for _, m := range []string{bankMarker, postMarker, "Quuxville", "Operated Kubernetes"} {
			if strings.Contains(s, m) {
				t.Errorf("output %d contains %q:\n%s", i, m, s)
			}
		}
	}
}

func TestSemanticStatus(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state, insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer", status: posting.Explained,
		desc: "Run k8s", posted: daysAgo(2), seenAgo: time.Hour})
	o, _ := mustCode(t, 0, "semantic", "status")
	for _, want := range []string{"Tessl engine: no", "configured (semantic.snapshot and kb.bank): yes", "profile chunks: ", "cached scores: 0", "model: none yet"} {
		if semantic.TesslBuilt && strings.Contains(want, "engine: no") {
			continue
		}
		if !strings.Contains(o, want) {
			t.Errorf("status missing %q:\n%s", want, o)
		}
	}
	mustCode(t, 0, "semantic", "warm")
	o, _ = mustCode(t, 0, "semantic", "status")
	if !strings.Contains(o, "cached scores: 1") || !strings.Contains(o, "fake-bow") {
		t.Errorf("status after warm:\n%s", o)
	}
}

func TestSemanticOffAndUsage(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 1, "semantic", "warm") // not configured
	if o, _ := mustCode(t, 0, "semantic", "status"); !strings.Contains(o, "configured (semantic.snapshot and kb.bank): no") {
		t.Errorf("status:\n%s", o)
	}
	mustCode(t, 2, "semantic")
	mustCode(t, 2, "semantic", "bogus")
	mustCode(t, 2, "semantic", "warm", "extra")
	seedInsight(t, filepath.Join(os.Getenv("VANI_STATE_DIR")), insightSeed{key: "gh:a:1", company: "Acme", title: "Infra Engineer",
		status: posting.Explained, desc: "Run k8s", seenAgo: time.Hour})
	// With it off, jobs carry no semantic field and no warning about it.
	if l := listJSON(t); l.Insight.Warning != "" && strings.Contains(l.Insight.Warning, "knowledge-base") {
		t.Errorf("warning %q", l.Insight.Warning)
	}
}

// The jobs JSON field is additive and absent without a match.
func TestJobJSONOmitsSemanticWhenUnknown(t *testing.T) {
	raw, err := json.Marshal(jobJSON{Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "semantic") {
		t.Errorf("job JSON %s", raw)
	}
}
