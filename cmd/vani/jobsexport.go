package main

import (
	"bufio"
	"bytes"
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
// list it reads the ledger without its lock; it writes only the workbook.
// Description text is left out, as the list leaves it out.

const (
	defaultExportTop = 100
	maxOutPathBytes  = 4096
)

// exportKeysInput is where --keys-stdin reads its keys. Tests replace it.
var exportKeysInput io.Reader = os.Stdin

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
	if err := parse(fs, args); err != nil {
		return err
	}
	var reply jobsExportJSON
	err := func() error {
		topSet := false
		fs.Visit(func(f *flag.Flag) { topSet = topSet || f.Name == "top" })
		switch {
		case *top < 1 || *top > maxJobsLimit:
			return usagef("jobs export: --top must be 1-%d", maxJobsLimit)
		case *keysStdin && topSet:
			return usagef("jobs export: --top and --keys-stdin both say which jobs to export; give one")
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
		limit := *top
		if keys != nil {
			limit = math.MaxInt
		}
		l := jobview.BuildList(snap, m, &o.Opts, in, in.info, listKeywords, limit)
		jobs, total := l.Jobs, l.Total
		var missing []posting.Key
		if keys != nil {
			jobs, missing = pickJobs(l.Jobs, keys)
			total = len(keys)
		}
		var buf bytes.Buffer
		if err := xlsx.Write(&buf, exportSheet(jobs, o.more.Currency)); err != nil {
			return fmt.Errorf("jobs export: %w", err)
		}
		if err := atomicfile.Write(path, buf.Bytes()); err != nil {
			return fmt.Errorf("jobs export: %w", err)
		}
		if missing == nil {
			missing = []posting.Key{}
		}
		reply = jobsExportJSON{Path: path, Rows: len(jobs), Total: total, View: l.View, Sort: l.Sort, Missing: missing,
			Ledger: filepath.Join(e.stateDir, "ledger"), Records: len(snap.Records)}
		return nil
	}()
	err = e.jobsReply(*asJSON, reply, err, func() {
		fmt.Fprintf(e.stdout, "exported %d of %d jobs (%s, %s order) to %s\n", reply.Rows, reply.Total, reply.View, reply.Sort, reply.Path)
		for _, k := range reply.Missing {
			fmt.Fprintf(e.stderr, "vani: %s is not in this selection; left out of the export\n", k)
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
