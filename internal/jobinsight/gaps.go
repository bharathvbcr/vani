package jobinsight

// Gaps answers "what should I learn next": across a set of roles, which skills
// the roles ask for that the seeker does not yet show, weighted by how much the
// seeker wants each role. Display-only, like every other insight: it never
// drops or reorders a posting.
//
// A term is a gap in a role when either
//   - DevPrism found it missing or weak (the role's gap.missing annotation), or
//   - the role names it and the target summary does not (keyword gap).
//
// Keyword gaps need the target summary; without one (target == nil) only
// DevPrism gaps are counted and the report says so, so an unjudged set never
// reads as "no gaps".

// GapJob is one role's input to Gaps.
type GapJob struct {
	Key      string  // posting key, carried into examples
	Company  string  // as the board wrote it
	Title    string  // as the board wrote it
	Priority float64 // the jobs list priority, 0-100
	Hits     []Hit   // keyword hits, company blurb already removed
	Missing  string  // raw gap.missing annotation; JD-derived, untrusted
}

// GapOptions bounds a report. Zero values take the defaults.
type GapOptions struct {
	Limit    int // gaps returned; default DefaultGapLimit, at most MaxGapLimit
	Examples int // example roles per gap; default DefaultGapExamples, at most MaxGapExamples
}

const (
	DefaultGapLimit    = 25
	MaxGapLimit        = 200
	DefaultGapExamples = 3
	MaxGapExamples     = 10
)

// GapExample names one role that asks for the gap.
type GapExample struct {
	Key     string `json:"key"`
	Company string `json:"company"`
	Title   string `json:"title"`
}

// Gap is one skill the roles ask for that the seeker does not show.
type Gap struct {
	Term      string       `json:"term"`
	Category  string       `json:"cat"`
	Roles     int          `json:"roles"`     // roles where it is a gap
	Missing   int          `json:"missing"`   // of those, roles where DevPrism found it missing
	Weight    float64      `json:"weight"`    // sum of those roles' priority, one decimal
	Companies int          `json:"companies"` // distinct companies among those roles
	Examples  []GapExample `json:"examples"`
}

// GapReport is the result of Gaps.
type GapReport struct {
	Roles       int   `json:"roles"`        // distinct roles considered
	WithMissing int   `json:"with_missing"` // of those, roles carrying a gap.missing annotation
	TargetKnown bool  `json:"target_known"` // false: keyword gaps were not judged
	TotalGaps   int   `json:"total_gaps"`   // gaps found before Limit
	Gaps        []Gap `json:"gaps"`
}
