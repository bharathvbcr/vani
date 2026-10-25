package main

import (
	"flag"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The browsing aids `vani jobs` adds to each posting: freshness, a relevance
// and a priority score, keywords and the picked note as structured reasons.
// internal/jobinsight computes them and internal/jobview selects, filters, sorts and
// aggregates; this file builds the ranking context and parses the flags. All of it
// is display-only and read-only: nothing here writes
// the ledger, and no engine rule reads any of it.

const (
	listKeywords = 8  // keywords per job in a list
	showKeywords = 30 // keywords in `jobs show`
	maxAgeDays   = 3650
	maxAreas     = 50 // areas one --area may name
	// maxPlaceFlagBytes bounds --state and --area, which carry lists, not a
	// search box (maxQueryBytes).
	maxPlaceFlagBytes = 2000
)

// jobsOpts are the selection flags list and stats share.
type jobsOpts struct {
	jobview.Opts
	fit, workMode, sponsor string     // comma lists of fixture option values, or "none"
	state, area            string     // comma lists: USPS codes or state names; areas; "none"
	more                   jobsFilter // pay, seniority, company, title, relevance (jobsfilter.go)
	// pendingOK lets --view pending through check: only `jobs list` sets it.
	pendingOK bool
}

func addSelectFlags(fs *flag.FlagSet, o *jobsOpts) {
	fs.StringVar(&o.View, "view", "new", "new, saved, applied, hidden or all (jobs list also: pending, the unranked postings no run has triaged yet)")
	fs.StringVar(&o.Query, "q", "", "only jobs whose title, company, location, team or note contains this")
	fs.StringVar(&o.fit, "fit", "", "only these role_fit readings (comma list; none = no reading)")
	fs.StringVar(&o.workMode, "work-mode", "", "only these work_mode readings (comma list; none = no reading)")
	fs.StringVar(&o.sponsor, "sponsorship", "", "only these sponsorship readings (comma list; none = no reading)")
	fs.StringVar(&o.state, "state", "", "only jobs in these US states (comma list of codes or names; none = no state listed)")
	fs.StringVar(&o.area, "area", "", "only jobs in these areas, as the location names them (comma list; none = no area listed)")
	fs.BoolVar(&o.Remote, "remote", false, "only jobs the board lists as remote (its remote flag or location text; not triage's work_mode)")
	fs.BoolVar(&o.OrRemote, "or-remote", false, "with --state or --area, also keep jobs the board lists as remote (remote, or in these places)")
	fs.IntVar(&o.MaxAge, "max-age", 0, fmt.Sprintf("only jobs at most this many days old (0 = any, up to %d)", maxAgeDays))
	fs.BoolVar(&o.Open, "open", false, "leave out roles the board no longer lists")
	fs.Float64Var(&o.HalfLife, "half-life", jobinsight.DefaultScoreHalfLifeDays, "days of age that halve a job's freshness bonus in priority")
	addFilterFlags(fs, &o.more)
}

// check validates the shared flags. sortable is false for stats, which has no
// order.
func (o *jobsOpts) check(cmd string, sortable bool) error {
	switch {
	case !slices.Contains(jobview.Views, o.View):
		return usagef("%s: --view must be one of %s", cmd, strings.Join(jobview.Views, ", "))
	case o.View == jobview.ViewPending && !o.pendingOK:
		// Untriaged postings have no fit, relevance or priority, so there is
		// nothing for stats, gaps or the audit to aggregate or sample.
		return usagef("%s: --view pending lists postings no run has triaged, which have no fit, relevance or priority; only `jobs list --view pending` takes it", cmd)
	case len(o.Query) > maxQueryBytes || !utf8.ValidString(o.Query):
		return usagef("%s: --q must be UTF-8 and at most %d bytes", cmd, maxQueryBytes)
	case sortable && !slices.Contains(jobview.Sorts, o.Sort):
		return usagef("%s: --sort must be one of %s", cmd, strings.Join(jobview.Sorts, ", "))
	case o.MaxAge < 0 || o.MaxAge > maxAgeDays:
		return usagef("%s: --max-age must be 0-%d", cmd, maxAgeDays)
	case !(o.HalfLife > 0 && o.HalfLife <= jobinsight.MaxHalfLifeDays):
		return usagef("%s: --half-life must be more than 0 and at most %g", cmd, jobinsight.MaxHalfLifeDays)
	}
	var err error
	if o.Fits, err = optionSet(cmd, "fit", "role_fit", o.fit); err != nil {
		return err
	}
	if o.WorkModes, err = optionSet(cmd, "work-mode", "work_mode", o.workMode); err != nil {
		return err
	}
	if o.Sponsors, err = optionSet(cmd, "sponsorship", "sponsorship", o.sponsor); err != nil {
		return err
	}
	if o.States, err = stateSet(cmd, o.state); err != nil {
		return err
	}
	if o.Areas, err = areaSet(cmd, o.area); err != nil {
		return err
	}
	switch {
	case o.OrRemote && o.Remote:
		return usagef("%s: --or-remote widens --state/--area to remote roles; --remote narrows to them; use one", cmd)
	case o.OrRemote && o.States == nil && o.Areas == nil:
		return usagef("%s: --or-remote needs --state or --area (alone it is --remote)", cmd)
	}
	if err := o.more.check(cmd); err != nil {
		return err
	}
	o.Opts.More = o.more.Filter
	return nil
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

// whereText is " · " and the job's location for the text list: the places
// and remote listing Vani read, or the board's text when it read none ("" when
// there is nothing to say). The Mac app's Job.whereLabel is the same rule.
func whereText(j jobview.Job) string {
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

// optionSet parses a comma list of the jdtriage fixture's options for slot.
// Empty means no filter (nil).
func optionSet(cmd, flagName, slot, raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	valid := jdtriage.Descriptions()[slot]
	if slot == "category" {
		valid = jobcategory.Descriptions()
	}
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
	info     jobview.InsightInfo
	now      time.Time
	halfLife float64
	// hits caches each browsable posting's keyword hits; boiler is each
	// company's blurb terms, learned from those hits (learn).
	hits   map[posting.Key][]jobinsight.Hit
	boiler map[string]map[string]bool
	// rc is the one ranking context (jobinsight.InputsFor) every job's score
	// is built from; newInsight fills it.
	rc jobinsight.RankContext
	// sem is the cache-only knowledge-base matcher; nil when off or unusable.
	sem *semantic.Matcher
	// logos finds each posting's cached company icon; nil when the config
	// could not be read (every job then shows its monogram).
	logos *logoIndex
	// drop is the config's prefilter (exclusions and title readings, as `vani
	// prefilter` and the run apply it); nil when the config sets none or could
	// not be read.
	drop *pipeline.DropPolicy
	// manual holds the owner's manual readings (internal/manualtriage) so Enrich
	// can show them, stale ones too; nil when none could be read.
	manual *manualReadingSet
	// resumes is the per-job resume store (internal/resumestore) Enrich reads a
	// summary from; nil when the state directory is unknown.
	resumes *resumestore.Store
}

// learn extracts the keywords of every posting the Jobs views can show (kept
// or marked) once, and learns each company's blurb from them, so a term every
// one of a company's postings repeats is not counted as that posting's ask.
//
// A non-empty only (a companyKey) restricts that to one company: a blurb is
// learned from a company's own postings alone, so one row's hits and boilerplate
// come out the same as in the full pass at the cost of that company's postings,
// not the ledger's. in.info.Boilerplate then counts only that company's terms,
// which is listing metadata no single row carries.
func (in *insight) learn(snap ledger.Snapshot, m map[posting.Key]marks.Entry, only string) {
	in.hits = map[posting.Key][]jobinsight.Hit{}
	var companies []string
	var all [][]jobinsight.Hit
	for _, r := range snap.Records {
		if only != "" && companyKey(r.Posting.Company) != only {
			continue
		}
		if !jobview.InView("all", r, m[r.Key].Mark) {
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
	cfg, err := e.loadConfig()
	return newInsightFor(cfg, err, e.stateDir, halfLife, snap, m)
}

// newRowInsight is newInsight for enriching the one record r (jobs find, show,
// mark, note). It learns only r's company, which is all r's keywords and score
// read, so the row is byte-identical to the full pass's
// (TestRowInsightParity) without tokenising every other company's postings.
func (e *env) newRowInsight(snap ledger.Snapshot, m map[posting.Key]marks.Entry, r ledger.Record) (*insight, error) {
	cfg, err := e.loadConfig()
	return newInsightScoped(cfg, err, e.stateDir, jobinsight.DefaultScoreHalfLifeDays, snap, m, companyKey(r.Posting.Company))
}

// newInsightFor is newInsight over a config already read (cfgErr is why it
// could not be). The daily run builds the notes gate's relevance from it, so
// the gate and the Jobs views rank with one context.
func newInsightFor(cfg config.Config, err error, stateDir string, halfLife float64, snap ledger.Snapshot, m map[posting.Key]marks.Entry) (*insight, error) {
	return newInsightScoped(cfg, err, stateDir, halfLife, snap, m, "")
}

// newInsightScoped is newInsightFor learning only the company only names (see
// learn); "" learns every company.
func newInsightScoped(cfg config.Config, err error, stateDir string, halfLife float64, snap ledger.Snapshot, m map[posting.Key]marks.Entry, only string) (*insight, error) {
	v, verr := jobinsight.Default()
	if verr != nil {
		return nil, verr
	}
	in := &insight{vocab: v, now: now(), halfLife: halfLife,
		info: jobview.InsightInfo{HalfLifeDays: halfLife, Vocab: v.Len()}}
	if err == nil {
		in.logos = newLogoIndex(cfg, stateDir)
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
	in.learn(snap, m, only)
	marked := make(map[posting.Key]marks.Mark, len(m))
	for k, e := range m {
		marked[k] = e.Mark
	}
	in.rc = jobinsight.NewRankContext(snap.Records, marked, in.now)
	in.rc.HalfLifeDays, in.rc.Target, in.rc.Hits = halfLife, in.target != nil, in.targetHits
	in.info.Weights = rankWeightsHand
	if err == nil {
		in.attachSemantic(cfg, stateDir)
		in.rc.DropRules, in.rc.TitleRank = rankRules(cfg)
		in.rc.EscalateBelowP = rankEscalateBelowP(cfg)
		if p := dropPolicy(cfg, cfg.Decide.OpenRouterName()); p.Prefilters() {
			in.drop = &p
		}
		in.rc.AgeNeutral = jobinsight.AgeNeutralSet(cfg.AgeNeutralCompanies)
		var warn string
		in.rc.KBMatch, warn = kbmatchLookup(cfg, stateDir)
		in.warn(warn)
		in.rc.Weights, in.info.Weights, warn = rankWeights(cfg)
		in.warn(warn)
	}
	// The owner's manual readings need no config: the store is beside the ledger.
	var warn string
	in.manual, in.rc.Manual, warn = manualReadings(stateDir)
	in.warn(warn)
	// Open creates nothing and reads nothing; an unusable store shows per job
	// (jobview.ResumeOf), so it can never look like "no resume".
	in.resumes, _ = resumestore.Open(stateDir)
	return in, nil
}

// warn appends a non-empty warning to the listing's.
func (in *insight) warn(w string) {
	if w == "" {
		return
	}
	if in.info.Warning != "" {
		in.info.Warning += "; "
	}
	in.info.Warning += w
}

// targetHits splits the distinct target terms a posting names into those its
// title names and those only the rest of it names (a term in both counts once,
// as a title term), with the company's blurb terms left out.
func (in *insight) targetHits(r ledger.Record) (title, body int) {
	boiler := in.boiler[companyKey(r.Posting.Company)]
	inTitle := map[string]bool{}
	for _, h := range jobinsight.Without(in.vocab.Extract(r.Posting.Title), boiler) {
		if in.target[h.Term] {
			inTitle[h.Term] = true
		}
	}
	whole, ok := in.hits[r.Key]
	if !ok {
		whole = in.vocab.Extract(r.Posting.Title + "\n" + r.Posting.Description)
	}
	for _, h := range jobinsight.Without(whole, boiler) {
		if in.target[h.Term] && !inTitle[h.Term] {
			body++
		}
	}
	return len(inTitle), body
}

// companyKey is the key Boilerplate files a company under (and the rank
// context's companyKey in internal/jobinsight).
func companyKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func oneLine(s string, n int) string { return jobview.CleanText(s, n, false) }

// enrich fills j's insight fields from r and returns the posting's full
// keyword hits (for stats).
func (in *insight) Enrich(j *jobview.Job, r ledger.Record, kwLimit int) []jobinsight.Hit {
	jobview.Locate(j, r.Posting)
	j.Resume = jobview.ResumeFor(in.resumes, string(r.Key))
	if at, src, known := jobinsight.FreshAge(r.Posting.PostedAt, r.FirstSeen, in.rc.FirstCrawl[jobinsight.BoardOf(string(r.Key))], in.now); known {
		age := jobinsight.AgeDays(at, in.now)
		j.FreshAt, j.FreshSrc, j.AgeDays = at, string(src), &age
	}
	if j.Untriaged {
		in.enrichUntriaged(j, r)
		return nil
	}
	hits, ok := in.hits[r.Key]
	if !ok {
		hits = in.vocab.Extract(r.Posting.Title + "\n" + r.Posting.Description)
	}
	hits = jobinsight.Without(hits, in.boiler[companyKey(r.Posting.Company)])
	gap := in.vocab.Terms(r.Annotations[ledger.AnnMissing])
	j.Keywords = jobinsight.Annotate(hits, in.target, gap, kwLimit)
	res := in.rc.ScoreOf(r)
	j.Priority, j.Relevance, j.RelevanceBasis, j.ScoreFactors = res.Score, res.Relevance, res.Basis, res.Factors
	j.RelevanceKnown = res.RelevanceKnown
	j.Reasons = jobview.ParseReasons(j.Explanation)
	in.semanticInto(j, r)
	j.ManualTriage = in.manual.view(r)
	j.Pay = jobinsight.PostingPay(r.Posting)
	j.Logo = in.logos.path(r.Key, r.Posting.Company)
	row := jobview.RowOf(*j)
	j.Row = &row
	return hits
}

// enrichUntriaged is Enrich for a posting no run has triaged: where it is, how
// fresh, its company's icon, and whether the next run's prefilter would drop
// it. It scores nothing, so it reads no keywords and builds no row: those need
// a triage reading, and a score computed without one would look like a rank.
func (in *insight) enrichUntriaged(j *jobview.Job, r ledger.Record) {
	j.Logo = in.logos.path(r.Key, r.Posting.Company)
	// Marked postings are never dropped by exclusions (hideExcluded, protectMarked).
	if in.drop == nil || r.BodyEvicted || j.Mark != marks.None {
		return
	}
	// The kind is the backend the policy names, not its reason text, which
	// carries the rule.
	if a, _, dropped := in.drop.EvaluatePosting(r.Posting); dropped {
		j.PrefilterDrop = "title"
		if a.Backend == pipeline.ExcludeBackend {
			j.PrefilterDrop = "excluded"
		}
	}
}

// --- stats ---

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
