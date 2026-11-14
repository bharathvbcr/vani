package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/clusterstudy"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

// The offline test of whether clustering posting embeddings gives better audit
// strata (docs/CLUSTER_STUDY.md). `export pooled-vectors` writes one vector per
// posting for a separate fit process; `strata compare` reads what the fit wrote
// and measures it. Neither changes the audit draw or writes a label.

const (
	pooledVectorsFile = "pooled.vectors"
	pooledKeysFile    = "pooled.keys"

	// sonnetReader is the teacher model whose stored jd_triage role_fit
	// reading is the target.
	sonnetReader = "claude-sonnet-5-5"

	defaultResamples = 1000
)

// fitReadings are the role_fit readings that count as a positive here:
// adjacent as well as the two eval.PositiveReadings, as the plan states it.
var fitReadings = map[string]bool{"adjacent": true, "partial_fit": true, "strong_fit": true}

// insideDir says whether path is dir or below it, after resolving both to
// absolute paths. Symlinks are not followed: this guards against a typo, not a
// hostile caller.
func insideDir(path, dir string) bool {
	p, err1 := filepath.Abs(path)
	d, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return true // cannot tell: refuse
	}
	rel, err := filepath.Rel(d, p)
	return err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// pooledExportReply is `export pooled-vectors --json`.
type pooledExportReply struct {
	Vectors    string `json:"vectors"`
	Keys       string `json:"keys"`
	KeysSHA256 string `json:"keys_sha256"`
	N          int    `json:"n"`
	Dim        int    `json:"dim"`
	Model      string `json:"model"`
	Pooling    string `json:"pooling"`
	// The funnel from the ledger to the file: each posting lands in exactly one
	// of Exported or the reasons it has no vector.
	Ledger          int `json:"ledger_records"`
	TextUnavailable int `json:"text_unavailable"` // the posting's text could not be read back from the ledger
	NoText          int `json:"no_text"`
	NoRecord        int `json:"no_vectors"`
	Stale           int `json:"stale_chunking"`
	Corrupt         int `json:"corrupt_record"`
	Degenerate      int `json:"degenerate_mean"`
	Exported        int `json:"exported"`
	// DistinctTexts is how many different texts the exported rows came from:
	// postings that share a text share a vector.
	DistinctTexts int   `json:"distinct_texts"`
	LogRecords    int   `json:"log_records"`
	LogTornBytes  int64 `json:"log_torn_bytes"`
}

// cmdExportPooledVectors is `vani export pooled-vectors --out DIR`: one pooled,
// L2-normalised vector per posting that has vectors in the semantic chunk-vector
// log. It reads the ledger and the log without taking a lock or writing
// anything in the state dir (no checkpoint is saved, no torn tail is cut), and
// refuses an --out inside the state dir.
func (e *env) cmdExportPooledVectors(args []string) error {
	fs := e.newFlags("export pooled-vectors")
	out := fs.String("out", "", "output directory for pooled.vectors and pooled.keys (required; not inside the state dir)")
	maxRows := fs.Int("max", clusterstudy.MaxRows, "refuse to write more than this many rows")
	asJSON := e.jsonFlag(fs, "the export's funnel and files as JSON on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	switch {
	case *out == "":
		return usagef("export pooled-vectors: --out is required")
	case *maxRows < 1 || *maxRows > clusterstudy.MaxRows:
		return usagef("export pooled-vectors: --max must be within 1..%d", clusterstudy.MaxRows)
	case insideDir(*out, e.stateDir):
		return usagef("export pooled-vectors: --out must not be inside the state dir (%s)", e.stateDir)
	}
	reply, err := e.exportPooledVectors(*out, *maxRows)
	return e.reply(*asJSON, reply, err, func() {
		fmt.Fprintf(e.stdout, "wrote %s and %s\n", reply.Vectors, reply.Keys)
		fmt.Fprintf(e.stdout, "%d postings x %d dims (model %s); keys sha256 %s\n", reply.N, reply.Dim, reply.Model, reply.KeysSHA256)
		fmt.Fprintf(e.stdout, "ledger %d records: exported %d, text unreadable %d, no text %d, no vectors %d, stale chunking %d, corrupt %d, degenerate %d\n",
			reply.Ledger, reply.Exported, reply.TextUnavailable, reply.NoText, reply.NoRecord, reply.Stale, reply.Corrupt, reply.Degenerate)
		fmt.Fprintf(e.stdout, "%d distinct texts; the log holds %d (torn tail %d bytes, left in place)\n", reply.DistinctTexts, reply.LogRecords, reply.LogTornBytes)
	})
}

func (e *env) exportPooledVectors(outDir string, maxRows int) (pooledExportReply, error) {
	var rep pooledExportReply
	vr, err := semantic.OpenVectorReader(semanticDir(e.stateDir))
	if err != nil {
		return rep, fmt.Errorf("export pooled-vectors: %w", err)
	}
	defer vr.Close()
	// A reader with no checkpoint save: SaveAfter 0 never saves, and the file is
	// only read if it is there.
	rd, err := ledger.OpenReader(filepath.Join(e.stateDir, "ledger"), ledger.ReaderOptions{Checkpoint: filepath.Join(e.stateDir, "readmodel", "ledger.ckpt")})
	if err != nil {
		return rep, fmt.Errorf("export pooled-vectors: read the ledger: %w", err)
	}
	defer rd.Close()
	snap := rd.Snapshot()
	rep.Ledger, rep.Model, rep.LogRecords, rep.LogTornBytes = len(snap.Records), vr.Model(), vr.Records(), vr.TornBytes()
	rep.Pooling = "mean of the posting's chunk vectors, then L2-normalised"

	var keys []string
	var rows [][]float32
	texts := map[string]struct{}{}
	// postingVectors reads a dropped, dead or duplicate record's description
	// back (it is cut out of the fold), through this reader.
	pv := e.newPostingVectors(vr, rd)
	for _, r := range snap.Records {
		v, cause := pv.of(r)
		switch cause {
		case vecTextUnreadable:
			rep.TextUnavailable++
		case vecNoText:
			rep.NoText++
		case vecNeverEmbedded:
			rep.NoRecord++
		case vecStaleChunking:
			rep.Stale++
		case vecCorrupt:
			rep.Corrupt++
		case vecDegenerate:
			rep.Degenerate++
		case "":
			if len(rows) >= maxRows {
				return rep, fmt.Errorf("export pooled-vectors: more than --max %d postings have vectors; raise --max (bound %d) rather than export a sample", maxRows, clusterstudy.MaxRows)
			}
			h, ok := pv.textHash(r)
			if !ok {
				return rep, fmt.Errorf("export pooled-vectors: %s: the text read back a moment ago could not be read again", r.Key)
			}
			keys = append(keys, string(r.Key))
			rows = append(rows, v)
			texts[h] = struct{}{}
		}
	}
	if len(rows) == 0 {
		return rep, errors.New("export pooled-vectors: no posting has vectors in the chunk-vector log; nothing to export")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return rep, fmt.Errorf("export pooled-vectors: %w", err)
	}
	rep.Vectors, rep.Keys = filepath.Join(outDir, pooledVectorsFile), filepath.Join(outDir, pooledKeysFile)
	if err := clusterstudy.WriteVectors(rep.Vectors, rep.Keys, keys, rows); err != nil {
		return rep, fmt.Errorf("export pooled-vectors: %w", err)
	}
	sum := clusterstudy.KeysSHA256(keys)
	rep.KeysSHA256, rep.N, rep.Dim, rep.Exported, rep.DistinctTexts = hex.EncodeToString(sum[:]), len(rows), len(rows[0]), len(rows), len(texts)
	return rep, nil
}

// cmdStrata dispatches `vani strata compare`.
func (e *env) cmdStrata(args []string) error {
	if len(args) == 0 || args[0] != "compare" {
		return usagef("strata: want `strata compare --keys FILE [--summary FILE]`")
	}
	return e.cmdStrataCompare(args[1:])
}

// baselineReport is one stratification the clusters are measured against.
type baselineReport struct {
	Name      string              `json:"name"`
	N         int                 `json:"n"`
	Cells     int                 `json:"cells"`
	Positives int                 `json:"positives"`
	Within    clusterstudy.Within `json:"within"`
	CellSizes []cellSize          `json:"cell_sizes"`
}

type cellSize struct {
	Cell string `json:"cell"`
	N    int    `json:"n"`
}

// fitRunReport is one fit as measured: its own figures and each baseline's result.
type fitRunReport struct {
	Algo        string  `json:"algo"`
	K           int     `json:"k"`
	Seed        uint64  `json:"seed"`
	MSE         float64 `json:"mse_f64"`
	NonEmpty    int     `json:"non_empty"`
	LabelsMatch bool    `json:"labels_match_engine"`
	// EngineLabelMismatches counts rows whose engine label differs from the
	// nearest centroid of the final centroids (the label used here).
	EngineLabelMismatches int              `json:"engine_label_mismatches"`
	RepeatIdentical       bool             `json:"repeat_identical"`
	FitMSMin              clusterstudy.Num `json:"fit_ms_min"`
	FitMSMedian           clusterstudy.Num `json:"fit_ms_median"`
	// Against maps a baseline name to the result. Both baselines are reported
	// for every run; EqualCells says the cluster cell count equals the
	// baseline's, which is the comparison the plan's rule is about.
	Against map[string]runAgainst `json:"against"`
}

type runAgainst struct {
	clusterstudy.RunResult
	EqualCells bool `json:"equal_cells"`
}

// groupReport is one algorithm at one k over its seeds, against one baseline.
type groupReport struct {
	Baseline      string                  `json:"baseline"`
	BaselineCells int                     `json:"baseline_cells"`
	Algo          string                  `json:"algo"`
	K             int                     `json:"k"`
	ClusterCells  []int                   `json:"cluster_cells"` // per seed
	EqualCells    bool                    `json:"equal_cells"`   // every seed
	Reductions    clusterstudy.SeedSpread `json:"reductions"`
	Pooled        clusterstudy.SeedSpread `json:"pooled_reductions"`
}

// strataReply is `strata compare --json`.
type strataReply struct {
	Model   string `json:"model"`
	Target  string `json:"target"`
	Keys    int    `json:"keys"`
	Funnel  funnel `json:"funnel"`
	NeededK []int  `json:"needed_k"`
	// Baselines are the category strata and the audit's current strata, each on
	// the postings it covers that also have a reading.
	Baselines []baselineReport `json:"baselines"`
	Fit       *fitReport       `json:"fit,omitempty"`
}

type funnel struct {
	Keys             int `json:"keys"`
	NotInLedger      int `json:"not_in_ledger"`
	WithReading      int `json:"with_reading"`
	WithCategory     int `json:"with_reading_and_category"`
	WithAuditStratum int `json:"with_reading_and_audit_stratum"`
	Positives        int `json:"positives_among_readings"`
}

// fitReport is what the fit summary adds.
type fitReport struct {
	N          int                      `json:"n"`
	Dim        int                      `json:"dim"`
	Spherical  bool                     `json:"spherical"`
	Nepochs    float64                  `json:"emeans_nepochs"`
	LloydIters int                      `json:"lloyd_iters"`
	Rounds     int                      `json:"timing_rounds"`
	Metal      clusterstudy.MetalStatus `json:"metal"`
	Runs       []fitRunReport           `json:"runs"`
	Groups     []groupReport            `json:"groups"`
	// Verdict is the plan's rule against the category strata, per baseline cell
	// count the category baseline has, for e-means CPU against Lloyd.
	Verdict *clusterstudy.Verdict `json:"verdict,omitempty"`
	// VerdictMetal is the same rule for the Metal e-means runs, when they ran.
	VerdictMetal *clusterstudy.Verdict `json:"verdict_metal,omitempty"`
	AdoptBar     float64               `json:"adopt_bar"`
}

const (
	baselineCategory = "jobcategory"
	baselineAudit    = "audit_strata"
)

// sweepK are cluster counts run besides the two baselines' cell counts, for
// the shape of the curve. They are not equal-cell comparisons.
var sweepK = []int{8, 16, 32, 64}

func (e *env) cmdStrataCompare(args []string) error {
	fs := e.newFlags("strata compare")
	keysPath := fs.String("keys", "", "the keys file `export pooled-vectors` wrote (required)")
	summary := fs.String("summary", "", "the fit binary's JSON summary; without it, only the baselines and the k to fit are reported")
	model := fs.String("model", sonnetReader, "teacher model whose stored jd_triage role_fit reading is the target")
	resamples := fs.Int("resamples", defaultResamples, "bootstrap and permutation resamples per comparison")
	seed := fs.Uint64("seed", 1, "seed for the resamples")
	asJSON := e.jsonFlag(fs, "the baselines, runs and verdict as JSON on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	switch {
	case *keysPath == "":
		return usagef("strata compare: --keys is required")
	case *resamples < 1 || *resamples > clusterstudy.MaxResamples:
		return usagef("strata compare: --resamples must be within 1..%d", clusterstudy.MaxResamples)
	case *model == "" || len(*model) > 200:
		return usagef("strata compare: --model must be a model name")
	}
	reply, err := e.strataCompare(*keysPath, *summary, *model, *resamples, *seed)
	return e.reply(*asJSON, reply, err, func() { fmt.Fprint(e.stdout, reply.markdown()) })
}

func (e *env) strataCompare(keysPath, summaryPath, model string, resamples int, seed uint64) (*strataReply, error) {
	keys, err := clusterstudy.ReadKeys(keysPath)
	if err != nil {
		return nil, fmt.Errorf("strata compare: keys: %w", err)
	}
	keysSHA := clusterstudy.KeysSHA256(keys)
	store, status, detail := e.openTeacherLog()
	if store == nil {
		return nil, fmt.Errorf("strata compare: the teacher readings log is not usable (%s %s); without Sonnet's readings there is no target", status, detail)
	}
	b, err := e.browse()
	if err != nil {
		return nil, fmt.Errorf("strata compare: %w", err)
	}
	cands, err := b.auditCandidates(e.dropRules(), true)
	if err != nil {
		return nil, fmt.Errorf("strata compare: %w", err)
	}
	audit := eval.AuditStratumOf(cands)

	reply := &strataReply{Model: model, Target: "role_fit in {adjacent, partial_fit, strong_fit}", Keys: len(keys)}
	s := clusterstudy.Study{Keys: keys, Target: make([]bool, len(keys)), Has: make([]bool, len(keys))}
	category := make([]string, len(keys))
	stratum := make([]string, len(keys))
	reply.Funnel.Keys = len(keys)
	for i, k := range keys {
		key := posting.Key(k)
		rec, ok := findRecord(b.snap, key)
		if !ok {
			reply.Funnel.NotInLedger++
			continue
		}
		if row, ok := store.LatestByModel("", key)[model]; ok {
			if sl, ok := row.Slots["role_fit"]; ok && sl.Reading == string(decide.Answered) && sl.Value != "" {
				s.Has[i], s.Target[i] = true, fitReadings[sl.Value]
				reply.Funnel.WithReading++
				if s.Target[i] {
					reply.Funnel.Positives++
				}
			}
		}
		category[i] = jobview.CategoryOf(rec)
		stratum[i] = audit[key]
	}
	if reply.Funnel.WithReading == 0 {
		return nil, fmt.Errorf("strata compare: none of the %d postings has a stored %s jd_triage role_fit reading", len(keys), model)
	}
	cat, err := s.Baseline(baselineCategory, func(i int) (string, bool) { return category[i], category[i] != "" })
	if err != nil {
		return nil, err
	}
	aud, err := s.Baseline(baselineAudit, func(i int) (string, bool) { return stratum[i], stratum[i] != "" })
	if err != nil {
		return nil, err
	}
	reply.Funnel.WithCategory, reply.Funnel.WithAuditStratum = cat.N(), aud.N()
	baselines := []clusterstudy.Baseline{cat, aud}
	for _, bl := range baselines {
		if bl.N() < 2 || bl.Cells() < 1 {
			return nil, fmt.Errorf("strata compare: the %s baseline covers %d postings with a reading; too few to measure", bl.Name, bl.N())
		}
		w, err := bl.Within()
		if err != nil {
			return nil, err
		}
		reply.Baselines = append(reply.Baselines, baselineReport{Name: bl.Name, N: bl.N(), Cells: bl.Cells(), Positives: bl.Positives, Within: w, CellSizes: cellSizes(bl)})
	}
	ks := map[int]bool{cat.Cells(): true, aud.Cells(): true}
	for _, k := range sweepK {
		ks[k] = true
	}
	for k := range ks {
		if k <= len(keys) {
			reply.NeededK = append(reply.NeededK, k)
		}
	}
	slices.Sort(reply.NeededK)
	if summaryPath == "" {
		return reply, nil
	}
	fit, err := e.compareFit(s, baselines, keysSHA, summaryPath, resamples, seed)
	if err != nil {
		return reply, err
	}
	reply.Fit = fit
	return reply, nil
}

func cellSizes(b clusterstudy.Baseline) []cellSize {
	n := make([]int, b.Cells())
	for _, c := range b.Cell {
		n[c]++
	}
	out := make([]cellSize, len(n))
	for i, c := range b.CellNames {
		out[i] = cellSize{c, n[i]}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Cell < out[j].Cell
	})
	return out
}

func (e *env) compareFit(s clusterstudy.Study, baselines []clusterstudy.Baseline, keysSHA [32]byte, summaryPath string, resamples int, seed uint64) (*fitReport, error) {
	sum, err := clusterstudy.ReadFitSummary(summaryPath)
	if err != nil {
		return nil, fmt.Errorf("strata compare: summary: %w", err)
	}
	want, _ := sum.KeysDigest()
	if want != keysSHA || sum.N != len(s.Keys) {
		return nil, fmt.Errorf("strata compare: the summary was made for another keys file (%d rows, sha256 %s); this one has %d rows, sha256 %s",
			sum.N, sum.KeysSHA256, len(s.Keys), hex.EncodeToString(keysSHA[:]))
	}
	fit := &fitReport{N: sum.N, Dim: sum.Dim, Spherical: sum.Spherical, Nepochs: sum.Nepochs, LloydIters: sum.LloydIters, Rounds: sum.Rounds,
		Metal: sum.Metal, AdoptBar: clusterstudy.AdoptReduction}
	type groupKey struct {
		baseline string
		algo     string
		k        int
	}
	type groupVals struct {
		reductions, pooled []float64
		cells              []int
		equal              bool
	}
	groups := map[groupKey]*groupVals{}
	var order []groupKey
	for _, run := range sum.Runs {
		a, err := clusterstudy.LoadRun(summaryPath, run, keysSHA, len(s.Keys))
		if err != nil {
			return nil, fmt.Errorf("strata compare: %w", err)
		}
		rr := fitRunReport{Algo: run.Algo, K: run.K, Seed: run.Seed, MSE: run.MSE, NonEmpty: run.NonEmpty, LabelsMatch: run.LabelsMatchEngine, EngineLabelMismatches: run.EngineLabelMismatches, RepeatIdentical: run.RepeatIdentical,
			Against: map[string]runAgainst{}}
		lo, med := minMedian(run.FitMS)
		rr.FitMSMin, rr.FitMSMedian = clusterstudy.Num(lo), clusterstudy.Num(med)
		for _, bl := range baselines {
			res, err := s.CompareRun(bl, a.Labels, resamples, seed)
			if err != nil {
				return nil, fmt.Errorf("strata compare: %s k=%d seed=%d against %s: %w", run.Algo, run.K, run.Seed, bl.Name, err)
			}
			eq := res.ClusterCells == bl.Cells()
			rr.Against[bl.Name] = runAgainst{res, eq}
			gk := groupKey{bl.Name, run.Algo, run.K}
			g := groups[gk]
			if g == nil {
				g = &groupVals{equal: true}
				groups[gk] = g
				order = append(order, gk)
			}
			g.reductions = append(g.reductions, float64(res.Compared.Reduction))
			g.pooled = append(g.pooled, float64(res.Compared.PooledReduction))
			g.cells = append(g.cells, res.ClusterCells)
			g.equal = g.equal && eq
		}
		fit.Runs = append(fit.Runs, rr)
	}
	cells := map[string]int{}
	for _, bl := range baselines {
		cells[bl.Name] = bl.Cells()
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.baseline != b.baseline {
			return a.baseline < b.baseline
		}
		if a.k != b.k {
			return a.k < b.k
		}
		return a.algo < b.algo
	})
	spreads := map[groupKey]clusterstudy.SeedSpread{}
	for _, gk := range order {
		g := groups[gk]
		sp := clusterstudy.Spread(gk.algo, gk.k, g.reductions)
		spreads[gk] = sp
		fit.Groups = append(fit.Groups, groupReport{Baseline: gk.baseline, BaselineCells: cells[gk.baseline], Algo: gk.algo, K: gk.k,
			ClusterCells: g.cells, EqualCells: g.equal, Reductions: sp, Pooled: clusterstudy.Spread(gk.algo, gk.k, g.pooled)})
	}
	// The plan's rule: against the category strata, at the category baseline's own cell count.
	k := cells[baselineCategory]
	verdict := func(emeans string) *clusterstudy.Verdict {
		em, okE := spreads[groupKey{baselineCategory, emeans, k}]
		ll, okL := spreads[groupKey{baselineCategory, clusterstudy.AlgoLloyd, k}]
		if !okE {
			return nil
		}
		if !okL {
			ll = clusterstudy.Spread(clusterstudy.AlgoLloyd, k, nil)
		}
		v := clusterstudy.Decide(em, ll)
		return &v
	}
	fit.Verdict = verdict(clusterstudy.AlgoEmeansCPU)
	fit.VerdictMetal = verdict(clusterstudy.AlgoEmeansMetal)
	if fit.Verdict == nil {
		return fit, fmt.Errorf("strata compare: the summary has no e-means CPU run at k=%d, the category baseline's cell count; the decision rule cannot be applied", k)
	}
	return fit, nil
}

func minMedian(v []float64) (lo, med float64) {
	if len(v) == 0 {
		return math.NaN(), math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	lo = s[0]
	if len(s)%2 == 1 {
		return lo, s[len(s)/2]
	}
	return lo, (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func percent(x clusterstudy.Num) string {
	if math.IsNaN(float64(x)) {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(x))
}

func (r *strataReply) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Strata comparison\n\ntarget: %s (%s, newest stored reading per posting)\n\n", r.Target, r.Model)
	f := r.Funnel
	fmt.Fprintf(&b, "funnel: %d exported postings; %d not in the ledger; %d with a reading (%d positive); %d of those with a category; %d with an audit stratum\n\n",
		f.Keys, f.NotInLedger, f.WithReading, f.Positives, f.WithCategory, f.WithAuditStratum)
	b.WriteString("baseline | n | cells | positives | total var | within var (plug-in) | within var (pooled)\n---|---|---|---|---|---|---\n")
	for _, bl := range r.Baselines {
		fmt.Fprintf(&b, "%s | %d | %d | %d | %.4f | %.4f | %.4f\n", bl.Name, bl.N, bl.Cells, bl.Positives, bl.Within.Total, bl.Within.Plugin, bl.Within.Pooled)
	}
	fmt.Fprintf(&b, "\nfit these k: %v\n", r.NeededK)
	if r.Fit == nil {
		return b.String()
	}
	ft := r.Fit
	fmt.Fprintf(&b, "\nfit: n=%d dim=%d spherical=%v e-means epochs=%g, Lloyd iterations=%d, timing rounds=%d, Metal: %s %s\n",
		ft.N, ft.Dim, ft.Spherical, ft.Nepochs, ft.LloydIters, ft.Rounds, ft.Metal.Status, ft.Metal.Detail)
	b.WriteString("\n## Variance reduction against each baseline (median over seeds [min, max])\n\n")
	b.WriteString("baseline | algo | k | cluster cells (per seed) vs baseline | equal cells | plug-in reduction | pooled reduction\n---|---|---|---|---|---|---\n")
	for _, g := range ft.Groups {
		fmt.Fprintf(&b, "%s | %s | %d | %v vs %d | %v | %s [%s, %s] | %s\n", g.Baseline, g.Algo, g.K, g.ClusterCells, g.BaselineCells, g.EqualCells,
			percent(g.Reductions.Median), percent(g.Reductions.Min), percent(g.Reductions.Max), percent(g.Pooled.Median))
	}
	b.WriteString("\n## Per run (bootstrap 95% interval of the reduction; permutation-null 95% band)\n\n")
	b.WriteString("algo | k | seed | baseline | reduction | bootstrap | null band | null >= observed | cells\n---|---|---|---|---|---|---|---|---\n")
	for _, run := range ft.Runs {
		for _, name := range []string{baselineCategory, baselineAudit} {
			a := run.Against[name]
			c := a.Compared
			fmt.Fprintf(&b, "%s | %d | %d | %s | %s | [%s, %s] | [%s, %s] | %.3f | %d/%d\n", run.Algo, run.K, run.Seed, name, percent(c.Reduction),
				percent(c.Bootstrap.Lo), percent(c.Bootstrap.Hi), percent(c.Null.Lo), percent(c.Null.Hi), c.NullAtOrAbove, a.ClusterCells, c.Base.Cells)
		}
	}
	b.WriteString("\n## Fits (same init and k per row across algorithms; f64 MSE computed by the fit binary the same way for each)\n\n")
	b.WriteString("algo | k | seed | mse (f64) | non-empty | engine label mismatches | repeat identical | fit ms min | fit ms median\n---|---|---|---|---|---|---|---|---\n")
	for _, run := range ft.Runs {
		fmt.Fprintf(&b, "%s | %d | %d | %.6f | %d | %d | %v | %.2f | %.2f\n", run.Algo, run.K, run.Seed, run.MSE, run.NonEmpty, run.EngineLabelMismatches, run.RepeatIdentical, run.FitMSMin, run.FitMSMedian)
	}
	for _, v := range []struct {
		name string
		v    *clusterstudy.Verdict
	}{{"e-means CPU", ft.Verdict}, {"e-means Metal", ft.VerdictMetal}} {
		if v.v == nil {
			continue
		}
		fmt.Fprintf(&b, "\n## Decision rule, %s against %s at its %d cells (bar %.0f%%)\n\n", v.name, baselineCategory, v.v.Emeans.K, 100*ft.AdoptBar)
		fmt.Fprintf(&b, "- e-means median %s [%s, %s]; Lloyd median %s [%s, %s]\n", percent(v.v.Emeans.Median), percent(v.v.Emeans.Min), percent(v.v.Emeans.Max), percent(v.v.Lloyd.Median), percent(v.v.Lloyd.Min), percent(v.v.Lloyd.Max))
		fmt.Fprintf(&b, "- adopt clusters: %v; Lloyd matches: %v; %s\n", v.v.AdoptClusters, v.v.LloydMatches, v.v.Reason)
	}
	return b.String()
}
