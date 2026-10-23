package rankfit

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// oracleObjective is the pair loss as it was before the fit loop shared one
// exponential between softplus and sigmoid and read the pair weights from a
// table: separate calls, weights recomputed per evaluation. It is the oracle
// the fast objective must match bit for bit, because Fit's determinism is a
// contract (same input and seed, identical FitResult).
func oracleObjective(m *model, x, g []float64) float64 {
	for i := range g {
		g[i] = 0
	}
	s := m.shares(x)
	tau := math.Exp(x[NumParts])
	ge, fe := 1.0, 1.0
	if m.diag {
		ge, fe = x[NumParts+1], x[NumParts+2]
	}
	n := len(m.items)
	u := make([]float64, n)
	du := make([][NumParts]float64, n)
	lg, lf := make([]float64, n), make([]float64, n)
	for i := range m.items {
		it := &m.items[i]
		lg[i], lf[i] = math.Log(max(it.Gate, logFloor)), math.Log(max(it.Fresh, logFloor))
		u[i] = m.b.logMatch(it, &s, &du[i]) + ge*lg[i] + fe*lf[i]
	}
	coef := make([]float64, n)
	loss, dtau := 0.0, 0.0
	for _, p := range m.pairs {
		d := u[p.win] - u[p.lose]
		pw := m.items[p.win].weight() * m.items[p.lose].weight()
		loss += pw * softplus(-tau*d)
		c := -sigmoid(-tau*d) * pw
		coef[p.win] += c * tau
		coef[p.lose] -= c * tau
		dtau += c * d
	}
	loss *= m.scale
	for i := range n {
		if coef[i] == 0 {
			continue
		}
		c := coef[i] * m.scale
		for k := range NumParts {
			if m.free[k] {
				g[k] += c * du[i][k] * s[k]
			}
		}
		if m.diag {
			g[NumParts+1] += c * lg[i]
			g[NumParts+2] += c * lf[i]
		}
	}
	g[NumParts] += m.scale * dtau * tau
	for k := range NumParts {
		if m.free[k] {
			d := x[k] - m.theta0[k]
			loss += m.lambda / 2 * d * d
			g[k] += m.lambda * d
		}
	}
	loss += tauRidge / 2 * x[NumParts] * x[NumParts]
	g[NumParts] += tauRidge * x[NumParts]
	if m.diag {
		for _, k := range []int{NumParts + 1, NumParts + 2} {
			d := x[k] - 1
			loss += m.lambda / 2 * d * d
			g[k] += m.lambda * d
		}
	}
	return loss
}

func TestObjectiveMatchesTheSeparateCallOracle(t *testing.T) {
	b := blendOf(jobinsight.DefaultWeights())
	hand := SharesOf(jobinsight.DefaultWeights()).Array()
	for seed := uint64(1); seed <= 5; seed++ {
		items := world(500+seed, 400, 150)
		var by grades
		for i, it := range items {
			by[it.Grade] = append(by[it.Grade], i)
		}
		pairs := by.sample(5000, seed) // sampled, so duplicate pairs occur
		for _, diag := range []bool{false, true} {
			m := newModel(items, pairs, b, hand, 1, float64(len(items)), diag)
			rng := rand.New(rand.NewPCG(seed, 3))
			for trial := 0; trial < 6; trial++ {
				x := m.start()
				for i := range x {
					x[i] += rng.NormFloat64() * []float64{0.2, 2, 30}[trial%3] // tau large enough to saturate softplus
				}
				g1, g2 := make([]float64, len(x)), make([]float64, len(x))
				f1, f2 := m.objective(x, g1), oracleObjective(m, x, g2)
				if math.Float64bits(f1) != math.Float64bits(f2) {
					t.Fatalf("seed %d diag %v trial %d: loss %v != oracle %v", seed, diag, trial, f1, f2)
				}
				for i := range g1 {
					if math.Float64bits(g1[i]) != math.Float64bits(g2[i]) {
						t.Fatalf("seed %d diag %v trial %d: grad[%d] %v != oracle %v", seed, diag, trial, i, g1[i], g2[i])
					}
				}
			}
		}
	}
}

func TestSoftplusSigmoidMatchesTheSeparateFunctions(t *testing.T) {
	zs := []float64{0, math.Copysign(0, -1), 1e-300, -1e-300, 0.5, -0.5, 3, -3, 37, -37, 745, -745, 800, -800, 1e6, -1e6, math.Inf(1), math.Inf(-1)}
	rng := rand.New(rand.NewPCG(9, 9))
	for range 2000 {
		zs = append(zs, rng.NormFloat64()*20)
	}
	for _, z := range zs {
		sp, sg := softplusSigmoid(z)
		if math.Float64bits(sp) != math.Float64bits(softplus(z)) || math.Float64bits(sg) != math.Float64bits(sigmoid(z)) {
			t.Fatalf("z=%v: fused (%v, %v) != separate (%v, %v)", z, sp, sg, softplus(z), sigmoid(z))
		}
	}
	if sp, sg := softplusSigmoid(math.NaN()); !math.IsNaN(sp) || !math.IsNaN(sg) {
		t.Fatalf("NaN in must stay NaN, got (%v, %v)", sp, sg)
	}
}
