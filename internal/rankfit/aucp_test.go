package rankfit

import (
	"math"
	"math/rand/v2"
	"testing"
)

// bruteP is AUCPValue by enumeration: every way of choosing which len(pos) of
// the pooled values are the wanted ones, scored by the AUC itself (ties half).
func bruteP(pos, neg []float64) float64 {
	all := append(append([]float64(nil), pos...), neg...)
	n, k := len(all), len(pos)
	obs := auc(pos, neg)
	hit, total := 0, 0
	var rec func(start int, chosen []int)
	rec = func(start int, chosen []int) {
		if len(chosen) == k {
			in := make([]bool, n)
			for _, c := range chosen {
				in[c] = true
			}
			var p, q []float64
			for i, x := range all {
				if in[i] {
					p = append(p, x)
				} else {
					q = append(q, x)
				}
			}
			total++
			if auc(p, q) >= obs-1e-12 {
				hit++
			}
			return
		}
		for i := start; i < n; i++ {
			rec(i+1, append(chosen, i))
		}
	}
	rec(0, nil)
	return float64(hit) / float64(total)
}

// The exact count agrees with enumeration, ties included (the AUC is a
// rank-sum statistic, so the rank-sum count and the AUC order agree), with
// either class the smaller.
func TestAUCPValueMatchesEnumeration(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	for trial := range 200 {
		n1, n0 := 1+r.IntN(5), 1+r.IntN(7)
		pos, neg := make([]float64, n1), make([]float64, n0)
		for i := range pos {
			pos[i] = float64(r.IntN(4)) // few values: many ties
		}
		for i := range neg {
			neg[i] = float64(r.IntN(4))
		}
		p, exact := AUCPValue(pos, neg)
		if !exact {
			t.Fatalf("trial %d: small pool not exact", trial)
		}
		if want := bruteP(pos, neg); math.Abs(p-want) > 1e-9 {
			t.Fatalf("trial %d: pos %v neg %v: p %v, enumeration %v", trial, pos, neg, p, want)
		}
	}
}

// Known values: perfect separation of 3 over 21 is 1 in C(24,3) = 2024; all
// values tied is no evidence at all (p 1); a reversed part is near 1.
func TestAUCPValueKnownValues(t *testing.T) {
	pos := []float64{22, 23, 24}
	var neg []float64
	for i := 1; i <= 21; i++ {
		neg = append(neg, float64(i))
	}
	if p, _ := AUCPValue(pos, neg); math.Abs(p-1.0/2024) > 1e-12 {
		t.Errorf("perfect 3 over 21: p %v, want 1/2024", p)
	}
	if p, _ := AUCPValue([]float64{5, 5, 5}, []float64{5, 5, 5, 5}); math.Abs(p-1) > 1e-12 {
		t.Errorf("all tied: p %v, want 1", p)
	}
	if p, _ := AUCPValue([]float64{1, 2, 3}, []float64{10, 11, 12, 13}); p < 0.99 {
		t.Errorf("reversed: p %v, want near 1", p)
	}
	if p, _ := AUCPValue(nil, neg); !math.IsNaN(p) {
		t.Errorf("empty class: p %v, want NaN", p)
	}
}

// Over MaxExactPItems the normal approximation takes over, says so, and is
// close to the exact count just under the bound.
func TestAUCPValueNormalApproximation(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	mk := func(n int, shift float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = math.Round(10*(r.NormFloat64()+shift)) / 10
		}
		return out
	}
	pos, neg := mk(40, 0.4), mk(60, 0)
	exactP, exact := AUCPValue(pos, neg)
	if !exact {
		t.Fatal("100 pooled values should be exact")
	}
	big, approx := AUCPValue(append(pos, 0.4), neg)
	if approx {
		t.Fatal("101 pooled values should be the normal approximation")
	}
	if math.Abs(big-exactP) > 0.02 {
		t.Errorf("approximation %v far from the exact %v on nearly the same data", big, exactP)
	}
}
