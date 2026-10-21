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
	// (two judgments of different verdicts) each ranks the right way round (a tie
	// counts half), each pair weighted by its postings' Horvitz-Thompson weights
	// (1/PSelect). HeldOutPairs is how many there were: the n of the gain.
	FitAccuracy  float64 `json:"fit_accuracy"`
	HandAccuracy float64 `json:"hand_accuracy"`
	HeldOutPairs int     `json:"held_out_pairs"`
	HeldOutItems int     `json:"held_out_items"`
	// GainLo and GainHi are the 95% percentile-bootstrap interval of
	// FitAccuracy - HandAccuracy, resampling held-out items Resamples times.
	GainLo    float64 `json:"gain_lo"`
	GainHi    float64 `json:"gain_hi"`
	Resamples int     `json:"resamples"`
	// Strata is the held-out accuracy by the pairs' audit stratum. ScoredPairs
	// counts the pairs between two scored-stratum postings, and Stratified is
	// true when the held-out set holds any audit-sampled posting (the
	// condition for MinScoredStratumPairs to apply).
	Strata      []StratumAccuracy `json:"strata,omitempty"`
	ScoredPairs int               `json:"scored_pairs"`
	Stratified  bool              `json:"stratified"`
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

// Fit fits Score's shares to items' pairwise preferences: every item should
// outrank every item of a lower grade. The model is ScoreWith's own order key
// without the company factor, log(match(s)) + log(gate) + log(fresh), with
// P(a above b) = sigmoid(tau * (u_a - u_b)), and an L2 prior on each
// log-share centred on base's. Judged items are split into training and a
// held-out set by a seeded hash of their key; marks and impressions train
// only. Fewer than MinJudged judged items, or no pair of different grades on
// either side, is a NotRunError. The fit is deterministic for a given seed and
// item set.
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
	if err := needAtLeast("weight fit", "usable audit judgments", res.Judged, MinJudged); err != nil {
		return res, err
	}

	var trainBy, testBy grades
	for i, it := range items {
		if it.Judged && heldOut(it.Key, opt.Seed, opt.HoldoutPct) {
			testBy[it.Grade] = append(testBy[it.Grade], i)
			res.HeldOutItems++
		} else {
			trainBy[it.Grade] = append(trainBy[it.Grade], i)
			res.TrainItems++
		}
	}
	if testBy.pairs() == 0 {
		return res, &NotRunError{Part: "weight fit", Why: fmt.Sprintf("the held-out judgments need two different verdicts (%s)", testBy)}
	}
	if trainBy.pairs() == 0 {
		return res, &NotRunError{Part: "weight fit", Why: fmt.Sprintf("the training labels need two different grades (%s)", trainBy)}
	}
	res.TrainPairsTotal = trainBy.pairs()
	train := trainBy.sample(opt.MaxPairs, opt.Seed)
	res.TrainPairs = len(train)
	if n := testBy.pairs(); n > MaxHeldOutPairs {
		return res, fmt.Errorf("rankfit: %d held-out pairs, max %d (lower --holdout)", n, MaxHeldOutPairs)
	}
	test := testBy.sample(math.MaxInt, 0)
	var testItems []int
	for _, g := range testBy {
		testItems = append(testItems, g...)
	}
	slices.Sort(testItems)

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

	ev := evaluateHeldOut(items, testItems, test, b, fitted, hand, opt.Seed)
	res.HeldOutPairs = len(test)
	res.HandAccuracy, res.FitAccuracy = ev.hand, ev.fit
	res.GainLo, res.GainHi, res.Resamples = ev.gainLo, ev.gainHi, ev.bootstrapSamples
	res.Strata, res.ScoredPairs, res.Stratified = ev.strata, ev.scoredPairs, ev.stratified
	return res, nil
}

func (it *Item) check() error {
	if err := checkInclusion(it.Key, it.Source, it.PSelect); err != nil {
		return err
	}
	if it.Grade < GradeNo || it.Grade > GradeYes {
		return fmt.Errorf("rankfit: %s grade %d, want %d..%d", it.Key, it.Grade, GradeNo, GradeYes)
	}
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

// grades holds item indexes by grade.
type grades [GradeYes + 1][]int

func (g grades) String() string {
	return fmt.Sprintf("%d yes, %d maybe, %d no", len(g[GradeYes]), len(g[GradeMaybe]), len(g[GradeNo]))
}

// gradePairs are the (higher, lower) grade buckets a pair can span.
var gradePairs = [...][2]int{{GradeYes, GradeMaybe}, {GradeYes, GradeNo}, {GradeMaybe, GradeNo}}

// pairs is how many pairs of different grades g holds.
func (g grades) pairs() int {
	n := 0
	for _, gp := range gradePairs {
		n += len(g[gp[0]]) * len(g[gp[1]])
	}
	return n
}

// sample is every pair of different grades, higher first, or max of them
// drawn uniformly (with replacement) by a generator seeded with seed.
func (g grades) sample(max int, seed uint64) []pair {
	total := g.pairs()
	if total <= max {
		out := make([]pair, 0, total)
		for _, gp := range gradePairs {
			for _, w := range g[gp[0]] {
				for _, l := range g[gp[1]] {
					out = append(out, pair{w, l})
				}
			}
		}
		return out
	}
	rng := rand.New(rand.NewPCG(seed, 0x72616e6b666974)) // "rankfit"
	out := make([]pair, max)
	for i := range out {
		k := rng.IntN(total)
		for _, gp := range gradePairs {
			hi, lo := g[gp[0]], g[gp[1]]
			if n := len(hi) * len(lo); k >= n {
				k -= n
				continue
			}
			out[i] = pair{hi[k/len(lo)], lo[k%len(lo)]}
			break
		}
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

// model is the penalized pair loss over x = (log-shares..., log tau[,
// gate exponent, fresh exponent]). A part whose hand share is 0 stays 0.
type model struct {
	items  []Item
	pairs  []pair
	b      blend
	theta0 [NumParts]float64
	free   [NumParts]bool
	lambda float64
	scale  float64 // training items per unit of pair weight
	diag   bool
}

func newModel(items []Item, pairs []pair, b blend, hand [NumParts]float64, lambda, trainItems float64, diag bool) *model {
	// A pair counts the product of its postings' Horvitz-Thompson weights; the
	// loss is scaled so those weights total as many pairs-worth as training
	// items, as before when every weight is 1.
	var total float64
	for _, p := range pairs {
		total += items[p.win].weight() * items[p.lose].weight()
	}
	m := &model{items: items, pairs: pairs, b: b, lambda: lambda, diag: diag, scale: trainItems / total}
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
		pw := m.items[p.win].weight() * m.items[p.lose].weight()
		loss += pw * softplus(-tau*d)
		c := -sigmoid(-tau*d) * pw // d (pw softplus(-tau d)) / d(tau d)
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
