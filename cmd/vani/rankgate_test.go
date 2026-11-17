package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

var gateShares = rankfit.Shares{Fit: 0.1, Coverage: 0.1, Semantic: 0.05, Keyword: 0.6, Title: 0.05, Employer: 0.05, KBMatch: 0.05}

func gateFile(t *testing.T, name string, f rankfit.File) config.Config {
	t.Helper()
	f.V, f.FittedAt = rankfit.FileVersion, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), name)
	if err := rankfit.Write(path, f); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RankWeightsFile = path
	return cfg
}

func heldOut(fit, hand float64, pairs int) *rankfit.Report {
	// The gain interval sits half way below to a little above the accuracy gain,
	// so a result that gains clears 0 and one that does not (equal, worse) cannot.
	return &rankfit.Report{Weights: &rankfit.FitResult{Shares: gateShares, FitAccuracy: fit, HandAccuracy: hand, HeldOutPairs: pairs,
		GainLo: (fit - hand) / 2, GainHi: fit - hand + 0.05, Resamples: 1000}}
}

// Fitted shares are used only while the file's own held-out result does not
// put them below the hand weights: a file whose recorded accuracy is worse is
// not loaded, the hand shares rank, and the listing and digest say so.
func TestRankWeightsRefusesSharesThatScoreWorseThanTheHandWeights(t *testing.T) {
	hand := jobinsight.DefaultWeights()
	for name, tc := range map[string]struct {
		rep      *rankfit.Report
		refused  bool
		warnPart string
	}{
		"worse":            {heldOut(0.55, 0.60, 400), true, "does not clear 0"},
		"slightly worse":   {heldOut(0.5999, 0.6, 400), true, "does not clear 0"},
		"better":           {heldOut(0.62, 0.60, 400), false, ""},
		"equal":            {heldOut(0.60, 0.60, 400), true, "does not clear 0"},
		"no pairs":         {heldOut(0.9, 0.5, 0), true, "only 0 held-out pairs"},
		"no recorded fit":  {&rankfit.Report{Labels: 200}, true, "records no held-out result"},
		"no report at all": {nil, true, "records no held-out result"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := gateFile(t, "w.json", rankfit.File{Shares: &gateShares, Report: tc.rep})
			w, used, warn := rankWeights(cfg)
			switch {
			case tc.refused:
				if w != nil || used != rankWeightsHand || !strings.Contains(warn, tc.warnPart) || !strings.Contains(warn, "rank_weights_file") {
					t.Errorf("w %v used %q warn %q, want the hand weights and a warning containing %q", w, used, warn, tc.warnPart)
				}
			default:
				if w == nil || w.Keyword <= hand.Keyword || used != "fitted 2026-10-18" {
					t.Errorf("w %+v used %q: the fitted shares should rank", w, used)
				}
				if (tc.warnPart == "") != (warn == "") || !strings.Contains(warn, tc.warnPart) {
					t.Errorf("warn %q, want one containing %q", warn, tc.warnPart)
				}
			}
		})
	}
}

// The check is on the shares: a file's calibration is a separate fit with no
// held-out result of its own, and stays in use when the shares are refused.
func TestRankWeightsRefusedSharesKeepTheCalibration(t *testing.T) {
	cal := map[string]jobinsight.Calibration{"d1-ultra": {A: 0, B: 1.5}}
	cfg := gateFile(t, "w.json", rankfit.File{Shares: &gateShares, Calibration: cal, Report: heldOut(0.5, 0.6, 300)})
	w, used, warn := rankWeights(cfg)
	hand := jobinsight.DefaultWeights()
	if w == nil || w.Fit != hand.Fit || w.Keyword != hand.Keyword {
		t.Fatalf("the refused shares were applied: %+v", w)
	}
	if _, ok := w.Calibration["d1-ultra"]; !ok {
		t.Errorf("the calibration was dropped with the shares: %+v", w.Calibration)
	}
	if used != "fitted 2026-10-18 (calibration only; shares refused)" || !strings.Contains(warn, "does not pass the gate") {
		t.Errorf("used %q warn %q", used, warn)
	}
	// A file with only calibration has no shares to check and says nothing.
	cfg = gateFile(t, "c.json", rankfit.File{Calibration: cal})
	if w, used, warn := rankWeights(cfg); w == nil || used != "fitted 2026-10-18" || warn != "" {
		t.Errorf("calibration only: %v %q %q", w, used, warn)
	}
}

// `vani status` says which weights rank and why, in its text and its JSON: a
// refused file, one used unchecked and one checked are three different answers.
func TestStatusSaysWhichRankWeightsRankAndWhetherTheyWereChecked(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	statusOf := func(cfg config.Config) (rankWeightsFacetJSON, string) {
		t.Helper()
		if err := config.Save(cfgPath, cfg); err != nil {
			t.Fatal(err)
		}
		o, _ := mustCode(t, 0, "status", "--json")
		var s struct {
			RankWeights *rankWeightsFacetJSON `json:"rank_weights"`
		}
		if err := json.Unmarshal([]byte(o), &s); err != nil {
			t.Fatal(err)
		}
		txt, _ := mustCode(t, 0, "status")
		if s.RankWeights == nil {
			return rankWeightsFacetJSON{}, txt
		}
		return *s.RankWeights, txt
	}
	base := config.Default()
	if f, txt := statusOf(base); f.Used != "" || strings.Contains(txt, "rank weights") {
		t.Errorf("no rank_weights_file: facet %+v\n%s", f, txt)
	}
	refused := gateFile(t, "refused.json", rankfit.File{Shares: &gateShares, Report: heldOut(0.4, 0.6, 500)})
	f, txt := statusOf(withWeights(base, refused))
	if f.OK || f.Used != "hand" || f.SharesChecked || !strings.Contains(f.Warning, "does not pass the gate") ||
		!strings.Contains(txt, "rank weights: hand") || !strings.Contains(txt, "does not pass the gate") {
		t.Errorf("refused: %+v\n%s", f, txt)
	}
	unchecked := gateFile(t, "unchecked.json", rankfit.File{Shares: &gateShares})
	f, txt = statusOf(withWeights(base, unchecked))
	if f.OK || f.SharesChecked || f.Used != "hand" || !strings.Contains(txt, "records no held-out result") {
		t.Errorf("unchecked: %+v\n%s", f, txt)
	}
	checked := gateFile(t, "checked.json", rankfit.File{Shares: &gateShares, Report: heldOut(0.7, 0.6, 500)})
	f, txt = statusOf(withWeights(base, checked))
	if !f.OK || !f.SharesChecked || f.Used != "fitted 2026-10-18" || f.Warning != "" || !strings.Contains(txt, "rank weights: fitted 2026-10-18") {
		t.Errorf("checked: %+v\n%s", f, txt)
	}
	broken := base
	broken.RankWeightsFile = filepath.Join(t.TempDir(), "missing.json")
	if f, txt := statusOf(broken); f.OK || f.Used != "hand" || !strings.Contains(txt, "could not be used") {
		t.Errorf("missing: %+v\n%s", f, txt)
	}
}

type rankWeightsFacetJSON struct {
	OK            bool   `json:"ok"`
	Used          string `json:"used"`
	Warning       string `json:"warning"`
	SharesChecked bool   `json:"shares_checked"`
}

func withWeights(base, from config.Config) config.Config {
	base.RankWeightsFile = from.RankWeightsFile
	return base
}

// The listing and the digest carry the refusal, so it is never silent.
func TestJobsAndDigestSayWhenFittedSharesWereRefused(t *testing.T) {
	cfgPath, stateDir := setup(t)
	fixedNow(t)
	cfg := gateFile(t, "w.json", rankfit.File{Shares: &gateShares, Report: heldOut(0.4, 0.6, 500)})
	cfg.TargetSummary = "Go"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedScores(t, stateDir, scoreSeed{key: "gh:a:one", company: "Acme", title: "Engineer", fit: "adjacent", cov: "60", posted: day, seenAgo: day})
	l := listJSON(t)
	if l.Insight.Weights != "hand" || !strings.Contains(l.Insight.Warning, "does not pass the gate") {
		t.Errorf("insight %+v", l.Insight)
	}
	if o, _ := mustCode(t, 0, "jobs", "list"); !strings.Contains(o, "does not pass the gate") {
		t.Errorf("the text listing does not show the refusal:\n%s", o)
	}
	if _, warn := digestOptions(cfg, stateDir); !strings.Contains(warn, "does not pass the gate") {
		t.Errorf("digest warning %q", warn)
	}
	if opts, _ := digestOptions(cfg, stateDir); opts.Weights != nil {
		t.Errorf("the digest ranks by refused shares: %+v", opts.Weights)
	}
}
