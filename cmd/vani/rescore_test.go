package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// rescoreScorer is a DevPrism stand-in: it fails with err when set, and
// counts its calls (postings score concurrently).
type rescoreScorer struct {
	err   error
	calls atomic.Int64
}

func (s *rescoreScorer) Gap(context.Context, string, string) (resume.Gap, error) {
	s.calls.Add(1)
	return resume.Gap{CoveragePct: 55}, s.err
}

func (s *rescoreScorer) Score(context.Context, string, string) (resume.Score, error) {
	s.calls.Add(1)
	return resume.Score{BlockIDs: []string{"b"}}, s.err
}

type rescoreSeed struct {
	id   string
	st   posting.Status
	desc string
	rank string
}

// rescoreLedger writes each seed as one event: status, posting, and a rank
// when set (the explanation on explained records, as a real run leaves it).
func rescoreLedger(t *testing.T, seeds []rescoreSeed) *ledger.File {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	for i, s := range seeds {
		k, err := posting.GreenhouseKey("acme", s.id)
		if err != nil {
			t.Fatal(err)
		}
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Eng " + s.id, Description: s.desc,
			PostedAt: at.Add(time.Duration(i) * time.Minute)}
		ann := map[string]string{}
		if s.st == posting.Explained {
			ann[ledger.AnnExplanation] = "picked reasons"
		}
		if s.rank != "" {
			ann[ledger.AnnRank], ann[ledger.AnnCoverage] = s.rank, s.rank
		}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: at, Key: k, Status: s.st, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

var rescoreMix = []rescoreSeed{
	{"1", posting.Explained, "jd", ""},
	{"2", posting.Explained, "jd", ""},
	{"3", posting.Scored, "jd", ""},
	{"4", posting.Explained, "jd", ""},
	{"5", posting.Explained, "jd", "70"}, // already scored
	{"6", posting.Digested, "jd", ""},    // shown already: left alone
	{"7", posting.Explained, "  ", ""},   // nothing to score
	{"8", posting.Dropped, "jd", ""},     // Rule 7: stays dropped
	{"9", posting.Live, "jd", ""},        // the run's business
	{"10", posting.Triaged, "jd", ""},    // the run's business
}

func rescoreRunner(l ledger.Ledger, sc resume.Scorer) *pipeline.Runner {
	return &pipeline.Runner{Ledger: l, Scorer: sc, Now: func() time.Time { return time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC) }}
}

func recordsByKey(t *testing.T, l ledger.Ledger) map[posting.Key]ledger.Record {
	t.Helper()
	out := map[posting.Key]ledger.Record{}
	for _, r := range l.All() {
		out[r.Key] = r
	}
	return out
}

func TestPlanRescore(t *testing.T) {
	l := rescoreLedger(t, rescoreMix)
	got := planRescore(l.All())
	if want := (rescorePlan{Eligible: 4, NoDescription: 1, Digested: 1}); got != want {
		t.Fatalf("plan %+v, want %+v", got, want)
	}
}

// The limit bounds each pass, passes add up to every eligible posting, and a
// pass with nothing left writes nothing. Only rescorable records are touched,
// and none changes status.
func TestRescoreHonoursTheLimitAndIsIdempotent(t *testing.T) {
	l := rescoreLedger(t, rescoreMix)
	before := recordsByKey(t, l)
	sc := &rescoreScorer{}
	r := rescoreRunner(l, sc)
	var warned strings.Builder
	warnf := func(f string, a ...any) { fmt.Fprintf(&warned, f+"\n", a...) }
	rep, err := rescoreWith(context.Background(), l, 3, 2, r.Rescore, warnf)
	if err != nil || rep.Rescored != 3 || rep.Advance.Attempted != 3 || rep.Advance.Stopped != stopCap {
		t.Fatalf("first pass: %+v, %v", rep, err)
	}
	if strings.Contains(warned.String(), "advance_per_run") {
		t.Errorf("the run's cap line leaked into rescore: %s", warned.String())
	}
	rep, err = rescoreWith(context.Background(), l, 3, 2, r.Rescore, t.Logf)
	if err != nil || rep.Rescored != 1 || rep.Plan.Eligible != 1 {
		t.Fatalf("second pass: %+v, %v", rep, err)
	}
	calls := sc.calls.Load()
	rep, err = rescoreWith(context.Background(), l, 3, 2, r.Rescore, t.Logf)
	if err != nil || rep.Rescored != 0 || rep.Plan.Eligible != 0 || rep.Advance.Attempted != 0 || sc.calls.Load() != calls {
		t.Fatalf("third pass: %+v, %v, %d DevPrism calls (was %d)", rep, err, sc.calls.Load(), calls)
	}
	for k, after := range recordsByKey(t, l) {
		b := before[k]
		if after.Status != b.Status {
			t.Errorf("%s: status %s -> %s", k, b.Status, after.Status)
		}
		scored := after.Annotations[ledger.AnnRank] != b.Annotations[ledger.AnnRank]
		if scored != rescorable(b) {
			t.Errorf("%s (%s, desc %q): scored %v, rescorable %v", k, b.Status, b.Posting.Description, scored, rescorable(b))
		}
		if b.Status == posting.Explained && after.Annotations[ledger.AnnExplanation] != "picked reasons" {
			t.Errorf("%s: explanation %q lost", k, after.Annotations[ledger.AnnExplanation])
		}
	}
}

// DevPrism down: every posting fails, the pass fails loudly, and nothing is
// marked rescored. With more postings than the failure streak, it stops early
// instead of failing every one.
func TestRescoreFailsLoudWhenDevPrismIsDown(t *testing.T) {
	var seeds []rescoreSeed
	for i := range maxConsecutiveFailed + 15 {
		seeds = append(seeds, rescoreSeed{fmt.Sprint(i + 1), posting.Explained, "jd", ""})
	}
	l := rescoreLedger(t, seeds)
	sc := &rescoreScorer{err: errors.New("devprism: start: exec format error")}
	var warned strings.Builder
	warnf := func(f string, a ...any) { fmt.Fprintf(&warned, f+"\n", a...) }
	rep, err := rescoreWith(context.Background(), l, 1000, 2, rescoreRunner(l, sc).Rescore, warnf)
	if !errors.Is(err, errAdvanceFailing) {
		t.Fatalf("err = %v, want errAdvanceFailing", err)
	}
	if rep.Rescored != 0 || rep.Advance.Attempted >= len(seeds) || rep.Advance.Stopped != stopFailStreak {
		t.Fatalf("report %+v", rep)
	}
	if !strings.Contains(warned.String(), "exec format error") {
		t.Errorf("the cause was not reported: %s", warned.String())
	}
	if p := planRescore(l.All()); p.Eligible != len(seeds) {
		t.Fatalf("%d still unscored, want all %d", p.Eligible, len(seeds))
	}
}

// A cancelled pass stops, says so, and writes nothing it did not finish.
func TestRescoreCancelled(t *testing.T) {
	l := rescoreLedger(t, rescoreMix)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := rescoreWith(ctx, l, 10, 2, rescoreRunner(l, &rescoreScorer{}).Rescore, t.Logf)
	if !errors.Is(err, context.Canceled) || rep.Rescored != 0 {
		t.Fatalf("report %+v, err %v", rep, err)
	}
	if p := planRescore(l.All()); p.Eligible != 4 {
		t.Fatalf("%d still unscored, want 4", p.Eligible)
	}
}

// noMustHaveScorer answers NoMustHaves for descriptions marked "nomh" and a
// 55% gap for the rest.
type noMustHaveScorer struct{}

func (noMustHaveScorer) Gap(_ context.Context, jd, _ string) (resume.Gap, error) {
	if jd == "nomh" {
		return resume.Gap{NoMustHaves: true}, nil
	}
	return resume.Gap{CoveragePct: 55}, nil
}

func (noMustHaveScorer) Score(context.Context, string, string) (resume.Score, error) {
	return resume.Score{BlockIDs: []string{"b"}}, nil
}

// A posting DevPrism scored but found no must-haves in leaves Unscored: it
// carries the coverage-unknown marker instead of a rank. It is rescored, not
// "still unscored" (A2-9).
func TestRescoreCountsCoverageUnknownAsRescored(t *testing.T) {
	seeds := []rescoreSeed{
		{"1", posting.Explained, "jd", ""},
		{"2", posting.Explained, "nomh", ""},
		{"3", posting.Scored, "nomh", ""},
		{"4", posting.Explained, "jd", ""},
	}
	l := rescoreLedger(t, seeds)
	rep, err := rescoreWith(context.Background(), l, 10, 2, rescoreRunner(l, noMustHaveScorer{}).Rescore, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Plan.Eligible != 4 || rep.Rescored != 4 {
		t.Fatalf("eligible %d, rescored %d, want 4 and 4 (the cmd prints eligible-rescored as \"still unscored\")", rep.Plan.Eligible, rep.Rescored)
	}
	for _, r := range l.All() {
		if rescorable(r) {
			t.Errorf("%s is still rescorable after the pass", r.Key)
		}
	}
}

// The wired rescore refuses while devprism.binary is empty, and builds the
// run's scoring stage when it is set.
func TestWiredRescoreNeedsDevPrism(t *testing.T) {
	l := rescoreLedger(t, rescoreMix)
	w := &wired{cfg: config.Default(), stateDir: t.TempDir(), led: l, warn: &strings.Builder{}}
	w.cfg.DevPrism.Binary = ""
	if _, err := w.rescore(context.Background(), 10); !errors.Is(err, errScoringOff) {
		t.Fatalf("err = %v, want errScoringOff", err)
	}
	if p := planRescore(l.All()); p.Eligible != 4 {
		t.Fatalf("a refused rescore changed the ledger: %+v", p)
	}
	// A binary that does not start: DevPrism is down, every posting fails.
	w.cfg.DevPrism.Binary = filepath.Join(t.TempDir(), "no-devprism")
	rep, err := w.rescore(context.Background(), 10)
	if !errors.Is(err, errAdvanceFailing) || rep.Rescored != 0 {
		t.Fatalf("report %+v, err %v", rep, err)
	}
}

func TestRescoreCommand(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	out, _ := mustCode(t, 0, "rescore", "--dry-run")
	if !strings.Contains(out, "0 posting(s) would be rescored") {
		t.Errorf("dry run = %q", out)
	}
	for _, args := range [][]string{{"rescore", "extra"}, {"rescore", "--limit", "-1"}, {"rescore", "--limit", fmt.Sprint(config.MaxAdvancePerRun + 1)}} {
		if code, _, _ := do(t, args...); code != 2 {
			t.Errorf("vani %v exited %d, want 2", args, code)
		}
	}
	// The default config has no devprism.binary: a real rescore refuses.
	code, _, stderr := do(t, "rescore")
	if code == 0 || !strings.Contains(stderr, "devprism.binary is empty") {
		t.Errorf("rescore without devprism: exit %d, stderr %q", code, stderr)
	}
}

// The dry run counts from a snapshot, so it works while another process holds
// the ledger lock, and changes nothing.
func TestRescoreDryRunReadsWithoutTheLock(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	held, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	for _, s := range rescoreMix {
		k, _ := posting.GreenhouseKey("acme", s.id)
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Eng", Description: s.desc}
		ann := map[string]string{}
		if s.rank != "" {
			ann[ledger.AnnRank] = s.rank
		}
		if err := held.Append(context.Background(), ledger.Event{V: 1, At: at, Key: k, Status: s.st, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := mustCode(t, 0, "rescore", "--dry-run", "--limit", "2")
	for _, want := range []string{"4 posting(s) would be rescored (at most 2 this time)", "1 unscored posting(s) have no description", "1 digested posting(s) have no score"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	if p := planRescore(held.All()); p.Eligible != 4 {
		t.Fatalf("dry run changed the ledger: %+v", p)
	}
}
