package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// w1cHandSet is a weights file the owner has been editing: every part, plus a
// spared seniority no fit produces.
func w1cHandSet(t *testing.T, stateDir string) rankfit.File {
	t.Helper()
	f := rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0,
		Shares:                 &rankfit.Shares{Fit: 0.2, Coverage: 0.5, Semantic: 0.2, Keyword: 0.01, Title: 0.05, Employer: 0.04, KBMatch: 0.1},
		Calibration:            map[string]jobinsight.Calibration{"lappi": {B: 0.7}},
		SpareSeniorityMismatch: []string{"mid"}}
	if err := rankfit.Write(weightsPath(stateDir), f); err != nil {
		t.Fatal(err)
	}
	return f
}

// --part platt replaces the calibration only: the shares and the spared
// seniority in the file on disk survive.
func TestW1cRankfitPlattKeepsSharesAndSpared(t *testing.T) {
	_, stateDir := setup(t)
	fixedNow(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 220)
	old := w1cHandSet(t, stateDir)

	mustCode(t, 0, "rankfit", "--part", "platt")
	f, err := rankfit.Load(weightsPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if f.Shares == nil || *f.Shares != *old.Shares {
		t.Errorf("shares %+v, want %+v kept", f.Shares, old.Shares)
	}
	if !slices.Equal(f.SpareSeniorityMismatch, []string{"mid"}) {
		t.Errorf("spared seniority %v lost", f.SpareSeniorityMismatch)
	}
	if len(f.Calibration) != 2 || f.Calibration["d1-ultra"].B == 0 {
		t.Errorf("calibration %+v, want the two fitted backends", f.Calibration)
	}
}

// --part weights replaces the shares only: the calibration and the spared
// seniority survive.
func TestW1cRankfitWeightsKeepsCalibrationAndSpared(t *testing.T) {
	_, stateDir := setup(t)
	fixedNow(t)
	mustCode(t, 0, "init")
	seedJudgments(t, stateDir, 400)
	old := w1cHandSet(t, stateDir)

	mustCode(t, 0, "rankfit", "--part", "weights")
	f, err := rankfit.Load(weightsPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calibration) != 1 || f.Calibration["lappi"] != old.Calibration["lappi"] {
		t.Errorf("calibration %+v, want %+v kept", f.Calibration, old.Calibration)
	}
	if !slices.Equal(f.SpareSeniorityMismatch, []string{"mid"}) {
		t.Errorf("spared seniority %v lost", f.SpareSeniorityMismatch)
	}
	if f.Shares == nil || *f.Shares == *old.Shares {
		t.Errorf("shares %+v, want the fitted ones", f.Shares)
	}
}

// A weights file that cannot be read is refused, whatever the part, and left
// as it is.
func TestW1cRankfitRefusesAMalformedExistingFile(t *testing.T) {
	_, stateDir := setup(t)
	fixedNow(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 220)
	path := weightsPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","shares":{"fit":`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, e := mustCode(t, 1, "rankfit", "--part", "platt"); e == "" {
		t.Error("no reason given")
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("file overwritten with %q", got)
	}
	mustCode(t, 1, "rankfit", "--spare-seniority", "senior")
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Errorf("file overwritten by --spare-seniority with %q", got)
	}
}

// A platt run that fitted no backend does not refresh a file over unchanged
// calibration.
func TestW1cMergeRankfitPartRefusesAnEmptyPlattOverAFile(t *testing.T) {
	cur := rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0, Calibration: map[string]jobinsight.Calibration{"a": {B: 1}}}
	fitted := rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0.Add(1), Report: &rankfit.Report{}}
	if _, err := mergeRankfitPart(cur, true, fitted, true, false); err == nil {
		t.Error("an empty platt fit was merged")
	}
	if got, err := mergeRankfitPart(rankfit.File{}, false, fitted, true, false); err != nil || got.FittedAt != fitted.FittedAt {
		t.Errorf("no prior file: %+v, %v; want the fit alone", got, err)
	}
}
