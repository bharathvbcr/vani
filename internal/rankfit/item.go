package rankfit

import (
	"fmt"
	"math"

	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// Part indexes Score's additive parts in ScoreWith's order.
const (
	PartFit = iota
	PartCoverage
	PartSemantic
	PartKeyword
	PartTitle
	PartEmployer
	PartKBMatch
	NumParts
)

// PartNames are the parts' Factor names.
var PartNames = [NumParts]string{"fit", "coverage", "semantic", "keyword", "title", "employer", "kb_match"}

// Item is one labelled posting as the fit sees it: Score's parts as
// ScoreWith computed them under the base weights, and the label.
type Item struct {
	Key string
	// Value is each part's value (an unknown part's is the prior).
	Value [NumParts]float64
	// Known marks the parts in ScoreWith's known-evidence blend: known parts,
	// with the employer only when preferred.
	Known [NumParts]bool
	// Present marks the parts that take a share at all: every part but an
	// unasked kb_match.
	Present [NumParts]bool
	// Gate and Fresh are Score's multipliers. The company factor is left
	// out: it is learned from the same marks the fit trains on.
	Gate, Fresh float64
	// Grade is the label (GradeNo, GradeMaybe, GradeYes); Judged is true when
	// it is an audit judgment rather than a mark or an impression.
	Grade  int
	Judged bool
}

// ItemFrom reads an Item from r, which must be ScoreWith's answer for the
// posting: its Factors hold every part the fit needs, unknown ones included,
// so the Item is exactly what ScoreWith ranked.
func ItemFrom(key string, r jobinsight.Result, t Target) (Item, error) {
	it := Item{Key: key, Grade: t.Grade, Judged: t.Judged, Gate: math.NaN(), Fresh: math.NaN()}
	index := map[string]int{}
	for i, n := range PartNames {
		index[n] = i
	}
	seen := [NumParts]bool{}
	for _, f := range r.Factors {
		_, weighed := index[f.Name]
		if (weighed || f.Name == "gate" || f.Name == "fresh") && !(f.Value >= 0 && f.Value <= 1) {
			return Item{}, fmt.Errorf("rankfit: %s factor %s is %v, want 0..1", key, f.Name, f.Value)
		}
		switch f.Name {
		case "gate":
			it.Gate = f.Value
		case "fresh":
			it.Fresh = f.Value
		default:
			i, ok := index[f.Name]
			if !ok {
				continue // company, and any factor the fit does not weigh
			}
			it.Value[i], it.Present[i], seen[i] = f.Value, true, true
			// ScoreWith's employer part is always known, but only a preferred
			// employer (value 1) counts as evidence.
			it.Known[i] = f.Known && (i != PartEmployer || f.Value == 1)
		}
	}
	for i := range PartKBMatch { // kb_match alone may be absent: not asked
		if !seen[i] {
			return Item{}, fmt.Errorf("rankfit: %s has no %s factor", key, PartNames[i])
		}
	}
	if math.IsNaN(it.Gate) || math.IsNaN(it.Fresh) {
		return Item{}, fmt.Errorf("rankfit: %s has no gate or fresh factor", key)
	}
	return it, nil
}

// ItemFromScores reads an Item from Score's parts as feedback recorded them
// when the posting was judged or shown (feedback.ScoresOf): the known parts
// by name, plus gate and fresh. A part not recorded was unknown and takes
// base's prior, as ScoreWith gives it; kb_match not recorded was not asked
// and takes no share. One approximation: an unknown role fit whose option
// was set but unanswered took ScoreWith's ordinal value, not the prior, and
// that value is not recorded.
func ItemFromScores(key string, s feedback.Scores, base jobinsight.Weights, t Target) (Item, error) {
	prior := blendOf(base).prior
	it := Item{Key: key, Grade: t.Grade, Judged: t.Judged}
	for i, n := range PartNames {
		v, ok := s.Factors[n]
		switch {
		case ok && !(v >= 0 && v <= 1):
			return Item{}, fmt.Errorf("rankfit: %s factor %s is %v, want 0..1", key, n, v)
		case ok:
			it.Value[i], it.Present[i] = v, true
			it.Known[i] = i != PartEmployer || v == 1
		case i == PartKBMatch:
		case i == PartEmployer: // always known to ScoreWith; recorded as 0 when absent
			it.Present[i] = true
		default:
			it.Value[i], it.Present[i] = prior, true
		}
	}
	g, okG := s.Factors["gate"]
	f, okF := s.Factors["fresh"]
	if !okG || !okF {
		return Item{}, fmt.Errorf("rankfit: %s has no recorded gate or fresh", key)
	}
	it.Gate, it.Fresh = g, f
	return it, nil
}

// blend is how ScoreWith turns shares into match, from the base weights.
type blend struct {
	prior      float64
	knownMatch bool
	shrink     float64
}

func blendOf(w jobinsight.Weights) blend {
	return blend{prior: min(max(w.UnknownPrior, 0), 1), knownMatch: w.KnownMatch,
		shrink: w.MatchShrink}
}

// logFloor keeps log(match), log(gate) and log(fresh) finite at 0.
const logFloor = 1e-12

// logMatch is log(ScoreWith's match) for item under shares s (s >= 0), and
// into grad (when non-nil) its derivative in each share. It mirrors ScoreWith
// exactly; the parity test holds it to that.
func (b blend) logMatch(it *Item, s *[NumParts]float64, grad *[NumParts]float64) float64 {
	if grad != nil {
		*grad = [NumParts]float64{}
	}
	var w, n, d, sv float64
	anyKnown := false
	for i := range NumParts {
		if !it.Present[i] {
			continue
		}
		w += s[i]
		sv += s[i] * it.Value[i]
		if it.Known[i] {
			n += s[i] * it.Value[i]
			d += s[i]
			if i != PartEmployer {
				anyKnown = true
			}
		}
	}
	if !(w > 0) {
		return math.Log(max(b.prior, logFloor))
	}
	if anyKnown && d > 0 && b.knownMatch && b.shrink > 0 && !math.IsInf(b.shrink, 0) {
		// match = (N/W + λp) / (D/W + λ) = (N + λpW) / (D + λW).
		num, den := n+b.shrink*b.prior*w, d+b.shrink*w
		if !(num > logFloor) {
			return math.Log(logFloor)
		}
		if grad != nil {
			for i := range NumParts {
				if !it.Present[i] {
					continue
				}
				dn, dd := b.shrink*b.prior, b.shrink
				if it.Known[i] {
					dn += it.Value[i]
					dd++
				}
				grad[i] = dn/num - dd/den
			}
		}
		return math.Log(min(num/den, 1))
	}
	if !(sv > logFloor*w) {
		return math.Log(logFloor)
	}
	if grad != nil {
		for i := range NumParts {
			if it.Present[i] {
				grad[i] = it.Value[i]/sv - 1/w
			}
		}
	}
	return math.Log(min(sv/w, 1))
}
