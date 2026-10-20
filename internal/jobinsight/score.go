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
}

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

	// AgeDays is whole days since the posting's freshness date; negative
	// means unknown (no plausible posted date, and first seen in its board's
	// first crawl).
	AgeDays int

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
	// Weight is its share of match for additive components, 0 for
	// multipliers.
	Weight float64 `json:"weight"`
	Known  bool    `json:"known"`
}

// Result is Score's answer.
type Result struct {
	// Score is the 0-100 order key: match x gate x fresh x company.
	Score float64
	// Relevance is the 0-100 age-independent part: match x gate x company.
	Relevance float64
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
	// UnknownPrior is the value an unknown additive component takes.
	UnknownPrior float64
	// AnsweredConf is the confidence of an answered reading with no Jev p.
	AnsweredConf float64
	// DisagreeConf multiplies a reading's confidence when the shadow
	// backend disagreed.
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
	// CompanyMinMarks is the marks a company needs before its factor moves,
	// CompanySmoothing the pseudo-count. CompanySpan is how far liked marks
	// lift the factor above 1, CompanyDown how far hidden marks sink it below:
	// hiding is the stronger signal, so it may move the factor further.
	CompanyMinMarks                            int
	CompanySmoothing, CompanySpan, CompanyDown float64
}

// BasisSemantic is the knowledge-base similarity.
const BasisSemantic Basis = "semantic"

const (
	// DefaultScoreHalfLifeDays is the age that halves the freshness bonus.
	// The red-team worlds (rankeval) favoured 10 over 7 on both the mean and
	// the worst case; 21, the in-sample sweep's pick, collapsed for an owner
	// who wants very recent postings.
	DefaultScoreHalfLifeDays = 10.0
	// CompanyMin and CompanyMax bound the company factor under
	// DefaultWeights (CompanyDown 0.40, CompanySpan 0.10).
	CompanyMin = 0.6
	CompanyMax = 1.1
)

// Score ranks in by DefaultWeights.
func Score(in Inputs) Result { return ScoreWith(in, DefaultWeights()) }

// DefaultWeights are the shipped constants.
func DefaultWeights() Weights {
	return Weights{
		Fit: 0.30, Coverage: 0.20, Semantic: 0.25, Keyword: 0.10, Title: 0.10, Employer: 0.05,
		UnknownPrior: 0.40, AnsweredConf: 0.70, DisagreeConf: 0.50,
		PenaltyStrength: 0.60, Mismatch: 0.60, ExcludedTitle: 0.25, UnsureGate: 0.85,
		FreshFloor: 0.25, HalfLifeDays: DefaultScoreHalfLifeDays,
		CompanyMinMarks: 3, CompanySmoothing: 5, CompanySpan: 0.10, CompanyDown: 0.40,
	}
}

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
	if s.Disagree {
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
	}
	var parts [6]part

	fitConf := w.conf(in.RoleFit)
	base, ok := fitBase[in.RoleFit.Value]
	if !ok {
		fitConf = 0
	}
	parts[0] = part{"fit", w.Fit, fitConf*base + (1-fitConf)*prior, fitConf > 0}

	parts[1] = part{"coverage", w.Coverage, prior, false}
	if in.HasCoverage && finite(in.Coverage) {
		parts[1].value, parts[1].known = clamp01(in.Coverage/100), true
	}

	parts[2] = part{"semantic", w.Semantic, prior, false}
	if in.HasSemantic && finite(in.Semantic) {
		parts[2].value, parts[2].known = clamp01(in.Semantic), true
	}

	parts[3] = part{"keyword", w.Keyword, prior, false}
	if in.Target {
		t, b := float64(max(in.TitleHits, 0)), float64(max(in.BodyHits, 0))
		parts[3].value = clamp01(0.6*(1-math.Exp(-t/1.5)) + 0.4*(1-math.Exp(-b/3)))
		parts[3].known = true
	}

	excluded := in.TitleSignals && in.TitleRank < 0
	parts[4] = part{"title", w.Title, prior, false}
	if in.TitleSignals {
		parts[4].known = true
		if excluded {
			parts[4].value = 0
		} else {
			parts[4].value = float64(min(in.TitleRank, 3)) / 3
		}
	}

	parts[5] = part{"employer", w.Employer, 0, true}
	if in.PreferredEmployer {
		parts[5].value = 1
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
	match, share := prior, [6]float64{}
	if sum > 0 {
		match = 0
		for i, p := range parts {
			share[i] = p.weight / sum
			match += share[i] * p.value
		}
		match = clamp01(match)
	}

	basis, best := BasisNone, 0.0
	for i, b := range [...]Basis{BasisFit, BasisRank, BasisSemantic} {
		if c := share[i] * parts[i].value; parts[i].known && c > best {
			basis, best = b, c
		}
	}

	gate := 1.0
	for _, p := range in.Penalties {
		gate *= 1 - clamp01(w.PenaltyStrength)*w.conf(p.Slot)
	}
	if fitConf > 0 && (in.RoleFit.Value == "mismatch_domain" || in.RoleFit.Value == "mismatch_seniority") {
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
	if in.AgeDays >= 0 {
		half := in.HalfLifeDays
		if !(half > 0 && half <= MaxHalfLifeDays) {
			half = w.HalfLifeDays
			if !(half > 0 && half <= MaxHalfLifeDays) {
				half = DefaultScoreHalfLifeDays
			}
		}
		fresh = floor + (1-floor)*math.Exp2(-float64(in.AgeDays)/half)
	}
	fresh = clamp01(fresh)

	company := 1.0
	liked, hidden := float64(max(in.Company.Liked, 0)), float64(max(in.Company.Hidden, 0))
	up, down := 0.0, 0.0
	if finite(w.CompanySpan) {
		up = clamp(w.CompanySpan, 0, 1)
	}
	if finite(w.CompanyDown) {
		down = clamp(w.CompanyDown, 0, 1)
	}
	if n := liked + hidden; n >= float64(w.CompanyMinMarks) && n > 0 && n+w.CompanySmoothing > 0 {
		lean := (liked - hidden) / (n + w.CompanySmoothing)
		if lean >= 0 {
			company = clamp(1+up*lean, 1, 1+up)
		} else {
			company = clamp(1+down*lean, 1-down, 1)
		}
	}

	r := Result{
		Score:     round1(clamp(100*match*gate*fresh*company, 0, 100)),
		Relevance: round1(clamp(100*match*gate*company, 0, 100)),
		Match:     match, Gate: gate, Fresh: fresh, Company: company,
		Basis:   basis,
		Factors: make([]Factor, 0, 9),
	}
	for i, p := range parts {
		r.Factors = append(r.Factors, Factor{Name: p.name, Value: p.value, Weight: share[i], Known: p.known})
	}
	r.Factors = append(r.Factors,
		Factor{Name: "gate", Value: gate, Known: true},
		Factor{Name: "fresh", Value: fresh, Known: true},
		Factor{Name: "company", Value: company, Known: true})
	return r
}
