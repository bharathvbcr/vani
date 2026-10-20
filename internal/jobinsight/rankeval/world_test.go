// Package rankeval is an offline evaluation and red-team harness for Vani's
// job ranker. It holds only tests.
//
// The harness avoids the usual trap of ranking evaluations: labels are never
// computed from the features a ranker reads. Each synthetic world has a HIDDEN
// ground truth (true skill overlap including synonyms, true domain match, true
// seniority, true age, the persona's secret company taste). Rankers see it only
// through noisy and biased observations (a scanner that misses synonyms, a
// decision model that is right ~75-80% of the time, boards that give no posted
// date, keyword-inflating boilerplate), and graded relevance is computed from
// the hidden truth alone.
package rankeval

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

const (
	nDomains        = 6
	skillsPerDomain = 6
	nSkills         = nDomains * skillsPerDomain
	nCompanies      = 40
	nPostings       = 400
	maxAge          = 180
)

var roleFits = []string{"strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain"}

// skillDef is one skill with its surface forms; forms[0] is the canonical form.
type skillDef struct {
	domain int
	forms  []string
}

// universe returns the skill universe: every skill has a canonical form and
// two synonyms, so a scanner that only knows canonical forms misses them.
func universe() []skillDef {
	u := make([]skillDef, nSkills)
	for i := range u {
		u[i] = skillDef{
			domain: i / skillsPerDomain,
			forms:  []string{fmt.Sprintf("skill%02d", i), fmt.Sprintf("skill%02d-alias", i), fmt.Sprintf("s%02d-synonym", i)},
		}
	}
	return u
}

type company struct {
	preferred   bool
	affinity    int  // hidden: +1 persona likes, -1 dislikes
	boilerplate int  // extra keyword hits its posting boilerplate adds
	dated       bool // the board gives posted dates
	crawlAge    int  // days since the board's first crawl
	marks       jobinsight.CompanyMarks
}

type reqSkill struct{ skill, form int }

// posting is the hidden truth of one posting plus the bookkeeping the
// observation model needs.
type posting struct {
	id, company  int
	domain       int
	domainDeg    float64
	seniority    int
	seniFit      float64
	req          []reqSkill
	skillOverlap float64 // true overlap with the persona's skills, synonyms included
	age          int
	dated        bool
	firstCrawl   bool
	firstSeenAge int
	titleSkills  []int // persona skills named canonically in the title
	wall         bool  // a keyword wall in the body
	penaltyTrue  bool
	penaltyP     float64
	hasPenalty   bool
	rel          float64 // true relevance, age independent
	adj          float64 // rel after company taste and penalty, capped at 1
	trueFit      string
}

// world is one seeded synthetic persona, company set and posting set.
type world struct {
	seed      uint64
	skills    []skillDef
	persona   map[int]bool
	pDomain   int
	companies []company
	postings  []posting
	obs       []obs
}

// obs is what the rankers see of one posting, plus the legacy age bookkeeping
// the legacy rankers need.
type obs struct {
	id           int
	in           jobinsight.Inputs
	legacyAge    int // 0 for an undated posting in its board's first crawl
	firstSeenAge int // legacy newest-first-seen key (board first-crawl age when listed in it)
}

func domainDegree(persona, d int) float64 {
	dist := persona - d
	if dist < 0 {
		dist = -dist
	}
	if dist > nDomains/2 {
		dist = nDomains - dist
	}
	switch dist {
	case 0:
		return 1
	case 1:
		return 0.4
	case 2:
		return 0.1
	}
	return 0
}

// personaSeniority is the persona's level: early career.
const personaSeniority = 1

func seniorityFit(s int) float64 {
	d := s - personaSeniority
	if d < 0 {
		d = -d
	}
	return [...]float64{1, 0.7, 0.3, 0.08, 0.02}[min(d, 4)]
}

func trueRelevance(dom, seni, skill float64) float64 {
	return (0.55*dom + 0.45*skill) * (0.2 + 0.8*seni)
}

// trueFitClass is the label an ideal decision model would give.
func trueFitClass(dom, seni, skill float64) string {
	switch {
	case seni < 0.35:
		return "mismatch_seniority"
	case dom < 0.05:
		return "mismatch_domain"
	case dom < 0.95:
		return "adjacent"
	case skill >= 0.55:
		return "strong_fit"
	}
	return "partial_fit"
}

func clampf(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }

// newWorld builds the world for seed. It is deterministic.
func newWorld(seed uint64) *world {
	r := rand.New(rand.NewPCG(seed, seed*0x9e3779b97f4a7c15+1))
	w := &world{seed: seed, skills: universe(), persona: map[int]bool{}}

	// Persona: early-career, one home domain, 5 of its 6 skills and 2 others.
	w.pDomain = r.IntN(nDomains)
	for _, k := range r.Perm(skillsPerDomain)[:5] {
		w.persona[w.pDomain*skillsPerDomain+k] = true
	}
	for len(w.persona) < 7 {
		w.persona[r.IntN(nSkills)] = true
	}

	// Companies.
	for range nCompanies {
		c := company{
			preferred: r.Float64() < 0.15,
			dated:     r.Float64() < 0.6,
		}
		switch x := r.Float64(); {
		case x < 0.22:
			c.affinity = 1
		case x < 0.37:
			c.affinity = -1
		}
		if r.Float64() < 0.2 {
			c.boilerplate = 3 + r.IntN(8)
		}
		if r.Float64() < 0.6 {
			c.crawlAge = r.IntN(3)
		} else {
			c.crawlAge = 7 + r.IntN(24)
		}
		// Marks the persona left: few, and only informative for some.
		switch c.affinity {
		case 1:
			if r.Float64() < 0.6 {
				c.marks.Liked = 1 + r.IntN(3)
			}
			if r.Float64() < 0.1 {
				c.marks.Hidden = 1
			}
		case -1:
			if r.Float64() < 0.6 {
				c.marks.Hidden = 1 + r.IntN(3)
			}
			if r.Float64() < 0.1 {
				c.marks.Liked = 1
			}
		default:
			if r.Float64() < 0.1 {
				c.marks.Liked = 1
			}
			if r.Float64() < 0.1 {
				c.marks.Hidden = 1
			}
		}
		w.companies = append(w.companies, c)
	}

	// Postings.
	for id := range nPostings {
		p := posting{id: id, company: r.IntN(nCompanies)}
		co := w.companies[p.company]
		switch x := r.Float64(); {
		case x < 0.22:
			p.domain = w.pDomain
		default:
			p.domain = r.IntN(nDomains)
		}
		p.domainDeg = domainDegree(w.pDomain, p.domain)
		p.seniority = [...]int{0, 0, 1, 1, 1, 2, 2, 3, 4}[r.IntN(9)]
		p.seniFit = seniorityFit(p.seniority)
		// Four required skills from its own domain, one from anywhere.
		seen := map[int]bool{}
		for _, k := range r.Perm(skillsPerDomain)[:4] {
			seen[p.domain*skillsPerDomain+k] = true
		}
		for len(seen) < 5 {
			seen[r.IntN(nSkills)] = true
		}
		hit := 0
		for s := range seen {
			p.req = append(p.req, reqSkill{skill: s})
		}
		sortReq(p.req) // map order must not reach the random stream
		for k := range p.req {
			p.req[k].form = pickForm(r)
			if w.persona[p.req[k].skill] {
				hit++
			}
		}
		p.skillOverlap = float64(hit) / float64(len(p.req))
		p.age = r.IntN(maxAge + 1)
		p.dated = co.dated
		p.firstCrawl = p.age > co.crawlAge
		if p.firstCrawl {
			p.firstSeenAge = co.crawlAge
		} else {
			p.firstSeenAge = max(0, p.age-r.IntN(2))
		}
		for _, rq := range p.req {
			if w.persona[rq.skill] && rq.form == 0 && len(p.titleSkills) < 2 && r.Float64() < 0.3 {
				p.titleSkills = append(p.titleSkills, rq.skill)
			}
		}
		p.wall = r.Float64() < 0.02
		if r.Float64() < 0.04 {
			p.hasPenalty = true
			p.penaltyP = 0.5 + 0.45*r.Float64()
			p.penaltyTrue = r.Float64() < p.penaltyP
		}
		p.rel = trueRelevance(p.domainDeg, p.seniFit, p.skillOverlap)
		p.trueFit = trueFitClass(p.domainDeg, p.seniFit, p.skillOverlap)
		boost := 1.0
		switch co.affinity {
		case 1:
			boost = 1.25
		case -1:
			boost = 0.7
		}
		if co.preferred {
			boost *= 1.1
		}
		if p.penaltyTrue {
			boost *= 0.6
		}
		p.adj = math.Min(1, p.rel*boost)
		w.postings = append(w.postings, p)
	}
	w.observe(r)
	return w
}

func pickForm(r *rand.Rand) int {
	if r.Float64() < 0.5 {
		return 0
	}
	return 1 + r.IntN(2)
}

func sortReq(rs []reqSkill) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].skill < rs[j-1].skill; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// scanCanonical is the keyword scanner: it knows only canonical forms of the
// persona's skills, so a posting that writes a synonym scores low (biased).
func (w *world) scanCanonical(p *posting) (matched, total int) {
	total = len(p.req)
	for _, rq := range p.req {
		if w.persona[rq.skill] && w.skills[rq.skill].forms[rq.form] == w.skills[rq.skill].forms[0] {
			matched++
		}
	}
	return matched, total
}

// observe derives each posting's full-signal observation. Every random draw
// is independent of the true grade except through the hidden truth it
// distorts.
func (w *world) observe(r *rand.Rand) {
	for i := range w.postings {
		p := &w.postings[i]
		co := w.companies[p.company]
		in := jobinsight.Inputs{PreferredEmployer: co.preferred, Company: co.marks, TitleSignals: true, Target: true}

		// Decision model.
		switch u := r.Float64(); {
		case u < 0.10:
			// abstain: Answered stays false
			if r.Float64() < 0.3 {
				in.Unsure = true
			}
		default:
			in.RoleFit.Answered = true
			var correct bool
			if r.Float64() < 0.7 { // Jev present, roughly calibrated
				pp := 0.45 + 0.53*r.Float64()
				in.RoleFit.P, in.RoleFit.HasP = pp, true
				correct = r.Float64() < pp
			} else {
				correct = r.Float64() < 0.8
			}
			in.RoleFit.Value = p.trueFit
			if !correct {
				for {
					v := roleFits[r.IntN(len(roleFits))]
					if v != p.trueFit {
						in.RoleFit.Value = v
						break
					}
				}
			}
			dis := 0.1
			if !correct {
				dis = 0.6
			}
			in.RoleFit.Disagree = r.Float64() < dis
		}

		// DevPrism coverage: canonical forms only, plus scanner noise.
		if r.Float64() < 0.95 {
			m, t := w.scanCanonical(p)
			in.Coverage = clampf(100*float64(m)/float64(t)+r.NormFloat64()*4, 0, 100)
			in.HasCoverage = true
		}

		// Semantic: sees through synonyms, noisy and monotone.
		if r.Float64() < 0.9 {
			in.Semantic = clampf(0.1+0.8*p.skillOverlap+r.NormFloat64()*0.1, 0, 1)
			in.HasSemantic = true
		}

		// Title and body keyword signals.
		in.TitleHits = len(p.titleSkills)
		m, _ := w.scanCanonical(p)
		in.BodyHits = max(0, m-in.TitleHits) + co.boilerplate
		if p.wall {
			in.BodyHits = 100 + r.IntN(400)
		}
		switch {
		case p.seniority >= 3 && r.Float64() < 0.7, r.Float64() < 0.03:
			in.TitleRank = -1
		case p.domain == w.pDomain && r.Float64() < 0.7:
			in.TitleRank = 1 + r.IntN(2)
		}

		// Age.
		switch {
		case p.dated:
			in.AgeKnown, in.AgeDays = true, p.age
		case !p.firstCrawl:
			in.AgeKnown, in.AgeDays = true, p.firstSeenAge
		default:
			in.AgeDays = -1
		}

		if p.hasPenalty {
			in.Penalties = []jobinsight.Penalty{{Slot: jobinsight.Slot{Value: "far_onsite", Answered: true, P: p.penaltyP, HasP: true}}}
		}
		legacyAge := in.AgeDays
		if in.AgeDays < 0 {
			legacyAge = 0
		}
		w.obs = append(w.obs, obs{id: p.id, in: in, legacyAge: legacyAge, firstSeenAge: p.firstSeenAge})
	}
}

// recency is the true utility of age under half-life h.
func recency(age int, h float64) float64 {
	return 0.1 + 0.9*math.Exp2(-float64(age)/h)
}

// utility selects the hidden recency utility.
type utility struct {
	name     string
	halfLife float64
}

var utilities = []utility{{"sharp", 7}, {"gentle", 30}}

// grades is the hidden graded relevance (0-3) of every posting.
func (w *world) grades(u utility) []int {
	g := make([]int, len(w.postings))
	for i, p := range w.postings {
		v := p.adj * recency(p.age, u.halfLife)
		switch {
		case v >= 0.45:
			g[i] = 3
		case v >= 0.30:
			g[i] = 2
		case v >= 0.15:
			g[i] = 1
		}
	}
	return g
}
