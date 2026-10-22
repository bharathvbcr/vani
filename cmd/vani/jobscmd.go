package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
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
)

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
	fs.StringVar(&o.Sort, "sort", "default", "default, priority, relevance, fresh, company or salary (top of the stated range in --currency)")
	limit := fs.Int("limit", defaultJobsLimit, fmt.Sprintf("at most this many jobs (1-%d)", maxJobsLimit))
	if err := parse(fs, args); err != nil {
		return err
	}
	var out jobview.List
	err := func() error {
		if *limit < 1 || *limit > maxJobsLimit {
			return usagef("jobs list: --limit must be 1-%d", maxJobsLimit)
		}
		o.pendingOK = true
		if err := o.check("jobs list", true); err != nil {
			return err
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		in, err := e.newInsight(o.HalfLife, snap, m)
		if err != nil {
			return err
		}
		out = jobview.BuildList(snap, m, &o.Opts, in, in.info, listKeywords, *limit)
		out.Ledger, out.Records = filepath.Join(e.stateDir, "ledger"), len(snap.Records)
		out.Pending = jobview.PendingOf(snap, m, strings.ToLower(strings.TrimSpace(o.Query)))
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
			pay := payNote(j, o.more.Currency)
			if pay != "" {
				pay = "  " + pay
			}
			// An untriaged posting has no priority: a dash, never 0.0.
			priority := fmt.Sprintf("%5.1f", j.Priority)
			if j.Untriaged {
				priority = "    -"
			}
			flag := ""
			if j.PrefilterDrop != "" {
				flag = "  (the prefilter would drop it)"
			}
			fmt.Fprintf(e.stdout, "%s  %s  %s  %-7s  %s: %s%s%s%s  [%s]\n", rank, priority, age, mark, j.Company, j.Title, pay, whereText(j), flag, j.Key)
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
	var out jobview.Stats
	err := func() error {
		if err := o.check("jobs stats", false); err != nil {
			return err
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		in, err := e.newInsight(o.HalfLife, snap, m)
		if err != nil {
			return err
		}
		sel, _ := jobview.Select(snap, m, &o.Opts, in, 0)
		out = jobview.StatsOf(sel, in.info, in.target)
		out.Salary = jobview.PayStatsOf(sel, o.more.Currency)
		out.View, out.Query, out.Torn = o.View, o.Query, snap.Torn
		out.Ledger, out.Records = filepath.Join(e.stateDir, "ledger"), len(snap.Records)
		out.Pending = jobview.PendingOf(snap, m, strings.ToLower(strings.TrimSpace(o.Query)))
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
		fmt.Fprintf(e.stdout, " median %s (stated %d, other currency %d, none %d)\nkeywords:", jobinsight.ShortMoney(s.Median), s.Listed, s.Other, s.Unlisted)
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
	view := fs.String("view", "new", "view to count: "+strings.Join(jobview.Views, ", "))
	if err := parse(fs, args); err != nil {
		return err
	}
	var out jobsFoldersJSON
	err := func() error {
		if !slices.Contains(jobview.Views, *view) {
			return usagef("jobs folders: --view %q must be one of %s", *view, strings.Join(jobview.Views, ", "))
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		counts := map[string]int{}
		total := 0
		for _, r := range snap.Records {
			entry := m[r.Key]
			if !jobview.InView(*view, r, entry.Mark) {
				continue
			}
			total++
			j := jobview.ToJob(r, entry)
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
	var out jobview.Job
	err := func() error {
		if fs.NArg() != 1 {
			return usagef("jobs show: expected one posting key")
		}
		k, err := checkJobKey(fs.Arg(0))
		if err != nil {
			return err
		}
		snap, m, err := e.jobsStateFor(rowScope(func(ledger.Snapshot) (posting.Key, bool) { return k, true }))
		if err != nil {
			return err
		}
		r, ok, err := rowRecord(snap, k)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("jobs show: no posting %q in the ledger", k)
		}
		in, err := e.newRowInsight(snap, m, r)
		if err != nil {
			return err
		}
		out = jobview.ToJob(r, m[k])
		in.Enrich(&out, r, showKeywords)
		out.Description = jobview.CleanText(r.Posting.Description, jobview.DescRunes, true)
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
	var out jobview.Job
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
	var out jobview.Job
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
func (e *env) jobsWrite(raw string, out *jobview.Job, set func(string, posting.Key) (marks.Entry, error)) error {
	k, err := checkJobKey(raw)
	if err != nil {
		return err
	}
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), rowScope(func(ledger.Snapshot) (posting.Key, bool) { return k, true }))
	if err != nil {
		return fmt.Errorf("jobs: read the ledger: %w", err)
	}
	r, ok, err := rowRecord(snap, k)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("jobs: no posting %q in the ledger", k)
	}
	entry, err := set(marks.Dir(e.stateDir), k)
	if err != nil {
		return err
	}
	// The insight is built from the marks as written, the same ones `jobs list`
	// reads, so the priority reported here (company factor included) is the one
	// the list shows for this job.
	m, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return fmt.Errorf("jobs: read marks: %w", err)
	}
	in, err := e.newRowInsight(snap, m, r)
	if err != nil {
		return err
	}
	*out = jobview.ToJob(r, entry)
	in.Enrich(out, r, showKeywords)
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
	return e.jobsStateFor(nil)
}

// rowScope is the ledger.ReadSnapshotScoped scope for enriching one row: the
// record pick names and every record of its company are read whole (what
// newRowInsight learns from); every other record keeps its description out
// (BodyEvicted) and is read only for rank context (NewRankContext) and URL
// matching, which read no description. A nil pick result, or a key the ledger
// lacks, loads nothing. A row with no company loads every record, as
// newRowInsight then learns from all of them.
func rowScope(pick func(ledger.Snapshot) (posting.Key, bool)) func(ledger.Snapshot) func(ledger.Record) bool {
	return func(lean ledger.Snapshot) func(ledger.Record) bool {
		k, ok := pick(lean)
		if !ok {
			return nil
		}
		r, ok := findRecord(lean, k)
		if !ok {
			return nil
		}
		co := companyKey(r.Posting.Company)
		return func(x ledger.Record) bool { return co == "" || companyKey(x.Posting.Company) == co }
	}
}

// rowRecord is findRecord for a snapshot read with rowScope: a row whose body
// was left out is an error, never a record with an empty description.
func rowRecord(snap ledger.Snapshot, k posting.Key) (ledger.Record, bool, error) {
	r, ok := findRecord(snap, k)
	if ok && r.BodyEvicted {
		return ledger.Record{}, false, fmt.Errorf("jobs: %s: the posting text was not read", k)
	}
	return r, ok, nil
}

// jobsStateFor is jobsState reading the ledger with scope (nil: whole, via
// ledger.ReadSnapshot). Records scope leaves bodiless are not matched against
// exclusion rules, which can only drop a record from its own company's view.
func (e *env) jobsStateFor(scope func(ledger.Snapshot) func(ledger.Record) bool) (ledger.Snapshot, map[posting.Key]marks.Entry, error) {
	var snap ledger.Snapshot
	var err error
	if scope == nil {
		snap, err = ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	} else {
		snap, err = ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), scope)
	}
	if err != nil {
		return ledger.Snapshot{}, nil, fmt.Errorf("jobs: read the ledger: %w", err)
	}
	m, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return ledger.Snapshot{}, nil, fmt.Errorf("jobs: read marks: %w", err)
	}
	// Listing jobs needs no config; when there is one, its exclusions hide now
	// what the next run will drop.
	if cfg, err := e.loadConfig(); err == nil {
		hideExcluded(snap.Records, m, cfg.Exclude)
	}
	return snap, m, nil
}

// hideExcluded shows each unmarked posting x matches as dropped, as the next
// run's exclusion pass (pipeline.ApplyExclusions) will record it. It changes
// the read snapshot only.
func hideExcluded(recs []ledger.Record, m map[posting.Key]marks.Entry, x config.Exclude) {
	if x.Empty() {
		return
	}
	for i, r := range recs {
		if r.BodyEvicted || m[r.Key].Mark != marks.None || !jobview.KeptStatus(r.Status) {
			continue
		}
		if _, ok := x.Match(r.Posting); ok {
			recs[i].Status = posting.Dropped
		}
	}
}

func findRecord(snap ledger.Snapshot, k posting.Key) (ledger.Record, bool) {
	i := sort.Search(len(snap.Records), func(i int) bool { return snap.Records[i].Key >= k })
	if i < len(snap.Records) && snap.Records[i].Key == k {
		return snap.Records[i], true
	}
	return ledger.Record{}, false
}

// roleFitOrder ranks triage's role_fit labels (jdtriage's fixture) for
// display: unscored postings sort by how well triage said they fit. Display
// order only; no engine rule reads it.
var roleFitOrder = []string{"strong_fit", "partial_fit", "adjacent", "mismatch_seniority", "mismatch_domain"}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
