package jobinsight

import "math"

// scoreWithOracle is ScoreWith as it was before it was split into BlendOf and
// Blend.Close (2026-11-11), kept verbatim as the oracle the split is checked
// against bit for bit (TestBlendCloseIsScoreWithBitForBit). Do not change it.
func scoreWithOracle(in Inputs, w Weights) Result {
	prior := clamp01(w.UnknownPrior)

	type part struct {
		name   string
		weight float64
		value  float64
		known  bool
		// conf is how far the part's reading is trusted. It scales the part's
		// WEIGHT in the blend, never its value: a doubtful strong_fit says little,
		// it does not say "somewhere between strong and partial".
		conf float64
	}
	var parts [7]part

	fitConf := w.conf(in.RoleFit)
	base, ok := w.fitValue(in.RoleFit.Value)
	if !ok {
		fitConf = 0
	}
	parts[0] = part{"fit", w.Fit, base, fitConf > 0, fitConf}
	// A confident mismatch is penalised once, by the gate below. Its low fit
	// value would penalise the same reading a second time, so the part leaves the
	// blend (the gate, not the match, is what no coverage can rescue).
	mismatch := fitConf > 0 && (in.RoleFit.Value == "mismatch_domain" || in.RoleFit.Value == "mismatch_seniority")
	if mismatch {
		parts[0].conf = 0
	}

	parts[1] = part{"coverage", w.Coverage, prior, false, 1}
	if in.HasCoverage && finite(in.Coverage) {
		parts[1].value, parts[1].known = clamp01(in.Coverage/100), true
	}

	parts[2] = part{"semantic", w.Semantic, prior, false, 1}
	if in.HasSemantic && finite(in.Semantic) {
		parts[2].value, parts[2].known = clamp01(in.Semantic), true
	}

	parts[3] = part{"keyword", w.Keyword, prior, false, 1}
	if in.Target {
		t, b := float64(max(in.TitleHits, 0)), float64(max(in.BodyHits, 0))
		parts[3].value = clamp01(0.6*(1-math.Exp(-t/1.5)) + 0.4*(1-math.Exp(-b/3)))
		parts[3].known = true
	}

	excluded := in.TitleSignals && in.TitleRank < 0
	parts[4] = part{"title", w.Title, prior, false, 1}
	if in.TitleSignals {
		parts[4].known = true
		if excluded {
			parts[4].value = 0
		} else {
			parts[4].value = float64(min(in.TitleRank, 3)) / 3
		}
	}

	parts[5] = part{"employer", w.Employer, 0, true, 1}
	if in.PreferredEmployer {
		parts[5].value = 1
	}

	// The model's read of the work, when this posting was asked and answered with
	// a listed option. Unknown, it takes no share at all rather than the prior, so
	// the postings nobody asked about score as they would without the part.
	parts[6] = part{"kb_match", w.KBMatch, prior, false, 1}
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
	match, share, nominal := prior, [len(parts)]float64{}, [len(parts)]float64{}
	if sum > 0 {
		match = 0
		var used float64
		for i, p := range parts {
			nominal[i] = p.weight / sum
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
	for i, b := range [...]Basis{BasisFit, BasisRank, BasisSemantic} {
		if c := share[i] * parts[i].value; parts[i].known && c > best {
			basis, best = b, c
		}
	}

	gate := 1.0
	for _, p := range in.Penalties {
		if mismatch && p.Slot.Value == in.RoleFit.Value {
			continue // the same reading, penalised once by the mismatch gate below
		}
		gate *= 1 - clamp01(w.PenaltyStrength)*w.conf(p.Slot)
	}
	if mismatch {
		gate *= 1 - clamp01(w.Mismatch)*fitConf
	}
	if excluded {
		gate *= clamp01(w.ExcludedTitle)
	}
	if in.Unsure {
		gate *= clamp01(w.UnsureGate)
	}
	gate = clamp01(gate)

	floor := clamp01(w.FreshFloor)
	fresh := floor
	half := in.HalfLifeDays
	if !(half > 0 && half <= MaxHalfLifeDays) {
		half = w.HalfLifeDays
		if !(half > 0 && half <= MaxHalfLifeDays) {
			half = DefaultScoreHalfLifeDays
		}
	}
	freshNote := ""
	switch {
	case in.AgeKnown:
		fresh = floor + (1-floor)*math.Exp2(-float64(max(in.AgeDays, 0))/half)
	case in.SeenKnown && in.AgeNeutral:
		freshNote = NoteAgeNeutral
		// The employer closes by application cap, so how long ago Vani first saw an
		// undated posting is not a sign it has gone stale: no ageing, and still
		// short of a posting dated today.
		fresh = w.NeutralFresh
		if !(fresh > 0 && fresh <= 1) {
			fresh = DefaultNeutralFresh
		}
		fresh = max(fresh, floor)
	case in.SeenKnown:
		// An undated posting is no older than when Vani first saw it plus what a
		// listed posting typically already is: a lower bound and a prior, so it
		// ages from there instead of sitting at the very-old floor.
		assumed := w.UnknownAgeDays
		if !(assumed > 0 && assumed <= MaxHalfLifeDays) {
			assumed = unknownAgeHalfLives * half
		}
		fresh = floor + (1-floor)*math.Exp2(-(float64(max(in.SeenDays, 0))+assumed)/half)
	}
	fresh = clamp01(fresh)

	// Saves and applies lift the company's other postings. Hides do not sink
	// them: a hidden posting says that posting was wrong, not the company.
	company := 1.0
	liked := float64(max(in.Company.Liked, 0))
	if liked > 0 && liked >= float64(w.CompanyMinMarks) && liked+w.CompanySmoothing > 0 {
		up := 0.0
		if finite(w.CompanySpan) {
			up = clamp(w.CompanySpan, 0, 1)
		}
		company = clamp(1+up*liked/(liked+w.CompanySmoothing), 1, 1+up)
	}

	r := Result{
		Score:          round1(clamp(100*match*gate*fresh*company, 0, 100)),
		Relevance:      round1(clamp(100*relMatch*gate*company, 0, 100)),
		RelevanceKnown: relKnown,
		Match:          match, Gate: gate, Fresh: fresh, Company: company,
		Basis:   basis,
		Factors: make([]Factor, 0, len(parts)+3),
	}
	for i, p := range parts {
		if i == 6 && !p.known {
			continue // not asked: the list reads as it did before the part existed
		}
		f := Factor{Name: p.name, Value: p.value, Weight: nominal[i], Known: p.known}
		if p.known {
			f.Conf = p.conf
		}
		if i == 0 && (in.RoleFit.Backend == ShadowEscalate || in.RoleFit.Backend == BackendManual) {
			// The fit rests on the escalation tier's reading or the owner's manual one
			// (SlotFor), not the primary's: say so, and which option it chose.
			f.Backend, f.Reading = in.RoleFit.Backend, in.RoleFit.Value
		}
		r.Factors = append(r.Factors, f)
	}
	r.Factors = append(r.Factors,
		Factor{Name: "gate", Value: gate, Known: true},
		Factor{Name: "fresh", Value: fresh, Known: in.AgeKnown, Note: freshNote},
		Factor{Name: "company", Value: company, Known: true})
	return r
}
