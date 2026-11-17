package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

const w4dGateMsg = "shares present but their held-out result does not pass the gate"

// w4dResult is a held-out result that Beats(): enough pairs and a gain
// interval above 0.
func w4dResult() *rankfit.FitResult {
	return &rankfit.FitResult{Shares: gateShares, FitAccuracy: 0.66, HandAccuracy: 0.60, HeldOutPairs: 400,
		GainLo: 0.01, GainHi: 0.11, Resamples: 1000}
}

func w4dReport(mod func(*rankfit.FitResult)) *rankfit.Report {
	r := w4dResult()
	if mod != nil {
		mod(r)
	}
	return &rankfit.Report{Weights: r}
}

// The read check is Beats(), not "fitted accuracy is not below hand accuracy":
// each stored result below passes the old check and fails the write gate, so
// its shares do not rank.
func TestW4dReadGateIsBeats(t *testing.T) {
	if !w4dResult().Beats() {
		t.Fatal("setup: the passing result does not Beat")
	}
	for name, tc := range map[string]struct {
		mod    func(*rankfit.FitResult)
		reason string
	}{
		"gain interval spans 0": {func(r *rankfit.FitResult) { r.GainLo = -0.02 }, "does not clear 0"},
		"never evaluated":       {func(r *rankfit.FitResult) { r.Resamples = 0 }, "never evaluated"},
		"too few pairs":         {func(r *rankfit.FitResult) { r.HeldOutPairs = rankfit.MinHeldOutPairs - 1 }, "held-out pairs"},
		"too few scored pairs": {func(r *rankfit.FitResult) {
			r.Stratified, r.ScoredPairs = true, rankfit.MinScoredStratumPairs-1
		}, "scored-stratum"},
	} {
		t.Run(name, func(t *testing.T) {
			rep := w4dReport(tc.mod)
			if r := rep.Weights; r.FitAccuracy < r.HandAccuracy || r.HeldOutPairs <= 0 {
				t.Fatal("setup: this result would fail the old read check too")
			}
			cfg := gateFile(t, "w.json", rankfit.File{Shares: &gateShares, Report: rep})
			w, used, warn := rankWeights(cfg)
			if w != nil || used != rankWeightsHand || !strings.Contains(warn, w4dGateMsg) || !strings.Contains(warn, tc.reason) {
				t.Errorf("w %v used %q warn %q: shares failing Beats ranked, or the reason is missing (%q)", w, used, warn, tc.reason)
			}
		})
	}
	// The result that Beats ranks, checked.
	cfg := gateFile(t, "ok.json", rankfit.File{Shares: &gateShares, Report: w4dReport(nil)})
	if w, _, warn, checked := rankWeightsChecked(cfg); w == nil || w.Keyword <= jobinsight.DefaultWeights().Keyword || warn != "" || !checked {
		t.Errorf("a passing result: %+v %q %v", w, warn, checked)
	}
}

// Shares with no held-out result at all (a platt-only write drops it) stay
// unused, with the same message; the calibration the file also carries ranks.
func TestW4dUncheckedSharesAreNotUsed(t *testing.T) {
	cal := map[string]jobinsight.Calibration{"d1-ultra": {B: 1.5}}
	hand := jobinsight.DefaultWeights()
	for name, rep := range map[string]*rankfit.Report{"no report": nil, "report without weights": {Labels: 9}} {
		t.Run(name, func(t *testing.T) {
			cfg := gateFile(t, "w.json", rankfit.File{Shares: &gateShares, Calibration: cal, Report: rep})
			w, used, warn := rankWeights(cfg)
			if w == nil || w.Keyword != hand.Keyword || w.Calibration["d1-ultra"].B != 1.5 {
				t.Fatalf("shares used unchecked or calibration lost: %+v", w)
			}
			if used != "fitted 2026-10-18 (calibration only; shares refused)" || !strings.Contains(warn, w4dGateMsg) {
				t.Errorf("used %q warn %q", used, warn)
			}
		})
	}
	// With nothing else in the file the hand weights rank outright.
	cfg := gateFile(t, "only.json", rankfit.File{Shares: &gateShares})
	if w, used, warn := rankWeights(cfg); w != nil || used != rankWeightsHand || !strings.Contains(warn, w4dGateMsg) {
		t.Errorf("shares only: %v %q %q", w, used, warn)
	}
}

// status --json says, per part of the file, whether it is in use and why not.
func TestW4dStatusSaysWhichPartsAreInUse(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	type part struct {
		Part   string `json:"part"`
		InUse  bool   `json:"in_use"`
		Reason string `json:"reason"`
	}
	partsOf := func(path string) (map[string]part, string) {
		t.Helper()
		cfg := config.Default()
		cfg.RankWeightsFile = path
		if err := config.Save(cfgPath, cfg); err != nil {
			t.Fatal(err)
		}
		o, _ := mustCode(t, 0, "status", "--json")
		var s struct {
			RankWeights struct {
				Parts []part `json:"parts"`
			} `json:"rank_weights"`
		}
		if err := json.Unmarshal([]byte(o), &s); err != nil {
			t.Fatal(err)
		}
		m := map[string]part{}
		for _, p := range s.RankWeights.Parts {
			m[p.Part] = p
		}
		txt, _ := mustCode(t, 0, "status")
		return m, txt
	}
	cal := map[string]jobinsight.Calibration{"d1-ultra": {B: 1.5}}

	// Shares that fail the gate: shares not in use with the reason, calibration in use.
	cfg := gateFile(t, "w.json", rankfit.File{Shares: &gateShares, Calibration: cal,
		Report: w4dReport(func(r *rankfit.FitResult) { r.GainLo = -0.01 })})
	m, txt := partsOf(cfg.RankWeightsFile)
	if p := m["shares"]; p.InUse || !strings.Contains(p.Reason, w4dGateMsg) {
		t.Errorf("shares part %+v", p)
	}
	if p := m["calibration"]; !p.InUse || p.Reason != "" {
		t.Errorf("calibration part %+v", p)
	}
	if !strings.Contains(txt, w4dGateMsg) {
		t.Errorf("text status:\n%s", txt)
	}

	// Shares that pass, in a file whose report gained a field.
	good := gateFile(t, "g.json", rankfit.File{Shares: &gateShares, Report: w4dReport(nil)})
	b, err := os.ReadFile(good.RankWeightsFile)
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(t.TempDir(), "extra.json")
	if err := os.WriteFile(extra, []byte(strings.Replace(string(b), `"report": {`, `"report": {"newer_field": 1,`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	m, txt = partsOf(extra)
	if p := m["shares"]; !p.InUse || p.Reason != "" {
		t.Errorf("shares part %+v", p)
	}
	if p := m["report"]; !p.InUse || !strings.Contains(p.Reason, "newer_field") {
		t.Errorf("report part %+v", p)
	}
	if !strings.Contains(txt, "newer_field") {
		t.Errorf("text status does not note the ignored report field:\n%s", txt)
	}

	// An unknown field in shares refuses the file, and status names the part.
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(strings.Replace(string(b), `"fit":`, `"extra": 1, "fit":`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	m, txt = partsOf(bad)
	if p := m["shares"]; p.InUse || !strings.Contains(p.Reason, "unknown field") {
		t.Errorf("refused shares part %+v (parts %+v)", p, m)
	}
	if !strings.Contains(txt, "could not be used") {
		t.Errorf("text status:\n%s", txt)
	}
}
