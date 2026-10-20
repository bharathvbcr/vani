package rankfit

import (
	"fmt"
	"math"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// PlattSample is one backend reading of one slot set against the human label
// for that slot: Correct when the backend's answer was the label.
type PlattSample struct {
	// Key is the labelled posting; the threshold counts postings, not slots.
	Key     posting.Key
	P       float64
	HasP    bool
	Correct bool
}

// PlattFit is one backend's calibration and what it rests on.
type PlattFit struct {
	Calibration jobinsight.Calibration `json:"calibration"`
	// N is the samples fitted, Correct how many of them were right, Postings
	// the distinct postings they came from.
	N        int `json:"n"`
	Correct  int `json:"correct"`
	Postings int `json:"postings"`
	// Unused counts samples left out: those without p (or with an invalid
	// one) when the fit is on p, and those with an invalid p otherwise.
	Unused int `json:"unused,omitempty"`
}

// DefaultPlattRidge is the L2 pull toward the identity calibration (A 1,
// B 0 on p; B 0 without it). It only keeps the Newton system well posed
// when every p is the same; at MinPlattPostings it moves no estimate visibly.
const DefaultPlattRidge = 1e-3

const (
	plattMaxIter = 100
	plattTol     = 1e-10
	// minLogitSpread is the least spread of logit(p) a fit on p needs.
	minLogitSpread = 1e-6
)

// FitPlatt fits sigmoid(A*logit(p)+B) to samples by Platt's method: targets
// smoothed to (N+ +1)/(N+ +2) and 1/(N- +2), so a backend that is always right
// (or always wrong) still gets a finite, bounded calibration; Newton's method
// with a backtracking line search, so the answer is deterministic. When most
// samples carry p the fit is on logit(p) and the rest are left out; otherwise
// it is the intercept alone (A 0), the backend's smoothed precision, which
// replaces AnsweredConf for it. Usable samples from fewer than MinPlattPostings
// distinct postings is a NotRunError. ridge < 0 or not finite means DefaultPlattRidge.
func FitPlatt(backend string, samples []PlattSample, ridge float64) (PlattFit, error) {
	if !(ridge >= 0) || math.IsInf(ridge, 0) {
		ridge = DefaultPlattRidge
	}
	part := "platt " + backend
	xs, ys, usesP, postings := plattData(samples)
	fit := PlattFit{N: len(xs), Unused: len(samples) - len(xs), Postings: postings}
	if err := needAtLeast(part, "judged postings", postings, MinPlattPostings); err != nil {
		return fit, err
	}
	pos := 0
	for _, y := range ys {
		if y {
			pos++
		}
	}
	fit.Correct = pos
	hi, lo := float64(pos+1)/float64(pos+2), 1/float64(len(ys)-pos+2)
	t := make([]float64, len(ys))
	for i, y := range ys {
		t[i] = lo
		if y {
			t[i] = hi
		}
	}
	a0, b0 := 0.0, 0.0
	if usesP {
		a0 = 1
	}
	a, b := plattNewton(xs, t, usesP, ridge, a0, b0)
	fit.Calibration = jobinsight.Calibration{A: a, B: b, UsesP: usesP}
	if !fit.Calibration.Valid() {
		return fit, fmt.Errorf("rankfit: %s ran away (A %v, B %v)", part, a, b)
	}
	return fit, nil
}

// PlattUsable is how many distinct postings the samples FitPlatt would fit
// come from: what its threshold is counted on.
func PlattUsable(samples []PlattSample) int {
	_, _, _, n := plattData(samples)
	return n
}

// plattData picks the samples a fit uses and their logit(p) (0 when the fit
// is on the intercept alone). The fit is on p when most samples carry a valid
// one and those p are not all alike; the rest are then left out. When every
// usable p is the same, the slope cannot be told from the intercept (only
// the ridge would pick it), so the fit is the intercept alone. A p outside
// 0..1 or NaN is never used.
func plattData(samples []PlattSample) (xs []float64, ys []bool, usesP bool, postings int) {
	validP := func(s PlattSample) bool { return s.HasP && s.P >= 0 && s.P <= 1 }
	withP := 0
	for _, s := range samples {
		if validP(s) {
			withP++
		}
	}
	usesP = withP*2 > len(samples)
	seen := map[posting.Key]bool{}
	if usesP {
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, s := range samples {
			if validP(s) {
				q := min(max(s.P, 1e-6), 1-1e-6)
				x := math.Log(q / (1 - q))
				lo, hi = min(lo, x), max(hi, x)
			}
		}
		usesP = hi-lo > minLogitSpread
	}
	for _, s := range samples {
		switch {
		case usesP && !validP(s), !usesP && s.HasP && !validP(s):
			continue
		case usesP:
			q := min(max(s.P, 1e-6), 1-1e-6)
			xs = append(xs, math.Log(q/(1-q)))
		default:
			xs = append(xs, 0)
		}
		ys = append(ys, s.Correct)
		seen[s.Key] = true
	}
	return xs, ys, usesP, len(seen)
}

// plattObjective is the cross-entropy of sigmoid(a*x+b) against t plus the
// ridge toward (a0, b0).
func plattObjective(xs, t []float64, a, b, ridge, a0, b0 float64) float64 {
	sum := 0.0
	for i, x := range xs {
		f := a*x + b
		sum += t[i]*softplus(-f) + (1-t[i])*softplus(f)
	}
	return sum + ridge/2*((a-a0)*(a-a0)+(b-b0)*(b-b0))
}

func plattNewton(xs, t []float64, usesP bool, ridge, a0, b0 float64) (a, b float64) {
	a, b = a0, b0
	obj := plattObjective(xs, t, a, b, ridge, a0, b0)
	for range plattMaxIter {
		var ga, gb, haa, hab, hbb float64
		for i, x := range xs {
			p := sigmoid(a*x + b)
			d, w := p-t[i], p*(1-p)
			ga += d * x
			gb += d
			haa += w * x * x
			hab += w * x
			hbb += w
		}
		ga += ridge * (a - a0)
		gb += ridge * (b - b0)
		// 1e-12 keeps the system solvable when every x is equal and ridge is 0.
		haa += ridge + 1e-12
		hbb += ridge + 1e-12
		var da, db float64
		if usesP {
			det := haa*hbb - hab*hab
			da = (hbb*ga - hab*gb) / det
			db = (haa*gb - hab*ga) / det
		} else {
			db = gb / hbb
		}
		if math.Max(math.Abs(ga), math.Abs(gb)) < plattTol || !(math.Abs(da)+math.Abs(db) > 0) {
			return a, b
		}
		step := 1.0
		for ; step > 1e-12; step /= 2 {
			na, nb := a-step*da, b-step*db
			if no := plattObjective(xs, t, na, nb, ridge, a0, b0); no <= obj-1e-4*step*(ga*da+gb*db) {
				a, b, obj = na, nb, no
				break
			}
		}
		if step <= 1e-12 {
			return a, b
		}
	}
	return a, b
}

// sigmoid is 1/(1+e^-z), computed without overflow.
func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}

// softplus is log(1+e^z), computed without overflow.
func softplus(z float64) float64 {
	return max(z, 0) + math.Log1p(math.Exp(-math.Abs(z)))
}
