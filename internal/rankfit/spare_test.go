package rankfit

import (
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

func TestFileSpareSeniorityValidatesAppliesAndRoundTrips(t *testing.T) {
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	ok := File{V: FileVersion, FittedAt: at, SpareSeniorityMismatch: []string{"mid", "senior"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("spare-only file refused: %v", err)
	}
	w := ok.Apply(jobinsight.DefaultWeights())
	if !slices.Equal(w.SpareSeniorityMismatch, []string{"mid", "senior"}) {
		t.Errorf("applied %v", w.SpareSeniorityMismatch)
	}
	ok.SpareSeniorityMismatch[0] = "staff_plus"
	if w.SpareSeniorityMismatch[0] != "mid" {
		t.Error("Apply shares the file's slice")
	}
	for name, levels := range map[string][]string{
		"unknown level": {"principal"},
		"manager":       {"manager"},
		"duplicate":     {"mid", "mid"},
		"empty string":  {""},
	} {
		f := File{V: FileVersion, FittedAt: at, SpareSeniorityMismatch: levels}
		if err := f.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (File{V: FileVersion, FittedAt: at}).Validate(); err == nil || !strings.Contains(err.Error(), "spared seniority") {
		t.Errorf("an empty file: %v", err)
	}
	path := filepath.Join(t.TempDir(), "w.json")
	f := File{V: FileVersion, FittedAt: at, SpareSeniorityMismatch: []string{"senior"}}
	if err := Write(path, f); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !slices.Equal(got.SpareSeniorityMismatch, []string{"senior"}) {
		t.Errorf("round trip: %v %v", got.SpareSeniorityMismatch, err)
	}
}

func TestAUCWithIntervalMatchesThePartAUC(t *testing.T) {
	pos, neg := []float64{0.9, 0.8, 0.7}, []float64{0.1, 0.2, 0.75}
	a, lo, hi, ok := AUCWithInterval(pos, neg, 200, 1)
	if !ok || math.Abs(a-8.0/9) > 1e-12 || lo > a || hi < a {
		t.Errorf("auc %v (%v-%v) ok=%v", a, lo, hi, ok)
	}
	if _, _, _, ok := AUCWithInterval(pos[:2], neg, 200, 1); ok {
		t.Error("two wanted must be insufficient")
	}
}
