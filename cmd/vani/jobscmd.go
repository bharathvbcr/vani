package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani jobs` is the browsing surface over what the pipeline retrieved, and
// the one writer of the user's marks (internal/marks). It reads the ledger
// with ledger.ReadSnapshot, so it works while a run or the daemon holds the
// ledger lock, and it never writes the ledger. With --json every reply is one
// JSON object on stdout ({"error"} on failure); the Mac app's Jobs tab is a
// client of exactly this contract (macos/Sources/VaniApplyKit/Jobs.swift).

const (
	defaultJobsLimit = 1000
	maxJobsLimit     = 5000
	maxQueryBytes    = 200
	jobFieldRunes    = 300
	jobNoteRunes     = 2000
	maxDescRunes     = 60000
)

// Views. "new" is every kept posting the user has not marked; the mark views
// show marked postings whatever became of them since (a saved role that
// closed stays saved, flagged closed); "all" is kept plus marked.
var jobViews = []string{"new", "saved", "applied", "hidden", "all"}

// keptStatus reports whether a posting passed triage and is still listed:
// these are the retrieved roles worth browsing. Dropped, dead and duplicate
// postings appear only when marked.
func keptStatus(s posting.Status) bool {
	switch s {
	case posting.Triaged, posting.Unsure, posting.Scored, posting.Explained, posting.Digested:
		return true
	}
	return false
}

type jobJSON struct {
	Key         posting.Key       `json:"key"`
	Company     string            `json:"company"`
	Title       string            `json:"title"`
	Location    string            `json:"location,omitempty"`
	Remote      bool              `json:"remote,omitempty"`
	Department  string            `json:"department,omitempty"`
	URL         string            `json:"url,omitempty"` // http(s) only (digest.HTTPURL)
	ApplyURL    string            `json:"apply_url,omitempty"`
	Status      posting.Status    `json:"status"`
	Closed      bool              `json:"closed,omitempty"` // the board no longer lists it
	Rank        *float64          `json:"rank,omitempty"`
	Coverage    *float64          `json:"coverage,omitempty"`
	Explanation string            `json:"explanation,omitempty"`
	Missing     string            `json:"missing,omitempty"`
	Unsure      string            `json:"unsure,omitempty"`
	LCA         string            `json:"lca,omitempty"`
	Employer    string            `json:"employer,omitempty"`
	Triage      map[string]string `json:"triage,omitempty"`
	Category    string            `json:"category,omitempty"`
	FirstSeen   time.Time         `json:"first_seen"`
	PostedAt    time.Time         `json:"posted_at,omitzero"`
	Mark        marks.Mark        `json:"mark,omitempty"`
	MarkedAt    time.Time         `json:"marked_at,omitzero"`
	Note        string            `json:"note,omitempty"`
	NoteAt      time.Time         `json:"note_at,omitzero"`
	Description string            `json:"description,omitempty"` // `jobs show` only

	// Display-only insight (jobsinsight.go): freshness, scores, keywords and
	// the note split into reasons. No engine rule reads them.
	FreshAt        time.Time            `json:"fresh_at,omitzero"`
	FreshSrc       string               `json:"fresh_src,omitempty"` // posted or seen
	AgeDays        *int                 `json:"age_days,omitempty"`
	Relevance      float64              `json:"relevance"`
	RelevanceBasis jobinsight.Basis     `json:"relevance_basis,omitempty"`
	Priority       float64              `json:"priority"`
	Keywords       []jobinsight.Keyword `json:"keywords,omitempty"`
	Reasons        []reasonJSON         `json:"reasons,omitempty"`
	// Places, RemoteListed and RemoteScope are what the board's location says
	// (internal/location), not triage's work_mode reading.
	Places       []placeJSON `json:"places,omitempty"`
	RemoteListed bool        `json:"remote_listed,omitempty"`
	RemoteScope  string      `json:"remote_scope,omitempty"`
	// Pay is the pay the posting's text states (jobinsight.ParsePay), at most
	// one range per currency.
	Pay []jobinsight.Pay `json:"pay,omitempty"`

	// Near-duplicate display (pipeline.AnnotateNearDuplicates): the posting
	// this one repeats (status duplicate), may repeat (candidate) or is the
	// same role elsewhere as (sibling), and the description overlap behind it.
	DupOf          string  `json:"dup_of,omitempty"`
	DupCandidateOf string  `json:"dup_candidate_of,omitempty"`
	DupSiblingOf   string  `json:"dup_sibling_of,omitempty"`
	DupScore       float64 `json:"dup_score,omitempty"`
}

type jobsListJSON struct {
	View   string         `json:"view"`
	Query  string         `json:"query,omitempty"`
	Jobs   []jobJSON      `json:"jobs"`
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
	Insight insightJSON `json:"insight"`
	// Pending is what no view lists yet: postings awaiting triage.
	Pending pendingJSON `json:"pending"`
}

func (e *env) cmdJobs(args []string) error {
	if len(args) == 0 {
		return usagef("jobs: expected list, stats, gaps, folders, show, find, ingest, mark or note")
	}
	switch args[0] {
	case "list":
		return e.jobsList(args[1:])
	case "stats":
		return e.jobsStatsCmd(args[1:])
	case "folders", "categories":
		return e.jobsFolders(args[1:])
	case "show":
		return e.jobsShow(args[1:])
	case "mark":
		return e.jobsMark(args[1:])
	case "gaps":
		return e.jobsGapsCmd(args[1:])
	case "note":
		return e.jobsNote(args[1:])
	case "find":
		return e.jobsFind(args[1:])
	case "ingest":
		return e.jobsIngest(args[1:])
	}
	return usagef("jobs: unknown subcommand %q", args[0])
}

// jobsReply writes v as JSON, or {"error"} when err is set, and returns the
// exit error. Usage errors stay usage errors.
func (e *env) jobsReply(asJSON bool, v any, err error, text func()) error {
	if !asJSON {
		if err == nil {
			text()
		}
		return err
	}
	if err != nil {
		if errors.As(err, new(usageError)) || errors.Is(err, flag.ErrHelp) {
			return err
		}
		v = struct {
			Error string `json:"error"`
		}{err.Error()}
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetEscapeHTML(false)
	if werr := enc.Encode(v); werr != nil {
		return werr
	}
	if err != nil {
		return errAlreadyReported
	}
	return nil
}

func (e *env) jobsList(args []string) error {
	fs := e.newFlags("jobs list")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	var o jobsOpts
	addSelectFlags(fs, &o)
	fs.StringVar(&o.sort, "sort", "default", "default, priority, relevance, fresh, company or salary (top of the stated range in --currency)")
	limit := fs.Int("limit", defaultJobsLimit, fmt.Sprintf("at most this many jobs (1-%d)", maxJobsLimit))
	if err := parse(fs, args); err != nil {
		return err
	}
	var out jobsListJSON
	err := func() error {
		if *limit < 1 || *limit > maxJobsLimit {
			return usagef("jobs list: --limit must be 1-%d", maxJobsLimit)
		}
		if err := o.check("jobs list", true); err != nil {
			return err
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		in, err := e.newInsight(o.halfLife, snap, m)
		if err != nil {
			return err
		}
		out = listJobs(snap, m, &o, in, *limit)
		out.Ledger, out.Records = filepath.Join(e.stateDir, "ledger"), len(snap.Records)
		out.Pending = pendingOf(snap, m, strings.ToLower(strings.TrimSpace(o.query)))
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		for _, j := range out.Jobs {
			rank := "  -"
			if j.Rank != nil {
				rank = fmt.Sprintf("%3.0f", *j.Rank)
			}
			mark := string(j.Mark)
			if mark == "" {
				mark = "-"
			}
			age := "   ?"
			if j.AgeDays != nil {
				age = fmt.Sprintf("%3dd", *j.AgeDays)
			}
			pay := payNote(j, o.more.currency)
			if pay != "" {
				pay = "  " + pay
			}
			fmt.Fprintf(e.stdout, "%s  %5.1f  %s  %-7s  %s: %s%s%s  [%s]\n", rank, j.Priority, age, mark, j.Company, j.Title, pay, whereText(j), j.Key)
		}
		fmt.Fprintf(e.stdout, "%s: %d of %d shown\n", out.View, out.Shown, out.Total)
		e.printPending(out.Pending, out.Query)
		if out.Insight.Warning != "" {
			fmt.Fprintf(e.stdout, "note: %s\n", out.Insight.Warning)
		}
	})
}

// jobsStatsCmd aggregates the same selection `jobs list` would show, for the
// app's Insights charts: distributions, not rows, so the app need not fetch
// every job to draw them.
func (e *env) jobsStatsCmd(args []string) error {
	fs := e.newFlags("jobs stats")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	var o jobsOpts
	addSelectFlags(fs, &o)
	if err := parse(fs, args); err != nil {
		return err
	}
	var out jobsStatsJSON
	err := func() error {
		if err := o.check("jobs stats", false); err != nil {
			return err
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		in, err := e.newInsight(o.halfLife, snap, m)
		if err != nil {
			return err
		}
		sel, _ := selectJobs(snap, m, &o, in, 0)
		out = jobsStats(sel, in)
		out.Salary = payStats(sel, o.more.currency)
		out.View, out.Query, out.Torn = o.view, o.query, snap.Torn
		out.Ledger, out.Records = filepath.Join(e.stateDir, "ledger"), len(snap.Records)
		out.Pending = pendingOf(snap, m, strings.ToLower(strings.TrimSpace(o.query)))
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: %d jobs\n", out.View, out.Total)
		e.printPending(out.Pending, out.Query)
		fmt.Fprint(e.stdout, "role fit:")
		for _, v := range roleFitOrder {
			fmt.Fprintf(e.stdout, " %s %d,", v, out.Fit[v])
		}
		fmt.Fprintf(e.stdout, " none %d\nage:", out.Fit["none"])
		for _, b := range out.Age {
			fmt.Fprintf(e.stdout, " %s %d,", b.Label, b.Posted+b.Seen)
		}
		fmt.Fprintf(e.stdout, " (board-dated %d, first-seen %d)\n", out.FreshSource["posted"], out.FreshSource["seen"])
		fmt.Fprintf(e.stdout, "where: remote %d,%s\n", out.RemoteListed, stateLine(out.State))
		s := out.Salary
		fmt.Fprintf(e.stdout, "pay (%s, middle of range):", s.Currency)
		for _, b := range s.Buckets {
			fmt.Fprintf(e.stdout, " %s %d,", b.Label, b.N)
		}
		fmt.Fprintf(e.stdout, " median %s (stated %d, other currency %d, none %d)\nkeywords:", shortMoney(s.Median), s.Listed, s.Other, s.Unlisted)
		for _, k := range out.Keywords {
			fmt.Fprintf(e.stdout, " %s %d,", k.Term, k.N)
		}
		fmt.Fprintln(e.stdout)
		if out.Insight.Warning != "" {
			fmt.Fprintf(e.stdout, "note: %s\n", out.Insight.Warning)
		}
	})
}

type jobFolderItem struct {
	Category string `json:"category"`
	Label    string `json:"label"`
	Count    int    `json:"count"`
}

type jobsFoldersJSON struct {
	View    string          `json:"view"`
	Total   int             `json:"total"`
	Folders []jobFolderItem `json:"folders"`
	Ledger  string          `json:"ledger"`
	Records int             `json:"records"`
}

func (e *env) jobsFolders(args []string) error {
	fs := e.newFlags("jobs folders")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	view := fs.String("view", "new", "view to count: "+strings.Join(jobViews, ", "))
	if err := parse(fs, args); err != nil {
		return err
	}
	var out jobsFoldersJSON
	err := func() error {
		if !contains(jobViews, *view) {
			return usagef("jobs folders: --view %q must be one of %s", *view, strings.Join(jobViews, ", "))
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		counts := map[string]int{}
		total := 0
		for _, r := range snap.Records {
			entry := m[r.Key]
			if !inView(*view, r, entry.Mark) {
				continue
			}
			total++
			j := toJob(r, entry)
			cat := j.Category
			if cat == "" {
				cat = "none"
			}
			counts[cat]++
		}
		var items []jobFolderItem
		for _, opt := range jobcategory.Options {
			items = append(items, jobFolderItem{
				Category: opt.Value,
				Label:    opt.Label,
				Count:    counts[opt.Value],
			})
		}
		items = append(items, jobFolderItem{
			Category: "none",
			Label:    "Uncategorized",
			Count:    counts["none"],
		})
		out = jobsFoldersJSON{
			View:    *view,
			Total:   total,
			Folders: items,
			Ledger:  filepath.Join(e.stateDir, "ledger"),
			Records: len(snap.Records),
		}
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: %d roles across category folders\n", out.View, out.Total)
		for _, f := range out.Folders {
			fmt.Fprintf(e.stdout, "  %-30s  %-28s  %d\n", f.Category, f.Label, f.Count)
		}
	})
}

func (e *env) jobsShow(args []string) error {
	fs := e.newFlags("jobs show")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	var out jobJSON
	err := func() error {
		if fs.NArg() != 1 {
			return usagef("jobs show: expected one posting key")
		}
		k, err := checkJobKey(fs.Arg(0))
		if err != nil {
			return err
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		r, ok := findRecord(snap, k)
		if !ok {
			return fmt.Errorf("jobs show: no posting %q in the ledger", k)
		}
		in, err := e.newInsight(jobinsight.DefaultHalfLifeDays, snap, m)
		if err != nil {
			return err
		}
		out = toJob(r, m[k])
		in.enrich(&out, r, showKeywords)
		out.Description = cleanText(r.Posting.Description, maxDescRunes, true)
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s at %s\n%s\n\n%s\n", out.Title, out.Company, out.URL, out.Description)
	})
}

func (e *env) jobsMark(args []string) error {
	fs := e.newFlags("jobs mark")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	var out jobJSON
	err := func() error {
		if fs.NArg() != 2 {
			return usagef("jobs mark: expected a posting key and saved, applied, hidden or clear")
		}
		mark := marks.Mark(fs.Arg(1))
		if fs.Arg(1) == "clear" {
			mark = marks.None
		} else if mark == marks.None || !mark.Valid() {
			return usagef("jobs mark: %q is not saved, applied, hidden or clear", fs.Arg(1))
		}
		return e.jobsWrite(fs.Arg(0), &out, func(dir string, k posting.Key) (marks.Entry, error) {
			return marks.SetMark(dir, k, mark, now())
		})
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: %s\n", out.Key, orDash(string(out.Mark)))
	})
}

func (e *env) jobsNote(args []string) error {
	fs := e.newFlags("jobs note")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	var out jobJSON
	err := func() error {
		if fs.NArg() != 2 {
			return usagef(`jobs note: expected a posting key and the note ("" clears it)`)
		}
		note := strings.TrimSpace(fs.Arg(1))
		return e.jobsWrite(fs.Arg(0), &out, func(dir string, k posting.Key) (marks.Entry, error) {
			return marks.SetNote(dir, k, note, now())
		})
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		fmt.Fprintf(e.stdout, "%s: note %s\n", out.Key, map[bool]string{true: "saved", false: "cleared"}[out.Note != ""])
	})
}

// jobsWrite checks the key names a posting the ledger has, applies set, and
// fills out with the updated job. Marking a posting Vani never saw is refused:
// a typo must not become an orphan entry.
func (e *env) jobsWrite(raw string, out *jobJSON, set func(string, posting.Key) (marks.Entry, error)) error {
	k, err := checkJobKey(raw)
	if err != nil {
		return err
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return fmt.Errorf("jobs: read the ledger: %w", err)
	}
	r, ok := findRecord(snap, k)
	if !ok {
		return fmt.Errorf("jobs: no posting %q in the ledger", k)
	}
	in, err := e.newInsight(jobinsight.DefaultHalfLifeDays, snap, nil)
	if err != nil {
		return err
	}
	entry, err := set(marks.Dir(e.stateDir), k)
	if err != nil {
		return err
	}
	*out = toJob(r, entry)
	in.enrich(out, r, showKeywords)
	return nil
}

func checkJobKey(raw string) (posting.Key, error) {
	k := posting.Key(raw)
	if err := marks.CheckKey(k); err != nil {
		return "", usagef("jobs: %v", err)
	}
	return k, nil
}

func (e *env) jobsState() (ledger.Snapshot, map[posting.Key]marks.Entry, error) {
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return ledger.Snapshot{}, nil, fmt.Errorf("jobs: read the ledger: %w", err)
	}
	m, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return ledger.Snapshot{}, nil, fmt.Errorf("jobs: read marks: %w", err)
	}
	return snap, m, nil
}

func findRecord(snap ledger.Snapshot, k posting.Key) (ledger.Record, bool) {
	i := sort.Search(len(snap.Records), func(i int) bool { return snap.Records[i].Key >= k })
	if i < len(snap.Records) && snap.Records[i].Key == k {
		return snap.Records[i], true
	}
	return ledger.Record{}, false
}

// inView reports whether a record with mark belongs in view.
func inView(view string, r ledger.Record, mark marks.Mark) bool {
	switch view {
	case "new":
		return mark == marks.None && keptStatus(r.Status)
	case "all":
		return mark != marks.None || keptStatus(r.Status)
	}
	return string(mark) == view
}

func listJobs(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *jobsOpts, in *insight, limit int) jobsListJSON {
	out := jobsListJSON{View: o.view, Query: o.query, Sort: o.sort, Torn: snap.Torn, Jobs: []jobJSON{}, Insight: in.info}
	sel, counts := selectJobs(snap, m, o, in, listKeywords)
	out.Counts = counts
	sortJobs(sel, o.view, o.sort)
	out.Total = len(sel)
	if len(sel) > limit {
		sel = sel[:limit]
	}
	for _, s := range sel {
		out.Jobs = append(out.Jobs, s.job)
	}
	out.Shown = len(out.Jobs)
	return out
}

// roleFitOrder ranks triage's role_fit labels (jdtriage's fixture) for
// display: unscored postings sort by how well triage said they fit. Display
// order only; no engine rule reads it.
var roleFitOrder = []string{"strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain"}

func fitOrder(j jobJSON) int {
	for i, v := range roleFitOrder {
		if j.Triage["role_fit"] == v {
			return i
		}
	}
	return len(roleFitOrder)
}

func toJob(r ledger.Record, entry marks.Entry) jobJSON {
	p := r.Posting
	ann := r.Annotations
	j := jobJSON{
		Key:         r.Key,
		Company:     cleanText(p.Company, jobFieldRunes, false),
		Title:       cleanText(p.Title, jobFieldRunes, false),
		Location:    cleanText(p.Location, jobFieldRunes, false),
		Remote:      p.Remote,
		Department:  cleanText(p.Department, jobFieldRunes, false),
		Status:      r.Status,
		Closed:      r.Status == posting.Dead,
		Explanation: cleanText(ann[ledger.AnnExplanation], jobNoteRunes, false),
		Missing:     cleanText(ann[ledger.AnnMissing], jobNoteRunes, false),
		LCA:         cleanText(ann[ledger.AnnLCA], jobFieldRunes, false),
		Employer:    cleanText(ann[ledger.AnnEmployer], jobFieldRunes, false),
		FirstSeen:   r.FirstSeen,
		PostedAt:    p.PostedAt,
		Mark:        entry.Mark,
		MarkedAt:    entry.MarkedAt,
		Note:        entry.Note,
		NoteAt:      entry.NoteAt,
	}
	if j.Title == "" {
		j.Title = "(untitled)"
	}
	if u, ok := digest.HTTPURL(p.URL); ok {
		j.URL = u
	}
	if u, ok := digest.HTTPURL(p.ApplyURL); ok {
		j.ApplyURL = u
	}
	if v, ok := digest.Rank(r); ok {
		j.Rank = &v
	}
	if v, ok := digest.Coverage(r); ok {
		j.Coverage = &v
	}
	if digest.IsUnsure(r) {
		j.Unsure = digest.WhyUnsure(ann)
	}
	for _, slot := range digest.TriageSlots {
		if v := cleanText(ann[ledger.AnnTriagePrefix+slot], 80, false); v != "" {
			if j.Triage == nil {
				j.Triage = map[string]string{}
			}
			j.Triage[slot] = v
		}
	}
	if cat := cleanText(ann[ledger.AnnCategory], 80, false); cat != "" {
		j.Category = cat
	} else if cat := cleanText(ann[ledger.AnnTriagePrefix+"category"], 80, false); cat != "" {
		j.Category = cat
	}
	if j.Category != "" {
		if j.Triage == nil {
			j.Triage = map[string]string{}
		}
		j.Triage["category"] = j.Category
	}
	j.DupOf = cleanText(ann[pipeline.DuplicateOfKey], jobFieldRunes, false)
	j.DupCandidateOf = cleanText(ann[pipeline.AnnDupCandidateOf], jobFieldRunes, false)
	j.DupSiblingOf = cleanText(ann[pipeline.AnnDupSiblingOf], jobFieldRunes, false)
	if s := ann[pipeline.AnnDupScore]; s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 && f <= 1 {
			j.DupScore = f
		}
	}
	return j
}

// cleanText makes untrusted posting text safe to show as plain text: control
// characters (and, unless keepLines, newlines) become single spaces, runs of
// spaces collapse, and the result is cut at maxRunes. It does no Markdown
// escaping; the app shows it verbatim, never as Markdown or HTML.
func cleanText(s string, maxRunes int, keepLines bool) string {
	var b strings.Builder
	n, space, newline := 0, false, false
	for _, r := range strings.ToValidUTF8(s, "�") {
		if keepLines && r == '\n' {
			newline = b.Len() > 0
			space = false
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == ' ' || r == ' ' {
			space = b.Len() > 0
			continue
		}
		if n >= maxRunes {
			b.WriteString("…")
			break
		}
		switch {
		case newline:
			b.WriteByte('\n')
			n++
		case space:
			b.WriteByte(' ')
			n++
		}
		newline, space = false, false
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
