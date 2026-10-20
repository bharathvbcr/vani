package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

const (
	infraDesc = "Requirements:\n- Experience operating k8s clusters in production\n- Strong golang skills building backend services"
	salesDesc = "Responsibilities:\n- Sell enterprise subscriptions and manage quota\n- Own territory accounts and renewals"
)

// semRec is a live posting first seen an hour ago and posted three days ago, so
// two of them are equally fresh.
func semRec(key, title, desc string) ledger.Record {
	at := now()
	return ledger.Record{Key: posting.Key(key), Status: posting.Live, FirstSeen: at.Add(-time.Hour),
		Posting: posting.Posting{Key: posting.Key(key), Title: title, Description: desc, PostedAt: at.AddDate(0, 0, -3)}}
}

func loadCfg(t *testing.T, cfgPath string) config.Config {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func docsOf(recs []ledger.Record) []semantic.Doc {
	docs := make([]semantic.Doc, len(recs))
	for i, r := range recs {
		docs[i] = semantic.Doc{Key: string(r.Key), Title: r.Posting.Title, Description: r.Posting.Description}
	}
	return docs
}

func orderedWith(recs []ledger.Record, rank func(posting.Posting) int, opts ...orderOption) []string {
	todo := slices.Clone(recs)
	orderAdvance(todo, recs, rank, opts...)
	return keysOf(todo)
}

// Among postings of one title rank and one age, the one that matches the
// knowledge base reaches the decision model first; with no cache the order is
// the old one (ledger order for equals), and the title rank still comes first.
func TestOrderAdvanceKBMatchBreaksTiesWithinTitleRank(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	recs := []ledger.Record{
		semRec("gh:a:1-sales", "Engineer", salesDesc), // first in ledger order
		semRec("gh:a:2-infra", "Engineer", infraDesc),
	}
	plain := orderedWith(recs, nil)
	if !slices.Equal(plain, []string{"gh:a:1-sales", "gh:a:2-infra"}) {
		t.Fatalf("baseline order %v", plain)
	}

	// No cache yet: semanticLookup is nil and the order is unchanged.
	m, _ := semanticLookup(cfg, state)
	if got := orderedWith(recs, nil, withSemantic(semanticRank(m))); !slices.Equal(got, plain) {
		t.Fatalf("no cache: order %v, want %v", got, plain)
	}

	if rep := warmSemanticDocs(context.Background(), cfg, state, docsOf(recs), 10); rep.Scored != 2 {
		t.Fatalf("warm %+v", rep)
	}
	m, warning := semanticLookup(cfg, state)
	if m == nil {
		t.Fatalf("no lookup after the warm: %s", warning)
	}
	sem := withSemantic(semanticRank(m))
	if got := orderedWith(recs, nil, sem); !slices.Equal(got, []string{"gh:a:2-infra", "gh:a:1-sales"}) {
		t.Errorf("KB match should lead: %v", got)
	}
	// Title rank is still the first key, then the match.
	rank := func(p posting.Posting) int {
		if p.Key == "gh:a:1-sales" {
			return 2
		}
		return 1
	}
	if got := orderedWith(recs, rank, sem); got[0] != "gh:a:1-sales" {
		t.Errorf("a higher title rank must still lead: %v", got)
	}
	// Order only: nothing is dropped.
	if got := orderedWith(recs, nil, sem); len(got) != len(recs) {
		t.Errorf("ordering dropped postings: %v", got)
	}
}

func newWired(cfg config.Config, state string, warn io.Writer) *wired {
	return &wired{cfg: cfg, stateDir: state, warn: warn}
}

// The warm before the advance phase embeds only live postings the title rule
// keeps, freshest first, at most preAdvanceBudget of the run's budget, and its
// report is what the run record gets.
func TestWarmBeforeAdvanceIsBoundedAndRecorded(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	cfg.Semantic.WarmPerRun = 4 // before the advance phase: 3
	if got := preAdvanceBudget(4); got != 3 {
		t.Fatalf("preAdvanceBudget(4) = %d", got)
	}
	var recs []ledger.Record
	for i, age := range []int{30, 20, 10, 5, 1} {
		r := semRec(fmt.Sprintf("gh:a:%d", i), "Engineer", infraDesc+fmt.Sprint(i))
		r.Posting.PostedAt = now().AddDate(0, 0, -age)
		recs = append(recs, r)
	}
	dropped := semRec("gh:a:drop", "Sales Engineer", infraDesc)
	kept := semRec("gh:a:kept", "Engineer", infraDesc)
	kept.Status = posting.Explained
	recs = append(recs, dropped, kept)
	drop := func(r ledger.Record) bool { return strings.HasPrefix(r.Posting.Title, "Sales") }

	var warn bytes.Buffer
	w := newWired(cfg, state, &warn)
	w.warmBeforeAdvance(context.Background(), recs, drop)
	pre := w.runReport().SemanticPre
	if pre == nil || !pre.Ran || pre.Scored != 3 || pre.Skipped != 2 {
		t.Fatalf("report %+v (warn %q)", pre, warn.String())
	}
	m, warning := semanticLookup(cfg, state)
	if m == nil {
		t.Fatal(warning)
	}
	for _, r := range recs {
		_, ok := semanticScore(m, r)
		want := r.Key == "gh:a:4" || r.Key == "gh:a:3" || r.Key == "gh:a:2" // the three freshest live, undropped
		if ok != want {
			t.Errorf("%s warmed=%v, want %v", r.Key, ok, want)
		}
	}
}

func TestWarmBeforeAdvanceOffRecordsNothing(t *testing.T) {
	_, state := setup(t)
	w := newWired(config.Default(), state, io.Discard)
	w.warmBeforeAdvance(context.Background(), []ledger.Record{semRec("gh:a:1", "Engineer", infraDesc)}, nil)
	if w.runReport().SemanticPre != nil {
		t.Error("a report with semantic matching off")
	}
	if w.advanceOrder() != nil {
		t.Error("an order option with semantic matching off")
	}
}

// A model that cannot load before the advance phase is a note in the report,
// not a stop: every posting is still advanced, in the old order.
func TestWarmFailureBeforeAdvanceDoesNotStopAdvance(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	openEmbedder = func(context.Context, config.Config) (semantic.Embedder, func() error, error) {
		return nil, nil, fmt.Errorf("%w: no snapshot", semantic.ErrUnavailable)
	}
	recs := []ledger.Record{semRec("gh:a:1", "Engineer", infraDesc), semRec("gh:a:2", "Engineer", salesDesc)}
	var warn bytes.Buffer
	w := newWired(cfg, state, &warn)
	w.warmBeforeAdvance(context.Background(), recs, nil)
	pre := w.runReport().SemanticPre
	if pre == nil || pre.Ran || !pre.Unavailable || !strings.Contains(pre.Note, "semantic matching not run") {
		t.Fatalf("report %+v", pre)
	}
	var advanced []string
	rep, err := advanceAll(context.Background(), recs, nil, 10, 1, nil, func(_ context.Context, k posting.Key) error {
		advanced = append(advanced, string(k))
		return nil
	}, func(string, ...any) {}, w.advanceOrder())
	if err != nil || rep.Attempted != 2 || !slices.Equal(advanced, []string{"gh:a:1", "gh:a:2"}) {
		t.Fatalf("advance %+v %v %v", rep, err, advanced)
	}
}

// The run's semantic budget is one budget: the post-advance warm gets what
// the warm before it did not embed, and none when nothing is left.
func TestDailyRunSemanticBudgetIsSharedWithPreAdvanceWarm(t *testing.T) {
	for _, tc := range []struct {
		name         string
		preScored    int
		wantScored   int
		wantSkipped  int
		wantNoteWord string
	}{
		{name: "partly spent", preScored: 4, wantScored: 1, wantSkipped: 1},
		{name: "all spent", preScored: 5, wantNoteWord: "spent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, state := semanticSetup(t)
			cfg := loadCfg(t, cfgPath)
			cfg.Semantic.WarmPerRun = 5
			if err := config.Save(cfgPath, cfg); err != nil {
				t.Fatal(err)
			}
			oldApp, oldNow := buildApp, now
			t.Cleanup(func() { buildApp, now = oldApp, oldNow })
			now = func() time.Time { return time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC) }
			pre := &semanticReport{Ran: true, Scored: tc.preScored}
			buildApp = func(config.Config, string, io.Writer) (*App, error) {
				return &App{Ledger: fixture(), Notifier: &fakeNotifier{},
					RunOnce:   func(context.Context) error { return nil },
					RunReport: func() runDetail { return runDetail{SemanticPre: pre} }}, nil
			}
			mustCode(t, 0, "run-once")
			r, err := readRunRecord(state)
			if err != nil || r == nil || r.Semantic == nil {
				t.Fatalf("record %+v %v", r, err)
			}
			s := *r.Semantic
			if s.Scored != tc.wantScored || s.Skipped != tc.wantSkipped || !strings.Contains(s.Note, tc.wantNoteWord) {
				t.Errorf("post-advance report %+v", s)
			}
			if r.SemanticPre == nil || r.SemanticPre.Scored != tc.preScored {
				t.Errorf("pre-advance report not on the record: %+v", r.SemanticPre)
			}
		})
	}
}

// Scores another model wrote are not the configured model's: the lookup says
// so and semantic stays unknown, until a warm rewrites them.
func TestSemanticLookupRefusesAnotherModelsScores(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	recs := []ledger.Record{semRec("gh:a:1", "Engineer", infraDesc)}
	if rep := warmSemanticDocs(context.Background(), cfg, state, docsOf(recs), 10); rep.Scored != 1 {
		t.Fatalf("warm %+v", rep)
	}

	semanticModelPrefix = func(config.Config) string { return "tessl/embeddinggemma-2@new-snapshot/f32/d" }
	m, warning := semanticLookup(cfg, state)
	if m != nil || !strings.Contains(warning, "another model") || !strings.Contains(warning, "vani semantic warm") {
		t.Fatalf("mismatch: matcher %v, warning %q", m, warning)
	}
	semanticModelPrefix = func(config.Config) string { return "fake-bow/" }
	if m, warning = semanticLookup(cfg, state); m == nil || warning != "" {
		t.Fatalf("matching prefix: %v %q", m, warning)
	}
	semanticModelPrefix = func(config.Config) string { return "" }
	if m, _ = semanticLookup(cfg, state); m == nil {
		t.Fatal("empty prefix should accept any model")
	}
}

func TestSemanticModelPrefixFollowsSnapshot(t *testing.T) {
	cfg := config.Default()
	cfg.Semantic.Snapshot = "/models/embeddinggemma-2/snapshots/rev9"
	if got := semanticModelPrefix(cfg); got != "tessl/embeddinggemma-2@rev9/f32/d" {
		t.Errorf("prefix %q", got)
	}
}
