package semantic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The calibration fixture: labelled pairs of job-form labels. The default build
// checks its shape; the tessl build measures the real model on it
// (similar_real_test.go).
const labelPairsPath = "../../extension/test/vectors/label-pairs.json"

type labelPair struct {
	A    string `json:"a"`
	B    string `json:"b"`
	Same bool   `json:"same"`
	Kind string `json:"kind"`
}

func loadLabelPairs(t testing.TB) []labelPair {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(labelPairsPath))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		About   string      `json:"about"`
		Version int         `json:"version"`
		Pairs   []labelPair `json:"pairs"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("label-pairs.json: %v", err)
	}
	if f.Version != 1 {
		t.Fatalf("label-pairs.json has version %d", f.Version)
	}
	return f.Pairs
}

// isHoldout puts about a third of the pairs aside, by a hash of their text so
// that the split does not move when the file is reordered or extended.
func isHoldout(p labelPair) bool {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s\x00%s", p.A, p.B)
	return h.Sum32()%3 == 0
}

func TestLabelPairsFixtureShape(t *testing.T) {
	pairs := loadLabelPairs(t)
	var same, diff, hard, held, heldNeg int
	seen := map[string]bool{}
	kinds := map[string]bool{"reword": true, "exact-ish": true, "hard-negative": true, "unrelated": true}
	for i, p := range pairs {
		if strings.TrimSpace(p.A) == "" || strings.TrimSpace(p.B) == "" || p.A == p.B {
			t.Errorf("pair %d: blank or identical wordings %q %q", i, p.A, p.B)
		}
		if len(p.A) > MaxSimilarTextBytes || len(p.B) > MaxSimilarTextBytes {
			t.Errorf("pair %d: wording over %d bytes", i, MaxSimilarTextBytes)
		}
		if !kinds[p.Kind] {
			t.Errorf("pair %d: unknown kind %q", i, p.Kind)
		}
		if p.Same != (p.Kind == "reword" || p.Kind == "exact-ish") {
			t.Errorf("pair %d (%q, %q): kind %q contradicts same=%v", i, p.A, p.B, p.Kind, p.Same)
		}
		key := p.A + "\x00" + p.B
		rev := p.B + "\x00" + p.A
		if seen[key] || seen[rev] {
			t.Errorf("pair %d (%q, %q) repeats an earlier pair", i, p.A, p.B)
		}
		seen[key] = true
		if p.Same {
			same++
		} else {
			diff++
		}
		if p.Kind == "hard-negative" {
			hard++
		}
		if isHoldout(p) {
			held++
			if !p.Same {
				heldNeg++
			}
		}
	}
	if len(pairs) < 80 {
		t.Errorf("%d pairs, want at least 80", len(pairs))
	}
	if same < 30 || diff < 30 || hard < 20 {
		t.Errorf("same=%d different=%d hard negatives=%d: too few to calibrate on", same, diff, hard)
	}
	if held < 15 || heldNeg < 8 {
		t.Errorf("holdout has %d pairs, %d of them different questions", held, heldNeg)
	}
	t.Logf("%d pairs: %d same, %d different (%d hard negatives); holdout %d (%d different)", len(pairs), same, diff, hard, held, heldNeg)
}

// curvePoint is the pair classifier at one cutoff.
type curvePoint struct {
	Cut                  float64
	TP, FP, FN           int
	Precision, Recall    float64
	FalseMergesPerNegPct float64
}

// calibration summarizes labelled cosines.
type calibration struct {
	MaxNeg, MinPos     float64 // highest different-question cosine; lowest same-question cosine
	ZeroFPCut          float64 // the least cutoff with no false merge
	ZeroFPRecall       float64 // same-question pairs at or above ZeroFPCut
	Points             []curvePoint
	NegCount, PosCount int
}

// calibrate scores cos[i] against pairs[i].Same. A cutoff keeps a pair when
// cos >= cutoff.
func calibrate(pairs []labelPair, cos []float64, step float64) calibration {
	c := calibration{MaxNeg: math.Inf(-1), MinPos: math.Inf(1)}
	for i, p := range pairs {
		if p.Same {
			c.PosCount++
			c.MinPos = math.Min(c.MinPos, cos[i])
		} else {
			c.NegCount++
			c.MaxNeg = math.Max(c.MaxNeg, cos[i])
		}
	}
	c.ZeroFPCut = math.Nextafter(c.MaxNeg, 2)
	at := func(cut float64) curvePoint {
		pt := curvePoint{Cut: cut}
		for i, p := range pairs {
			switch {
			case cos[i] >= cut && p.Same:
				pt.TP++
			case cos[i] >= cut && !p.Same:
				pt.FP++
			case cos[i] < cut && p.Same:
				pt.FN++
			}
		}
		if pt.TP+pt.FP > 0 {
			pt.Precision = float64(pt.TP) / float64(pt.TP+pt.FP)
		}
		if c.PosCount > 0 {
			pt.Recall = float64(pt.TP) / float64(c.PosCount)
		}
		if c.NegCount > 0 {
			pt.FalseMergesPerNegPct = 100 * float64(pt.FP) / float64(c.NegCount)
		}
		return pt
	}
	c.ZeroFPRecall = at(c.ZeroFPCut).Recall
	lo := math.Floor(math.Min(c.MinPos, c.MaxNeg)/step) * step
	for cut := lo; cut <= 1+step; cut += step {
		c.Points = append(c.Points, at(math.Round(cut/step)*step))
	}
	sort.Slice(c.Points, func(i, j int) bool { return c.Points[i].Cut < c.Points[j].Cut })
	return c
}

func TestCalibrateOnKnownScores(t *testing.T) {
	pairs := []labelPair{{Same: true}, {Same: true}, {Same: true}, {Same: false}, {Same: false}}
	cos := []float64{0.95, 0.90, 0.80, 0.85, 0.70}
	c := calibrate(pairs, cos, 0.05)
	if c.MaxNeg != 0.85 || c.MinPos != 0.80 || c.PosCount != 3 || c.NegCount != 2 {
		t.Fatalf("%+v", c)
	}
	if !(c.ZeroFPCut > 0.85 && c.ZeroFPCut < 0.86) || math.Abs(c.ZeroFPRecall-2.0/3) > 1e-9 {
		t.Errorf("zero-FP cut %v recall %v", c.ZeroFPCut, c.ZeroFPRecall)
	}
	for _, pt := range c.Points {
		if math.Abs(pt.Cut-0.80) < 1e-9 && (pt.TP != 3 || pt.FP != 1 || pt.Precision != 0.75 || pt.Recall != 1) {
			t.Errorf("at 0.80: %+v", pt)
		}
	}
}
