package jobview

import (
	"cmp"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Views. "new" is every kept posting the user has not marked; the mark views
// show marked postings whatever became of them since (a saved role that
// closed stays saved, flagged closed); "all" is kept plus marked.
var Views = []string{"new", "saved", "applied", "hidden", "all"}

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
	if !InSet(f.Seniorities, j.Triage["seniority"]) {
		return false
	}
	cat := j.Category
	if cat == "" && j.Triage != nil {
		cat = j.Triage["category"]
	}
	if !InSet(f.Categories, cat) {
		return false
	}
	company := strings.ToLower(strings.Join(strings.Fields(j.Company), " "))
	if (f.Companies != nil && !f.Companies[company]) || f.NotCompanies[company] {
		return false
	}
	if len(f.ExcludeTerms) > 0 {
		title := strings.ToLower(j.Title)
		for _, w := range f.ExcludeTerms {
			if strings.Contains(title, w) {
				return false
			}
		}
	}
	if f.MinRelevance > 0 && j.Relevance < f.MinRelevance {
		return false
	}
	if f.PayListed && len(j.Pay) == 0 {
		return false
	}
	if f.MinPay > 0 && len(j.Pay) > 0 {
		// Stated pay is compared in the chosen currency only; a job stating
		// pay only in another one cannot be compared, so it is left out.
		p, ok := jobinsight.PayIn(j.Pay, f.Currency)
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
	Remote, OrRemote          bool
	MaxAge                    int
	Open                      bool
	HalfLife                  float64
	More                      Filter
}

// PlaceOK applies the location options to a located job: --state and --area,
// widened by --or-remote to roles the board lists as remote, or narrowed by
// --remote to them.
func (o *Opts) PlaceOK(j Job) bool {
	in := InPlaces(o.States, o.Areas, j.Places)
	if o.OrRemote {
		return in || j.RemoteListed
	}
	return in && (!o.Remote || j.RemoteListed)
}

// Ranker fills a job's insight fields (freshness, scores, keywords, reasons,
// pay, logo, row) and returns the posting's full keyword hits for stats.
// cmd/vani's ranking context implements it.
type Ranker interface {
	Enrich(j *Job, r ledger.Record, kwLimit int) []jobinsight.Hit
}

// Select returns the jobs in o's view that pass its query and filters,
// unsorted, and the per-view counts (which ignore the query and filters).
func Select(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *Opts, rk Ranker, kwLimit int) ([]Selected, map[string]int) {
	counts := map[string]int{}
	q := strings.ToLower(strings.TrimSpace(o.Query))
	var sel []Selected
	for _, r := range snap.Records {
		entry := m[r.Key]
		for _, v := range Views {
			if InView(v, r, entry.Mark) {
				counts[v]++
			}
		}
		if !InView(o.View, r, entry.Mark) {
			continue
		}
		j := ToJob(r, entry)
		if !MatchesQuery(j, q) {
			continue
		}
		if !InSet(o.Fits, j.Triage["role_fit"]) || !InSet(o.WorkModes, j.Triage["work_mode"]) || !InSet(o.Sponsors, j.Triage["sponsorship"]) {
			continue
		}
		if o.Open && j.Closed {
			continue
		}
		if o.Remote || o.States != nil || o.Areas != nil {
			Locate(&j, r.Posting)
			if !o.PlaceOK(j) {
				continue
			}
		}
		hits := rk.Enrich(&j, r, kwLimit)
		if o.MaxAge > 0 && (j.AgeDays == nil || *j.AgeDays > o.MaxAge) {
			continue
		}
		if !o.More.Keep(&j) {
			continue
		}
		sel = append(sel, Selected{Job: j, Hits: hits, Pay: PayKey(j, o.More.Currency)})
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
}

// BuildList selects, sorts, folds and limits the jobs o names.
func BuildList(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *Opts, rk Ranker, info InsightInfo, kwLimit, limit int) List {
	out := List{View: o.View, Query: o.Query, Sort: o.Sort, Torn: snap.Torn, Jobs: []Job{}, Insight: info}
	sel, counts := Select(snap, m, o, rk, kwLimit)
	out.Counts = counts
	Sort(sel, o.View, o.Sort)
	sel = Diversify(sel, o.View, o.Sort)
	out.Total = len(sel)
	if len(sel) > limit {
		sel = sel[:limit]
	}
	for _, s := range sel {
		out.Jobs = append(out.Jobs, s.Job)
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
	if view != "new" || by != "default" && by != "priority" && by != "relevance" {
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

// resultOf is the two numbers an order reads, as jobinsight's comparators take them.
func resultOf(j Job) jobinsight.Result {
	return jobinsight.Result{Score: j.Priority, Relevance: j.Relevance}
}

// Sort orders a list. "default" is the digest's order: marked views by
// when they were marked, the rest by priority (jobinsight.Score), then
// relevance, then key (jobinsight.ComparePriority, the one tie-break).
// Every order ends on the key, so it is total and stable across calls.
func Sort(sel []Selected, view, by string) {
	byMarkTime := by == "default" && (view == "saved" || view == "applied" || view == "hidden")
	sort.SliceStable(sel, func(i, j int) bool {
		a, b := sel[i].Job, sel[j].Job
		switch by {
		case "priority":
			return jobinsight.ComparePriority(resultOf(a), resultOf(b), string(a.Key), string(b.Key)) < 0
		case "relevance":
			return jobinsight.CompareRelevance(resultOf(a), resultOf(b), string(a.Key), string(b.Key)) < 0
		case "fresh":
			if a.FreshAt.IsZero() != b.FreshAt.IsZero() {
				return !a.FreshAt.IsZero()
			}
			if !a.FreshAt.Equal(b.FreshAt) {
				return a.FreshAt.After(b.FreshAt)
			}
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
		case "company":
			if ca, cb := strings.ToLower(a.Company), strings.ToLower(b.Company); ca != cb {
				return ca < cb
			}
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
		case "salary":
			// Highest top of range first; jobs stating no pay in the
			// currency last.
			if pa, pb := sel[i].Pay, sel[j].Pay; pa != pb {
				return pa > pb
			}
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
		default:
			if byMarkTime && !a.MarkedAt.Equal(b.MarkedAt) {
				return a.MarkedAt.After(b.MarkedAt)
			}
			// The digest's order: priority, relevance, key. Rank (DevPrism coverage)
			// is already in both numbers, so it is not a tie-break.
			return jobinsight.ComparePriority(resultOf(a), resultOf(b), string(a.Key), string(b.Key)) < 0
		}
		return a.Key < b.Key
	})
}

// MaxPendingCompanies bounds Pending.Companies; Total still counts all.
const MaxPendingCompanies = 20

// Pending is what the ledger holds that no view lists yet: postings read
// from a board but not triaged (status live). Views list only postings that
// passed triage (KeptStatus), so without this a board whose postings are still
// waiting — the per-run cap reached, or every backend refused — looks like a
// board with no jobs at all.
type Pending struct {
	// Total is every posting awaiting triage.
	Total int `json:"total"`
	// Matching is how many of them the query names (Total when there is no
	// query), so "no Acme jobs" can read "N Acme jobs await triage".
	Matching int `json:"matching"`
	// Companies are the companies with the most postings awaiting triage,
	// most first, at most MaxPendingCompanies of them.
	Companies []PendingCompany `json:"companies"`
}

type PendingCompany struct {
	Company string `json:"company"`
	N       int    `json:"n"`
}

// PendingOf counts the postings awaiting triage, and those the query
// (MatchesQuery's rule, q lowered and trimmed) names.
func PendingOf(snap ledger.Snapshot, m map[posting.Key]marks.Entry, q string) Pending {
	out := Pending{Companies: []PendingCompany{}}
	by := map[string]int{}
	for _, r := range snap.Records {
		if r.Status != posting.Live {
			continue
		}
		out.Total++
		j := ToJob(r, m[r.Key])
		by[j.Company]++
		if MatchesQuery(j, q) {
			out.Matching++
		}
	}
	for c, n := range by {
		out.Companies = append(out.Companies, PendingCompany{Company: c, N: n})
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
