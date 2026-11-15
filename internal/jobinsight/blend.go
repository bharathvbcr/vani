package jobinsight

import (
	"math"
	"strings"
)

// A score is computed in two stages, so a caller that keeps what did not
// change (`vani jobs serve`) recomputes only what did:
//
//   - BlendOf reads everything about a posting except the clock and the marks:
//     its readings, coverage, similarity, keyword and title signals, employer,
//     the model's read of its work, penalties and doubt. It gives match, gate,
//     the displayed relevance's known share, and the basis.
//   - Blend.Close adds what does: freshness (the posting's age against the
//     clock, the half-life, an age-neutral employer) and the company factor
//     (the marks on the company's other postings).
//
// ScoreWith is the two in a row; there is no other scorer. The arithmetic of
// each stage is the pre-split ScoreWith's, expression for expression, and a
// test holds the two to the same bits (scoreoracle_test.go).

// blendPart is one additive part of match.
type blendPart struct {
	name   string
	weight float64
	value  float64
	known  bool
	// conf is how far the part's reading is trusted. It scales the part's
	// WEIGHT in the blend, never its value: a doubtful strong_fit says little,
	// it does not say "somewhere between strong and partial".
	conf float64
}

// Blend is the clock- and marks-free stage of a score (BlendOf). It is a value:
// keeping one and closing it again under another clock or other marks is how a
// resident reader reuses it.
type Blend struct {
	parts   [7]blendPart
	nominal [7]float64
	// match, relMatch and gate are the blend's three numbers; relKnown is
	// Result.RelevanceKnown.
	match, relMatch, relKnown, gate float64
	basis                           Basis
	// fitBackend and fitReading name the fit's reading when it is not the
	// primary's (the fit factor's Backend and Reading).
	fitBackend, fitReading string
}

// FitReading is the role_fit reading b rests on when it is not the primary's
// (the owner's manual reading or the escalation tier's, as SlotFor picked it):
// its backend and value, both "" when b rests on the primary's.
func (b *Blend) FitReading() (backend, value string) { return b.fitBackend, b.fitReading }

// Closing is what Blend.Close reads: the posting's age against the clock, the
// half-life, whether its employer is age-neutral, and its company's marks.
type Closing struct {
	AgeKnown     bool
	AgeDays      int
	SeenKnown    bool
	SeenDays     int
	AgeNeutral   bool
	HalfLifeDays float64
	Company      CompanyMarks
}

// ClosingOf is the part of in that Blend.Close reads.
func ClosingOf(in Inputs) Closing {
	return Closing{AgeKnown: in.AgeKnown, AgeDays: in.AgeDays, SeenKnown: in.SeenKnown, SeenDays: in.SeenDays,
		AgeNeutral: in.AgeNeutral, HalfLifeDays: in.HalfLifeDays, Company: in.Company}
}

// ScoreWith ranks in by w. It is pure: the same inputs give the same Result,
// and nothing in it is dropped or changes status (Rule 7).
func ScoreWith(in Inputs, w Weights) Result {
	b := BlendOf(in, w)
	return b.Close(ClosingOf(in), w)
}

// sparesLevel reports whether in's mismatch_seniority role_fit is spared by
// w.SpareSeniorityMismatch: the seniority slot was answered with a listed level.
func (w Weights) sparesLevel(in Inputs) bool {
	if in.RoleFit.Value != mismatchSeniority || !in.Seniority.Answered || in.Seniority.Value == "" {
		return false
	}
	for _, v := range w.SpareSeniorityMismatch {
		if strings.TrimSpace(v) == in.Seniority.Value {
			return true
		}
	}
	return false
}

// BlendOf is the first stage of ScoreWith: everything but freshness and the
// company factor. It reads no age, half-life or company field of in.
func BlendOf(in Inputs, w Weights) Blend {
	prior := clamp01(w.UnknownPrior)
	var b Blend
	parts := &b.parts

	fitConf := w.conf(in.RoleFit)
	base, ok := w.fitValue(in.RoleFit.Value)
	if !ok || w.sparesLevel(in) {
		fitConf = 0
	}
	parts[0] = blendPart{"fit", w.Fit, base, fitConf > 0, fitConf}
	// A confident mismatch is penalised once, by the gate below. Its low fit
	// value would penalise the same reading a second time, so the part leaves the
	// blend (the gate, not the match, is what no coverage can rescue).
	mismatch := fitConf > 0 && (in.RoleFit.Value == "mismatch_domain" || in.RoleFit.Value == "mismatch_seniority")
	if mismatch {
		parts[0].conf = 0
	}

	parts[1] = blendPart{"coverage", w.Coverage, prior, false, 1}
	if in.HasCoverage && finite(in.Coverage) {
		parts[1].value, parts[1].known = clamp01(in.Coverage/100), true
	}

	parts[2] = blendPart{"semantic", w.Semantic, prior, false, 1}
	if in.HasSemantic && finite(in.Semantic) {
		parts[2].value, parts[2].known = clamp01(in.Semantic), true
	}

	parts[3] = blendPart{"keyword", w.Keyword, prior, false, 1}
	if in.Target {
		t, bh := float64(max(in.TitleHits, 0)), float64(max(in.BodyHits, 0))
		parts[3].value = clamp01(0.6*(1-math.Exp(-t/1.5)) + 0.4*(1-math.Exp(-bh/3)))
		parts[3].known = true
	}

	excluded := in.TitleSignals && in.TitleRank < 0
	parts[4] = blendPart{"title", w.Title, prior, false, 1}
	if in.TitleSignals {
		parts[4].known = true
		if excluded {
			parts[4].value = 0
		} else {
			parts[4].value = float64(min(in.TitleRank, 3)) / 3
		}
	}

	parts[5] = blendPart{"employer", w.Employer, 0, true, 1}
	if in.PreferredEmployer {
		parts[5].value = 1
	}

	// The model's read of the work, when this posting was asked and answered with
	// a listed option. Unknown, it takes no share at all rather than the prior, so
	// the postings nobody asked about score as they would without the part.
	parts[6] = blendPart{"kb_match", w.KBMatch, prior, false, 1}
	if c := w.conf(in.KBMatch); c > 0 {
		for i, o := range KBMatchOrder {
			if o == in.KBMatch.Value {
				parts[6].value, parts[6].known, parts[6].conf = kbMatchValues[i], true, c
			}
		}
	}
	if !parts[6].known {
		parts[6].weight = 0
	}

	// Weights that are not positive finite numbers are skipped; the rest are
	// scaled by the largest first, so huge weights cannot overflow the sum.
	var top float64
	for i := range parts {
		if !(parts[i].weight > 0) || math.IsInf(parts[i].weight, 0) {
			parts[i].weight = 0
		}
		top = max(top, parts[i].weight)
	}
	var sum float64
	for i := range parts {
		if top > 0 {
			parts[i].weight /= top
		}
		sum += parts[i].weight
	}
	// A part's share is w_i*c_i over the nominal total, so what doubt takes off a
	// part is not handed to the others: thin or doubtful evidence leaves the match
	// nearer the prior (MatchShrink below), which is the point.
	match, share := prior, [len(parts)]float64{}
	if sum > 0 {
		match = 0
		var used float64
		for i, p := range parts {
			b.nominal[i] = p.weight / sum
			share[i] = p.weight * p.conf / sum
			used += share[i]
			match += share[i] * p.value
		}
		match = clamp01(match + (1-used)*prior) // an unconfident share is unknown: the prior
	}

	// The displayed relevance: only the parts that are known, reweighted. A
	// preferred employer is a known 1; an unpreferred one says nothing.
	relMatch, relKnown, anyKnown := match, 0.0, false
	var knownSum, knownVal float64
	for i, p := range parts {
		if !p.known || (i == 5 && !in.PreferredEmployer) {
			continue
		}
		if i != 5 { // every part but the employer preference is evidence
			anyKnown = true
		}
		knownSum += share[i]
		knownVal += share[i] * p.value
	}
	if anyKnown && knownSum > 0 {
		// Shrunk toward the prior by a pseudo-weight, so one thin known part
		// (zero keyword overlap alone) cannot read as a confident 0 and sink
		// below a known mismatch, while well-evidenced postings barely move.
		relMatch = clamp01((knownVal + relevanceShrink*prior) / (knownSum + relevanceShrink))
		relKnown = clamp01(knownSum)
		if shrink := w.MatchShrink; w.KnownMatch && finite(shrink) && shrink > 0 {
			match = clamp01((knownVal + shrink*prior) / (knownSum + shrink))
		}
	}

	basis, best := BasisNone, 0.0
	for i, bs := range [...]Basis{BasisFit, BasisRank, BasisSemantic} {
		if c := share[i] * parts[i].value; parts[i].known && c > best {
			basis, best = bs, c
		}
	}

	gate := 1.0
	// level is the factor a seniority penalty would take when the role_fit
	// reading is mismatch_seniority: both say the role's level is wrong, so the
	// gate below takes the stronger of the two, never their product.
	level := 1.0
	for _, p := range in.Penalties {
		if mismatch && p.Slot.Value == in.RoleFit.Value {
			continue // the same reading, penalised once by the mismatch gate below
		}
		f := 1 - clamp01(w.PenaltyStrength)*w.conf(p.Slot)
		if mismatch && in.RoleFit.Value == mismatchSeniority && p.Name == seniorityName {
			level = min(level, f)
			continue
		}
		gate *= f
	}
	if mismatch {
		gate *= min(level, 1-clamp01(w.Mismatch)*fitConf)
	}
	if excluded {
		gate *= clamp01(w.ExcludedTitle)
	}
	if in.Unsure {
		gate *= clamp01(w.UnsureGate)
	}
	gate = clamp01(gate)

	b.match, b.relMatch, b.relKnown, b.gate, b.basis = match, relMatch, relKnown, gate, basis
	if in.RoleFit.Backend == ShadowEscalate || in.RoleFit.Backend == BackendManual {
		// The fit rests on the escalation tier's reading or the owner's manual one
		// (SlotFor), not the primary's: the fit factor says so, and which option it chose.
		b.fitBackend, b.fitReading = in.RoleFit.Backend, in.RoleFit.Value
	}
	return b
}

// Close is the second stage of ScoreWith: freshness and the company factor,
// and the Result with its factor list.
func (b *Blend) Close(c Closing, w Weights) Result {
	fresh, freshNote := b.fresh(c, w)
	company := b.company(c, w)
	r := Result{
		Score:          round1(clamp(100*b.match*b.gate*fresh*company, 0, 100)),
		Relevance:      round1(clamp(100*b.relMatch*b.gate*company, 0, 100)),
		RelevanceKnown: b.relKnown,
		Match:          b.match, Gate: b.gate, Fresh: fresh, Company: company,
		Basis:   b.basis,
		Factors: make([]Factor, 0, len(b.parts)+3),
	}
	for i, p := range b.parts {
		if i == 6 && !p.known {
			continue // not asked: the list reads as it did before the part existed
		}
		f := Factor{Name: p.name, Value: p.value, Weight: b.nominal[i], Known: p.known}
		if p.known {
			f.Conf = p.conf
		}
		if i == 0 && b.fitBackend != "" {
			f.Backend, f.Reading = b.fitBackend, b.fitReading
		}
		r.Factors = append(r.Factors, f)
	}
	r.Factors = append(r.Factors,
		Factor{Name: "gate", Value: b.gate, Known: true},
		Factor{Name: "fresh", Value: fresh, Known: c.AgeKnown, Note: freshNote},
		Factor{Name: "company", Value: company, Known: true})
	return r
}

// Order is Close without the factor list: the Result's Score, Relevance,
// RelevanceKnown and Basis, for a caller that orders and filters many postings
// and lists the factors of only a few.
func (b *Blend) Order(c Closing, w Weights) Result {
	fresh, _ := b.fresh(c, w)
	company := b.company(c, w)
	return Result{
		Score:          round1(clamp(100*b.match*b.gate*fresh*company, 0, 100)),
		Relevance:      round1(clamp(100*b.relMatch*b.gate*company, 0, 100)),
		RelevanceKnown: b.relKnown,
		Match:          b.match, Gate: b.gate, Fresh: fresh, Company: company,
		Basis: b.basis,
	}
}

// fresh is the freshness factor and its note.
func (b *Blend) fresh(c Closing, w Weights) (float64, string) {
	floor := clamp01(w.FreshFloor)
	fresh := floor
	half := c.HalfLifeDays
	if !(half > 0 && half <= MaxHalfLifeDays) {
		half = w.HalfLifeDays
		if !(half > 0 && half <= MaxHalfLifeDays) {
			half = DefaultScoreHalfLifeDays
		}
	}
	freshNote := ""
	switch {
	case c.AgeKnown:
		fresh = floor + (1-floor)*math.Exp2(-float64(max(c.AgeDays, 0))/half)
	case c.SeenKnown && c.AgeNeutral:
		freshNote = NoteAgeNeutral
		// The employer closes by application cap, so how long ago Vani first saw an
		// undated posting is not a sign it has gone stale: no ageing, and still
		// short of a posting dated today.
		fresh = w.NeutralFresh
		if !(fresh > 0 && fresh <= 1) {
			fresh = DefaultNeutralFresh
		}
		fresh = max(fresh, floor)
	case c.SeenKnown:
		// An undated posting is no older than when Vani first saw it plus what a
		// listed posting typically already is: a lower bound and a prior, so it
		// ages from there instead of sitting at the very-old floor.
		assumed := w.UnknownAgeDays
		if !(assumed > 0 && assumed <= MaxHalfLifeDays) {
			assumed = unknownAgeHalfLives * half
		}
		fresh = floor + (1-floor)*math.Exp2(-(float64(max(c.SeenDays, 0))+assumed)/half)
	}
	return clamp01(fresh), freshNote
}

// company is the company factor. Saves and applies lift the company's other
// postings. Hides do not sink them: a hidden posting says that posting was
// wrong, not the company.
func (b *Blend) company(c Closing, w Weights) float64 {
	company := 1.0
	liked := float64(max(c.Company.Liked, 0))
	if liked > 0 && liked >= float64(w.CompanyMinMarks) && liked+w.CompanySmoothing > 0 {
		up := 0.0
		if finite(w.CompanySpan) {
			up = clamp(w.CompanySpan, 0, 1)
		}
		company = clamp(1+up*liked/(liked+w.CompanySmoothing), 1, 1+up)
	}
	return company
}
