package jobinsight

import "math"

// Score is the one owner of how a posting is ordered for a person: the Jobs
// views' default and priority sorts, the Mac app's Home list (which reads
// priority) and the digest all rank by it. It is display-only (Rule 7): it
// sorts, it never drops a posting or changes its status.
//
// The shape: an additive match of what is known about fit (so one strong
// component is never erased by one zero), multiplied by gates (so a mismatch
// or an excluded title cannot be rescued by coverage), by freshness (so recent
// postings lead) and by a bounded company factor learned from marks. Every
// component that is unknown takes a prior strictly below "known good", so a
// posting the pipeline knows little about never outranks one it knows is a
// fit.

// Slot is one triage slot as Score reads it, from the ledger's
// triage.<slot>, triage.<slot>.reading, .p and .disagree annotations.
type Slot struct {
	// Value is the chosen option ("" when none was chosen).
	Value string
	// Answered is true when the reading was decide.Answered.
	Answered bool
	// P is Jev's calibrated probability of Value, when HasP.
	P    float64
	HasP bool
	// Disagree is true when the shadow backend answered a different value.
	Disagree bool
	// ShadowBackend names the shadow that was asked ("" when none was).
	ShadowBackend string
}

// ShadowEscalate is the escalation backend's name in the ledger. It is asked
// only when the primary's p is already under the escalation threshold, so the
// primary's confidence already carries that doubt, and DisagreeConf was tuned
// on Lappi shadow pairs, not on escalation: its disagreement does not lower
// the confidence again. A promoted escalation is to bring its own measured
// confidence when it comes.
const ShadowEscalate = "escalate"

// Penalty is a drop rule whose slot value matched a posting the policy kept
// (the rule's confidence threshold was not met). It softens the score in
// proportion to how sure the reading is. The rule's values never leave the
// caller: Score sees only the slot reading, so nothing here reveals them.
type Penalty struct {
	Slot Slot
}

// CompanyMarks counts a company's marked postings. Liked is saved plus
// applied; Hidden is hidden.
type CompanyMarks struct {
	Liked, Hidden int
}

// Inputs is everything Score reads about one posting. The zero value is a
// posting nothing is known about; every field is optional.
type Inputs struct {
	RoleFit Slot

	// Coverage is DevPrism's must-have coverage, 0-100 (the ledger's
	// coverage annotation, not the employer-boosted rank).
	Coverage    float64
	HasCoverage bool

	// PreferredEmployer is true when the employer index matched a preferred
	// employer for this company.
	PreferredEmployer bool

	// TitleSignals is true when the config has include or exclude title
	// signals; TitleRank is TitleSignals.Rank (-1 excluded, else the count of
	// distinct include signals the title names).
	TitleSignals bool
	TitleRank    int

	// Target is true when the config has a target summary; TitleHits and
	// BodyHits are the distinct target terms the title and the rest of the
	// posting name (company boilerplate removed).
	Target              bool
	TitleHits, BodyHits int

	// Semantic is the calibrated 0-1 similarity of the posting to the
	// owner's knowledge base, when HasSemantic (an embedding model read it).
	Semantic    float64
	HasSemantic bool

	// AgeKnown is true when AgeDays holds a real age. The zero value is
	// unknown (no plausible posted date, and first seen in its board's first
	// crawl), so a caller that forgets the age gets no freshness bonus.
	AgeKnown bool
	// AgeDays is whole days since the posting's freshness date, read only when
	// AgeKnown. A known negative age (clock skew) counts as today.
	AgeDays int
	// SeenKnown is true when SeenDays holds the days since Vani first saw the
	// posting. It is read only when AgeKnown is false: an undated posting from
	// its board's first crawl may be arbitrarily old, but it is no older than
	// that first sighting plus what a listed posting typically already is
	// (Weights.UnknownAgeDays), and it keeps ageing from then on.
	SeenKnown bool
	SeenDays  int
	// AgeNeutral is true when the posting's employer closes postings by an
	// application cap, not by age (the config's age_neutral_companies). Its age is
	// then no evidence of staleness: when AgeKnown is false the guess is held at
	// Weights.NeutralFresh and does not age. A date the board gave still counts.
	AgeNeutral bool

	// KBMatch is a language model's read of whether the posting's work is the
	// work in the owner's knowledge base (one of KBMatchOrder). Only the postings
	// that were asked have one; for the rest it is unknown and the part is left
	// out of the blend entirely, so they score exactly as without it.
	KBMatch Slot

	// Unsure is true when triage could not decide and kept the posting.
	Unsure bool

	// Penalties are the drop rules this posting matched but was kept by.
	Penalties []Penalty

	// Company counts the marks on this posting's company's other postings.
	Company CompanyMarks

	// HalfLifeDays is the age that halves the freshness bonus; outside
	// (0, MaxHalfLifeDays] means DefaultScoreHalfLifeDays.
	HalfLifeDays float64
}

// Factor is one component's contribution, for display and for tests.
type Factor struct {
	Name string `json:"name"`
	// Value is the component in [0,1] (a prior when Known is false).
	Value float64 `json:"value"`
	// Weight is its nominal share of match for additive components, 0 for
	// multipliers.
	Weight float64 `json:"weight"`
	Known  bool    `json:"known"`
	// Conf is how far a known additive component's reading is trusted, in
	// [0,1]: the blend uses Weight x Conf. 0 on a known mismatch fit, which the
	// gate penalises instead; omitted when unknown or a multiplier.
	Conf float64 `json:"conf,omitempty"`
	// Note says, for the few factors that need it, why the value is what it is
	// (a NoteAgeNeutral for a freshness held flat); empty otherwise.
	Note string `json:"note,omitempty"`
}

// NoteAgeNeutral is the fresh factor's Note when the employer is age-neutral and
// the posting undated: the value is a flat assumption, not an age.
const NoteAgeNeutral = "age_neutral"

// Result is Score's answer.
type Result struct {
	// Score is the 0-100 order key: match x gate x fresh x company.
	Score float64
	// Relevance is the 0-100 age-independent number the app shows and
	// --min-relevance filters on: how relevant, from what is known. It is
	// gate x company x the match of the KNOWN additive parts only, their
	// weights renormalized, so a confident strong fit is not shown as ~50
	// merely because coverage or similarity is missing on this machine. An
	// unpreferred employer is absence of evidence, not a known zero, so it does
	// not count as known. With no known part besides the employer it falls back
	// to match x gate x company. It is NOT the sort key: Score is, and Score
	// keeps the unknown prior so unknown never outranks known-good in the order.
	Relevance float64
	// RelevanceKnown is the share (0-1) of the additive weight Relevance rests
	// on; 0 when it fell back to the prior-filled match.
	RelevanceKnown float64
	// Match, Gate, Fresh and Company are the four parts, each in [0,1]
	// (Company in [CompanyMin, CompanyMax]).
	Match, Gate, Fresh, Company float64
	// Basis says what match mostly rests on.
	Basis Basis
	// Factors lists every component in a fixed order.
	Factors []Factor
}

// Weights are Score's tunable constants. DefaultWeights is what every caller
// uses; the evaluation harness sweeps others.
type Weights struct {
	// Additive shares of match. ScoreWith normalizes them to sum to 1.
	Fit, Coverage, Semantic, Keyword, Title, Employer float64
	// KBMatch is the share of a judged posting's blend that the language
	// model's read of its work takes. An unjudged posting has none.
	KBMatch float64
	// UnknownPrior is the value an unknown additive component takes.
	UnknownPrior float64
	// AnsweredConf is the confidence of an answered reading with no Jev p.
	AnsweredConf float64
	// DisagreeConf multiplies a reading's confidence when the shadow
	// backend disagreed, unless that shadow was ShadowEscalate.
	DisagreeConf float64
	// PenaltyStrength is how much a fully confident kept-by drop rule
	// removes: gate x (1 - PenaltyStrength*conf).
	PenaltyStrength float64
	// Mismatch is how much a fully confident mismatch_domain or
	// mismatch_seniority reading removes: gate x (1 - Mismatch*conf). As a
	// gate, no amount of coverage or similarity rescues a confident mismatch.
	Mismatch float64
	// ExcludedTitle and UnsureGate are the gates for an excluded title and
	// an unsure triage.
	ExcludedTitle, UnsureGate float64
	// FreshFloor is the freshness of a very old posting and of one whose
	// age is unknown; HalfLifeDays halves the rest.
	FreshFloor, HalfLifeDays float64
	// UnknownAgeDays is the age an undated posting is assumed to have had when
	// Vani first saw it; the days since then are added (Inputs.SeenDays). Zero or
	// unusable means unknownAgeHalfLives half-lives, so it follows the half-life
	// setting. A posting of unknown age first seen today thereby scores a
	// freshness of about 0.5, what the pre-Score priority gave an undated
	// posting, not the floor reserved for very old ones; the live ledger's dated
	// postings average 0.34, so this is a deliberate lean toward surfacing.
	UnknownAgeDays float64
	// NeutralFresh is the freshness of an undated posting whose employer is
	// age-neutral (Inputs.AgeNeutral) and was seen at all: flat, not aged. Outside
	// (0, 1] means DefaultNeutralFresh.
	NeutralFresh float64
	// CompanyMinMarks is the marks a company needs before its factor moves,
	// CompanySmoothing the pseudo-count. CompanySpan is how far liked marks
	// lift the factor above 1. Marks only lift: a hide says nothing about the
	// company's other postings.
	CompanyMinMarks               int
	CompanySmoothing, CompanySpan float64
	// FitValues are the role_fit options' values in fitOrder (strong_fit
	// first); the zero value means fitBase's.
	FitValues [5]float64
	// KnownMatch orders by the match of the known additive parts, shrunk
	// toward UnknownPrior by MatchShrink pseudo-weight, instead of letting
	// every unknown part take the prior inside the match. Unknown parts then
	// stop compressing the known ones' differences (with coverage and
	// similarity missing, the prior filled 70% of every match and freshness
	// decided the order); the shrink keeps an unknown posting below a known
	// good one.
	KnownMatch  bool
	MatchShrink float64
}

// fitOrder is role_fit's options from best to worst; fitBase gives each a
// value in this order.
var fitOrder = [...]string{"strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain"}

// fitValue is option v's value under w; ok is false for an unknown option.
func (w Weights) fitValue(v string) (val float64, ok bool) {
	for i, o := range fitOrder {
		if o != v {
			continue
		}
		if w.FitValues != ([5]float64{}) && finite(w.FitValues[i]) {
			return clamp01(w.FitValues[i]), true
		}
		return fitBase[o], true
	}
	return 0, false
}

// KBMatchOrder is the kb_match options from closest to the owner's work to
// furthest; kbMatchValues gives each a value in this order. internal/decide/kbmatch
// asks the question with exactly these options.
var KBMatchOrder = [...]string{"core", "partial", "adjacent", "unrelated"}

var kbMatchValues = [...]float64{1, 0.65, 0.3, 0}

// BasisSemantic is the knowledge-base similarity.
const BasisSemantic Basis = "semantic"

const (
	// DefaultScoreHalfLifeDays is the age that halves the freshness bonus.
	// The red-team worlds (rankeval) favoured 10 over 7 on both the mean and
	// the worst case; 21, the in-sample sweep's pick, collapsed for an owner
	// who wants very recent postings.
	DefaultScoreHalfLifeDays = 10.0
	// DefaultNeutralFresh is the freshness an undated posting of an age-neutral
	// employer holds: that of one about a week old at the default half-life (0.25
	// floor + 0.75 x 2^-0.5 = 0.78), above the 0.34 the live ledger's dated
	// postings average and below the 1.0 of one dated today.
	DefaultNeutralFresh = 0.78
	// CompanyMin and CompanyMax bound the company factor under
	// DefaultWeights (CompanySpan 0.10): marks only lift it.
	CompanyMin = 1.0
	CompanyMax = 1.1
)

// relevanceShrink is the prior's pseudo-weight in the displayed relevance,
// in the same units as the additive shares (which sum to 1).
const relevanceShrink = 0.10

// Score ranks in by DefaultWeights.
func Score(in Inputs) Result { return ScoreWith(in, DefaultWeights()) }

// DefaultWeights are the shipped constants.
func DefaultWeights() Weights {
	return Weights{
		// Meaning leads: role fit, the knowledge-base embedding match and DevPrism's
		// coverage carry the blend. Keyword overlap is a surface string, so it only
		// breaks ties (it was 0.10 against semantic's 0.25, and a keyword-rich
		// posting distant in meaning outranked a close one). On rankeval's worlds
		// keyword 0.03 / semantic 0.35 scores NDCG@10 0.7346 against 0.7341 before:
		// the metric is flat here, so the choice rests on the principle, not the gain.
		Fit: 0.30, Coverage: 0.20, Semantic: 0.35, Keyword: 0.03, Title: 0.10, Employer: 0.05,
		// A judged posting's blend gains the model's read of its work; an unjudged
		// one is unchanged (the part is left out, not filled with a prior).
		KBMatch:      0.15,
		UnknownPrior: 0.40, AnsweredConf: 0.70, DisagreeConf: 0.50,
		PenaltyStrength: 0.80, Mismatch: 0.60, ExcludedTitle: 0.25, UnsureGate: 0.85,
		FreshFloor: 0.25, HalfLifeDays: DefaultScoreHalfLifeDays, NeutralFresh: DefaultNeutralFresh,
		CompanyMinMarks: 0, CompanySmoothing: 8, CompanySpan: 0.10,
		// Thin evidence shrinks toward the prior hard: a posting known only by its
		// semantic score reads well below one with every part known at that value.
		KnownMatch: true, MatchShrink: 0.30,
	}
}

// unknownAgeHalfLives is the age, in half-lives, an undated posting is assumed
// to have when first seen (Weights.UnknownAgeDays unset): 2^-1.5 puts its
// freshness at 0.25 + 0.75*0.354 = 0.52 at the default floor.
const unknownAgeHalfLives = 1.5

// finite reports whether v is neither NaN nor infinite.
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// conf is how far a slot's reading is trusted, in [0,1].
func (w Weights) conf(s Slot) float64 {
	c := 0.0
	switch {
	case s.HasP && finite(s.P):
		c = clamp01(s.P)
	case s.Answered:
		c = clamp01(w.AnsweredConf)
	}
	if s.Disagree && s.ShadowBackend != ShadowEscalate {
		c *= clamp01(w.DisagreeConf)
	}
	return c
}

// ScoreWith ranks in by w. It is pure: the same inputs give the same Result,
// and nothing in it is dropped or changes status (Rule 7).
func ScoreWith(in Inputs, w Weights) Result {
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
		r.Factors = append(r.Factors, f)
	}
	r.Factors = append(r.Factors,
		Factor{Name: "gate", Value: gate, Known: true},
		Factor{Name: "fresh", Value: fresh, Known: in.AgeKnown, Note: freshNote},
		Factor{Name: "company", Value: company, Known: true})
	return r
}
