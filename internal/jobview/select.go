package jobview

import (
	"cmp"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Views. "new" is every kept posting the user has not marked; the mark views
// show marked postings whatever became of them since (a saved role that
// closed stays saved, flagged closed); "all" is kept plus marked; "pending" is
// the postings no run has triaged yet, unmarked, which no other view lists
// (ViewPending): it is unranked, and only `jobs list` takes it.
var Views = []string{"new", "saved", "applied", "hidden", "all", ViewPending}

// Sorts are the orders `jobs list` takes.
var Sorts = []string{"default", "priority", "relevance", "fresh", "company", "salary"}

// Filter is the pay, seniority, category, company, title and relevance part of
// a selection, already parsed (cmd/vani parses the flags). A nil set takes
// everything.
type Filter struct {
	MinPay       float64 // a year's pay in Currency; 0 is no floor
	Currency     string
	PayListed    bool
	Seniorities  map[string]bool
	Categories   map[string]bool
	Companies    map[string]bool
	NotCompanies map[string]bool
	ExcludeTerms []string
	MinRelevance float64
}

// Keep reports whether an enriched job passes the filters.
func (f *Filter) Keep(j *Job) bool {
	cat := j.Category
	if cat == "" && j.Triage != nil {
		cat = j.Triage["category"]
	}
	return f.keep(j.Triage["seniority"], cat, j.Company, j.Title, j.Relevance, j.Pay)
}

// keep is Keep over the fields it reads.
func (f *Filter) keep(seniority, category, company, title string, relevance float64, pay []jobinsight.Pay) bool {
	if !InSet(f.Seniorities, seniority) {
		return false
	}
	if !InSet(f.Categories, category) {
		return false
	}
	ck := CompanyKey(company)
	if (f.Companies != nil && !f.Companies[ck]) || f.NotCompanies[ck] {
		return false
	}
	if len(f.ExcludeTerms) > 0 {
		lt := strings.ToLower(title)
		for _, w := range f.ExcludeTerms {
			if strings.Contains(lt, w) {
				return false
			}
		}
	}
	if f.MinRelevance > 0 && relevance < f.MinRelevance {
		return false
	}
	if f.PayListed && len(pay) == 0 {
		return false
	}
	if f.MinPay > 0 && len(pay) > 0 {
		// Stated pay is compared in the chosen currency only; a job stating
		// pay only in another one cannot be compared, so it is left out.
		p, ok := jobinsight.PayIn(pay, f.Currency)
		if !ok || p.AnnualMax < f.MinPay {
			return false
		}
	}
	return true
}

// Opts are the selection options list and stats share, already parsed.
type Opts struct {
	View, Query, Sort         string
	Fits, WorkModes, Sponsors map[string]bool // role_fit, work_mode, sponsorship; "none" is no reading
	States, Areas             map[string]bool // "none" is none listed
	Countries                 map[string]bool // canonical names (location.Countries); "none" is none listed
	Remote, OrRemote          bool
	MaxAge                    int
	Open                      bool
	HalfLife                  float64
	More                      Filter

	// Stages keeps only applied jobs at these stages (marks.StageNone is
	// applied with no stage yet); nil takes every job.
	Stages map[marks.Stage]bool

	// where, when set, receives place counts for jobs that pass every filter
	// except the place ones. Nil keeps the place filter ahead of ranking.
	where *WhereChoices
}

// PlaceOK applies the location options to a located job: --state, --area and
// --country, widened by --or-remote to roles the board lists as remote, or
// narrowed by --remote to them.
func (o *Opts) PlaceOK(j Job) bool { return o.placeOK(j.Places, j.RemoteListed, j.RemoteScope) }

// placeOK is PlaceOK over the fields it reads.
func (o *Opts) placeOK(places []Place, remoteListed bool, remoteScope string) bool {
	in := InPlaces(o.States, o.Areas, places) && InCountries(o.Countries, countriesIn(places, remoteScope))
	if o.OrRemote {
		return in || remoteListed
	}
	return in && (!o.Remote || remoteListed)
}

// Ranker fills a job's insight fields (freshness, scores, keywords, reasons,
// pay, logo, row) and returns the posting's full keyword hits for stats.
// cmd/vani's ranking context implements it.
type Ranker interface {
	Enrich(j *Job, r ledger.Record, kwLimit int) []jobinsight.Hit
}

// PageRanker is a Ranker that can split Enrich in parts, so a selection ranks
// every job in its view without building a Job for it, and builds and
// finishes a Job only for the jobs a reply returns (EnrichWith).
//
//   - Rank fills what selection, the filters, every sort and the stats read
//     (place, freshness, priority, relevance; pay when withPay).
//   - Hits is the keyword hits Finish returns, without finishing a Job: the
//     stats and gaps count them.
//   - Finish fills the rest of a job that will be shown, after its Ranked was
//     applied (Ranked.Apply), and returns its hits.
//
// Rank, Apply and Finish must leave a job exactly as Enrich does.
type PageRanker interface {
	Ranker
	Rank(rk *Ranked, r ledger.Record, withPay bool)
	Hits(r ledger.Record) []jobinsight.Hit
	Finish(j *Job, r ledger.Record, kwLimit int, paid bool) []jobinsight.Hit
}

// needsPay says whether o's selection or order reads a job's pay.
func (o *Opts) needsPay() bool {
	return o.Sort == "salary" || o.More.PayListed || o.More.MinPay > 0
}

// Select returns the jobs in o's view that pass its query and filters,
// unsorted, each whole (enriched, with its keyword hits), and the per-view
// counts (which ignore the query and filters). A list builds whole only the
// page it returns (BuildList); Select is for callers that read every job.
func Select(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *Opts, rk Ranker, kwLimit int) ([]Selected, map[string]int) {
	cs, counts := selectCands(snap, m, o, rk, selectArgs{withPay: true, kwLimit: kwLimit})
	sel := make([]Selected, len(cs))
	for i := range cs {
		j, hits := materialize(&cs[i], snap, m, rk, kwLimit)
		sel[i] = Selected{Job: j, Hits: hits, Pay: cs[i].pay}
	}
	return sel, counts
}

// List is the `jobs list` reply. Ledger and Records are the caller's.
type List struct {
	View   string         `json:"view"`
	Query  string         `json:"query,omitempty"`
	Jobs   []Job          `json:"jobs"`
	Total  int            `json:"total"` // matches before the limit
	Shown  int            `json:"shown"`
	Counts map[string]int `json:"counts"` // per view, ignoring the query
	// Torn means a run was mid-append: the list is complete up to that line.
	Torn bool `json:"torn,omitempty"`
	// Ledger is the directory read and Records how many postings it holds, so
	// an empty list from the wrong state folder does not pass for "no jobs".
	Ledger  string      `json:"ledger"`
	Records int         `json:"records"`
	Sort    string      `json:"sort"`
	Insight InsightInfo `json:"insight"`
	// Pending is what no view lists yet: postings awaiting triage.
	Pending Pending `json:"pending"`
	// Where counts places in this selection before --state, --area, --country
	// and --remote, so a menu can offer them after one is chosen. Stats stay
	// on the filtered selection.
	Where *WhereChoices `json:"where,omitempty"`
}

// WhereChoices is the place menu for a list: each job once per state and
// once per country, and the most-named areas. "none" names no state or no country.
type WhereChoices struct {
	State   map[string]int `json:"state"`
	Country map[string]int `json:"country"`
	Area    []AreaStat     `json:"area"`
}

// BuildList selects, sorts, folds and limits the jobs o names.
func BuildList(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *Opts, rk Ranker, info InsightInfo, kwLimit, limit int) List {
	out := List{View: o.View, Query: o.Query, Sort: EffectiveSort(o.View, o.Sort), Torn: snap.Torn, Jobs: []Job{}, Insight: info}
	opts := *o
	where := &WhereChoices{State: map[string]int{}, Country: map[string]int{}}
	opts.where = where
	cs, counts := selectCands(snap, m, &opts, rk, selectArgs{withPay: opts.needsPay(), kwLimit: kwLimit})
	out.Where = where
	out.Counts = counts
	sortCands(cs, o.View, o.Sort)
	cs = diversifyCands(cs, o.View, o.Sort)
	out.Total = len(cs)
	if len(cs) > limit {
		cs = cs[:max(limit, 0)]
	}
	// A whole Job is built and finished only for the page.
	out.Jobs = make([]Job, 0, len(cs))
	for i := range cs {
		j, _ := materialize(&cs[i], snap, m, rk, kwLimit)
		out.Jobs = append(out.Jobs, j)
	}
	out.Shown = len(out.Jobs)
	return out
}

type Selected struct {
	Job  Job
	Hits []jobinsight.Hit
	Pay  float64 // top annual pay in --currency, -1 for none (the salary sort)
}

// Diversify shapes a sorted list for reading, as the digest does
// (jobinsight.Diversify): exact company+title duplicates fold into the first
// row, which counts them in Similar, and one company leads at most
// jobinsight.DiversityPerCompany rows of the first jobinsight.DiversityWindow.
// Only the ranked orders of the "new" view (unmarked roles) are shaped: the owner's marked views,
// the "all" view, which mixes in marked roles,
// and the fresh, company and salary sorts are lists the owner asked for as is.
func Diversify(sel []Selected, view, by string) []Selected {
	if !diversified(view, by) {
		return sel
	}
	rows := jobinsight.Diversify(len(sel),
		func(i int) string { return sel[i].Job.Company },
		func(i int) string { return sel[i].Job.Title },
		jobinsight.DiversityPerCompany, jobinsight.DiversityWindow)
	out := make([]Selected, 0, len(rows))
	for _, r := range rows {
		s := sel[r.Index]
		s.Job.Similar = len(r.Similar)
		out = append(out, s)
	}
	return out
}

// diversified says whether Diversify shapes a list in view sorted by.
func diversified(view, by string) bool {
	return view == "new" && (by == "default" || by == "priority" || by == "relevance")
}

// EffectiveSort is the order a view really uses for the order asked. Untriaged
// postings have no priority or relevance, so the default, priority and
// relevance orders (which all rank by them) become "fresh": newest first, the
// only signal they have that is not a score. Replies report this, never the
// order that was asked for and not applied.
func EffectiveSort(view, by string) string {
	if view == ViewPending && (by == "default" || by == "priority" || by == "relevance") {
		return "fresh"
	}
	return by
}

// Sort orders a list. "default" is the digest's order: marked views by
// when they were marked, the rest by priority (jobinsight.Score), then
// relevance, then key (jobinsight.ComparePriority, the one tie-break).
// Every order ends on the key, so it is total and stable across calls.
// sortOrder is the one comparator; a list sorts its candidates with it too.
//
// It sorts a permutation and moves each Selected once: a Selected carries a
// whole Job, too large to swap at every step of the sort.
func Sort(sel []Selected, view, by string) {
	keys := make([]sortKey, len(sel))
	for i := range sel {
		j := &sel[i].Job
		keys[i] = sortKey{key: j.Key, priority: j.Priority, relevance: j.Relevance, pay: sel[i].Pay,
			freshAt: j.FreshAt, markedAt: j.MarkedAt, company: j.Company}
	}
	perm := sortOrder(keys, view, by)
	sorted := make([]Selected, len(sel))
	for i, p := range perm {
		sorted[i] = sel[p]
	}
	copy(sel, sorted)
}

// MaxPendingCompanies bounds Pending.Companies; Total still counts all.
const MaxPendingCompanies = 20

// Pending is what the ledger holds that no ranked view lists: postings read
// from a board but not triaged (status live) and not marked. The ranked views
// list only postings that passed triage (KeptStatus), so without this a board
// whose postings are still waiting — the per-run cap reached, or every backend
// refused — looks like a board with no jobs at all. Total is the size of the
// pending view (counts["pending"]); a marked posting is in its mark view.
type Pending struct {
	// Total is every posting awaiting triage.
	Total int `json:"total"`
	// Matching is how many of them the query names (Total when there is no
	// query), so "no Acme jobs" can read "N Acme jobs await triage".
	Matching int `json:"matching"`
	// Companies are the companies with the most postings awaiting triage,
	// most first, at most MaxPendingCompanies of them. It is a bounded list:
	// ranked by N, so a company with few postings, all of them in the US, can
	// fall off it while US still counts it. Sum the entries only to compare
	// them, never to size the backlog; Total and US are the sizes.
	Companies []PendingCompany `json:"companies"`
	// US is how many of Total are located in the United States (InUS): the
	// count a "triage US postings first" run is sized by. It counts every
	// posting awaiting triage, not only those the query names.
	US int `json:"us"`
}

type PendingCompany struct {
	Company string `json:"company"`
	N       int    `json:"n"`
	// US is how many of N are located in the United States (InUS).
	US int `json:"us"`
}

// InUS reports whether a posting's board location is in the United States, or
// remote work whose scope takes in the US (location.InUS). A location the
// board left empty or that names no place or scope is not: the count is a
// lower bound, and it only orders a run, never drops a posting.
func InUS(p posting.Posting) bool {
	return location.InUS(location.Parse(p.Location, p.Remote))
}

// PendingOf counts the postings awaiting triage, those in the US, and those
// the query (MatchesQuery's rule, q lowered and trimmed) names.
func PendingOf(snap ledger.Snapshot, m map[posting.Key]marks.Entry, q string) Pending {
	out := Pending{Companies: []PendingCompany{}}
	by := map[string]*PendingCompany{}
	for _, r := range snap.Records {
		if !InView(ViewPending, r, m[r.Key].Mark) {
			continue
		}
		out.Total++
		j := ToJob(r, m[r.Key])
		c := by[j.Company]
		if c == nil {
			c = &PendingCompany{Company: j.Company}
			by[j.Company] = c
		}
		c.N++
		if InUS(r.Posting) {
			out.US++
			c.US++
		}
		if MatchesQuery(j, q) {
			out.Matching++
		}
	}
	for _, c := range by {
		out.Companies = append(out.Companies, *c)
	}
	slices.SortFunc(out.Companies, func(a, b PendingCompany) int {
		if c := cmp.Compare(b.N, a.N); c != 0 {
			return c
		}
		return strings.Compare(a.Company, b.Company)
	})
	if len(out.Companies) > MaxPendingCompanies {
		out.Companies = out.Companies[:MaxPendingCompanies]
	}
	return out
}
