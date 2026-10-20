package jobinsight

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

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

// Bounds on the untrusted text Gaps reads: the gap.missing annotation, and the
// company and title that become example labels.
const (
	maxMissingBytes = 4096
	maxMissingItems = 50
	maxLabelBytes   = 1024 // raw bytes read before cleaning
	maxLabelRunes   = 120
)

// gapRole is one deduplicated role with its inputs already bounded and cleaned.
type gapRole struct {
	key, company, title string
	companyID           string // lowercased, trimmed company, for counting
	priority            float64
	devprism            map[string]bool   // vocab terms DevPrism named missing
	keyword             map[string]string // hit terms the target lacks -> hit category
}

// gapAcc accumulates one term across roles.
type gapAcc struct {
	category  string
	roles     []*gapRole
	missing   int
	companies map[string]bool
}

// Gaps aggregates the skills roles ask for that the seeker does not show.
// target is the target-roles term set; nil means it was not judged, so only
// DevPrism gaps are counted. vocab may be nil, in which case DevPrism's
// annotation cannot be mapped to terms and only keyword gaps are found. The
// result does not depend on the order of jobs, except that of two jobs sharing
// a key the first wins.
func Gaps(jobs []GapJob, vocab *Vocab, target map[string]bool, opt GapOptions) GapReport {
	limit, examples := opt.Limit, opt.Examples
	if limit <= 0 {
		limit = DefaultGapLimit
	}
	limit = min(limit, MaxGapLimit)
	if examples <= 0 {
		examples = DefaultGapExamples
	}
	examples = min(examples, MaxGapExamples)

	report := GapReport{TargetKnown: target != nil, Gaps: []Gap{}}
	cats := vocab.categories()

	seen := map[string]bool{}
	var roles []*gapRole
	for _, j := range jobs {
		if j.Key == "" || seen[j.Key] {
			continue
		}
		seen[j.Key] = true
		r := &gapRole{
			key:      j.Key,
			company:  cleanLabel(j.Company),
			title:    cleanLabel(j.Title),
			priority: clampPriority(j.Priority),
			devprism: map[string]bool{},
			keyword:  map[string]string{},
		}
		r.companyID = strings.ToLower(r.company)
		if strings.TrimSpace(j.Missing) != "" {
			report.WithMissing++
			if vocab != nil {
				for t := range vocab.Terms(boundMissing(j.Missing)) {
					r.devprism[t] = true
				}
			}
		}
		if target != nil {
			for _, h := range j.Hits {
				if h.Term != "" && !target[h.Term] {
					r.keyword[h.Term] = h.Category
				}
			}
		}
		roles = append(roles, r)
	}
	report.Roles = len(roles)

	// Sum in key order so float addition does not depend on input order.
	sort.Slice(roles, func(i, j int) bool { return roles[i].key < roles[j].key })

	accs := map[string]*gapAcc{}
	touch := func(term, hitCat string, missing bool) {
		a := accs[term]
		if a == nil {
			a = &gapAcc{companies: map[string]bool{}}
			accs[term] = a
		}
		if a.category == "" {
			a.category = hitCat
		}
		if a.category == "" {
			a.category = cats[term]
		}
		if missing {
			a.missing++
		}
	}
	for _, r := range roles {
		in := map[string]bool{}
		for t := range r.devprism {
			touch(t, r.keyword[t], true)
			in[t] = true
		}
		for t, c := range r.keyword {
			if !in[t] {
				touch(t, c, false)
				in[t] = true
			}
		}
		for t := range in {
			a := accs[t]
			a.roles = append(a.roles, r)
			if r.companyID != "" {
				a.companies[r.companyID] = true
			}
		}
	}

	type ranked struct {
		gap    Gap
		weight float64
	}
	out := make([]ranked, 0, len(accs))
	for term, a := range accs {
		var w float64
		for _, r := range a.roles { // key order
			w += r.priority
		}
		sort.Slice(a.roles, func(i, j int) bool {
			x, y := a.roles[i], a.roles[j]
			switch {
			case x.priority != y.priority:
				return x.priority > y.priority
			case x.company != y.company:
				return x.company < y.company
			case x.title != y.title:
				return x.title < y.title
			}
			return x.key < y.key
		})
		ex := make([]GapExample, 0, min(examples, len(a.roles)))
		for _, r := range a.roles[:min(examples, len(a.roles))] {
			ex = append(ex, GapExample{Key: r.key, Company: r.company, Title: r.title})
		}
		out = append(out, ranked{weight: w, gap: Gap{
			Term: term, Category: a.category, Roles: len(a.roles), Missing: a.missing,
			Weight: round1(w), Companies: len(a.companies), Examples: ex,
		}})
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		switch {
		case x.weight != y.weight:
			return x.weight > y.weight
		case x.gap.Roles != y.gap.Roles:
			return x.gap.Roles > y.gap.Roles
		case x.gap.Missing != y.gap.Missing:
			return x.gap.Missing > y.gap.Missing
		}
		return x.gap.Term < y.gap.Term
	})
	report.TotalGaps = len(out)
	for _, r := range out[:min(limit, len(out))] {
		report.Gaps = append(report.Gaps, r.gap)
	}
	return report
}

// categories maps term name to category, stop phrases aside. A nil vocabulary
// has none.
func (v *Vocab) categories() map[string]string {
	if v == nil {
		return nil
	}
	m := make(map[string]string, len(v.terms))
	for _, t := range v.terms {
		if t.category != stopCategory {
			m[t.name] = t.category
		}
	}
	return m
}

// boundMissing keeps the first maxMissingBytes of a gap.missing annotation (cut
// on a rune boundary) and its first maxMissingItems ';'-separated items.
func boundMissing(s string) string {
	s = cutRunes(s, maxMissingBytes)
	parts := strings.SplitN(s, ";", maxMissingItems+1)
	if len(parts) > maxMissingItems {
		parts = parts[:maxMissingItems]
	}
	return strings.Join(parts, ";")
}

// cutRunes returns at most n bytes of s without splitting a rune.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cleanLabel makes board-supplied text safe to show: control and format
// characters become spaces, whitespace collapses, and the result is cut to
// maxLabelRunes.
func cleanLabel(s string) string {
	s = strings.ToValidUTF8(cutRunes(s, maxLabelBytes), "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxLabelRunes {
		s = strings.TrimSpace(string(r[:maxLabelRunes]))
	}
	return s
}

// clampPriority maps a priority into 0-100; NaN and negatives become 0.
func clampPriority(p float64) float64 {
	if math.IsNaN(p) || p < 0 {
		return 0
	}
	return min(p, 100)
}
