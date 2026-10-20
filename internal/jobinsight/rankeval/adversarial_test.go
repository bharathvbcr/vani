package rankeval

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// advCase is one adversarial comparison. order lists inputs best first; the
// ranker must score each strictly above the next (or all equal when equal is
// set). soft cases are logged, never asserted: their expected order is a
// judgement call, recorded in note.
type advCase struct {
	name   string
	order  []jobinsight.Inputs
	labels []string
	equal  bool
	soft   bool
	note   string
}

var advNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// ageVia runs the pipeline's freshness rule, so the date cases exercise
// FreshAge and Score together.
func ageVia(posted, firstSeen, boardFirstCrawl time.Time) int {
	at, _, known := jobinsight.FreshAge(posted, firstSeen, boardFirstCrawl, advNow)
	if !known {
		return -1
	}
	return jobinsight.AgeDays(at, advNow)
}

func with(f func(in *jobinsight.Inputs)) jobinsight.Inputs {
	in := good()
	f(&in)
	return in
}

func days(n int) time.Time { return advNow.Add(-time.Duration(n) * 24 * time.Hour) }

func advCases() []advCase {
	boardFirst := days(120)
	bogusFuture := with(func(in *jobinsight.Inputs) { in.AgeDays = ageVia(days(30).Add(5*24*time.Hour), days(30), boardFirst) })
	placeholder := with(func(in *jobinsight.Inputs) { in.AgeDays = ageVia(time.Unix(0, 0), days(40), boardFirst) })
	placeholderFirstCrawl := with(func(in *jobinsight.Inputs) { in.AgeDays = ageVia(time.Unix(0, 0), days(40), days(40)) })
	age := func(n int) jobinsight.Inputs { return with(func(in *jobinsight.Inputs) { in.AgeDays = n }) }
	strong := func(p float64) jobinsight.Inputs {
		return with(func(in *jobinsight.Inputs) { in.RoleFit = fit("strong_fit", p) })
	}

	return []advCase{
		{name: "posted date after first-seen+36h is distrusted (age falls back to first seen: 30d)",
			order: []jobinsight.Inputs{age(3), bogusFuture}, labels: []string{"honest 3d", "claims-future posted, really 30d"}},
		{name: "bogus future posted date scores exactly like an honest 30d posting",
			order: []jobinsight.Inputs{age(30), bogusFuture}, labels: []string{"honest 30d", "claims-future posted"}, equal: true},
		{name: "1970 placeholder posted date falls back to first seen (40d), not 'ancient' or 'new'",
			order: []jobinsight.Inputs{age(3), placeholder}, labels: []string{"honest 3d", "1970 posted, seen 40d ago"}},
		{name: "1970 placeholder with known first-seen beats the same placeholder in the board's first crawl",
			order: []jobinsight.Inputs{placeholder, placeholderFirstCrawl}, labels: []string{"1970, first seen after first crawl", "1970, first crawl (age unknown)"}},
		{name: "NaN coverage reads as unknown, below real coverage 60",
			order:  []jobinsight.Inputs{good(), with(func(in *jobinsight.Inputs) { in.Coverage = math.NaN() })},
			labels: []string{"coverage 60", "coverage NaN"}},
		{name: "coverage 105 is clamped to 100",
			order:  []jobinsight.Inputs{with(func(in *jobinsight.Inputs) { in.Coverage = 100 }), with(func(in *jobinsight.Inputs) { in.Coverage = 105 })},
			labels: []string{"coverage 100", "coverage 105"}, equal: true},
		{name: "first-crawl undated posting must not tie with a same-day dated one; dated wins",
			order:  []jobinsight.Inputs{age(0), age(-1)},
			labels: []string{"dated today", "first-crawl undated"}},
		{name: "keyword wall (title 0, body 500) loses to a modest real match (title 2, body 5)",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.TitleHits, in.BodyHits = 2, 5 }),
				with(func(in *jobinsight.Inputs) { in.TitleHits, in.BodyHits = 0, 500 })},
			labels: []string{"title 2 body 5", "title 0 body 500"}},
		{name: "abstained fit with coverage 95 vs answered strong_fit p0.9 coverage 40",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.RoleFit = fit("strong_fit", 0.9); in.Coverage = 40 }),
				with(func(in *jobinsight.Inputs) { in.RoleFit = jobinsight.Slot{}; in.Coverage = 95 })},
			labels: []string{"strong p0.9 cov 40", "abstained cov 95"}, soft: true,
			note: "coverage is biased low by synonyms and the reading is calibrated, so neither dominates; the order is logged, not asserted"},
		{name: "shadow disagreement on a low-confidence strong_fit lowers it below the agreed reading, which is below a confident one",
			order: []jobinsight.Inputs{strong(0.9), strong(0.52),
				with(func(in *jobinsight.Inputs) { in.RoleFit = fit("strong_fit", 0.52); in.RoleFit.Disagree = true })},
			labels: []string{"strong p0.9", "strong p0.52", "strong p0.52 disagree"}},
		{name: "Lappi-only strong_fit (answered, no p) sits between a confident reading and no reading",
			order: []jobinsight.Inputs{strong(0.99),
				with(func(in *jobinsight.Inputs) { in.RoleFit = jobinsight.Slot{Value: "strong_fit", Answered: true} }),
				with(func(in *jobinsight.Inputs) { in.RoleFit = jobinsight.Slot{} })},
			labels: []string{"strong p0.99", "strong answered no p", "no reading"}},
		{name: "unsure top-coverage posting ranks below the same posting not unsure",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.Coverage = 100 }),
				with(func(in *jobinsight.Inputs) { in.Coverage = 100; in.Unsure = true })},
			labels: []string{"coverage 100", "coverage 100 unsure"}},
		{name: "half-life 0 means the default half-life",
			order: []jobinsight.Inputs{age(30),
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = 0 }),
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = math.NaN() }),
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = -3 })},
			labels: []string{"unset", "0", "NaN", "-3"}, equal: true},
		{name: "half-life 400 exceeds the cap and means the default",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = 0 }),
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = 400 })},
			labels: []string{"0", "400"}, equal: true},
		{name: "a longer half-life keeps a 30d posting fresher",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = 100 }),
				with(func(in *jobinsight.Inputs) { in.AgeDays = 30; in.HalfLifeDays = 7 })},
			labels: []string{"half-life 100", "half-life 7"}},
		{name: "empty posting (age marked unknown) ranks below a known-good one",
			order:  []jobinsight.Inputs{good(), {AgeDays: -1}},
			labels: []string{"good", "empty, age unknown"}},
		{name: "zero-value Inputs: 'nothing known' vs the same with age unknown",
			order:  []jobinsight.Inputs{{AgeDays: -1}, {}},
			labels: []string{"empty, age unknown", "zero value (AgeDays 0 means brand new)"}, soft: true,
			note: "the zero value reads as age 0, i.e. newest; documented as 'nothing is known', so a caller that forgets AgeDays gets a freshness bonus"},
		{name: "excluded title with perfect coverage loses to an included title with modest coverage",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.TitleRank = 1; in.Coverage = 50 }),
				with(func(in *jobinsight.Inputs) { in.TitleRank = -1; in.Coverage = 100; in.RoleFit = fit("strong_fit", 0.9) })},
			labels: []string{"included title, partial, cov 50", "excluded title, strong p0.9, cov 100"}},
		{name: "senior/staff (mismatch_seniority p0.95, coverage 100) loses to strong_fit p0.8 coverage 60",
			order: []jobinsight.Inputs{
				with(func(in *jobinsight.Inputs) { in.RoleFit = fit("strong_fit", 0.8); in.Coverage = 60 }),
				with(func(in *jobinsight.Inputs) { in.RoleFit = fit("mismatch_seniority", 0.95); in.Coverage = 100 })},
			labels: []string{"strong p0.8 cov 60", "mismatch_seniority p0.95 cov 100"}},
	}
}

// legacyKeep reports, for a best-first pair, whether each legacy ranker also
// orders it that way (true) or inverts/ties it (false).
func legacyKeep(better, worse jobinsight.Inputs) string {
	rec := func(in jobinsight.Inputs, id int) legacyRec {
		a := max(in.AgeDays, 0)
		return legacyFrom(obs{id: id, in: in, legacyAge: a, firstSeenAge: a})
	}
	b, w := rec(better, 1), rec(worse, 2)
	ok := func(less func(a, b legacyRec) bool) string {
		if less(b, w) && !less(w, b) {
			return "ok"
		}
		return "WRONG"
	}
	return fmt.Sprintf("legacyDefault=%s legacyPriority=%s legacyDigest=%s",
		ok(legacyDefaultLess), ok(legacyPriorityLess), ok(legacyDigestLess))
}

func TestAdversarialOrdering(t *testing.T) {
	w := jobinsight.DefaultWeights()
	for _, c := range advCases() {
		t.Run(c.name, func(t *testing.T) {
			scores := make([]float64, len(c.order))
			for i, in := range c.order {
				r := jobinsight.ScoreWith(in, w)
				scores[i] = r.Score
				if !finite01(r.Score, 100) {
					t.Errorf("%s: score %v not finite in [0,100]", c.labels[i], r.Score)
				}
			}
			var msg string
			for i := 0; i+1 < len(scores); i++ {
				switch {
				case c.equal && math.Abs(scores[i]-scores[i+1]) > 1e-6:
					msg = fmt.Sprintf("want %q == %q, got %.4f vs %.4f", c.labels[i], c.labels[i+1], scores[i], scores[i+1])
				case !c.equal && !(scores[i] > scores[i+1]):
					msg = fmt.Sprintf("want %q > %q, got %.4f vs %.4f", c.labels[i], c.labels[i+1], scores[i], scores[i+1])
				}
				if c.equal {
					continue
				}
				t.Logf("  %-40s %.3f  | %s", c.labels[i], scores[i], legacyKeep(c.order[i], c.order[i+1]))
			}
			t.Logf("  %-40s %.3f", c.labels[len(c.labels)-1], scores[len(scores)-1])
			switch {
			case msg == "":
			case c.soft:
				t.Logf("SOFT (not asserted): %s -- %s", msg, c.note)
			default:
				t.Errorf("FAIL: %s", msg)
			}
			if c.soft && msg == "" {
				t.Logf("soft expectation held -- %s", c.note)
			}
		})
	}
}

// TestUnsureStillScores: unsure postings are kept (display-only ranking), so
// they must not collapse to zero.
func TestUnsureStillScores(t *testing.T) {
	in := with(func(in *jobinsight.Inputs) { in.Coverage = 100; in.Unsure = true })
	if r := jobinsight.Score(in); !(r.Score > 0) {
		t.Errorf("unsure posting scored %v, want > 0", r.Score)
	}
}

// TestOneCompanyCannotFlood: 10,000 mediocre postings from one company with 3
// liked marks must not push 20 clearly stronger unmarked postings out of the
// top 20.
func TestOneCompanyCannotFlood(t *testing.T) {
	type item struct {
		strong bool
		in     jobinsight.Inputs
	}
	var items []item
	for range 10000 {
		items = append(items, item{false, with(func(in *jobinsight.Inputs) {
			in.RoleFit = fit("partial_fit", 0.6)
			in.Coverage = 50
			in.Company = jobinsight.CompanyMarks{Liked: 3}
		})})
	}
	for range 20 {
		items = append(items, item{true, with(func(in *jobinsight.Inputs) { in.RoleFit = fit("strong_fit", 0.9); in.Coverage = 85 })})
	}
	// Put the strong ones last so a stable sort cannot help them.
	sc := make([]float64, len(items))
	for i, it := range items {
		sc[i] = jobinsight.Score(it.in).Score
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return sc[idx[a]] > sc[idx[b]] })
	for pos := range 20 {
		if !items[idx[pos]].strong {
			t.Fatalf("position %d is a liked-company mediocre posting (score %.2f); strong unmarked best %.2f", pos, sc[idx[pos]], sc[len(sc)-1])
		}
	}
}
