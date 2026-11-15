package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type coverageSeed struct {
	id      string
	st      posting.Status
	desc    string
	unknown bool   // the coverage-unknown marker DevPrism's 0-of-0 left
	rank    string // a measured coverage
}

// appendCoverageSeeds writes each seed as one event, freshest last.
func appendCoverageSeeds(t *testing.T, l *ledger.File, seeds []coverageSeed) {
	t.Helper()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	for i, s := range seeds {
		k, err := posting.GreenhouseKey("acme", s.id)
		if err != nil {
			t.Fatal(err)
		}
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Eng " + s.id, Description: s.desc,
			PostedAt: at.Add(time.Duration(i) * time.Minute)}
		ann := map[string]string{}
		if s.st == posting.Explained || s.st == posting.Digested {
			ann[ledger.AnnExplanation] = "picked reasons"
		}
		if s.unknown {
			ann[ledger.AnnCoverageUnknown] = ledger.CoverageNoMustHaves
		}
		if s.rank != "" {
			ann[ledger.AnnRank], ann[ledger.AnnCoverage] = s.rank, s.rank
		}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: at, Key: k, Status: s.st, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
}

func coverageLedger(t *testing.T, seeds []coverageSeed) *ledger.File {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	appendCoverageSeeds(t, l, seeds)
	return l
}

// The mix: noMustHaveScorer finds must-haves in "jd" and none in "nomh".
var coverageMix = []coverageSeed{
	{id: "1", st: posting.Explained, desc: "jd", unknown: true},  // measured
	{id: "2", st: posting.Digested, desc: "jd", unknown: true},   // measured, status kept
	{id: "3", st: posting.Digested, desc: "nomh", unknown: true}, // asked, still unknown
	{id: "4", st: posting.Scored, desc: "jd", unknown: true},     // measured
	{id: "5", st: posting.Dropped, desc: "jd", unknown: true},    // Rule 7: never asked
	{id: "6", st: posting.Dead, desc: "jd", unknown: true},       // closed: never asked
	{id: "7", st: posting.Digested, desc: "  ", unknown: true},   // no text to read
	{id: "8", st: posting.Digested, desc: "jd", rank: "70"},      // already measured
	{id: "9", st: posting.Explained, desc: "jd"},                 // unscored: plain rescore's, not this mode's
}

func coverageKey(t *testing.T, id string) posting.Key {
	t.Helper()
	k, err := posting.GreenhouseKey("acme", id)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The pass asks DevPrism about exactly the shown, unknown, described postings,
// and every count comes from the ledger afterwards: measured, still unknown,
// not reached. Statuses never change; closed postings and the default mode's
// unscored ones are untouched.
func TestRescoreCoverageCountsFromTheLedger(t *testing.T) {
	l := coverageLedger(t, coverageMix)
	before := recordsByKey(t, l)
	rep, err := rescoreCoverageWith(context.Background(), l, 10, 2, rescoreRunner(l, noMustHaveScorer{}).RescoreUnknown, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Eligible != 4 || rep.Asked != 4 || rep.Measured != 3 || rep.StillUnknown != 1 || rep.NotReached != 0 || rep.Failed != 0 {
		t.Fatalf("report %+v, want eligible 4, asked 4, measured 3, still unknown 1", rep)
	}
	after := recordsByKey(t, l)
	for k, b := range before {
		a := after[k]
		if a.Status != b.Status {
			t.Errorf("%s: status %s -> %s", k, b.Status, a.Status)
		}
	}
	for _, id := range []string{"1", "2", "4"} {
		if r := after[coverageKey(t, id)]; ledger.CoverageUnknown(r) || r.Annotations[ledger.AnnCoverage] != "55" ||
			r.Annotations[ledger.AnnExplanation] != before[coverageKey(t, id)].Annotations[ledger.AnnExplanation] {
			t.Errorf("%s not measured in place: %v", id, r.Annotations)
		}
	}
	for _, id := range []string{"3", "5", "6", "7", "8", "9"} {
		k := coverageKey(t, id)
		if !after[k].LastEvent.Equal(before[k].LastEvent) {
			t.Errorf("%s was written", id)
		}
	}
	if !rescorable(after[coverageKey(t, "9")]) {
		t.Error("the default mode's unscored posting was touched")
	}
}

// A limit below the eligible count says how many were not reached; a second
// pass reaches them, and what was measured is not asked again.
func TestRescoreCoverageLimit(t *testing.T) {
	var seeds []coverageSeed
	for i := range 5 {
		seeds = append(seeds, coverageSeed{id: fmt.Sprint(i + 1), st: posting.Digested, desc: "jd", unknown: true})
	}
	l := coverageLedger(t, seeds)
	run := rescoreRunner(l, noMustHaveScorer{}).RescoreUnknown
	rep, err := rescoreCoverageWith(context.Background(), l, 2, 1, run, t.Logf)
	if err != nil || rep.Eligible != 5 || rep.Asked != 2 || rep.Measured != 2 || rep.NotReached != 3 {
		t.Fatalf("first pass %+v, err %v; want 5 eligible, 2 asked and measured, 3 not reached", rep, err)
	}
	rep, err = rescoreCoverageWith(context.Background(), l, 10, 1, run, t.Logf)
	if err != nil || rep.Eligible != 3 || rep.Measured != 3 || rep.NotReached != 0 {
		t.Fatalf("second pass %+v, err %v; want the other 3", rep, err)
	}
	if p := planCoverage(l.All()); p != 0 {
		t.Fatalf("%d still eligible", p)
	}
}

// DevPrism down: the pass fails loudly after the failure streak and writes
// nothing; a cancelled pass writes nothing.
func TestRescoreCoverageFailuresWriteNothing(t *testing.T) {
	var seeds []coverageSeed
	for i := range maxConsecutiveFailed + 5 {
		seeds = append(seeds, coverageSeed{id: fmt.Sprint(i + 1), st: posting.Digested, desc: "jd", unknown: true})
	}
	l := coverageLedger(t, seeds)
	sc := &rescoreScorer{err: errors.New("devprism: start: exec format error")}
	rep, err := rescoreCoverageWith(context.Background(), l, 1000, 2, rescoreRunner(l, sc).RescoreUnknown, t.Logf)
	if !errors.Is(err, errAdvanceFailing) || rep.Measured != 0 || rep.Asked != 0 {
		t.Fatalf("report %+v, err %v", rep, err)
	}
	if p := planCoverage(l.All()); p != len(seeds) {
		t.Fatalf("%d eligible after a failed pass, want all %d", p, len(seeds))
	}

	l2 := coverageLedger(t, coverageMix)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err = rescoreCoverageWith(ctx, l2, 10, 2, rescoreRunner(l2, noMustHaveScorer{}).RescoreUnknown, t.Logf)
	if !errors.Is(err, context.Canceled) || rep.Measured != 0 {
		t.Fatalf("cancelled: report %+v, err %v", rep, err)
	}
	if p := planCoverage(l2.All()); p != 4 {
		t.Fatalf("cancelled pass changed the ledger: %d eligible", p)
	}
}

// The wired pass refuses while devprism.binary is empty, and skips the 0-of-0
// repair (that is the default mode's).
func TestWiredRescoreCoverageNeedsDevPrism(t *testing.T) {
	l := coverageLedger(t, coverageMix)
	w := &wired{cfg: config.Default(), stateDir: t.TempDir(), led: l, warn: &strings.Builder{}}
	w.cfg.DevPrism.Binary = ""
	if _, err := w.rescoreCoverage(context.Background(), 10); !errors.Is(err, errScoringOff) {
		t.Fatalf("err = %v, want errScoringOff", err)
	}
	if p := planCoverage(l.All()); p != 4 {
		t.Fatalf("a refused pass changed the ledger: %d eligible", p)
	}
}

// `vani rescore --coverage-unknown --dry-run` counts the eligible postings from a
// snapshot (it works while another process holds the lock); the default dry
// run's JSON has no coverage part.
func TestRescoreCoverageCommand(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	held, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	appendCoverageSeeds(t, held, coverageMix)

	out, _ := mustCode(t, 0, "rescore", "--coverage-unknown", "--dry-run", "--limit", "2")
	if !strings.Contains(out, "4 posting(s) with unknown coverage would be asked again (at most 2 this time)") {
		t.Errorf("dry run = %q", out)
	}
	o, _ := mustCode(t, 0, "rescore", "--coverage-unknown", "--dry-run", "--json")
	var r struct {
		DryRun   bool `json:"dry_run"`
		Coverage *struct {
			Eligible int `json:"eligible"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(o), &r); err != nil || !r.DryRun || r.Coverage == nil || r.Coverage.Eligible != 4 {
		t.Fatalf("json %s (err %v)", o, err)
	}
	o, _ = mustCode(t, 0, "rescore", "--dry-run", "--json")
	if strings.Contains(o, `"coverage"`) {
		t.Errorf("the default dry run's JSON has a coverage part: %s", o)
	}
	if p := planCoverage(held.All()); p != 4 {
		t.Fatalf("dry run changed the ledger: %d eligible", p)
	}
	if code, _, _ := do(t, "rescore", "--coverage-unknown", "--limit", "-1"); code != 2 {
		t.Errorf("a negative limit exited %d, want 2", code)
	}
}

// The selection is the pipeline's, so a dead or dropped posting never enters it.
func TestPlanCoverageIsCoverageRescorable(t *testing.T) {
	l := coverageLedger(t, coverageMix)
	n := 0
	for _, r := range l.All() {
		if pipeline.CoverageRescorable(r) {
			n++
		}
	}
	if p := planCoverage(l.All()); p != n || n != 4 {
		t.Fatalf("planCoverage %d, CoverageRescorable %d, want 4", p, n)
	}
}
