package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

func weightsPath(stateDir string) string { return filepath.Join(stateDir, "rank", "weights.json") }

// Below every threshold rankfit exits 1, says "could not run" for each part,
// prints the counts and writes nothing: a refusal never reads as a result.
func TestRankfitRefusesBelowThresholds(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 45)

	o, e := mustCode(t, 1, "rankfit")
	for _, want := range []string{
		"could not run: platt needs 100 judged readings for at least one backend, has 23",
		"could not run: weight fit needs 150 judged would-apply labels, has 0",
		"nothing written",
	} {
		if !strings.Contains(e, want) {
			t.Errorf("stderr lacks %q:\n%s", want, e)
		}
	}
	if !strings.Contains(o, "labels: 45 eval entries") || strings.Contains(o, "accuracy") {
		t.Errorf("stdout should carry counts and no figures:\n%s", o)
	}
	if _, err := os.Stat(weightsPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused run left a weights file: %v", err)
	}
	// Each part alone refuses alone.
	if _, e := mustCode(t, 1, "rankfit", "--part", "weights"); !strings.Contains(e, "weight fit needs 150") || strings.Contains(e, "platt") {
		t.Errorf("--part weights: %s", e)
	}
	if _, e := mustCode(t, 1, "rankfit", "--part", "platt"); !strings.Contains(e, "platt needs 100") || strings.Contains(e, "weight fit") {
		t.Errorf("--part platt: %s", e)
	}
}

func TestRankfitDryRunPrintsCounts(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 45)
	o, _ := mustCode(t, 0, "rankfit", "--dry-run")
	for _, want := range []string{
		"platt d1-ultra: 22 judged readings (would refuse: 22 of 100)",
		"platt jev: 23 judged readings (would refuse: 23 of 100)",
		"weight fit: 0 judged would-apply labels (would refuse: 0 of 150)",
		"dry run: nothing fitted or written",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("dry run lacks %q:\n%s", want, o)
		}
	}
	if _, err := os.Stat(weightsPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote a file: %v", err)
	}
}

func TestRankfitFlagsAreChecked(t *testing.T) {
	setup(t)
	for _, args := range [][]string{
		{"--part", "everything"}, {"--holdout", "0"}, {"--holdout", "95"}, {"--lambda", "0"},
		{"--lambda", "NaN"}, {"--max-pairs", "0"}, {"stray"},
	} {
		mustCode(t, 2, append([]string{"rankfit"}, args...)...)
	}
}

// Above the per-backend threshold the calibration is fitted and written, and
// `vani jobs` then says it ranks by the fitted file.
func TestRankfitPlattWritesACalibrationAndJobsUsesIt(t *testing.T) {
	cfgPath, stateDir := setup(t)
	fixedNow(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 220) // 110 readings per backend, p .9, 4 in 5 right

	o, _ := mustCode(t, 0, "rankfit", "--part", "platt")
	if !strings.Contains(o, "wrote "+weightsPath(stateDir)) {
		t.Fatalf("no file reported:\n%s", o)
	}
	f, err := rankfit.Load(weightsPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if f.Shares != nil || len(f.Calibration) != 2 {
		t.Fatalf("file %+v, want two calibrations and no shares", f)
	}
	for _, b := range []string{"d1-ultra", "jev"} {
		c, ok := f.Calibration[b].Conf(0.9, true)
		if !ok || math.Abs(c-0.8) > 0.02 {
			t.Errorf("%s: a p of .9 that is right 4 times in 5 calibrates to %v (ok %v)", b, c, ok)
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RankWeightsFile = weightsPath(stateDir)
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if l := listJSON(t, "--view", "all"); l.Insight.Weights != "fitted 2026-10-18" || strings.Contains(l.Insight.Warning, "rank_weights_file") {
		t.Errorf("insight %+v", l.Insight)
	}
}

// A configured file that is missing or broken means the hand weights, and
// the listing says so: never silently.
func TestJobsWarnWhenTheRankWeightsFileCannotBeUsed(t *testing.T) {
	cfgPath, stateDir := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TargetSummary = "Go"
	broken := filepath.Join(filepath.Dir(cfgPath), "broken.json")
	if err := os.WriteFile(broken, []byte(`{"v":1,"fitted_at":"2026-10-20T00:00:00Z","shares":{"fit":-1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seedScores(t, stateDir, scoreSeed{key: "gh:a:one", company: "Acme", title: "Engineer", fit: "adjacent", cov: "60", posted: day, seenAgo: day})
	for name, path := range map[string]string{"missing": filepath.Join(stateDir, "nope.json"), "broken": broken} {
		cfg.RankWeightsFile = path
		if err := config.Save(cfgPath, cfg); err != nil {
			t.Fatal(err)
		}
		l := listJSON(t)
		if l.Insight.Weights != "hand" || !strings.Contains(l.Insight.Warning, "rank_weights_file could not be used, so the hand weights rank") {
			t.Errorf("%s: insight %+v", name, l.Insight)
		}
		if o, _ := mustCode(t, 0, "jobs", "list"); !strings.Contains(o, "rank_weights_file could not be used") {
			t.Errorf("%s: the text listing does not show the warning:\n%s", name, o)
		}
		_, warn := digestOptions(cfg, stateDir)
		if !strings.Contains(warn, "rank_weights_file could not be used") {
			t.Errorf("%s: digest warning %q", name, warn)
		}
	}
	cfg.RankWeightsFile = ""
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	if l := listJSON(t); l.Insight.Weights != "hand" || strings.Contains(l.Insight.Warning, "rank_weights_file") {
		t.Errorf("unset: insight %+v", l.Insight)
	}
}

// A relative rank_weights_file is refused by the config check, like every
// other path in the config.
func TestConfigRefusesARelativeRankWeightsFile(t *testing.T) {
	cfg := config.Default()
	cfg.RankWeightsFile = "rank/weights.json"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "rank_weights_file") {
		t.Fatalf("relative path: %v", err)
	}
}

// The weight fit end to end below the store: postings ranked by `vani jobs`'s
// own insight, labels that favour coverage over fit. The would-apply label
// cannot be written to the eval store until Phase 1 extends it, so the
// targets are given here directly.
func TestRankfitWeightsFitsJobsInsightParts(t *testing.T) {
	cfgPath, stateDir := setup(t)
	fixedNow(t)
	if err := config.Save(cfgPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	fits := []string{"strong_fit", "partial_fit", "adjacent"}
	var recs []ledger.Record
	targets := map[posting.Key]rankfit.Target{}
	for i := range 400 {
		k := posting.Key(fmt.Sprintf("gh:w:%04d", i))
		cov := (i * 37) % 101
		fit := fits[(i*7)%3]
		recs = append(recs, ledger.Record{Key: k, Status: posting.Explained, FirstSeen: jobsT0.Add(-48 * time.Hour),
			Posting: posting.Posting{Key: k, ATS: "gh", Company: fmt.Sprintf("Co%d", i%40), Title: "Engineer", Description: "Build things.", PostedAt: jobsT0.Add(-time.Duration(i%20) * 24 * time.Hour)},
			Annotations: map[string]string{ledger.AnnCoverage: fmt.Sprint(cov), ledger.AnnRank: fmt.Sprint(cov),
				"triage.role_fit": fit, "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "0.7"}})
		// Judged on coverage, against what the hand weights' fit share says.
		targets[k] = rankfit.Target{Positive: cov >= 50, Judged: i < 300}
	}
	var out bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &out, stderr: &out}
	res, err := e.rankfitWeights(ledger.Snapshot{Records: recs}, nil, targets, jobinsight.DefaultWeights(), rankfit.Options{Seed: 1}, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if res.Judged != 300 || !res.Beats() || !(res.Shares.Coverage/res.Shares.Fit > res.Hand.Coverage/res.Hand.Fit) {
		t.Errorf("fit %+v\n%s", res, out.String())
	}
	for _, want := range []string{"held-out pairwise accuracy: fitted", "coverage  hand 0.2000"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
