package clusterstudy

import (
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStudy() Study {
	return Study{
		Keys:   []string{"a:1", "a:2", "a:3", "a:4", "a:5", "a:6"},
		Target: []bool{true, true, false, false, true, false},
		Has:    []bool{true, true, true, true, false, true}, // a:5 has no reading
	}
}

func TestBaselineCoversOnlyPostingsWithATargetAndACell(t *testing.T) {
	s := testStudy()
	cells := map[int]string{0: "x", 1: "x", 2: "y", 4: "y"} // a:4 (index 3) has no cell; a:5 has no target
	b, err := s.Baseline("cat", func(i int) (string, bool) { c, ok := cells[i]; return c, ok })
	if err != nil {
		t.Fatal(err)
	}
	if b.N() != 3 || b.Cells() != 2 || b.Positives != 2 {
		t.Errorf("n %d cells %d positives %d", b.N(), b.Cells(), b.Positives)
	}
	if got := b.Rows; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("rows %v", got)
	}
	w, err := b.Within()
	if err != nil || w.Plugin != 0 { // x holds two yes, y holds one no
		t.Errorf("within %+v, %v", w, err)
	}
	if _, err := (Study{}).Baseline("x", nil); !errors.Is(err, ErrInput) {
		t.Errorf("empty study: %v", err)
	}
	if _, err := (Study{Keys: []string{"a"}, Target: []bool{true}}).Baseline("x", nil); !errors.Is(err, ErrInput) {
		t.Errorf("ragged study: %v", err)
	}
}

func TestCompareRunCountsTheClustersTheBaselineReaches(t *testing.T) {
	s := testStudy()
	b, _ := s.Baseline("cat", func(i int) (string, bool) { return []string{"x", "x", "y", "y", "y", "y"}[i], true })
	labels := []uint32{5, 5, 9, 9, 7, 9} // cluster 7 holds only a:5, which has no target
	r, err := s.CompareRun(b, labels, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.ClusterCells != 2 {
		t.Errorf("cluster cells %d, want 2 (cluster 7 holds nothing the baseline covers)", r.ClusterCells)
	}
	if _, err := s.CompareRun(b, labels[:3], 30, 1); !errors.Is(err, ErrInput) {
		t.Errorf("short labels: %v", err)
	}
}

func fitSummaryJSON(mod func(s string) string) string {
	sum := KeysSHA256([]string{"a:1"})
	base := `{"format":"vani-cluster-fit/1","vectors":"v.bin","n":10,"dim":4,"keys_sha256":"` + hex.EncodeToString(sum[:]) + `",` +
		`"spherical":true,"emeans_nepochs":2.6,"lloyd_iters":25,"timing_rounds":5,"init":"forgy",` +
		`"metal":{"status":"not_run","detail":"x"},` +
		`"runs":[{"algo":"emeans_cpu","k":3,"seed":1,"assign":"a.assign","centroids":"a.cent","inertia_f64":1.5,"mse_f64":0.15,"non_empty":3,"labels_match_engine":true,"fit_ms":[1,2]}]}`
	return mod(base)
}

func writeSummary(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "summary.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFitSummary(t *testing.T) {
	s, err := ReadFitSummary(writeSummary(t, fitSummaryJSON(func(s string) string { return s })))
	if err != nil {
		t.Fatal(err)
	}
	if s.N != 10 || s.Dim != 4 || len(s.Runs) != 1 || s.Runs[0].K != 3 || s.Metal.Status != "not_run" {
		t.Errorf("%+v", s)
	}
	for name, mod := range map[string]func(string) string{
		"unknown field":   func(s string) string { return strings.Replace(s, `"init"`, `"extra":1,"init"`, 1) },
		"wrong format":    func(s string) string { return strings.Replace(s, "fit/1", "fit/2", 1) },
		"bad digest":      func(s string) string { return strings.Replace(s, `"keys_sha256":"`, `"keys_sha256":"zz`, 1) },
		"short digest":    func(s string) string { return strings.Replace(s, `"keys_sha256":"`, `"keys_sha256":"00`, 1) },
		"unknown algo":    func(s string) string { return strings.Replace(s, "emeans_cpu", "kmeans", 1) },
		"k over n":        func(s string) string { return strings.Replace(s, `"k":3`, `"k":11`, 1) },
		"k zero":          func(s string) string { return strings.Replace(s, `"k":3`, `"k":0`, 1) },
		"absolute path":   func(s string) string { return strings.Replace(s, "a.assign", "/etc/passwd", 1) },
		"parent path":     func(s string) string { return strings.Replace(s, "a.cent", "../a.cent", 1) },
		"negative mse":    func(s string) string { return strings.Replace(s, `"mse_f64":0.15`, `"mse_f64":-1`, 1) },
		"no runs":         func(s string) string { return s[:strings.Index(s, `"runs":`)] + `"runs":[]}` },
		"n over bound":    func(s string) string { return strings.Replace(s, `"n":10`, `"n":4000000`, 1) },
		"dim zero":        func(s string) string { return strings.Replace(s, `"dim":4`, `"dim":0`, 1) },
		"two values":      func(s string) string { return s + s },
		"truncated":       func(s string) string { return s[:len(s)/2] },
		"not json":        func(string) string { return "nope" },
		"empty":           func(string) string { return "" },
		"trailing object": func(s string) string { return s + "{}" },
	} {
		if _, err := ReadFitSummary(writeSummary(t, fitSummaryJSON(mod))); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
	}
	if _, err := ReadFitSummary(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestLoadRunChecksTheAssignmentsAgainstTheStudy(t *testing.T) {
	dir := t.TempDir()
	sum := KeysSHA256([]string{"a:1", "a:2", "a:3"})
	if err := os.WriteFile(filepath.Join(dir, "a.assign"), EncodeAssignments(sum, 2, []uint32{0, 1, 1}), 0o600); err != nil {
		t.Fatal(err)
	}
	sp := filepath.Join(dir, "summary.json")
	run := FitRun{Assign: "a.assign", K: 2}
	if a, err := LoadRun(sp, run, sum, 3); err != nil || a.K != 2 {
		t.Fatalf("good run: %+v, %v", a, err)
	}
	other := KeysSHA256([]string{"b:1", "b:2", "b:3"})
	for name, tc := range map[string]struct {
		run FitRun
		sum [32]byte
		n   int
	}{
		"other keys file": {run, other, 3},
		"other row count": {run, sum, 4},
		"other k":         {FitRun{Assign: "a.assign", K: 3}, sum, 3},
	} {
		if _, err := LoadRun(sp, tc.run, tc.sum, tc.n); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
	}
	if _, err := LoadRun(sp, FitRun{Assign: "gone.assign", K: 2}, sum, 3); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing assignments: %v", err)
	}
}

func TestSpreadAndDecide(t *testing.T) {
	nan := Spread("x", 4, []float64{math.NaN(), math.NaN()})
	if !math.IsNaN(float64(nan.Median)) {
		t.Errorf("all-NaN spread: %+v", nan)
	}
	s := Spread("emeans_cpu", 8, []float64{0.30, math.NaN(), 0.26, 0.34})
	if float64(s.Median) != 0.30 || float64(s.Min) != 0.26 || float64(s.Max) != 0.34 || len(s.Reductions) != 4 {
		t.Errorf("spread %+v", s)
	}
	em := Spread(AlgoEmeansCPU, 8, []float64{0.30, 0.28, 0.32})
	cases := []struct {
		name         string
		em, ll       SeedSpread
		adopt, match bool
		reason       string
	}{
		{"clears the bar, Lloyd level", em, Spread(AlgoLloyd, 8, []float64{0.29, 0.30}), true, true, "pure-Go Lloyd"},
		{"clears the bar, Lloyd behind", em, Spread(AlgoLloyd, 8, []float64{0.10, 0.12}), true, false, "earns its port"},
		{"misses the bar", Spread(AlgoEmeansCPU, 8, []float64{0.10, 0.12}), Spread(AlgoLloyd, 8, []float64{0.11}), false, true, "under the 25% bar"},
		{"no e-means", Spread(AlgoEmeansCPU, 8, nil), Spread(AlgoLloyd, 8, []float64{0.9}), false, false, "no e-means run"},
		{"no Lloyd", em, Spread(AlgoLloyd, 8, nil), true, false, "no Lloyd run"},
	}
	for _, tc := range cases {
		v := Decide(tc.em, tc.ll)
		if v.AdoptClusters != tc.adopt || v.LloydMatches != tc.match || !strings.Contains(v.Reason, tc.reason) {
			t.Errorf("%s: %+v", tc.name, v)
		}
	}
	// Exactly at the bar adopts; just under does not.
	if !Decide(Spread("e", 1, []float64{AdoptReduction}), Spread("l", 1, nil)).AdoptClusters {
		t.Error("a median at the bar should adopt")
	}
	if Decide(Spread("e", 1, []float64{AdoptReduction - 1e-9}), Spread("l", 1, nil)).AdoptClusters {
		t.Error("a median under the bar should not adopt")
	}
}
