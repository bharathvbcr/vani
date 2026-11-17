package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

func w1cMergeCur() rankfit.File {
	return rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0,
		Shares:      &rankfit.Shares{Fit: 0.2, Coverage: 0.5, Semantic: 0.2, Keyword: 0.01, Title: 0.05, Employer: 0.04, KBMatch: 0.1},
		Calibration: map[string]jobinsight.Calibration{"lappi": {B: 0.7}},
		Report: &rankfit.Report{Weights: &rankfit.FitResult{HeldOutPairs: 50, FitAccuracy: 0.8, HandAccuracy: 0.7},
			Platt: map[string]rankfit.PlattFit{"lappi": {N: 30}}}}
}

// A platt-only write keeps the shares but drops their held-out result: it was
// measured under the calibration just replaced, so the reader must see the
// shares as unchecked, not as checked. (Round 1 carried the result over.)
func TestW1cMergePlattOnlyDropsTheSharesHeldOutResult(t *testing.T) {
	cur := w1cMergeCur()
	fitted := rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0.Add(1),
		Calibration: map[string]jobinsight.Calibration{"lappi": {B: 0.9}},
		Report:      &rankfit.Report{Platt: map[string]rankfit.PlattFit{"lappi": {N: 40}}}}
	got, err := mergeRankfitPart(cur, true, fitted, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shares == nil || *got.Shares != *cur.Shares {
		t.Errorf("shares %+v, want kept", got.Shares)
	}
	if got.Report == nil || got.Report.Weights != nil {
		t.Errorf("report weights %+v, want dropped", got.Report)
	}
	// Unchecked shares stay unused on read, and the message explains why.
	if g := sharesGate(got); !strings.Contains(g, "does not pass the gate") || !strings.Contains(g, "calibration-only fit") {
		t.Errorf("gate verdict %q, want the shares unused with a kept-unchecked explanation", g)
	}
}

// A weights-only fit refuses, writing nothing, when the calibration it was
// evaluated under (read before the fit) is not the one on disk under the lock.
func TestW1cMergeWeightsOnlyRefusesAChangedCalibration(t *testing.T) {
	cur := w1cMergeCur()
	fitted := rankfit.File{V: rankfit.FileVersion, FittedAt: jobsT0.Add(1), Shares: cur.Shares,
		Report: &rankfit.Report{Weights: &rankfit.FitResult{HeldOutPairs: 10}}}
	other := map[string]jobinsight.Calibration{"lappi": {B: 0.1}}
	if _, err := mergeRankfitPart(cur, true, fitted, false, true, other); err == nil {
		t.Error("merged over a calibration the fit was not evaluated under")
	}
	if _, err := mergeRankfitPart(cur, true, fitted, false, true, nil); err == nil {
		t.Error("merged over a calibration that appeared after the fit read an empty file")
	}
	if _, err := mergeRankfitPart(rankfit.File{}, false, fitted, false, true, other); err == nil {
		t.Error("merged though the file the fit saw is gone")
	}
	if _, err := mergeRankfitPart(cur, true, fitted, false, true, cur.Calibration); err != nil {
		t.Errorf("an unchanged calibration was refused: %v", err)
	}
	// A fit that also refits the calibration owns it and has nothing to compare.
	if _, err := mergeRankfitPart(cur, true, fitted, true, true, other); err != nil {
		t.Errorf("a full fit was refused: %v", err)
	}
}

// A weights-only fit is evaluated under the file's calibration, not none.
func TestW1cFitBaseWeightsOnlyCarriesTheFilesCalibration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rank", "w.json")
	if err := rankfit.Write(path, w1cMergeCur()); err != nil {
		t.Fatal(err)
	}
	base, seen, err := rankfitFitBase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if base.Calibration["lappi"] != (jobinsight.Calibration{B: 0.7}) || seen["lappi"].B != 0.7 {
		t.Errorf("base calibration %+v, seen %+v; want the file's", base.Calibration, seen)
	}
	hand, _, _ := rankfitFitBase(path, true)
	if len(hand.Calibration) != 0 {
		t.Errorf("a platt fit started from calibration %+v", hand.Calibration)
	}
	if b, s, err := rankfitFitBase(filepath.Join(t.TempDir(), "none.json"), false); err != nil || len(s) != 0 || len(b.Calibration) != 0 {
		t.Errorf("no file: %+v %+v %v", b.Calibration, s, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"v":1,`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rankfitFitBase(bad, false); err == nil {
		t.Error("a malformed file was not refused")
	}
}
