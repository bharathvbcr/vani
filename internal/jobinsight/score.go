package jobinsight

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
	// ExcludedTitle and UnsureGate are the gates for an excluded title and
	// an unsure triage.
	ExcludedTitle, UnsureGate float64
	// FreshFloor is the freshness of a very old posting and of one whose
	// age is unknown; HalfLifeDays halves the rest.
	FreshFloor, HalfLifeDays float64
	// CompanyMinMarks is the marks a company needs before its factor moves,
	// CompanySmoothing the pseudo-count, CompanySpan the factor's half-range
	// around 1.
	CompanyMinMarks               int
	CompanySmoothing, CompanySpan float64
}

// Score ranks in by DefaultWeights.
//
// TEMPORARY: the ranker body lands in the next commit; until then every
// posting scores zero, which the evaluation harness must report as failing.
func Score(in Inputs) Result { return ScoreWith(in, DefaultWeights()) }

// ScoreWith ranks in by w. TEMPORARY stub, see Score.
func ScoreWith(in Inputs, w Weights) Result { return Result{} }

// DefaultWeights are the shipped constants. TEMPORARY stub, see Score.
func DefaultWeights() Weights { return Weights{} }
