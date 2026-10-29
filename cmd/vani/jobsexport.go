package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/exportlog"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/xlsx"
)

// `vani jobs export` writes jobs to an Excel workbook (internal/xlsx): the
// first --top of the list `jobs list` would show with the same flags, in the
// same order, or, with --keys-stdin, the roles the app has selected. Like
// list it reads the ledger without its lock; it writes the workbook and the
// record of what was exported (internal/exportlog). Description text is left
// out, as the list leaves it out.
//
// A job an earlier export wrote is not written again unasked: by default the
// export asks in a terminal, and elsewhere (--json, --keys-stdin, a pipe)
// refuses with code already_exported and writes nothing. --already-exported
// skip leaves those jobs out (--top then fills from the rest of the list, so
// "the next 100" is the next 100 not yet exported); include writes them again.
// --dry-run writes nothing and reports what the export would hold.

const (
	defaultExportTop = 100
	maxOutPathBytes  = 4096

	exportedAsk     = "ask"
	exportedSkip    = "skip"
	exportedInclude = "include"
	// codeAlreadyExported is the refusal of an export holding jobs exported
	// before, when it cannot ask.
	codeAlreadyExported = "already_exported"
	// maxPromptAnswerBytes bounds the answer read at the terminal prompt.
	maxPromptAnswerBytes = 256
)

// exportKeysInput is where --keys-stdin reads its keys. Tests replace it.
var exportKeysInput io.Reader = os.Stdin

// exportPromptInput and exportPromptTTY are where the already-exported prompt
// reads its answer and whether it may ask at all. Tests replace them.
var (
	exportPromptInput io.Reader = os.Stdin
	exportPromptTTY             = stdinIsTerminal
)

// stdinIsTerminal reports whether stdin is a character device other than
// /dev/null (which is one too, and is what a launchd job or a test is given):
// a terminal, in practice. Asking anything else would read no answer and cancel.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err == nil && !os.SameFile(fi, null)
}

type jobsExportJSON struct {
	Path string `json:"path"`
	Rows int    `json:"rows"`
	// Total is how many jobs matched before --top, or how many keys were asked
	// for; Rows < Total says the workbook holds fewer.
	Total int    `json:"total"`
	View  string `json:"view"`
	Sort  string `json:"sort"`
	// Missing are asked-for keys the selection does not hold (not in this
	// view, filtered out, or gone from the ledger); they are not in the file.
	Missing []posting.Key `json:"missing"`
	Ledger  string        `json:"ledger"`
	Records int           `json:"records"`
	// AlreadyExported is how many of the jobs the export holds, or would hold
	// with --already-exported include, an earlier export wrote. LastExportedAt
	// and LastFile name the latest of those exports; LastFile is "" when that
	// export is too old for the record to name its file.
	AlreadyExported int        `json:"already_exported"`
	LastExportedAt  *time.Time `json:"last_exported_at,omitempty"`
	LastFile        string     `json:"last_file,omitempty"`
	// Skipped is how many jobs exported before --already-exported skip left out.
	Skipped int `json:"skipped"`
	// Forgotten is how many of the oldest exported keys the record dropped to
	// stay bounded (exportlog.MaxJobs); they read as never exported from now on.
	Forgotten int  `json:"forgotten,omitempty"`
	DryRun    bool `json:"dry_run"`
	failure
}

func (r *jobsExportJSON) fail(err error) {
	r.failure.fail(err)
	if errors.As(err, new(alreadyExportedError)) {
		r.Code = codeAlreadyExported
	}
}

// alreadyExportedError is the refusal of an export that would write jobs an
// earlier export wrote, made when the export cannot ask.
type alreadyExportedError struct {
	prior exportlog.Prior
	rows  int
}

func (a alreadyExportedError) Error() string {
	return fmt.Sprintf("jobs export: %s; nothing written. Pass --already-exported skip to leave them out, or --already-exported include to export them again",
		priorLine(a.prior, a.rows))
}

// priorLine says how many of rows jobs were exported before, and when and where last.
func priorLine(p exportlog.Prior, rows int) string {
	line := fmt.Sprintf("%d of these %d jobs were exported before (last on %s", p.N, rows, p.Last.At.Local().Format("2006-01-02 15:04"))
	if p.Last.File != "" {
		line += " to " + p.Last.File
	}
	return line + ")"
}

func (e *env) jobsExport(args []string) error {
	fs := e.newFlags("jobs export")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	var o jobsOpts
	addSelectFlags(fs, &o)
	fs.StringVar(&o.Sort, "sort", "default", "the list order the rows keep: "+strings.Join(jobview.Sorts, ", "))
	top := fs.Int("top", defaultExportTop, fmt.Sprintf("export the first this many jobs of the list (1-%d)", maxJobsLimit))
	out := fs.String("out", "", "the .xlsx file to write (default: <state dir>/export/jobs/jobs-<view>-<time>.xlsx)")
	keysStdin := fs.Bool("keys-stdin", false, fmt.Sprintf("export the jobs whose keys stdin lists, one per line, in that order (at most %d); the selection flags still apply", maxJobsLimit))
	already := fs.String("already-exported", exportedAsk, "jobs an earlier export wrote: ask (in a terminal; elsewhere refuse and write nothing), "+
		"skip (leave them out; --top fills from the rest of the list) or include (write them again)")
	dryRun := fs.Bool("dry-run", false, "write nothing: report what the export would hold and how many of its jobs were exported before")
	if err := parse(fs, args); err != nil {
		return err
	}
	reply := jobsExportJSON{Missing: []posting.Key{}}
	err := func() error {
		topSet := false
		fs.Visit(func(f *flag.Flag) { topSet = topSet || f.Name == "top" })
		switch {
		case *top < 1 || *top > maxJobsLimit:
			return usagef("jobs export: --top must be 1-%d", maxJobsLimit)
		case *keysStdin && topSet:
			return usagef("jobs export: --top and --keys-stdin both say which jobs to export; give one")
		case *already != exportedAsk && *already != exportedSkip && *already != exportedInclude:
			return usagef("jobs export: --already-exported must be ask, skip or include")
		}
		o.pendingOK = true
		if err := o.check("jobs export", true); err != nil {
			return err
		}
		path, err := exportOutPath(*out, e.stateDir, o.View, now())
		if err != nil {
			return err
		}
		var keys []posting.Key
		if *keysStdin {
			if keys, err = readExportKeys(exportKeysInput); err != nil {
				return err
			}
		}
		snap, m, err := e.jobsState()
		if err != nil {
			return err
		}
		in, err := e.newInsight(o.HalfLife, snap, m)
		if err != nil {
			return err
		}
		logPath := exportlog.Path(e.stateDir)
		rec, err := exportlog.Read(logPath)
		if err != nil {
			return fmt.Errorf("jobs export: %w", err)
		}
		// Skipping fills --top from past the cut, so it needs the whole list.
		limit := *top
		if keys != nil || (*already != exportedInclude && len(rec.Jobs) > 0) {
			limit = math.MaxInt
		}
		l := jobview.BuildList(snap, m, &o.Opts, in, in.info, listKeywords, limit)
		jobs, total := l.Jobs[:min(*top, len(l.Jobs))], l.Total
		if keys != nil {
			jobs, reply.Missing = pickJobs(l.Jobs, keys)
			total = len(keys)
			if reply.Missing == nil {
				reply.Missing = []posting.Key{}
			}
		}
		prior := rec.Prior(jobKeys(jobs))
		reply.Path, reply.Total, reply.View, reply.Sort = path, total, l.View, l.Sort
		reply.Ledger, reply.Records, reply.DryRun = filepath.Join(e.stateDir, "ledger"), len(snap.Records), *dryRun
		reply.Rows, reply.AlreadyExported = len(jobs), prior.N
		if prior.N > 0 {
			at := prior.Last.At
			reply.LastExportedAt, reply.LastFile = &at, prior.Last.File
		}
		choice := *already
		if prior.N > 0 && choice == exportedAsk && !*dryRun {
			if *asJSON || *keysStdin || !exportPromptTTY() {
				return alreadyExportedError{prior, len(jobs)}
			}
			if choice, err = e.askAlreadyExported(prior, len(jobs), keys == nil); err != nil {
				return err
			}
		}
		if choice == exportedSkip && prior.N > 0 {
			if keys != nil {
				jobs, reply.Skipped = unexported(jobs, rec, len(jobs))
			} else {
				jobs, reply.Skipped = unexported(l.Jobs, rec, *top)
			}
			reply.Rows = len(jobs)
		}
		if *dryRun {
			return nil
		}
		var buf bytes.Buffer
		if err := xlsx.Write(&buf, exportSheet(jobs, o.more.Currency)); err != nil {
			return fmt.Errorf("jobs export: %w", err)
		}
		if err := atomicfile.Write(path, buf.Bytes()); err != nil {
			return fmt.Errorf("jobs export: %w", err)
		}
		if len(jobs) == 0 {
			return nil
		}
		// The workbook is written: a record that cannot be kept is a failure that
		// says so, never a quiet success that lets these jobs read as new.
		if reply.Forgotten, err = exportlog.Record(context.Background(), logPath, jobKeys(jobs), path, now()); err != nil {
			return fmt.Errorf("jobs export: wrote %s, but could not record its jobs as exported: %w", path, err)
		}
		return nil
	}()
	err = e.reply(*asJSON, &reply, err, func() {
		verb := "exported"
		if reply.DryRun {
			verb = "would export"
		}
		fmt.Fprintf(e.stdout, "%s %d of %d jobs (%s, %s order) to %s\n", verb, reply.Rows, reply.Total, reply.View, reply.Sort, reply.Path)
		switch {
		case reply.Skipped > 0:
			fmt.Fprintf(e.stdout, "left out %d jobs exported before\n", reply.Skipped)
		case reply.AlreadyExported > 0:
			last := exportlog.Prior{N: reply.AlreadyExported, Last: exportlog.Export{At: *reply.LastExportedAt, File: reply.LastFile}}
			fmt.Fprintf(e.stderr, "vani: %s\n", priorLine(last, reply.Rows))
		}
		for _, k := range reply.Missing {
			fmt.Fprintf(e.stderr, "vani: %s is not in this selection; left out of the export\n", k)
		}
		if reply.Forgotten > 0 {
			fmt.Fprintf(e.stderr, "vani: the export record keeps at most %d jobs; the %d exported longest ago now read as never exported\n", exportlog.MaxJobs, reply.Forgotten)
		}
	})
	// Text mode fails on a short export so a script sees it; a --json client
	// reads "missing" (the file is written either way).
	if err == nil && !*asJSON && len(reply.Missing) > 0 {
		return fmt.Errorf("jobs export: %d of %d asked-for jobs are not in this selection and are not in the file", len(reply.Missing), reply.Total)
	}
	return err
}

// exportOutPath checks --out, or names the default file under the state dir.
// An explicit path's folder must exist: a mistyped folder is an error, never
// a new directory tree.
func exportOutPath(out, stateDir, view string, at time.Time) (string, error) {
	if out == "" {
		name := fmt.Sprintf("jobs-%s-%s.xlsx", view, at.Format("20060102-150405"))
		return filepath.Join(stateDir, "export", "jobs", name), nil
	}
	switch {
	case len(out) > maxOutPathBytes || !utf8.ValidString(out) || strings.ContainsRune(out, 0):
		return "", usagef("jobs export: --out must be UTF-8 and at most %d bytes", maxOutPathBytes)
	case !strings.EqualFold(filepath.Ext(out), ".xlsx"):
		return "", usagef("jobs export: --out must name a .xlsx file")
	}
	path, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("jobs export: --out: %w", err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("jobs export: --out: folder %s does not exist", filepath.Dir(path))
	}
	return path, nil
}

// readExportKeys reads one key per line; blank lines are skipped and a repeat
// keeps its first place. It refuses an invalid key, more than maxJobsLimit
// keys, or none.
func readExportKeys(r io.Reader) ([]posting.Key, error) {
	sc := bufio.NewScanner(io.LimitReader(r, int64(maxJobsLimit)*(marks.MaxKeyBytes+2)+1))
	sc.Buffer(make([]byte, 0, 4096), marks.MaxKeyBytes+2)
	seen := map[posting.Key]bool{}
	var keys []posting.Key
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		k, err := checkJobKey(line)
		if err != nil {
			return nil, err
		}
		if seen[k] {
			continue
		}
		if len(keys) == maxJobsLimit {
			return nil, usagef("jobs export: --keys-stdin takes at most %d keys", maxJobsLimit)
		}
		seen[k] = true
		keys = append(keys, k)
	}
	if err := sc.Err(); err != nil {
		return nil, usagef("jobs export: --keys-stdin: %v", err)
	}
	if len(keys) == 0 {
		return nil, usagef("jobs export: --keys-stdin read no keys")
	}
	return keys, nil
}

// askAlreadyExported asks at the terminal what to do with jobs exported
// before: skip or include; anything else, or no answer, cancels.
func (e *env) askAlreadyExported(p exportlog.Prior, rows int, fills bool) (string, error) {
	skip := "skip them"
	if fills {
		skip = "skip them (the next jobs not yet exported take their place)"
	}
	fmt.Fprintf(e.stderr, "vani: %s.\n%s, include them again, or cancel? [s/i/C] ", priorLine(p, rows), strings.ToUpper(skip[:1])+skip[1:])
	line, err := bufio.NewReader(io.LimitReader(exportPromptInput, maxPromptAnswerBytes)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("jobs export: read the answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "s", "skip":
		return exportedSkip, nil
	case "i", "include":
		return exportedInclude, nil
	}
	return "", errors.New("jobs export: cancelled; nothing written")
}

func jobKeys(jobs []jobview.Job) []posting.Key {
	out := make([]posting.Key, len(jobs))
	for i, j := range jobs {
		out[i] = j.Key
	}
	return out
}

// unexported returns the first n of jobs the record does not hold, in order,
// and how many it passed over to find them.
func unexported(jobs []jobview.Job, rec exportlog.Log, n int) ([]jobview.Job, int) {
	out := make([]jobview.Job, 0, min(n, len(jobs)))
	passed := 0
	for _, j := range jobs {
		if len(out) == n {
			break
		}
		if rec.Exported(j.Key) {
			passed++
			continue
		}
		out = append(out, j)
	}
	return out, passed
}

// pickJobs returns the jobs keys name, in keys' order, and the keys jobs lacks.
func pickJobs(jobs []jobview.Job, keys []posting.Key) ([]jobview.Job, []posting.Key) {
	byKey := make(map[posting.Key]int, len(jobs))
	for i, j := range jobs {
		byKey[j.Key] = i
	}
	out := make([]jobview.Job, 0, len(keys))
	var missing []posting.Key
	for _, k := range keys {
		if i, ok := byKey[k]; ok {
			out = append(out, jobs[i])
		} else {
			missing = append(missing, k)
		}
	}
	return out, missing
}

// exportColumn is one column of the workbook. perCurrency columns name the
// --currency in their header.
type exportColumn struct {
	name        string
	width       float64
	perCurrency bool
	cell        func(n int, j jobview.Job, cur string) xlsx.Cell
}

var exportColumns = []exportColumn{
	{"#", 5, false, func(n int, _ jobview.Job, _ string) xlsx.Cell { return xlsx.Number(float64(n)) }},
	{"Company", 24, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(j.Company) }},
	{"Title", 44, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(j.Title) }},
	{"Location", 28, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(j.Location) }},
	{"Remote", 9, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return yes(j.RemoteListed || j.Remote) }},
	// An untriaged posting has no priority or relevance: empty, never 0.
	{"Priority", 9, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return triagedScore(j, j.Priority) }},
	{"Relevance", 10, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return triagedScore(j, j.Relevance) }},
	{"Rank", 7, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return optNumber(j.Rank) }},
	{"Role fit", 18, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		return xlsx.Text(labelOr(jobview.FitLabel(j.Triage["role_fit"]), j.Triage["role_fit"]))
	}},
	{"Seniority", 15, false, triageCell("seniority")},
	{"Work mode", 12, false, triageCell("work_mode")},
	{"Sponsorship", 22, false, triageCell("sponsorship")},
	{"Category", 24, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		if j.Category == "" {
			return xlsx.Cell{}
		}
		return xlsx.Text(jobcategory.Label(j.Category))
	}},
	{"Pay", 22, false, func(_ int, j jobview.Job, cur string) xlsx.Cell { return xlsx.Text(payNote(j, cur)) }},
	{"Pay min/yr", 14, true, func(_ int, j jobview.Job, cur string) xlsx.Cell {
		if p, ok := jobinsight.PayIn(j.Pay, cur); ok {
			return xlsx.Number(math.Round(p.AnnualMin))
		}
		return xlsx.Cell{}
	}},
	{"Pay max/yr", 14, true, func(_ int, j jobview.Job, cur string) xlsx.Cell {
		if p, ok := jobinsight.PayIn(j.Pay, cur); ok {
			return xlsx.Number(math.Round(p.AnnualMax))
		}
		return xlsx.Cell{}
	}},
	{"Age (days)", 10, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		if j.AgeDays == nil {
			return xlsx.Cell{}
		}
		return xlsx.Number(float64(*j.AgeDays))
	}},
	{"Posted", 12, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return exportDay(j.PostedAt) }},
	{"First seen", 12, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return exportDay(j.FirstSeen) }},
	{"Status", 11, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		return xlsx.Text(jobview.StatusLabel(string(j.Status)))
	}},
	{"Mark", 9, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(string(j.Mark)) }},
	// What became of an application (marks stages): empty for an old mark with
	// none, and for a role never applied to. Furthest outlives an unstage.
	{"Stage", 12, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(string(j.Stage)) }},
	{"Furthest stage", 14, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(string(j.FurthestStage)) }},
	// MarkedAt is when a role was saved or hidden too; only an applied mark's is the date applied.
	{"Applied", 12, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		if j.Mark != marks.Applied {
			return xlsx.Cell{}
		}
		return exportDay(j.MarkedAt)
	}},
	{"Note", 40, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(j.Note) }},
	// The company-history notes the list shows (internal/samecompany), as vani words them.
	{"Repeat apply", 50, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		if j.RepeatApply == nil {
			return xlsx.Cell{}
		}
		return xlsx.Text(j.RepeatApply.Text)
	}},
	{"Repost", 40, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		if j.Repost == nil {
			return xlsx.Cell{}
		}
		return xlsx.Text(j.Repost.Text)
	}},
	{"Keywords", 36, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		terms := make([]string, 0, len(j.Keywords))
		for _, k := range j.Keywords {
			terms = append(terms, k.Term)
		}
		return xlsx.Text(strings.Join(terms, ", "))
	}},
	{"Similar listings", 9, false, func(_ int, j jobview.Job, _ string) xlsx.Cell {
		if j.Similar == 0 {
			return xlsx.Cell{}
		}
		return xlsx.Number(float64(j.Similar))
	}},
	{"URL", 50, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(j.URL) }},
	{"Apply URL", 50, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(j.ApplyURL) }},
	{"Key", 28, false, func(_ int, j jobview.Job, _ string) xlsx.Cell { return xlsx.Text(string(j.Key)) }},
}

// exportSheet lays jobs out one per row, numbered from 1 in their order.
func exportSheet(jobs []jobview.Job, cur string) xlsx.Sheet {
	s := xlsx.Sheet{Name: "Jobs", Rows: make([][]xlsx.Cell, 0, len(jobs))}
	for _, c := range exportColumns {
		name := c.name
		if c.perCurrency {
			name += " (" + cur + ")"
		}
		s.Header = append(s.Header, name)
		s.Widths = append(s.Widths, c.width)
	}
	for i, j := range jobs {
		row := make([]xlsx.Cell, len(exportColumns))
		for c, col := range exportColumns {
			row[c] = col.cell(i+1, j, cur)
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}

func triageCell(slot string) func(int, jobview.Job, string) xlsx.Cell {
	return func(_ int, j jobview.Job, _ string) xlsx.Cell {
		return xlsx.Text(labelOr(jobview.TriageLabel(slot, j.Triage[slot]), j.Triage[slot]))
	}
}

// labelOr is the display label, or the raw reading when it has none.
func labelOr(label, raw string) string {
	if label != "" {
		return label
	}
	return raw
}

func triagedScore(j jobview.Job, v float64) xlsx.Cell {
	if j.Untriaged {
		return xlsx.Cell{}
	}
	return xlsx.Number(math.Round(v*10) / 10)
}

func optNumber(v *float64) xlsx.Cell {
	if v == nil {
		return xlsx.Cell{}
	}
	return xlsx.Number(*v)
}

func yes(b bool) xlsx.Cell {
	if b {
		return xlsx.Text("Yes")
	}
	return xlsx.Cell{}
}

// exportDay is t's UTC date as 2006-01-02 text, which sorts as a date does.
func exportDay(t time.Time) xlsx.Cell {
	if t.IsZero() {
		return xlsx.Cell{}
	}
	return xlsx.Text(t.UTC().Format("2006-01-02"))
}
