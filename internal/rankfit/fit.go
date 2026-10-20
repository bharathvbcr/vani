package rankfit

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// Options tune the weight fit. The zero value means the defaults.
type Options struct {
	// Lambda is the L2 pull of each log-share toward the hand weights' log,
	// in units of one training item (the pair loss is scaled so its total
	// weighs as many items as trained). 0 or less means DefaultLambda.
	Lambda float64
	// Seed drives the held-out split and the pair sample.
	Seed uint64
	// HoldoutPct is the share (1-90) of judged items held out for the
	// evaluation; 0 means DefaultHoldoutPct.
	HoldoutPct int
	// MaxPairs bounds the training pairs; above it they are sampled. 0 means
	// DefaultMaxPairs.
	MaxPairs int
}

const (
	DefaultLambda     = 1.0
	DefaultHoldoutPct = 30
	DefaultMaxPairs   = 200_000
	// MaxPairsLimit bounds Options.MaxPairs and MaxItems the items one fit
	// takes, so a fit's memory and time stay bounded.
	MaxPairsLimit = 5_000_000
	MaxItems      = 200_000

	// tauRidge is the weak pull of log(temperature) toward 0. The
	// temperature only scales the utility differences; without a pull a
	// separable training set would send it to infinity.
	tauRidge = 0.01
	// maxLogShift bounds how far a log-share may move from the hand weights'.
	maxLogShift = 12
	fitMaxIter  = 500
)

func (o Options) withDefaults() (Options, error) {
	if !(o.Lambda > 0) || math.IsInf(o.Lambda, 0) {
		o.Lambda = DefaultLambda
	}
	if o.HoldoutPct == 0 {
		o.HoldoutPct = DefaultHoldoutPct
	}
	if o.HoldoutPct < 1 || o.HoldoutPct > 90 {
		return o, fmt.Errorf("rankfit: holdout %d%% must be within 1..90", o.HoldoutPct)
	}
	if o.MaxPairs == 0 {
		o.MaxPairs = DefaultMaxPairs
	}
	if o.MaxPairs < 1 || o.MaxPairs > MaxPairsLimit {
		return o, fmt.Errorf("rankfit: max pairs %d must be within 1..%d", o.MaxPairs, MaxPairsLimit)
	}
	return o, nil
}

// FitResult is the weight fit's answer.
type FitResult struct {
	// Shares are the fitted shares; Hand the base weights' they started from.
	Shares Shares `json:"shares"`
	Hand   Shares `json:"hand"`
	// FitAccuracy and HandAccuracy are the share of held-out judged pairs
	// (a would-apply yes against a no) each ranks the right way round (a tie
	// counts half). HeldOutPairs is how many there were.
	FitAccuracy  float64 `json:"fit_accuracy"`
	HandAccuracy float64 `json:"hand_accuracy"`
	HeldOutPairs int     `json:"held_out_pairs"`
	HeldOutItems int     `json:"held_out_items"`
	// TrainPairs were fitted, out of TrainPairsTotal possible (sampled above
	// MaxPairs).
	TrainItems      int `json:"train_items"`
	TrainPairs      int `json:"train_pairs"`
	TrainPairsTotal int `json:"train_pairs_total"`
	Judged          int `json:"judged"`
	// Temperature scales utility differences into the pair probability.
	Temperature float64 `json:"temperature"`
	// GateExp and FreshExp are a diagnostic: the exponents a second fit puts
	// on the gate and freshness when they are free. ScoreWith fixes both at
	// 1 and the file does not carry them; far from 1, they say that the
	// gate's or freshness' constants, not the shares, are what is off.
	GateExp  float64 `json:"gate_exp"`
	FreshExp float64 `json:"fresh_exp"`
	// DiagConverged is false when the diagnostic fit stopped at its cap: the
	// exponents are then not to be read.
	DiagConverged bool `json:"diag_converged"`
	// Converged is false when the optimizer stopped at its iteration cap.
	Converged  bool    `json:"converged"`
	Iterations int     `json:"iterations"`
	Lambda     float64 `json:"lambda"`
	Seed       uint64  `json:"seed"`
}

// Beats reports whether the fitted shares order the held-out pairs better
// than the hand weights (Phase 5's exit test).
func (r FitResult) Beats() bool { return r.FitAccuracy > r.HandAccuracy }

type pair struct{ win, lose int }

// heldOut reports whether a judged item is held out, by a hash of its key and
// the seed: the split does not depend on the items' order.
func heldOut(key string, seed uint64, pct int) bool {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], seed)
	h.Write(b[:])
	h.Write([]byte(key))
	return h.Sum64()%100 < uint64(pct)
}

// Fit fits Score's shares to items' pairwise preferences: every positive
// item should outrank every negative one. The model is ScoreWith's own order
// key without the company factor, log(match(s)) + log(gate) + log(fresh),
// with P(a above b) = sigmoid(tau * (u_a - u_b)), and an L2 prior on each
// log-share centred on base's. Judged items are split into training and a
// held-out set by a seeded hash of their key; marks train only. Fewer than
// MinJudgedLabels judged items, or a side with only one class, is a
// NotRunError. The fit is deterministic for a given seed and item set.
func Fit(items []Item, base jobinsight.Weights, opt Options) (FitResult, error) {
	opt, err := opt.withDefaults()
	if err != nil {
		return FitResult{}, err
	}
	if len(items) > MaxItems {
		return FitResult{}, fmt.Errorf("rankfit: %d items, max %d", len(items), MaxItems)
	}
	res := FitResult{Lambda: opt.Lambda, Seed: opt.Seed, Hand: SharesOf(base)}
	seen := make(map[string]bool, len(items))
	for i := range items {
		it := &items[i]
		if seen[it.Key] {
			return res, fmt.Errorf("rankfit: item %q appears twice", it.Key)
		}
		seen[it.Key] = true
		if err := it.check(); err != nil {
			return res, err
		}
		if it.Judged {
			res.Judged++
		}
	}
	if err := needAtLeast("weight fit", "judged would-apply labels", res.Judged, MinJudgedLabels); err != nil {
		return res, err
	}

	var trainPos, trainNeg, testPos, testNeg []int
	for i, it := range items {
		test := it.Judged && heldOut(it.Key, opt.Seed, opt.HoldoutPct)
		switch {
		case test && it.Positive:
			testPos = append(testPos, i)
		case test:
			testNeg = append(testNeg, i)
		case it.Positive:
			trainPos = append(trainPos, i)
		default:
			trainNeg = append(trainNeg, i)
		}
	}
	res.HeldOutItems = len(testPos) + len(testNeg)
	res.TrainItems = len(trainPos) + len(trainNeg)
	if len(testPos) == 0 || len(testNeg) == 0 {
		return res, &NotRunError{Part: "weight fit", Why: fmt.Sprintf("the held-out judged labels need both a yes and a no (%d yes, %d no)", len(testPos), len(testNeg))}
	}
	if len(trainPos) == 0 || len(trainNeg) == 0 {
		return res, &NotRunError{Part: "weight fit", Why: fmt.Sprintf("the training labels need both a positive and a negative (%d positive, %d negative)", len(trainPos), len(trainNeg))}
	}
	res.TrainPairsTotal = len(trainPos) * len(trainNeg)
	train := samplePairs(trainPos, trainNeg, opt.MaxPairs, opt.Seed)
	res.TrainPairs = len(train)
	test := make([]pair, 0, len(testPos)*len(testNeg))
	for _, p := range testPos {
		for _, n := range testNeg {
			test = append(test, pair{p, n})
		}
	}

	b := blendOf(base)
	hand := res.Hand.Array()
	m := newModel(items, train, b, hand, opt.Lambda, float64(res.TrainItems), false)
	x, iters, conv := minimize(m.objective, m.start())
	res.Iterations, res.Converged = iters, conv
	fitted := m.shares(x)
	res.Shares = sharesFrom(fitted)
	res.Temperature = math.Exp(x[NumParts])

	diag := newModel(items, train, b, hand, opt.Lambda, float64(res.TrainItems), true)
	dx, _, dconv := minimize(diag.objective, diag.start())
	res.GateExp, res.FreshExp, res.DiagConverged = dx[NumParts+1], dx[NumParts+2], dconv

	res.HeldOutPairs = len(test)
	res.HandAccuracy = accuracy(items, test, b, hand)
	res.FitAccuracy = accuracy(items, test, b, fitted)
	return res, nil
}

func (it *Item) check() error {
	for i := range NumParts {
		if v := it.Value[i]; !(v >= 0 && v <= 1) {
			return fmt.Errorf("rankfit: %s part %s is %v, want 0..1", it.Key, PartNames[i], v)
		}
	}
	if !(it.Gate >= 0 && it.Gate <= 1) || !(it.Fresh >= 0 && it.Fresh <= 1) {
		return fmt.Errorf("rankfit: %s gate %v or fresh %v outside 0..1", it.Key, it.Gate, it.Fresh)
	}
	return nil
}

// samplePairs is every positive-negative pair, or max of them drawn
// uniformly (with replacement) by a generator seeded with seed.
func samplePairs(pos, neg []int, max int, seed uint64) []pair {
	total := len(pos) * len(neg)
	if total <= max {
		out := make([]pair, 0, total)
		for _, p := range pos {
			for _, n := range neg {
				out = append(out, pair{p, n})
			}
		}
		return out
	}
	rng := rand.New(rand.NewPCG(seed, 0x72616e6b666974)) // "rankfit"
	out := make([]pair, max)
	for i := range out {
		k := rng.IntN(total)
		out[i] = pair{pos[k/len(neg)], neg[k%len(neg)]}
	}
	slices.SortFunc(out, func(a, b pair) int {
		if a.win != b.win {
			return a.win - b.win
		}
		return a.lose - b.lose
	})
	return out
}

// utility is the order key the fit and the evaluation share.
func utility(it *Item, b blend, s *[NumParts]float64, gateExp, freshExp float64) float64 {
	return b.logMatch(it, s, nil) + gateExp*math.Log(max(it.Gate, logFloor)) + freshExp*math.Log(max(it.Fresh, logFloor))
}

// accuracy is the share of pairs s orders winner first; a tie counts half.
func accuracy(items []Item, pairs []pair, b blend, s [NumParts]float64) float64 {
	if len(pairs) == 0 {
		return 0
	}
	u := make([]float64, len(items))
	for i := range items {
		u[i] = utility(&items[i], b, &s, 1, 1)
	}
	right := 0.0
	for _, p := range pairs {
		switch d := u[p.win] - u[p.lose]; {
		case d > 0:
			right++
		case d == 0:
			right += 0.5
		}
	}
	return right / float64(len(pairs))
}

// model is the penalized pair loss over x = (log-shares..., log tau[,
// gate exponent, fresh exponent]). A part whose hand share is 0 stays 0.
type model struct {
	items  []Item
	pairs  []pair
	b      blend
	theta0 [NumParts]float64
	free   [NumParts]bool
	lambda float64
	scale  float64 // training items per pair
	diag   bool
}

func newModel(items []Item, pairs []pair, b blend, hand [NumParts]float64, lambda, trainItems float64, diag bool) *model {
	m := &model{items: items, pairs: pairs, b: b, lambda: lambda, diag: diag, scale: trainItems / float64(len(pairs))}
	for i, s := range hand {
		if s > 0 {
			m.theta0[i], m.free[i] = math.Log(s), true
		}
	}
	return m
}

func (m *model) start() []float64 {
	n := NumParts + 1
	if m.diag {
		n += 2
	}
	x := make([]float64, n)
	copy(x, m.theta0[:])
	if m.diag {
		x[NumParts+1], x[NumParts+2] = 1, 1
	}
	return x
}

func (m *model) shares(x []float64) [NumParts]float64 {
	var s [NumParts]float64
	for i := range NumParts {
		if m.free[i] {
			s[i] = math.Exp(min(max(x[i], m.theta0[i]-maxLogShift), m.theta0[i]+maxLogShift))
		}
	}
	return s
}

// objective is the loss at x, with its gradient written into g.
func (m *model) objective(x, g []float64) float64 {
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
	du := make([][NumParts]float64, n) // d u / d share
	lg, lf := make([]float64, n), make([]float64, n)
	for i := range m.items {
		it := &m.items[i]
		lg[i], lf[i] = math.Log(max(it.Gate, logFloor)), math.Log(max(it.Fresh, logFloor))
		u[i] = m.b.logMatch(it, &s, &du[i]) + ge*lg[i] + fe*lf[i]
	}
	coef := make([]float64, n) // d loss / d u_i
	loss, dtau := 0.0, 0.0
	for _, p := range m.pairs {
		d := u[p.win] - u[p.lose]
		loss += softplus(-tau * d)
		c := -sigmoid(-tau * d) // d softplus(-tau d) / d(tau d)
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
				g[k] += c * du[i][k] * s[k] // d share / d log-share = share
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
