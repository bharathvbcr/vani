package rankfit

import (
	"math"
	"testing"
)

func TestPairedAUCDiffIdenticalArmsIsZero(t *testing.T) {
	pos := []float64{0.9, 0.8, 0.4, 0.7, 0.6}
	neg := []float64{0.1, 0.5, 0.3, 0.2, 0.65, 0.35}
	d, lo, hi, ok := PairedAUCDiff(pos, pos, neg, neg, 0.05, 500, 1)
	if !ok || d != 0 || lo != 0 || hi != 0 {
		t.Fatalf("identical arms: diff %v [%v, %v] ok %v, want exactly 0", d, lo, hi, ok)
	}
}

// A shift that moves every wanted posting above every unwanted one is a clear
// gain: the paired interval excludes 0, while two unpaired intervals of these
// sizes would overlap.
func TestPairedAUCDiffDetectsPairedGain(t *testing.T) {
	posA := []float64{0.30, 0.55, 0.42, 0.61, 0.38, 0.47, 0.52, 0.44}
	negA := []float64{0.40, 0.35, 0.50, 0.45, 0.33, 0.58, 0.41, 0.36, 0.49}
	posB := make([]float64, len(posA))
	for i, v := range posA {
		posB[i] = v + 0.5
	}
	d, lo, hi, ok := PairedAUCDiff(posA, posB, negA, negA, 0.05, 2000, 3)
	if !ok {
		t.Fatal("not ok")
	}
	if !(d > 0 && lo > 0 && hi >= lo) {
		t.Fatalf("diff %v [%v, %v]: want a gain whose interval excludes 0", d, lo, hi)
	}
	// Reversed arms give the mirror image.
	d2, lo2, hi2, _ := PairedAUCDiff(posB, posA, negA, negA, 0.05, 2000, 3)
	if math.Abs(d+d2) > 1e-12 || !(hi2 < 0) || lo2 > hi2 {
		t.Fatalf("reversed: diff %v [%v, %v], want the mirror of %v", d2, lo2, hi2, d)
	}
}

func TestPairedAUCDiffRefusesBadInput(t *testing.T) {
	three := []float64{1, 2, 3}
	two := []float64{1, 2}
	cases := map[string][4][]float64{
		"length mismatch pos": {three, two, three, three},
		"length mismatch neg": {three, three, three, two},
		"too few":             {two, two, three, three},
		"empty":               {nil, nil, nil, nil},
		"NaN":                 {{1, 2, math.NaN()}, three, three, three},
	}
	for name, c := range cases {
		if _, _, _, ok := PairedAUCDiff(c[0], c[1], c[2], c[3], 0.05, 100, 1); ok {
			t.Errorf("%s: ok, want refused", name)
		}
	}
	big := make([]float64, MaxPartAUCItems+1)
	if _, _, _, ok := PairedAUCDiff(big, big, three, three, 0.05, 100, 1); ok {
		t.Error("oversized class: ok, want refused")
	}
}

func TestPairedAUCDiffClampsKnobs(t *testing.T) {
	pos := []float64{0.9, 0.8, 0.7}
	neg := []float64{0.1, 0.2, 0.3}
	// Bad alpha and resamples fall back to the defaults instead of panicking.
	for _, a := range []float64{-1, 0, 0.5, 2, math.NaN()} {
		for _, n := range []int{-5, 0, MaxPairedResamples + 10} {
			if _, lo, hi, ok := PairedAUCDiff(pos, pos, neg, neg, a, n, 1); !ok || lo > hi {
				t.Fatalf("alpha %v resamples %d: ok %v [%v, %v]", a, n, ok, lo, hi)
			}
		}
	}
}

func TestPairedAUCDiffDeterministic(t *testing.T) {
	posA := []float64{0.3, 0.5, 0.7, 0.2}
	posB := []float64{0.4, 0.5, 0.9, 0.1}
	neg := []float64{0.35, 0.6, 0.25, 0.45}
	_, lo1, hi1, _ := PairedAUCDiff(posA, posB, neg, neg, 0.05, 1000, 9)
	_, lo2, hi2, _ := PairedAUCDiff(posA, posB, neg, neg, 0.05, 1000, 9)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("same seed gave [%v, %v] then [%v, %v]", lo1, hi1, lo2, hi2)
	}
}
