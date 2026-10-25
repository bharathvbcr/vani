//go:build tessl && darwin && arm64

package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// dumpScores reads a `vani jobs list --json` dump and returns the semantic score
// each job holds, by key. Jobs without a semantic block are left out.
func dumpScores(t *testing.T, path string) map[string]float64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Jobs []struct {
			Key      string `json:"key"`
			Semantic *struct {
				Score float64 `json:"score"`
			} `json:"semantic"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := map[string]float64{}
	for _, j := range d.Jobs {
		if j.Semantic != nil {
			out[j.Key] = j.Semantic.Score
		}
	}
	return out
}

// percentile is the q-quantile (0..1) of xs by linear interpolation. xs is
// sorted in place; an empty xs is NaN, never 0.
func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	sort.Float64s(xs)
	pos := q * float64(len(xs)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(xs)-1)
	return xs[lo] + (xs[hi]-xs[lo])*(pos-float64(lo))
}

// TestTesslRealSavedCalibration re-scores the owner's saved postings with the
// current anchor and checks that the saved jobs spread across the range instead
// of piling up under 1.0. It reads the ledger without a lock and writes only to a
// temp dir. Output is counts, quantiles and calibration numbers, never bank,
// config or posting text.
//
//	SEMANTIC_REAL_LEDGER=<ledger dir> SEMANTIC_REAL_CONFIG=<config.json>
//	SEMANTIC_REAL_SAVED=<jobs list --view saved --json dump> KBSYNC_BANK=<bank.json>
//	VANI_TESSL_SNAPSHOT=<snapshot dir> go test -tags tessl ./internal/semantic -run SavedCalibration -v
//
// SEMANTIC_REAL_BASELINE (a `jobs list --json` dump of every job) adds the
// saved-minus-all median gap, which costs scoring those postings too, and
// SEMANTIC_REAL_LIMIT caps how many of them are scored. The saved rows must
// all be scored; too few non-thin ones to judge is a failure, not a pass.
func TestTesslRealSavedCalibration(t *testing.T) {
	dir, cfgPath, savedPath := os.Getenv("SEMANTIC_REAL_LEDGER"), os.Getenv("SEMANTIC_REAL_CONFIG"), os.Getenv("SEMANTIC_REAL_SAVED")
	if dir == "" || cfgPath == "" || savedPath == "" {
		t.Skip("set SEMANTIC_REAL_LEDGER, SEMANTIC_REAL_CONFIG, SEMANTIC_REAL_SAVED and KBSYNC_BANK")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Target string `json:"target_summary"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	saved := dumpScores(t, savedPath)
	var all map[string]float64
	if p := os.Getenv("SEMANTIC_REAL_BASELINE"); p != "" {
		all = dumpScores(t, p)
	}

	snap, err := ledger.ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]ledger.Record{}
	for _, r := range snap.Records {
		byKey[string(r.Key)] = r
	}
	docsFor := func(keys map[string]float64) []Doc {
		var docs []Doc
		for k := range keys {
			if r, ok := byKey[k]; ok && r.Status != posting.Dead {
				docs = append(docs, Doc{Key: k, Title: r.Posting.Title, Description: r.Posting.Description})
			}
		}
		sort.Slice(docs, func(i, j int) bool { return docs[i].Key < docs[j].Key })
		return docs
	}
	savedDocs := docsFor(saved)
	if len(savedDocs) == 0 {
		t.Fatalf("none of the %d saved rows is a live posting in the ledger", len(saved))
	}
	allDocs := docsFor(all)
	if n := os.Getenv("SEMANTIC_REAL_LIMIT"); n != "" {
		var lim int
		if _, err := fmt.Sscan(n, &lim); err == nil && lim > 0 && lim < len(allDocs) {
			allDocs = allDocs[:lim]
		}
	}

	e := realTessl(t)
	b, err := kbsync.Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcher(e, p, filepath.Join(t.TempDir(), "semantic"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	start := time.Now()
	docs := append(append([]Doc(nil), savedDocs...), allDocs...)
	rep, err := m.Warm(ctx, docs, len(docs))
	if err != nil {
		t.Fatalf("warm: %v (%+v)", err, rep)
	}
	t.Logf("scored %d postings in %v: %+v", len(docs), time.Since(start).Round(time.Second), rep)
	if rep.Failed > 0 || rep.Skipped > 0 {
		t.Fatalf("not every posting was scored (%d failed, %d skipped): the numbers below would describe a sample", rep.Failed, rep.Skipped)
	}
	cal, ok := m.Calibration()
	if !ok {
		t.Fatal("the profile has no calibration after a warm")
	}
	// Version 5 (top-third anchor, anchorDepth) measured 0.8070 on the owner's
	// 6-sentence target with the real model (2026-10-24, variant V5; Version 4's
	// all-sentence mean was 0.7579); floor 0.6554. The window is logged, not
	// asserted: it describes that one profile, and the assertions below judge the
	// distribution, which is what the calibration is for.
	t.Logf("calibration: floor %.4f, ceil' %.4f over %d anchor chunk(s) read at depth %d; V5 measured 0.807 on the owner's profile, window [0.78, 0.81] (inside it: %v)",
		cal.Floor, cal.Ceil, cal.AnchorChunks, anchorDepth(cal.AnchorChunks), cal.Ceil >= 0.78 && cal.Ceil <= 0.81)

	type row struct{ old, new float64 }
	var rows []row
	var newSaved []float64
	var ge95 int
	thin := 0
	for _, d := range savedDocs {
		mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
		if !ok {
			t.Fatalf("a saved posting has no score after the warm")
		}
		if mt.Thin {
			thin++
			continue
		}
		rows = append(rows, row{saved[d.Key], mt.Score})
		newSaved = append(newSaved, mt.Score)
		if mt.Score >= 0.95 {
			ge95++
		}
	}
	n := len(rows)
	t.Logf("saved: %d rows in the dump, %d live in the ledger, %d thin (left out), %d judged", len(saved), len(savedDocs), thin, n)
	if n < 10 {
		t.Fatalf("only %d non-thin saved rows: too few to judge a distribution", n)
	}
	oldSaved := make([]float64, n)
	for i, r := range rows {
		oldSaved[i] = r.old
	}
	share := float64(ge95) / float64(n)
	p50 := percentile(append([]float64(nil), newSaved...), 0.5)
	t.Logf("saved scores now: p25 %.3f p50 %.3f p75 %.3f; share >= 0.95: %.1f%% (%d of %d); was p50 %.3f",
		percentile(append([]float64(nil), newSaved...), 0.25), p50, percentile(append([]float64(nil), newSaved...), 0.75),
		100*share, ge95, n, percentile(oldSaved, 0.5))
	t.Logf("rank correlation (Spearman) of old and new saved scores: %.3f", spearman(rows, func(r row) (float64, float64) { return r.old, r.new }))

	if len(allDocs) > 0 {
		var allNew []float64
		for _, d := range allDocs {
			if mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description)); ok && !mt.Thin {
				allNew = append(allNew, mt.Score)
			}
		}
		allP50 := percentile(allNew, 0.5)
		t.Logf("all: %d non-thin of %d scored, p50 %.3f; saved-minus-all p50 gap %.3f", len(allNew), len(allDocs), allP50, p50-allP50)
	} else {
		t.Log("saved-minus-all gap not measured: set SEMANTIC_REAL_BASELINE to a `jobs list --json` dump")
	}

	if share > 0.25 {
		t.Errorf("%.1f%% of the saved postings still score >= 0.95 (limit 25%%): the ceiling is still too low", 100*share)
	}
	if !(p50 > 0.6 && p50 < 0.95) {
		t.Errorf("saved median %.3f is outside (0.6, 0.95)", p50)
	}
}
