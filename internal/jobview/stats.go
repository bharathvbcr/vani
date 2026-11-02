package jobview

import (
	"math"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/location"
)

// The aggregates `vani jobs stats` reports over a selection: distributions,
// not rows. Pure and display-only, like everything in this package that is
// not the row contract.

const (
	statKeywords = 25 // keywords in `jobs stats`
	statCompany  = 15 // companies in `jobs stats`
	statAreas    = 20 // areas in `jobs stats`
)

type AgeBucket struct {
	Label   string `json:"label"`
	MaxDays int    `json:"max_days"` // -1: no upper bound
	Posted  int    `json:"posted"`   // dated by the board
	Seen    int    `json:"seen"`     // dated by when Vani first saw it
}

type CompanyStat struct {
	Company       string  `json:"company"`
	N             int     `json:"n"`
	Strong        int     `json:"strong"`
	MeanRelevance float64 `json:"mean_relevance"`
}

type KeywordStat struct {
	Term   string `json:"term"`
	Cat    string `json:"cat"`
	N      int    `json:"n"` // postings that mention it
	Target bool   `json:"target,omitempty"`
}

// AreaStat counts the jobs naming one area (with its state when it has
// one, so Springfield, IL and Springfield, MO stay apart).
type AreaStat struct {
	Area  string `json:"area"`
	State string `json:"state,omitempty"`
	N     int    `json:"n"`
}

type Stats struct {
	View        string         `json:"view"`
	Query       string         `json:"query,omitempty"`
	Total       int            `json:"total"`
	Fit         map[string]int `json:"fit"`
	WorkMode    map[string]int `json:"work_mode"`
	Sponsorship map[string]int `json:"sponsorship"`
	Seniority   map[string]int `json:"seniority"`
	Category    map[string]int `json:"category,omitempty"`
	Basis       map[string]int `json:"basis"`
	FreshSource map[string]int `json:"fresh_source"`
	Age         []AgeBucket    `json:"age"`
	Relevance   []int          `json:"relevance"` // ten buckets: 0-10 ... 90-100
	Priority    []int          `json:"priority"`
	Companies   []CompanyStat  `json:"companies"`
	Keywords    []KeywordStat  `json:"keywords"`
	// State counts each job once per US state it names; "none" is a job that
	// names no state. Country counts each job once per country it names
	// (location.Countries); "none" names no country. Area is the most-named
	// areas. RemoteListed counts jobs the board lists as remote, and
	// RemoteScope splits them by where ("unstated" when the board does not say).
	State        map[string]int `json:"state"`
	Country      map[string]int `json:"country"`
	Area         []AreaStat     `json:"area"`
	RemoteListed int            `json:"remote_listed"`
	RemoteScope  map[string]int `json:"remote_scope"`
	Insight      InsightInfo    `json:"insight"`
	Torn         bool           `json:"torn,omitempty"`
	Ledger       string         `json:"ledger"`
	Records      int            `json:"records"`
	// Pending is what no view lists yet: postings awaiting triage.
	Pending Pending `json:"pending"`

	// Salary is the pay the jobs state in --currency (jobsfilter.go).
	Salary PayStats `json:"salary"`
}

var ageBuckets = []struct {
	label string
	max   int
}{{"≤1d", 1}, {"2–3d", 3}, {"4–7d", 7}, {"1–2w", 14}, {"2–4w", 30}, {"1–2mo", 60}, {">2mo", -1}}

func bucket10(v float64) int {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return min(int(v/10), 9)
}

func StatsOf(sel []Selected, info InsightInfo, target map[string]bool) Stats {
	out := Stats{
		Total: len(sel), Fit: map[string]int{}, WorkMode: map[string]int{}, Sponsorship: map[string]int{},
		Seniority: map[string]int{}, Category: map[string]int{}, Basis: map[string]int{}, FreshSource: map[string]int{},
		Relevance: make([]int, 10), Priority: make([]int, 10), Companies: []CompanyStat{}, Keywords: []KeywordStat{},
		State: map[string]int{}, Country: map[string]int{}, Area: []AreaStat{}, RemoteScope: map[string]int{},
		Insight: info,
	}
	areas := map[string]*AreaStat{}
	for _, b := range ageBuckets {
		out.Age = append(out.Age, AgeBucket{Label: b.label, MaxDays: b.max})
	}
	or := func(v string) string {
		if v == "" {
			return "none"
		}
		return v
	}
	type acc struct {
		name      string
		n, strong int
		sum       float64
	}
	companies := map[string]*acc{}
	terms := map[string]*KeywordStat{}
	for _, s := range sel {
		j := s.Job
		out.Fit[or(j.Triage["role_fit"])]++
		out.WorkMode[or(j.Triage["work_mode"])]++
		out.Sponsorship[or(j.Triage["sponsorship"])]++
		out.Seniority[or(j.Triage["seniority"])]++
		cat := j.Category
		if cat == "" && j.Triage != nil {
			cat = j.Triage["category"]
		}
		out.Category[or(cat)]++
		out.Basis[string(j.RelevanceBasis)]++
		out.Relevance[bucket10(j.Relevance)]++
		out.Priority[bucket10(j.Priority)]++
		if j.AgeDays != nil {
			out.FreshSource[j.FreshSrc]++
			for i, b := range ageBuckets {
				if b.max < 0 || *j.AgeDays <= b.max {
					if j.FreshSrc == string(jobinsight.FromPosted) {
						out.Age[i].Posted++
					} else {
						out.Age[i].Seen++
					}
					break
				}
			}
		} else {
			out.FreshSource["unknown"]++
		}
		countPlaces(&out, areas, j)
		ck := strings.ToLower(j.Company)
		a := companies[ck]
		if a == nil {
			a = &acc{name: j.Company}
			companies[ck] = a
		}
		a.n++
		a.sum += j.Relevance
		if j.Triage["role_fit"] == "strong_fit" {
			a.strong++
		}
		for _, h := range s.Hits {
			k := terms[h.Term]
			if k == nil {
				k = &KeywordStat{Term: h.Term, Cat: h.Category, Target: target[h.Term]}
				terms[h.Term] = k
			}
			k.N++
		}
	}
	for _, a := range companies {
		out.Companies = append(out.Companies, CompanyStat{Company: a.name, N: a.n, Strong: a.strong,
			MeanRelevance: math.Round(a.sum/float64(a.n)*10) / 10})
	}
	sort.Slice(out.Companies, func(i, j int) bool {
		a, b := out.Companies[i], out.Companies[j]
		if a.N != b.N {
			return a.N > b.N
		}
		return strings.ToLower(a.Company) < strings.ToLower(b.Company)
	})
	if len(out.Companies) > statCompany {
		out.Companies = out.Companies[:statCompany]
	}
	for _, k := range terms {
		out.Keywords = append(out.Keywords, *k)
	}
	sort.Slice(out.Keywords, func(i, j int) bool {
		a, b := out.Keywords[i], out.Keywords[j]
		if a.N != b.N {
			return a.N > b.N
		}
		return a.Term < b.Term
	})
	if len(out.Keywords) > statKeywords {
		out.Keywords = out.Keywords[:statKeywords]
	}
	out.Area = rankedAreas(areas)
	return out
}

// rankedAreas is the most-named areas, most first, then by name.
func rankedAreas(areas map[string]*AreaStat) []AreaStat {
	out := make([]AreaStat, 0, len(areas))
	for _, a := range areas {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.N != b.N {
			return a.N > b.N
		}
		if la, lb := strings.ToLower(a.Area), strings.ToLower(b.Area); la != lb {
			return la < lb
		}
		return a.State < b.State
	})
	if len(out) > statAreas {
		out = out[:statAreas]
	}
	return out
}

// countPlaces adds one job's states, areas and remote listing to the stats.
// A job counts once per distinct state and once per distinct area.
func countPlaces(out *Stats, areas map[string]*AreaStat, j Job) {
	states, seen := map[string]bool{}, map[string]bool{}
	for _, p := range j.Places {
		if p.State != "" && !states[p.State] {
			states[p.State] = true
			out.State[p.State]++
		}
		a := location.NormArea(p.Area)
		if a == "" {
			continue
		}
		k := a + "\x00" + p.State
		if seen[k] {
			continue
		}
		seen[k] = true
		if areas[k] == nil {
			areas[k] = &AreaStat{Area: p.Area, State: p.State}
		}
		areas[k].N++
	}
	if len(states) == 0 {
		out.State["none"]++
	}
	if j.RemoteListed {
		out.RemoteListed++
		scope := j.RemoteScope
		if scope == "" {
			scope = "unstated"
		}
		out.RemoteScope[scope]++
	}
	cs := countriesOf(j)
	if len(cs) == 0 {
		out.Country["none"]++
		return
	}
	for _, c := range cs {
		out.Country[c]++
	}
}

// PayKey is a job's top annual pay in cur, or -1 when it states none in cur.
func PayKey(j Job, cur string) float64 {
	if p, ok := jobinsight.PayIn(j.Pay, cur); ok {
		return p.AnnualMax
	}
	return -1
}

type PayBucket struct {
	Label string  `json:"label"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"` // -1: no upper bound
	N     int     `json:"n"`
}

// PayStats describes the pay the Selected jobs state in Currency, by the
// middle of each job's annual range.
type PayStats struct {
	Currency string `json:"currency"`
	Listed   int    `json:"listed"`   // state pay in Currency
	Other    int    `json:"other"`    // state pay only in another currency
	Unlisted int    `json:"unlisted"` // state no pay
	// Median is the median middle of the listed ranges; 0 when none.
	Median  float64     `json:"median"`
	Buckets []PayBucket `json:"buckets"`
}

const PayBuckets = 8

func PayStatsOf(sel []Selected, cur string) PayStats {
	step := jobinsight.PayStep(cur)
	out := PayStats{Currency: cur, Buckets: make([]PayBucket, 0, PayBuckets)}
	// Buckets: under 2 steps, then one step wide, then the rest.
	for i := 0; i < PayBuckets; i++ {
		lo, hi := float64(i+1)*step, float64(i+2)*step
		switch i {
		case 0:
			lo = 0
		case PayBuckets - 1:
			hi = -1
		}
		label := "<" + jobinsight.ShortMoney(hi)
		if i > 0 {
			label = jobinsight.ShortMoney(lo) + "–" + jobinsight.ShortMoney(hi)
		}
		if hi < 0 {
			label = "≥" + jobinsight.ShortMoney(lo)
		}
		out.Buckets = append(out.Buckets, PayBucket{Label: label, Min: lo, Max: hi})
	}
	var mids []float64
	for _, s := range sel {
		p, ok := jobinsight.PayIn(s.Job.Pay, cur)
		switch {
		case ok:
			out.Listed++
			mid := (p.AnnualMin + p.AnnualMax) / 2
			mids = append(mids, mid)
			for i := range out.Buckets {
				if b := out.Buckets[i]; b.Max < 0 || mid < b.Max {
					out.Buckets[i].N++
					break
				}
			}
		case len(s.Job.Pay) > 0:
			out.Other++
		default:
			out.Unlisted++
		}
	}
	if n := len(mids); n > 0 {
		sort.Float64s(mids)
		out.Median = mids[n/2]
		if n%2 == 0 {
			out.Median = (mids[n/2-1] + mids[n/2]) / 2
		}
		out.Median = math.Round(out.Median)
	}
	return out
}
