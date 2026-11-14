package semantic

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// kendallTauB is Kendall's tau-b of two paired samples (ties in either are
// corrected for). It is NaN when either sample is constant or they differ in
// length: no rank correlation exists, which is not a perfect one.
func kendallTauB(a, b []float64) float64 {
	n := len(a)
	if n != len(b) || n < 2 {
		return math.NaN()
	}
	var conc, disc, tiedA, tiedB float64 // tiedA counts pairs tied in a (both included)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			da, db := a[i]-a[j], b[i]-b[j]
			if da == 0 {
				tiedA++
			}
			if db == 0 {
				tiedB++
			}
			switch {
			case da == 0 || db == 0:
			case (da > 0) == (db > 0):
				conc++
			default:
				disc++
			}
		}
	}
	pairs := float64(n) * float64(n-1) / 2
	if pairs-tiedA <= 0 || pairs-tiedB <= 0 {
		return math.NaN()
	}
	return (conc - disc) / math.Sqrt((pairs-tiedA)*(pairs-tiedB))
}

func TestKendallTauB(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-12 }
	if got := kendallTauB([]float64{1, 2, 3, 4}, []float64{10, 20, 30, 40}); !near(got, 1) {
		t.Errorf("same order: %v", got)
	}
	if got := kendallTauB([]float64{1, 2, 3, 4}, []float64{4, 3, 2, 1}); !near(got, -1) {
		t.Errorf("reversed: %v", got)
	}
	// One swapped pair of six: 14 concordant and 1 discordant of 15 pairs.
	if got := kendallTauB([]float64{1, 2, 3, 4, 5, 6}, []float64{1, 2, 3, 4, 6, 5}); !near(got, 13.0/15) {
		t.Errorf("one swap: %v", got)
	}
	// Ties in both samples at the same place do not lower a perfect agreement.
	if got := kendallTauB([]float64{0, 0, 1, 2}, []float64{5, 5, 6, 7}); !near(got, 1) {
		t.Errorf("joint ties: %v", got)
	}
	for name, c := range map[string][2][]float64{
		"constant": {{1, 1, 1}, {1, 2, 3}},
		"short":    {{1}, {1}},
		"empty":    {nil, nil},
		"unequal":  {{1, 2}, {1}},
	} {
		if got := kendallTauB(c[0], c[1]); !math.IsNaN(got) {
			t.Errorf("%s: %v, want NaN (no correlation is not a correlation of 0 or 1)", name, got)
		}
	}
}

// int8RoundTrip is the vector after symmetric int8 quantization with one f32
// scale (scale = max|x|/127, q = round(x/scale)) and back. It is the measured
// candidate for a smaller chunk-vector log, not something the log stores.
func int8RoundTrip(v []float32) ([]float32, error) {
	var m float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite value")
		}
		m = max(m, math.Abs(f))
	}
	scale := float32(m / 127)
	if m == 0 || scale < 1.1754944e-38 { // zero, or below the smallest normal float32
		return nil, fmt.Errorf("zero or underflowing vector")
	}
	out := make([]float32, len(v))
	for i, x := range v {
		q := max(-127, min(127, math.Round(float64(x)/float64(scale))))
		out[i] = float32(q) * scale
	}
	return out, nil
}

func TestInt8RoundTrip(t *testing.T) {
	v := []float32{0.3, -0.7, 0.123456789, 1e-7, 0.5, 0.25, -0.11, 0.9}
	got, err := int8RoundTrip(v)
	if err != nil {
		t.Fatal(err)
	}
	// Each value is within half a step (scale/2 = max/254) of the original.
	for i, x := range v {
		if d := math.Abs(float64(got[i] - x)); d > 0.9/254*1.0001 {
			t.Errorf("component %d: %v -> %v, off by %v", i, x, got[i], d)
		}
	}
	if got[7] != 0.9 {
		t.Errorf("the largest magnitude should land on 127*scale = %v, got %v", float32(127)*float32(0.9/127), got[7])
	}
	for name, bad := range map[string][]float32{
		"zero":      {0, 0, 0},
		"nan":       {1, float32(math.NaN())},
		"inf":       {1, float32(math.Inf(1))},
		"neg inf":   {float32(math.Inf(-1))},
		"underflow": {1e-39, 0},
		"smallest":  {math.SmallestNonzeroFloat32},
		"empty":     {},
	} {
		if _, err := int8RoundTrip(bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// A constant vector is fine: every value lands on +-127 steps.
	if got, err := int8RoundTrip([]float32{2, 2, 2, 2}); err != nil || got[0] != got[3] {
		t.Errorf("a constant vector must quantize: %v %v", got, err)
	}
}

// quantParity is one stored posting scored from its f32 vectors (as embedded)
// and again from the same vectors after the int8 round trip.
type quantParity struct {
	f32, q8       float64 // calibrated scores
	thin32, thin8 bool
	ev32, ev8     string // the top evidence chunk (label and snippet)
	pairs         int    // in-posting chunk pairs at or above nearDupCos in f32
	flips         int    // pairs whose side of nearDupCos the round trip changed
}

// TestRealQuantizedVectorParity measures how far an int8 chunk-vector log would
// move the scores of the owner's real stored f32 vectors: the gate for a smaller
// log. It reads <SEMANTIC_REAL_STATE> (a state directory: semantic/vectors.log,
// semantic/semantic-profile.json for the embedded profile and its calibration,
// ledger/ for the posting text that gives each chunk its weight) read-only: it
// takes no lock and writes nothing there. It needs no model. Output is counts and
// differences, never posting text.
//
//	SEMANTIC_REAL_STATE=<state dir> go test ./internal/semantic -run RealQuantizedVectorParity -v
//
// The bar was: max calibrated score difference <= 0.005, Kendall tau >= 0.995
// over the postings, no posting changes Thin, the top evidence chunk the same
// for >= 99% of postings. The log stays f32 because the real vectors miss it
// (2026-11-14: 7,604 postings, max difference 0.0545, tau 0.9931, evidence
// identical for 91.1%). The test logs each figure and whether it met the bar,
// and fails only when it cannot measure.
func TestRealQuantizedVectorParity(t *testing.T) {
	state := os.Getenv("SEMANTIC_REAL_STATE")
	if state == "" {
		t.Skip("set SEMANTIC_REAL_STATE to a state directory holding semantic/vectors.log")
	}
	semDir := filepath.Join(state, "semantic")
	f, err := os.Open(filepath.Join(semDir, vectorLogFileName)) // read-only: Matcher would truncate or rewrite it
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := newVectorReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if r.torn > 0 {
		t.Logf("note: %d torn trailing bytes ignored", r.torn)
	}

	var pf profileFile
	if missing, err := readCache(filepath.Join(semDir, profileFileName), &pf); err != nil || missing {
		t.Fatalf("profile cache: missing=%v err=%v", missing, err)
	}
	if pf.Version != CacheVersion || pf.Model != r.model || pf.Dim != r.s.dim || len(pf.Vectors) == 0 || !finite(pf.Floor, pf.Ceil) || pf.Ceil-pf.Floor < minSpread {
		t.Fatalf("the profile cache does not match the vector log (version %d, dim %d vs %d, model match %v, %d vectors)",
			pf.Version, pf.Dim, r.s.dim, pf.Model == r.model, len(pf.Vectors))
	}
	pd := &profileData{dim: pf.Dim, floor: pf.Floor, ceil: pf.Ceil}
	labels := make([]Chunk, len(pf.Vectors))
	for i, s := range pf.Vectors {
		v, err := decodeVec(s, pf.Dim)
		if err != nil {
			t.Fatalf("profile vector %d: %v", i, err)
		}
		pd.vecs = append(pd.vecs, v)
		labels[i].Label = fmt.Sprintf("p%d", i)
	}
	m := &Matcher{p: Profile{Chunks: labels}}

	snap, err := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	type job struct {
		h      [16]byte
		chunks []Chunk
	}
	var jobs []job
	seen := map[[16]byte]bool{}
	var noRecord, stale int
	for _, rec := range snap.Records {
		chunks := ChunkPosting(rec.Posting.Title, rec.Posting.Description)
		h, ok := parseTextHash(TextHash(rec.Posting.Title, rec.Posting.Description))
		if len(chunks) == 0 || !ok || seen[h] {
			continue
		}
		seen[h] = true
		loc, ok := r.s.idx[h]
		switch {
		case !ok:
			noRecord++
		case int(loc.n) != len(chunks):
			stale++
		default:
			jobs = append(jobs, job{h, chunks})
		}
	}
	snap = ledger.Snapshot{}
	t.Logf("log: %d records, dim %d, %d profile chunks; ledger postings joined: %d (no record %d, stale chunking %d)",
		r.Records(), r.Dim(), len(pd.vecs), len(jobs), noRecord, stale)
	if len(jobs) < 100 {
		t.Fatalf("only %d postings joined to a stored record: too few to judge parity", len(jobs))
	}

	res := make([]quantParity, len(jobs))
	var corrupt int
	var mu sync.Mutex
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < runtime.GOMAXPROCS(0); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				j := jobs[i]
				raw, rerr := r.s.read(j.h, len(j.chunks), pd.dim) // positional reads on the read-only handle
				var dq [][]float32
				for _, v := range raw {
					var q []float32
					if q, rerr = int8RoundTrip(v); rerr != nil {
						break
					}
					dq = append(dq, q)
				}
				if rerr != nil {
					mu.Lock()
					corrupt++
					mu.Unlock()
					continue
				}
				r32, e32, n32 := m.rawScoreN(j.chunks, raw, pd, topChunks)
				r8, e8, n8 := m.rawScoreN(j.chunks, dq, pd, topChunks)
				q := quantParity{f32: pd.score(r32, n32), q8: pd.score(r8, n8), thin32: n32 < minDistinctChunks, thin8: n8 < minDistinctChunks}
				q.ev32 = e32[0].Label + "\x00" + e32[0].Snippet
				q.ev8 = e8[0].Label + "\x00" + e8[0].Snippet
				for a := range raw {
					for c := a + 1; c < len(raw); c++ {
						d32, d8 := dot(raw[a], raw[c]) >= nearDupCos, dot(dq[a], dq[c]) >= nearDupCos
						if d32 {
							q.pairs++
						}
						if d32 != d8 {
							q.flips++
						}
					}
				}
				res[i] = q
			}
		}()
	}
	for i := range jobs {
		next <- i
	}
	close(next)
	wg.Wait()
	if corrupt > 0 {
		t.Fatalf("%d stored records could not be decoded or quantized", corrupt)
	}

	var diffs, s32, s8 []float64
	var thinFlips, evSame, pairs, flips, postingsWithFlip, over int
	for _, q := range res {
		d := math.Abs(q.f32 - q.q8)
		diffs = append(diffs, d)
		s32, s8 = append(s32, q.f32), append(s8, q.q8)
		if d > 0.005 {
			over++
		}
		if q.thin32 != q.thin8 {
			thinFlips++
		}
		if q.ev32 == q.ev8 {
			evSame++
		}
		pairs += q.pairs
		flips += q.flips
		if q.flips > 0 {
			postingsWithFlip++
		}
	}
	sort.Float64s(diffs)
	n := len(res)
	maxDiff := diffs[n-1]
	tau := kendallTauB(s32, s8)
	evShare := float64(evSame) / float64(n)
	t.Logf("n=%d postings: max |score diff| %.6f (p50 %.6f, p99 %.6f; %d over 0.005); Kendall tau-b %.6f; Thin flips %d; top evidence chunk identical %.2f%% (%d of %d); nearDupCos (%.2f) flips: %d of %d near-duplicate pairs, in %d postings",
		n, maxDiff, diffs[n/2], diffs[n*99/100], over, tau, thinFlips, 100*evShare, evSame, n, nearDupCos, flips, pairs, postingsWithFlip)
	met := maxDiff <= 0.005 && !math.IsNaN(tau) && tau >= 0.995 && thinFlips == 0 && evShare >= 0.99
	t.Logf("int8 meets the parity bar (diff <= 0.005, tau >= 0.995, no Thin flip, evidence >= 99%%): %v", met)
}
