package jobview

import (
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A selection works on a compact candidate (cand) per job, not on a Job: a
// view holds tens of thousands of jobs, and a Job (some sixty fields, maps,
// cleaned free text) built for each was most of a list's cost and garbage.
// A Job is built only for the jobs a reply returns: the page of a list, or
// every selected job for Select's callers.

// Ranked is what PageRanker.Rank fills for one job: what selection, the
// filters, every sort and the stats read. Apply puts it on a Job.
type Ranked struct {
	Places       []Place
	RemoteListed bool
	RemoteScope  string
	// FreshAt, FreshSrc and AgeDays are set when AgeKnown.
	FreshAt  time.Time
	FreshSrc string
	AgeDays  int
	AgeKnown bool
	// The score; zero for an untriaged posting, which Rank does not score.
	Priority, Relevance, RelevanceKnown float64
	Basis                               jobinsight.Basis
	// Pay is set when PayRead (Rank was asked withPay).
	Pay     []jobinsight.Pay
	PayRead bool
}

// Apply sets j's ranked fields from rk, as ranking a whole Job set them.
func (rk *Ranked) Apply(j *Job) {
	j.Places, j.RemoteListed, j.RemoteScope = rk.Places, rk.RemoteListed, rk.RemoteScope
	if rk.AgeKnown {
		age := rk.AgeDays
		j.FreshAt, j.FreshSrc, j.AgeDays = rk.FreshAt, rk.FreshSrc, &age
	}
	j.Priority, j.Relevance, j.RelevanceBasis, j.RelevanceKnown = rk.Priority, rk.Relevance, rk.Basis, rk.RelevanceKnown
	if rk.PayRead {
		j.Pay = rk.Pay
	}
}

// rankedOf is what Rank would have filled, read back from an enriched Job.
func rankedOf(j *Job) Ranked {
	rk := Ranked{Places: j.Places, RemoteListed: j.RemoteListed, RemoteScope: j.RemoteScope,
		Priority: j.Priority, Relevance: j.Relevance, RelevanceKnown: j.RelevanceKnown, Basis: j.RelevanceBasis,
		Pay: j.Pay, PayRead: true}
	if j.AgeDays != nil {
		rk.FreshAt, rk.FreshSrc, rk.AgeDays, rk.AgeKnown = j.FreshAt, j.FreshSrc, *j.AgeDays, true
	}
	return rk
}

// EnrichWith is Enrich for a PageRanker: Rank (with pay), Apply, Finish.
func EnrichWith(pr PageRanker, j *Job, r ledger.Record, kwLimit int) []jobinsight.Hit {
	var rk Ranked
	pr.Rank(&rk, r, true)
	rk.Apply(j)
	return pr.Finish(j, r, kwLimit, true)
}

// cand is one job in a selection.
type cand struct {
	rec      int // index of its record in the snapshot
	key      posting.Key
	st       rowStatic
	closed   bool
	markedAt time.Time
	rk       Ranked
	pay      float64 // payKey in --currency
	hits     []jobinsight.Hit
	similar  int
	// job is the whole Job when a plain Ranker enriched it (no PageRanker).
	job *Job
}

// candOfJob is the cand an enriched Job stands for.
func candOfJob(j *Job, hits []jobinsight.Hit, pay float64) cand {
	cat := j.Category
	if cat == "" && j.Triage != nil {
		cat = j.Triage["category"]
	}
	return cand{key: j.Key, closed: j.Closed, markedAt: j.MarkedAt, rk: rankedOf(j), pay: pay, hits: hits, similar: j.Similar,
		st: rowStatic{company: j.Company, title: j.Title, fit: j.Triage["role_fit"], sponsorship: j.Triage["sponsorship"],
			workMode: j.Triage["work_mode"], seniority: j.Triage["seniority"], category: cat}}
}

// selectArgs are what one selection reads beyond the snapshot and marks.
type selectArgs struct {
	withPay  bool // rank pay (the order, a filter or the stats read it)
	withHits bool // keep each job's keyword hits (stats, gaps)
	kwLimit  int  // a plain Ranker's Enrich keyword limit
}

// selectCands is the one selection: the cands in o's view that pass its query
// and filters, in record order, and the per-view counts (which ignore the
// query and filters).
func selectCands(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *Opts, rk Ranker, a selectArgs) ([]cand, map[string]int) {
	counts := map[string]int{}
	q := strings.ToLower(strings.TrimSpace(o.Query))
	inView := 0
	for _, r := range snap.Records {
		if InView(o.View, r, m[r.Key].Mark) {
			inView++
		}
	}
	sel := make([]cand, 0, inView)
	var areas map[string]*AreaStat
	var census Stats
	if o.where != nil {
		if o.where.State == nil {
			o.where.State = map[string]int{}
		}
		if o.where.Country == nil {
			o.where.Country = map[string]int{}
		}
		areas = map[string]*AreaStat{}
		census = Stats{State: o.where.State, Country: o.where.Country, RemoteScope: map[string]int{}}
	}
	placeOn := o.Remote || o.States != nil || o.Areas != nil || o.Countries != nil
	pr, _ := rk.(PageRanker)
	for i, r := range snap.Records {
		entry := m[r.Key]
		for _, v := range Views {
			if InView(v, r, entry.Mark) {
				counts[v]++
			}
		}
		if !InView(o.View, r, entry.Mark) {
			continue
		}
		var c cand
		if pr == nil {
			j := ToJob(r, entry)
			c = cand{st: rowStatic{company: j.Company, title: j.Title}, job: &j}
		} else {
			c = cand{st: staticOf(r)}
		}
		c.rec, c.key, c.closed, c.markedAt = i, r.Key, r.Status == posting.Dead, entry.MarkedAt
		if q != "" && !matchesQuery(q, c.st.title, c.st.company, CleanText(r.Posting.Location, FieldRunes, false),
			CleanText(r.Posting.Department, FieldRunes, false), entry.Note) {
			continue
		}
		if job := c.job; job != nil {
			c = candOfJob(job, nil, 0)
			c.rec, c.job = i, job
		}
		if !InSet(o.Fits, c.st.fit) || !InSet(o.WorkModes, c.st.workMode) || !InSet(o.Sponsors, c.st.sponsorship) {
			continue
		}
		if o.Open && c.closed {
			continue
		}
		if o.Stages != nil && (entry.Mark != marks.Applied || !o.Stages[entry.Stage]) {
			continue
		}
		// Without a menu census, drop other places before ranking them.
		// With one, rank them too and count them, then drop them from the list.
		if placeOn && o.where == nil {
			var located Ranked
			located.Locate(r.Posting)
			if !o.placeOK(located.Places, located.RemoteListed, located.RemoteScope) {
				continue
			}
		}
		if pr != nil {
			pr.Rank(&c.rk, r, a.withPay)
			if a.withHits {
				c.hits = pr.Hits(r)
			}
		} else {
			hits := rk.Enrich(c.job, r, a.kwLimit)
			job := c.job
			c = candOfJob(job, hits, 0)
			c.rec, c.job = i, job
		}
		if o.MaxAge > 0 && (!c.rk.AgeKnown || c.rk.AgeDays > o.MaxAge) {
			continue
		}
		var pay []jobinsight.Pay
		if c.rk.PayRead {
			pay = c.rk.Pay
		}
		if !o.More.keep(c.st.seniority, c.st.category, c.st.company, c.st.title, c.rk.Relevance, pay) {
			continue
		}
		if o.where != nil {
			countPlacesIn(&census, areas, c.rk.Places, c.rk.RemoteListed, c.rk.RemoteScope)
			if placeOn && !o.placeOK(c.rk.Places, c.rk.RemoteListed, c.rk.RemoteScope) {
				continue
			}
		}
		c.pay = payKey(pay, o.More.Currency)
		sel = append(sel, c)
	}
	if o.where != nil {
		o.where.Area = rankedAreas(areas)
	}
	return sel, counts
}

// Brief is a selected job without its whole Job: what `jobs gaps` reads.
type Brief struct {
	Key            posting.Key
	Company, Title string // as the Job shows them
	Priority       float64
	Hits           []jobinsight.Hit // as Enrich returns them
	Record         int              // index of the job's record in the snapshot
}

// SelectBriefs is Select without building or finishing a Job, in record order.
func SelectBriefs(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *Opts, rk Ranker) []Brief {
	cs, _ := selectCands(snap, m, o, rk, selectArgs{withPay: true, withHits: true})
	out := make([]Brief, len(cs))
	for i := range cs {
		c := &cs[i]
		out[i] = Brief{Key: c.key, Company: c.st.company, Title: c.st.title, Priority: c.rk.Priority, Hits: c.hits, Record: c.rec}
	}
	return out
}

// materialize is the whole Job a cand stands for, finished for display: what
// enriching it whole gives.
func materialize(c *cand, snap ledger.Snapshot, m map[posting.Key]marks.Entry, rk Ranker, kwLimit int) (Job, []jobinsight.Hit) {
	if c.job != nil {
		j := *c.job
		j.Similar = c.similar
		return j, c.hits
	}
	r := snap.Records[c.rec]
	j := toJob(r, m[r.Key], c.st)
	c.rk.Apply(&j)
	j.Similar = c.similar
	hits := rk.(PageRanker).Finish(&j, r, kwLimit, c.rk.PayRead)
	return j, hits
}

// sortKey is what the orders read of one job.
type sortKey struct {
	key                      posting.Key
	priority, relevance, pay float64
	freshAt, markedAt        time.Time
	company                  string
}

func (c *cand) sortKey() sortKey {
	k := sortKey{key: c.key, pay: c.pay, markedAt: c.markedAt, company: c.st.company,
		priority: c.rk.Priority, relevance: c.rk.Relevance}
	if c.rk.AgeKnown {
		k.freshAt = c.rk.FreshAt
	}
	return k
}

// sortOrder is the stable order of keys under the view's order by (Sort).
func sortOrder(keys []sortKey, view, by string) []int {
	by = EffectiveSort(view, by)
	byMarkTime := by == "default" && (view == "saved" || view == "applied" || view == "hidden")
	var lower []string // each job's company, lower-cased once, for the orders that compare it
	if by == "company" || by == "fresh" && view == ViewPending {
		lower = make([]string, len(keys))
		for i := range keys {
			lower[i] = strings.ToLower(keys[i].company)
		}
	}
	res := func(k *sortKey) jobinsight.Result {
		return jobinsight.Result{Score: k.priority, Relevance: k.relevance}
	}
	less := func(i, j int) bool {
		a, b := &keys[i], &keys[j]
		switch by {
		case "priority":
			return jobinsight.ComparePriority(res(a), res(b), string(a.key), string(b.key)) < 0
		case "relevance":
			return jobinsight.CompareRelevance(res(a), res(b), string(a.key), string(b.key)) < 0
		case "fresh":
			if a.freshAt.IsZero() != b.freshAt.IsZero() {
				return !a.freshAt.IsZero()
			}
			if !a.freshAt.Equal(b.freshAt) {
				return a.freshAt.After(b.freshAt)
			}
			// A board read in one sweep dates many postings alike; with no
			// scores to separate them, group them by company.
			if view == ViewPending && lower[i] != lower[j] {
				return lower[i] < lower[j]
			}
			if a.relevance != b.relevance {
				return a.relevance > b.relevance
			}
		case "company":
			if lower[i] != lower[j] {
				return lower[i] < lower[j]
			}
			if a.relevance != b.relevance {
				return a.relevance > b.relevance
			}
		case "salary":
			// Highest top of range first; jobs stating no pay in the
			// currency last.
			if a.pay != b.pay {
				return a.pay > b.pay
			}
			if a.relevance != b.relevance {
				return a.relevance > b.relevance
			}
		default:
			if byMarkTime && !a.markedAt.Equal(b.markedAt) {
				return a.markedAt.After(b.markedAt)
			}
			// The digest's order: priority, relevance, key. Rank (DevPrism coverage)
			// is already in both numbers, so it is not a tie-break.
			return jobinsight.ComparePriority(res(a), res(b), string(a.key), string(b.key)) < 0
		}
		return a.key < b.key
	}
	perm := make([]int, len(keys))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(x, y int) bool { return less(perm[x], perm[y]) })
	return perm
}

// sortCands orders cands as Sort orders the jobs they stand for.
func sortCands(cs []cand, view, by string) {
	keys := make([]sortKey, len(cs))
	for i := range cs {
		keys[i] = cs[i].sortKey()
	}
	perm := sortOrder(keys, view, by)
	sorted := make([]cand, len(cs))
	for i, p := range perm {
		sorted[i] = cs[p]
	}
	copy(cs, sorted)
}

// diversifyCands is Diversify over cands.
func diversifyCands(cs []cand, view, by string) []cand {
	if !diversified(view, by) {
		return cs
	}
	rows := jobinsight.Diversify(len(cs),
		func(i int) string { return cs[i].st.company },
		func(i int) string { return cs[i].st.title },
		jobinsight.DiversityPerCompany, jobinsight.DiversityWindow)
	out := make([]cand, 0, len(rows))
	for _, r := range rows {
		c := cs[r.Index]
		c.similar = len(r.Similar)
		out = append(out, c)
	}
	return out
}

// payKey is a job's top annual pay in cur, or -1 when it states none in cur
// (the salary sort's key).
func payKey(pay []jobinsight.Pay, cur string) float64 {
	if p, ok := jobinsight.PayIn(pay, cur); ok {
		return p.AnnualMax
	}
	return -1
}
