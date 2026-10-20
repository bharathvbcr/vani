package main

import (
	"flag"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The browsing aids `vani jobs` adds to each posting: freshness, a relevance
// and a priority score, keywords and the picked note as structured reasons.
// internal/jobinsight computes them; this file selects, filters, sorts and
// aggregates. All of it is display-only and read-only: nothing here writes
// the ledger, and no engine rule reads any of it.

const (
	listKeywords = 8  // keywords per job in a list
	showKeywords = 30 // keywords in `jobs show`
	statKeywords = 25 // keywords in `jobs stats`
	statCompany  = 15 // companies in `jobs stats`
	statAreas    = 20 // areas in `jobs stats`
	maxAgeDays   = 3650
	maxAreas     = 50 // areas one --area may name
	// maxPlaceFlagBytes bounds --state and --area, which carry lists, not a
	// search box (maxQueryBytes).
	maxPlaceFlagBytes = 2000
)

var jobSorts = []string{"default", "priority", "relevance", "fresh", "company", "salary"}

// insightJSON tells the client what the scores rest on, so a missing target
// summary reads as "not computed", not as "nothing matched".
type insightJSON struct {
	// Target is true when the target-roles summary was read, so keywords can
	// be marked as wanted; false leaves every keyword unmarked.
	Target       bool    `json:"target"`
	TargetTerms  int     `json:"target_terms"`
	HalfLifeDays float64 `json:"half_life_days"`
	Vocab        int     `json:"vocab"`
	// Boilerplate is how many company-blurb terms were left out (learn).
	Boilerplate int    `json:"boilerplate"`
	Warning     string `json:"warning,omitempty"`
}

type reasonJSON struct {
	Kind string `json:"kind"` // fits, gap, check or other
	Text string `json:"text"`
	Pct  *int   `json:"pct,omitempty"`
}

// placeJSON is one place a posting's location names (internal/location).
type placeJSON struct {
	Area   string `json:"area,omitempty"`
	State  string `json:"state,omitempty"` // USPS code
	Region string `json:"region,omitempty"`
}

// jobsOpts are the selection flags list and stats share.
type jobsOpts struct {
	view, query, sort         string
	fit, workMode, sponsor    string // comma lists of fixture option values, or "none"
	fits, workModes, sponsors map[string]bool
	state, area               string // comma lists: USPS codes or state names; areas; "none"
	states, areas             map[string]bool
	remote, orRemote          bool
	maxAge                    int
	open                      bool
	halfLife                  float64
	more                      jobsFilter // pay, seniority, company, title, relevance (jobsfilter.go)
}

func addSelectFlags(fs *flag.FlagSet, o *jobsOpts) {
	fs.StringVar(&o.view, "view", "new", "new, saved, applied, hidden or all")
	fs.StringVar(&o.query, "q", "", "only jobs whose title, company, location, team or note contains this")
	fs.StringVar(&o.fit, "fit", "", "only these role_fit readings (comma list; none = no reading)")
	fs.StringVar(&o.workMode, "work-mode", "", "only these work_mode readings (comma list; none = no reading)")
	fs.StringVar(&o.sponsor, "sponsorship", "", "only these sponsorship readings (comma list; none = no reading)")
	fs.StringVar(&o.state, "state", "", "only jobs in these US states (comma list of codes or names; none = no state listed)")
	fs.StringVar(&o.area, "area", "", "only jobs in these areas, as the location names them (comma list; none = no area listed)")
	fs.BoolVar(&o.remote, "remote", false, "only jobs the board lists as remote (its remote flag or location text; not triage's work_mode)")
	fs.BoolVar(&o.orRemote, "or-remote", false, "with --state or --area, also keep jobs the board lists as remote (remote, or in these places)")
	fs.IntVar(&o.maxAge, "max-age", 0, fmt.Sprintf("only jobs at most this many days old (0 = any, up to %d)", maxAgeDays))
	fs.BoolVar(&o.open, "open", false, "leave out roles the board no longer lists")
	fs.Float64Var(&o.halfLife, "half-life", jobinsight.DefaultHalfLifeDays, "days of age that halve a job's freshness bonus in priority")
	addFilterFlags(fs, &o.more)
}

// check validates the shared flags. sortable is false for stats, which has no
// order.
func (o *jobsOpts) check(cmd string, sortable bool) error {
	switch {
	case !contains(jobViews, o.view):
		return usagef("%s: --view must be one of %s", cmd, strings.Join(jobViews, ", "))
	case len(o.query) > maxQueryBytes || !utf8.ValidString(o.query):
		return usagef("%s: --q must be UTF-8 and at most %d bytes", cmd, maxQueryBytes)
	case sortable && !contains(jobSorts, o.sort):
		return usagef("%s: --sort must be one of %s", cmd, strings.Join(jobSorts, ", "))
	case o.maxAge < 0 || o.maxAge > maxAgeDays:
		return usagef("%s: --max-age must be 0-%d", cmd, maxAgeDays)
	case !(o.halfLife > 0 && o.halfLife <= jobinsight.MaxHalfLifeDays):
		return usagef("%s: --half-life must be more than 0 and at most %g", cmd, jobinsight.MaxHalfLifeDays)
	}
	var err error
	if o.fits, err = optionSet(cmd, "fit", "role_fit", o.fit); err != nil {
		return err
	}
	if o.workModes, err = optionSet(cmd, "work-mode", "work_mode", o.workMode); err != nil {
		return err
	}
	if o.sponsors, err = optionSet(cmd, "sponsorship", "sponsorship", o.sponsor); err != nil {
		return err
	}
	if o.states, err = stateSet(cmd, o.state); err != nil {
		return err
	}
	if o.areas, err = areaSet(cmd, o.area); err != nil {
		return err
	}
	switch {
	case o.orRemote && o.remote:
		return usagef("%s: --or-remote widens --state/--area to remote roles; --remote narrows to them; use one", cmd)
	case o.orRemote && o.states == nil && o.areas == nil:
		return usagef("%s: --or-remote needs --state or --area (alone it is --remote)", cmd)
	}
	return o.more.check(cmd)
}

// placeOK applies the location flags to a located job: --state and --area,
// widened by --or-remote to roles the board lists as remote, or narrowed by
// --remote to them.
func (o *jobsOpts) placeOK(j jobJSON) bool {
	in := inPlaces(o.states, o.areas, j.Places)
	if o.orRemote {
		return in || j.RemoteListed
	}
	return in && (!o.remote || j.RemoteListed)
}

// stateSet parses --state: USPS codes or state names in any case, or "none".
// Empty means no filter (nil).
func stateSet(cmd, raw string) (map[string]bool, error) {
	if raw = strings.TrimSpace(raw); raw == "" {
		return nil, nil
	}
	if len(raw) > maxPlaceFlagBytes || !utf8.ValidString(raw) {
		return nil, usagef("%s: --state must be UTF-8 and at most %d bytes", cmd, maxPlaceFlagBytes)
	}
	out := map[string]bool{}
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if strings.EqualFold(v, "none") {
			out["none"] = true
			continue
		}
		code, ok := location.StateCode(v)
		if !ok {
			return nil, usagef("%s: --state %q is not a US state code or name (or none)", cmd, v)
		}
		out[code] = true
	}
	return out, nil
}

// areaSet parses --area: areas compared folded (location.NormArea), or
// "none". Empty means no filter (nil).
func areaSet(cmd, raw string) (map[string]bool, error) {
	if raw = strings.TrimSpace(raw); raw == "" {
		return nil, nil
	}
	if len(raw) > maxPlaceFlagBytes || !utf8.ValidString(raw) {
		return nil, usagef("%s: --area must be UTF-8 and at most %d bytes", cmd, maxPlaceFlagBytes)
	}
	out := map[string]bool{}
	for _, v := range strings.Split(raw, ",") {
		a := location.NormArea(v)
		if a == "" {
			return nil, usagef("%s: --area has an empty entry", cmd)
		}
		out[a] = true
	}
	if len(out) > maxAreas {
		return nil, usagef("%s: --area names more than %d areas", cmd, maxAreas)
	}
	return out, nil
}

// locate fills j's places and remote_listed from the posting's location.
func locate(j *jobJSON, p posting.Posting) location.Location {
	loc := location.Parse(p.Location, p.Remote)
	j.Places = nil
	for _, pl := range loc.Places {
		j.Places = append(j.Places, placeJSON(pl))
	}
	j.RemoteListed, j.RemoteScope = loc.Remote, loc.RemoteScope
	return loc
}

// whereText is " · " and the job's location for the text list: the places
// and remote listing Vani read, or the board's text when it read none ("" when
// there is nothing to say). The Mac app's Job.whereLabel is the same rule.
func whereText(j jobJSON) string {
	var parts []string
	for _, p := range j.Places {
		if s := location.Place(p).String(); s != "" {
			parts = append(parts, s)
		}
	}
	s := strings.Join(parts, "; ")
	remote := ""
	if j.RemoteListed || j.Remote {
		remote = "Remote"
		if j.RemoteScope != "" {
			remote += " (" + j.RemoteScope + ")"
		}
	}
	if s == "" && j.Location != "" && (remote == "" || !strings.Contains(strings.ToLower(j.Location), "remote")) {
		s = j.Location
	}
	if remote != "" {
		if s != "" {
			s += " · "
		}
		s += remote
	}
	if s == "" {
		return ""
	}
	return " · " + s
}

// inPlaces applies --state and --area: a job passes when one of its places is
// in the set, and "none" takes a job with no state (or no area) listed.
func inPlaces(states, areas map[string]bool, places []placeJSON) bool {
	if states != nil {
		hit, listed := false, false
		for _, p := range places {
			if p.State != "" {
				listed = true
				hit = hit || states[p.State]
			}
		}
		if !hit && !(states["none"] && !listed) {
			return false
		}
	}
	if areas != nil {
		hit, listed := false, false
		for _, p := range places {
			if a := location.NormArea(p.Area); a != "" {
				listed = true
				hit = hit || areas[a]
			}
		}
		if !hit && !(areas["none"] && !listed) {
			return false
		}
	}
	return true
}

// optionSet parses a comma list of the jdtriage fixture's options for slot.
// Empty means no filter (nil).
func optionSet(cmd, flagName, slot, raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	valid := jdtriage.Descriptions()[slot]
	out := map[string]bool{}
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if _, ok := valid[v]; !ok && v != "none" {
			opts := make([]string, 0, len(valid)+1)
			for k := range valid {
				opts = append(opts, k)
			}
			sort.Strings(opts)
			return nil, usagef("%s: --%s %q is not one of %s, none", cmd, flagName, v, strings.Join(opts, ", "))
		}
		out[v] = true
	}
	return out, nil
}

// insight carries what every job's scores need: the vocabulary, the terms the
// target summary names, and the clock.
type insight struct {
	vocab    *jobinsight.Vocab
	target   map[string]bool
	info     insightJSON
	now      time.Time
	halfLife float64
	// hits caches each browsable posting's keyword hits; boiler is each
	// company's blurb terms, learned from those hits (learn).
	hits   map[posting.Key][]jobinsight.Hit
	boiler map[string]map[string]bool
	// logos finds each posting's cached company icon; nil when the config
	// could not be read (every job then shows its monogram).
	logos *logoIndex
}

// learn extracts the keywords of every posting the Jobs views can show (kept
// or marked) once, and learns each company's blurb from them, so a term every
// one of a company's postings repeats is not counted as that posting's ask.
func (in *insight) learn(snap ledger.Snapshot, m map[posting.Key]marks.Entry) {
	in.hits = map[posting.Key][]jobinsight.Hit{}
	var companies []string
	var all [][]jobinsight.Hit
	for _, r := range snap.Records {
		if !inView("all", r, m[r.Key].Mark) {
			continue
		}
		h := in.vocab.Extract(r.Posting.Title + "\n" + r.Posting.Description)
		in.hits[r.Key] = h
		companies, all = append(companies, r.Posting.Company), append(all, h)
	}
	in.boiler = jobinsight.Boilerplate(companies, all)
	n := 0
	for _, b := range in.boiler {
		n += len(b)
	}
	in.info.Boilerplate = n
}

// newInsight reads the target summary from the config and learns the
// snapshot's company blurbs (m may be nil: kept postings only). A config that
// cannot be read leaves keywords unmarked and says so; listing jobs never
// needed a config, and does not start needing one here.
func (e *env) newInsight(halfLife float64, snap ledger.Snapshot, m map[posting.Key]marks.Entry) (*insight, error) {
	v, err := jobinsight.Default()
	if err != nil {
		return nil, err
	}
	in := &insight{vocab: v, now: now(), halfLife: halfLife,
		info: insightJSON{HalfLifeDays: halfLife, Vocab: v.Len()}}
	cfg, err := e.loadConfig()
	if err == nil {
		in.logos = newLogoIndex(cfg, e.stateDir)
	}
	switch {
	case err != nil:
		in.info.Warning = "keywords are not marked against your target roles: " + oneLine(err.Error(), 200)
	case strings.TrimSpace(cfg.TargetSummary) == "":
		in.info.Warning = "keywords are not marked against your target roles: the config has no target_summary"
	default:
		in.target = v.Terms(cfg.TargetSummary)
		in.info.Target, in.info.TargetTerms = true, len(in.target)
	}
	in.learn(snap, m)
	return in, nil
}

func oneLine(s string, n int) string { return cleanText(s, n, false) }

// enrich fills j's insight fields from r and returns the posting's full
// keyword hits (for stats).
func (in *insight) enrich(j *jobJSON, r ledger.Record, kwLimit int) []jobinsight.Hit {
	locate(j, r.Posting)
	at, src := jobinsight.Fresh(r.Posting.PostedAt, r.FirstSeen, in.now)
	if !at.IsZero() {
		j.FreshAt, j.FreshSrc = at, string(src)
	}
	age := jobinsight.AgeDays(at, in.now)
	if age >= 0 {
		j.AgeDays = &age
	}
	hits, ok := in.hits[r.Key]
	if !ok {
		hits = in.vocab.Extract(r.Posting.Title + "\n" + r.Posting.Description)
	}
	hits = jobinsight.Without(hits, in.boiler[strings.ToLower(strings.TrimSpace(r.Posting.Company))])
	gap := in.vocab.Terms(r.Annotations[ledger.AnnMissing])
	j.Keywords = jobinsight.Annotate(hits, in.target, gap, kwLimit)
	j.Relevance, j.RelevanceBasis = jobinsight.Relevance(j.Rank, j.Triage["role_fit"], jobinsight.TargetHits(hits, in.target))
	j.Priority = jobinsight.Priority(j.Relevance, age, in.halfLife)
	j.Reasons = parseReasons(j.Explanation)
	j.Pay = jobinsight.ParsePay(r.Posting.Description)
	j.Logo = in.logos.path(r.Key, r.Posting.Company)
	return hits
}

// reasonPct is the " (91%)" a picked reason ends with (jdreasons.Note).
var reasonPct = regexp.MustCompile(`(?:^| )\((\d{1,3})%\)$`)

// parseReasons splits the picked note ("Fits: X (91%). Gap: Y (40%).") into
// its reasons, using jdreasons' own headings. A note in any other shape comes
// back whole as one "other" reason, never half-parsed.
func parseReasons(note string) []reasonJSON {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil
	}
	heads := jdreasons.Headings()
	var out []reasonJSON
	rest := note
	for rest != "" {
		head := ""
		for _, h := range heads {
			if strings.HasPrefix(rest, h+": ") {
				head = h
				break
			}
		}
		if head == "" {
			return []reasonJSON{{Kind: "other", Text: note}}
		}
		rest = rest[len(head)+2:]
		end := len(rest)
		for _, h := range heads {
			if i := strings.Index(rest, ". "+h+": "); i >= 0 && i < end {
				end = i
			}
		}
		text := strings.TrimSuffix(strings.TrimSpace(rest[:end]), ".")
		rest = strings.TrimSpace(strings.TrimPrefix(rest[end:], "."))
		rj := reasonJSON{Kind: strings.ToLower(head), Text: text}
		if m := reasonPct.FindStringSubmatch(text); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil && p <= 100 {
				rj.Pct, rj.Text = &p, strings.TrimSpace(strings.TrimSuffix(text, m[0]))
			}
		}
		if rj.Text == "" {
			return []reasonJSON{{Kind: "other", Text: note}}
		}
		out = append(out, rj)
	}
	return out
}

type selected struct {
	job  jobJSON
	hits []jobinsight.Hit
	pay  float64 // top annual pay in --currency, -1 for none (the salary sort)
}

// matchesQuery is the --q rule: q (already lowered and trimmed) is empty or
// appears in the job's title, company, location, team or note.
func matchesQuery(j jobJSON, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(strings.Join([]string{j.Title, j.Company, j.Location, j.Department, j.Note}, "\n")), q)
}

// selectJobs returns the jobs in o's view that pass its query and filters,
// unsorted, and the per-view counts (which ignore the query and filters).
func selectJobs(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *jobsOpts, in *insight, kwLimit int) ([]selected, map[string]int) {
	counts := map[string]int{}
	q := strings.ToLower(strings.TrimSpace(o.query))
	var sel []selected
	for _, r := range snap.Records {
		entry := m[r.Key]
		for _, v := range jobViews {
			if inView(v, r, entry.Mark) {
				counts[v]++
			}
		}
		if !inView(o.view, r, entry.Mark) {
			continue
		}
		j := toJob(r, entry)
		if !matchesQuery(j, q) {
			continue
		}
		if !inSet(o.fits, j.Triage["role_fit"]) || !inSet(o.workModes, j.Triage["work_mode"]) || !inSet(o.sponsors, j.Triage["sponsorship"]) {
			continue
		}
		if o.open && j.Closed {
			continue
		}
		if o.remote || o.states != nil || o.areas != nil {
			locate(&j, r.Posting)
			if !o.placeOK(j) {
				continue
			}
		}
		hits := in.enrich(&j, r, kwLimit)
		if o.maxAge > 0 && (j.AgeDays == nil || *j.AgeDays > o.maxAge) {
			continue
		}
		if !o.more.keep(&j) {
			continue
		}
		sel = append(sel, selected{job: j, hits: hits, pay: payKey(j, o.more.currency)})
	}
	return sel, counts
}

// inSet: a nil set takes everything; "none" stands for no reading.
func inSet(set map[string]bool, v string) bool {
	if set == nil {
		return true
	}
	if v == "" {
		v = "none"
	}
	return set[v]
}

// sortJobs orders a list. "default" is the original order: marked views by
// when they were marked, the rest by rank, then triage fit, then first seen.
// Every order ends on the key, so it is total and stable across calls.
func sortJobs(sel []selected, view, by string) {
	byMarkTime := by == "default" && (view == "saved" || view == "applied" || view == "hidden")
	sort.SliceStable(sel, func(i, j int) bool {
		a, b := sel[i].job, sel[j].job
		switch by {
		case "priority":
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
		case "relevance":
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
			if a.Priority != b.Priority {
				return a.Priority > b.Priority
			}
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
			if pa, pb := sel[i].pay, sel[j].pay; pa != pb {
				return pa > pb
			}
			if a.Relevance != b.Relevance {
				return a.Relevance > b.Relevance
			}
		default:
			if byMarkTime && !a.MarkedAt.Equal(b.MarkedAt) {
				return a.MarkedAt.After(b.MarkedAt)
			}
			if (a.Rank != nil) != (b.Rank != nil) {
				return a.Rank != nil
			}
			if a.Rank != nil && *a.Rank != *b.Rank {
				return *a.Rank > *b.Rank
			}
			if fa, fb := fitOrder(a), fitOrder(b); fa != fb {
				return fa < fb
			}
			if !a.FirstSeen.Equal(b.FirstSeen) {
				return a.FirstSeen.After(b.FirstSeen)
			}
		}
		return a.Key < b.Key
	})
}

// --- stats ---

type ageBucketJSON struct {
	Label   string `json:"label"`
	MaxDays int    `json:"max_days"` // -1: no upper bound
	Posted  int    `json:"posted"`   // dated by the board
	Seen    int    `json:"seen"`     // dated by when Vani first saw it
}

type companyStatJSON struct {
	Company       string  `json:"company"`
	N             int     `json:"n"`
	Strong        int     `json:"strong"`
	MeanRelevance float64 `json:"mean_relevance"`
}

type keywordStatJSON struct {
	Term   string `json:"term"`
	Cat    string `json:"cat"`
	N      int    `json:"n"` // postings that mention it
	Target bool   `json:"target,omitempty"`
}

// areaStatJSON counts the jobs naming one area (with its state when it has
// one, so Springfield, IL and Springfield, MO stay apart).
type areaStatJSON struct {
	Area  string `json:"area"`
	State string `json:"state,omitempty"`
	N     int    `json:"n"`
}

type jobsStatsJSON struct {
	View        string            `json:"view"`
	Query       string            `json:"query,omitempty"`
	Total       int               `json:"total"`
	Fit         map[string]int    `json:"fit"`
	WorkMode    map[string]int    `json:"work_mode"`
	Sponsorship map[string]int    `json:"sponsorship"`
	Seniority   map[string]int    `json:"seniority"`
	Basis       map[string]int    `json:"basis"`
	FreshSource map[string]int    `json:"fresh_source"`
	Age         []ageBucketJSON   `json:"age"`
	Relevance   []int             `json:"relevance"` // ten buckets: 0-10 ... 90-100
	Priority    []int             `json:"priority"`
	Companies   []companyStatJSON `json:"companies"`
	Keywords    []keywordStatJSON `json:"keywords"`
	// State counts each job once per US state it names; "none" is a job that
	// names no state. Area is the most-named areas. RemoteListed counts jobs
	// the board lists as remote, and RemoteScope splits them by where
	// ("unstated" when the board does not say).
	State        map[string]int `json:"state"`
	Area         []areaStatJSON `json:"area"`
	RemoteListed int            `json:"remote_listed"`
	RemoteScope  map[string]int `json:"remote_scope"`
	Insight      insightJSON    `json:"insight"`
	Torn         bool           `json:"torn,omitempty"`
	Ledger       string         `json:"ledger"`
	Records      int            `json:"records"`
	// Pending is what no view lists yet: postings awaiting triage.
	Pending pendingJSON `json:"pending"`

	// Salary is the pay the jobs state in --currency (jobsfilter.go).
	Salary payStatsJSON `json:"salary"`
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

func jobsStats(sel []selected, in *insight) jobsStatsJSON {
	out := jobsStatsJSON{
		Total: len(sel), Fit: map[string]int{}, WorkMode: map[string]int{}, Sponsorship: map[string]int{},
		Seniority: map[string]int{}, Basis: map[string]int{}, FreshSource: map[string]int{},
		Relevance: make([]int, 10), Priority: make([]int, 10), Companies: []companyStatJSON{}, Keywords: []keywordStatJSON{},
		State: map[string]int{}, Area: []areaStatJSON{}, RemoteScope: map[string]int{},
		Insight: in.info,
	}
	areas := map[string]*areaStatJSON{}
	for _, b := range ageBuckets {
		out.Age = append(out.Age, ageBucketJSON{Label: b.label, MaxDays: b.max})
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
	terms := map[string]*keywordStatJSON{}
	for _, s := range sel {
		j := s.job
		out.Fit[or(j.Triage["role_fit"])]++
		out.WorkMode[or(j.Triage["work_mode"])]++
		out.Sponsorship[or(j.Triage["sponsorship"])]++
		out.Seniority[or(j.Triage["seniority"])]++
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
		for _, h := range s.hits {
			k := terms[h.Term]
			if k == nil {
				k = &keywordStatJSON{Term: h.Term, Cat: h.Category, Target: in.target[h.Term]}
				terms[h.Term] = k
			}
			k.N++
		}
	}
	for _, a := range companies {
		out.Companies = append(out.Companies, companyStatJSON{Company: a.name, N: a.n, Strong: a.strong,
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
	for _, a := range areas {
		out.Area = append(out.Area, *a)
	}
	sort.Slice(out.Area, func(i, j int) bool {
		a, b := out.Area[i], out.Area[j]
		if a.N != b.N {
			return a.N > b.N
		}
		if la, lb := strings.ToLower(a.Area), strings.ToLower(b.Area); la != lb {
			return la < lb
		}
		return a.State < b.State
	})
	if len(out.Area) > statAreas {
		out.Area = out.Area[:statAreas]
	}
	return out
}

// stateLine renders state counts for text stats, most first: " TX 3, NM 1,
// none 2".
func stateLine(counts map[string]int) string {
	codes := make([]string, 0, len(counts))
	for c := range counts {
		if c != "none" {
			codes = append(codes, c)
		}
	}
	sort.Slice(codes, func(i, j int) bool {
		if counts[codes[i]] != counts[codes[j]] {
			return counts[codes[i]] > counts[codes[j]]
		}
		return codes[i] < codes[j]
	})
	var b strings.Builder
	for _, c := range codes {
		fmt.Fprintf(&b, " %s %d,", c, counts[c])
	}
	fmt.Fprintf(&b, " none %d", counts["none"])
	return b.String()
}

// countPlaces adds one job's states, areas and remote listing to the stats.
// A job counts once per distinct state and once per distinct area.
func countPlaces(out *jobsStatsJSON, areas map[string]*areaStatJSON, j jobJSON) {
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
			areas[k] = &areaStatJSON{Area: p.Area, State: p.State}
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
}
