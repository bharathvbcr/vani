package clusterstudy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Study is the postings of one export with the binary target (is Sonnet's
// role_fit reading adjacent, partial_fit or strong_fit) where there is a
// reading. A stratification covers only the postings it is defined for, and is
// measured on those postings that also have a target: Baseline.
type Study struct {
	Keys   []string
	Target []bool // meaningful where Has is true
	Has    []bool
}

// Baseline is a stratification measured on the postings that have both a
// target and a cell in it.
type Baseline struct {
	Name string
	// Rows indexes Study.Keys; Cell is each covered posting's cell number and
	// CellNames the name of cell i.
	Rows      []int
	Cell      []int
	CellNames []string
	Target    []bool // the target of each covered posting
	Positives int
}

// Baseline cuts the stratification cellOf (a posting's cell name; false for
// a posting it does not cover) over the postings that have a target.
func (s Study) Baseline(name string, cellOf func(i int) (string, bool)) (Baseline, error) {
	if len(s.Keys) == 0 || len(s.Has) != len(s.Keys) || len(s.Target) != len(s.Keys) {
		return Baseline{}, ErrInput
	}
	b := Baseline{Name: name}
	index := map[string]int{}
	for i := range s.Keys {
		if !s.Has[i] {
			continue
		}
		cell, ok := cellOf(i)
		if !ok {
			continue
		}
		id, seen := index[cell]
		if !seen {
			id = len(b.CellNames)
			index[cell] = id
			b.CellNames = append(b.CellNames, cell)
		}
		b.Rows = append(b.Rows, i)
		b.Cell = append(b.Cell, id)
		b.Target = append(b.Target, s.Target[i])
		if s.Target[i] {
			b.Positives++
		}
	}
	return b, nil
}

// N is the postings the baseline covers.
func (b Baseline) N() int { return len(b.Rows) }

// Cells is its number of non-empty cells.
func (b Baseline) Cells() int { return len(b.CellNames) }

// Within is the baseline's own within-cell variance.
func (b Baseline) Within() (Within, error) { return WithinVariance(b.Cell, b.Target) }

// RunResult is one fit's stratification measured against a baseline on the
// baseline's postings.
type RunResult struct {
	// ClusterCells is how many of the fit's clusters hold a posting the
	// baseline covers: the cell count to set beside Baseline.Cells().
	ClusterCells int      `json:"cluster_cells"`
	Compared     Compared `json:"compared"`
}

// CompareRun measures the fit's labels (one per Study key) against the
// baseline on the baseline's postings.
func (s Study) CompareRun(b Baseline, labels []uint32, reps int, seed uint64) (RunResult, error) {
	if len(labels) != len(s.Keys) {
		return RunResult{}, fmt.Errorf("%w: %d labels for %d keys", ErrInput, len(labels), len(s.Keys))
	}
	cand := make([]int, len(b.Rows))
	used := map[uint32]struct{}{}
	for i, r := range b.Rows {
		cand[i] = int(labels[r])
		used[labels[r]] = struct{}{}
	}
	c, err := Compare(b.Cell, cand, b.Target, reps, seed)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{ClusterCells: len(used), Compared: c}, nil
}

// FitSummaryFormat names the JSON the fit binary prints on stdout.
const FitSummaryFormat = "vani-cluster-fit/1"

// Algorithms a fit summary may name.
const (
	AlgoEmeansCPU   = "emeans_cpu"
	AlgoEmeansMetal = "emeans_metal"
	AlgoLloyd       = "lloyd"
)

// MaxRuns bounds the runs one summary may list.
const MaxRuns = 4096

// FitRun is one fit the binary made: algorithm, k, seed, the files it wrote
// (names beside the summary), and what it measured.
type FitRun struct {
	Algo      string `json:"algo"`
	K         int    `json:"k"`
	Seed      uint64 `json:"seed"`
	Assign    string `json:"assign"`
	Centroids string `json:"centroids"`
	// Inertia is the sum over rows of the squared distance to the nearest
	// centroid and MSE that over n, both in f64 and computed by the binary
	// itself from the centroids, the same way for every algorithm.
	Inertia  float64 `json:"inertia_f64"`
	MSE      float64 `json:"mse_f64"`
	NonEmpty int     `json:"non_empty"`
	// LabelsMatchEngine says the engine's own labels equal the labels written
	// (the nearest centroid of the final centroids).
	LabelsMatchEngine     bool `json:"labels_match_engine"`
	EngineLabelMismatches int  `json:"engine_label_mismatches"`
	// RepeatIdentical says every timed round produced the same centroid bits as
	// the untimed first fit (the fit is deterministic on this machine).
	RepeatIdentical bool `json:"repeat_identical"`
	// FitMS is each timed round's fit time in milliseconds (after an untimed
	// warmup), in the order run.
	FitMS []float64 `json:"fit_ms"`
}

// MetalStatus says whether the Metal engine ran.
type MetalStatus struct {
	Status string `json:"status"` // "ok" or "not_run"
	Detail string `json:"detail,omitempty"`
}

// FitSummary is what the fit binary prints on stdout.
type FitSummary struct {
	Format     string      `json:"format"`
	Vectors    string      `json:"vectors"`
	N          int         `json:"n"`
	Dim        int         `json:"dim"`
	KeysSHA256 string      `json:"keys_sha256"`
	Spherical  bool        `json:"spherical"`
	Nepochs    float64     `json:"emeans_nepochs"`
	LloydIters int         `json:"lloyd_iters"`
	Rounds     int         `json:"timing_rounds"`
	Init       string      `json:"init"`
	Metal      MetalStatus `json:"metal"`
	Runs       []FitRun    `json:"runs"`
}

// ReadFitSummary reads and validates a summary: its format, shape, bounded
// run list, and every run's algorithm, k and file names. Unknown fields are an
// error, so a binary that has grown past this reader is noticed.
func ReadFitSummary(path string) (FitSummary, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FitSummary{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<20 {
		return FitSummary{}, formatErr("%s is not a regular file within 64 MiB", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return FitSummary{}, err
	}
	var s FitSummary
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return FitSummary{}, formatErr("%s: %v", path, err)
	}
	if dec.More() {
		return FitSummary{}, formatErr("%s: more than one JSON value", path)
	}
	if s.Format != FitSummaryFormat {
		return FitSummary{}, formatErr("%s: format %q, want %q", path, s.Format, FitSummaryFormat)
	}
	if s.N < 1 || s.N > MaxRows || s.Dim < 1 || s.Dim > MaxDim {
		return FitSummary{}, formatErr("%s: n %d dim %d out of bounds", path, s.N, s.Dim)
	}
	if len(s.Runs) == 0 || len(s.Runs) > MaxRuns {
		return FitSummary{}, formatErr("%s: %d runs (bound %d)", path, len(s.Runs), MaxRuns)
	}
	if _, err := s.KeysDigest(); err != nil {
		return FitSummary{}, err
	}
	for i, r := range s.Runs {
		switch r.Algo {
		case AlgoEmeansCPU, AlgoEmeansMetal, AlgoLloyd:
		default:
			return FitSummary{}, formatErr("%s: run %d has algorithm %q", path, i, r.Algo)
		}
		if r.K < 1 || r.K > s.N {
			return FitSummary{}, formatErr("%s: run %d has k %d for %d rows", path, i, r.K, s.N)
		}
		if !filepath.IsLocal(r.Assign) || !filepath.IsLocal(r.Centroids) {
			return FitSummary{}, formatErr("%s: run %d names a file outside the summary's directory", path, i)
		}
		if math.IsNaN(r.MSE) || math.IsInf(r.MSE, 0) || r.MSE < 0 {
			return FitSummary{}, formatErr("%s: run %d has mse %v", path, i, r.MSE)
		}
	}
	return s, nil
}

// KeysDigest is the keys digest the summary carries, decoded.
func (s FitSummary) KeysDigest() ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	b, err := hex.DecodeString(s.KeysSHA256)
	if err != nil || len(b) != sha256.Size {
		return out, formatErr("keys_sha256 %q is not a sha256", s.KeysSHA256)
	}
	copy(out[:], b)
	return out, nil
}

// LoadRun reads run i's assignments from beside the summary at path and checks
// them against the keys digest and row count the study was built from.
func LoadRun(summaryPath string, run FitRun, keysSHA [sha256.Size]byte, n int) (Assignments, error) {
	a, err := ReadAssignments(filepath.Join(filepath.Dir(summaryPath), run.Assign))
	if err != nil {
		return Assignments{}, err
	}
	switch {
	case a.KeysSHA != keysSHA:
		return Assignments{}, formatErr("%s: assignments were made for another keys file", run.Assign)
	case a.N != n:
		return Assignments{}, formatErr("%s: %d rows, the keys file has %d", run.Assign, a.N, n)
	case a.K != run.K:
		return Assignments{}, formatErr("%s: k %d, the summary says %d", run.Assign, a.K, run.K)
	}
	return a, nil
}

// SeedSpread is one algorithm's reductions at one k over its seeds.
type SeedSpread struct {
	Algo       string `json:"algo"`
	K          int    `json:"k"`
	Reductions []Num  `json:"reductions"` // per seed, in seed order
	// NullHi is each seed's permutation-null upper bound (Compared.Null.Hi), in
	// the same order as Reductions; empty when the caller did not supply it.
	NullHi []Num `json:"null_hi,omitempty"`
	Median Num   `json:"median"`
	Min    Num   `json:"min"`
	Max    Num   `json:"max"`
}

// WithNullBand returns s with each seed's permutation-null upper bound
// attached. A length that differs from the reductions' is kept as given: Decide
// reads it as an unavailable band, never as a pass.
func (s SeedSpread) WithNullBand(hi []float64) SeedSpread {
	s.NullHi = make([]Num, len(hi))
	for i, h := range hi {
		s.NullHi[i] = Num(h)
	}
	return s
}

// seeds counts the seeds with a defined reduction.
func (s SeedSpread) seeds() int {
	n := 0
	for _, r := range s.Reductions {
		if !math.IsNaN(float64(r)) && !math.IsInf(float64(r), 0) {
			n++
		}
	}
	return n
}

// clearsNull reports whether every seed's reduction is above its permutation
// null band, and says why not otherwise. A seed with an undefined reduction is
// not a seed here (seeds counts it out); a missing or undefined band is a
// failure, never a pass.
func (s SeedSpread) clearsNull() (bool, string) {
	if len(s.NullHi) != len(s.Reductions) {
		return false, "the permutation-null band was not supplied"
	}
	inside, judged := 0, 0
	for i, r := range s.Reductions {
		if math.IsNaN(float64(r)) || math.IsInf(float64(r), 0) {
			continue
		}
		judged++
		hi := float64(s.NullHi[i])
		if math.IsNaN(hi) || !(float64(r) > hi) {
			inside++
		}
	}
	if inside > 0 {
		return false, fmt.Sprintf("the reduction does not clear the permutation-null band on %d of %d seeds", inside, judged)
	}
	return true, ""
}

// Spread summarises reductions (NaN entries are kept out of the statistics;
// all NaN gives NaN).
func Spread(algo string, k int, reductions []float64) SeedSpread {
	s := SeedSpread{Algo: algo, K: k, Reductions: make([]Num, len(reductions))}
	for i, r := range reductions {
		s.Reductions[i] = Num(r)
	}
	f := finiteSorted(reductions)
	if len(f) == 0 {
		s.Median, s.Min, s.Max = Num(math.NaN()), Num(math.NaN()), Num(math.NaN())
		return s
	}
	s.Median, s.Min, s.Max = Num(percentile(f, 0.5)), Num(f[0]), Num(f[len(f)-1])
	return s
}

// AdoptReduction is the plan's bar (the Fable plan of 2026-11-12, decision 5): clusters
// replace the category strata for the audit only if the within-stratum
// variance falls by at least this share.
const AdoptReduction = 0.25

// AdoptMinSeeds is how many seeds with a defined reduction the adopt verdict
// needs: one seed cannot tell a real reduction from a lucky fit.
const AdoptMinSeeds = 2

// Verdict is the plan's decision rule applied to the measured spreads.
type Verdict struct {
	// Emeans and Lloyd are the reductions against the baseline at the baseline's
	// own cell count.
	Emeans SeedSpread `json:"emeans"`
	Lloyd  SeedSpread `json:"lloyd"`
	// Seeds is how many e-means seeds had a defined reduction.
	Seeds int `json:"seeds"`
	// AdoptClusters: the e-means median reduction reaches AdoptReduction, on at
	// least AdoptMinSeeds seeds, and every seed's reduction is above its
	// permutation-null band. Failed names each condition that did not hold.
	AdoptClusters bool     `json:"adopt_clusters"`
	Failed        []string `json:"failed_conditions,omitempty"`
	// LloydMatches: the Lloyd median is no lower than the e-means median less
	// the e-means spread across seeds (and at least 0.01). When clusters are
	// adopted and Lloyd matches, a pure-Go Lloyd is what to build.
	LloydMatches bool   `json:"lloyd_matches"`
	Reason       string `json:"reason"`
}

// Decide applies the rule. A missing arm (no e-means runs, or no Lloyd runs,
// at that k) cannot be judged: the verdict says so and adopts nothing for a
// missing e-means arm.
func Decide(emeans, lloyd SeedSpread) Verdict {
	v := Verdict{Emeans: emeans, Lloyd: lloyd, Seeds: emeans.seeds()}
	if math.IsNaN(float64(emeans.Median)) {
		v.Reason = "no e-means run at this cell count: not judged"
		return v
	}
	if float64(emeans.Median) < AdoptReduction {
		v.Failed = append(v.Failed, fmt.Sprintf("e-means median reduction %.1f%% is under the %.0f%% bar", 100*float64(emeans.Median), 100*AdoptReduction))
	}
	if v.Seeds < AdoptMinSeeds {
		v.Failed = append(v.Failed, fmt.Sprintf("%d seed(s) with a defined reduction, at least %d needed", v.Seeds, AdoptMinSeeds))
	}
	if ok, why := emeans.clearsNull(); !ok {
		v.Failed = append(v.Failed, why)
	}
	v.AdoptClusters = len(v.Failed) == 0
	if math.IsNaN(float64(lloyd.Median)) {
		v.Reason = adoptReason(v, "no Lloyd run at this cell count: Lloyd match not judged")
		return v
	}
	tol := math.Max(0.01, float64(emeans.Max-emeans.Min))
	v.LloydMatches = float64(lloyd.Median) >= float64(emeans.Median)-tol
	switch {
	case !v.AdoptClusters:
		v.Reason = adoptReason(v, "")
	case v.LloydMatches:
		v.Reason = "adopt clusters; Lloyd matches e-means, so build a pure-Go Lloyd"
	default:
		v.Reason = "adopt clusters; Lloyd is behind e-means, so e-means earns its port"
	}
	return v
}

// adoptReason words the adopt verdict: which conditions failed, or adopt,
// then an optional note.
func adoptReason(v Verdict, note string) string {
	r := "adopt clusters"
	if !v.AdoptClusters {
		r = "not adopted: " + strings.Join(v.Failed, "; ")
	}
	if note != "" {
		r += "; " + note
	}
	return r
}
