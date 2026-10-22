package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// `vani runs` reads the run history (runhistory.go): list the finished runs,
// show one, and print one's failures as the plain text the Mac app copies.
// The failure report has one owner, runReportOf below: the app calls
// `vani runs failures --json` and copies its text, so the CLI and the app never
// word a failure two ways. A run is named by its id (runID); no id, or
// "latest", is last.json, the same record `vani status --json` reports.

// runsVersion is the contract version of `runs ... --json`. Fields are only
// added; a reader ignores the ones it does not know.
const runsVersion = 1

// The failure report is bounded: one line is cut at maxReportLine runes and
// the report at maxReportBytes, and it says how many lines each cut touched.
// The header and the files list are kept whole (they are a handful of lines),
// so the cut only ever takes failed boards and backend causes from the end.
const (
	maxReportLine  = 600
	maxReportBytes = 24 << 10
	// reportFooterReserve is room left for the line that says what was cut.
	reportFooterReserve = 400
)

func (e *env) cmdRuns(args []string) error {
	if len(args) == 0 {
		return usagef("runs: expected subcommand list, show or failures")
	}
	switch args[0] {
	case "list":
		return e.runsList(args[1:])
	case "show":
		return e.runsShow(args[1:])
	case "failures":
		return e.runsFailures(args[1:])
	}
	return usagef("runs: unknown subcommand %q (want list, show or failures)", args[0])
}

// runsArgs parses `[--json] [ID]`: the flag may come before or after the id.
func (e *env) runsArgs(name string, args []string, wantID bool) (asJSON bool, id string, err error) {
	flags := e.newFlags("runs " + name)
	j := flags.Bool("json", false, "one JSON object on stdout")
	var rest []string
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return false, "", err
			}
			return false, "", usageError{err.Error()}
		}
		args = flags.Args()
		if len(args) > 0 {
			rest = append(rest, args[0])
			args = args[1:]
		}
	}
	if !wantID && len(rest) > 0 {
		return false, "", usagef("runs %s: unexpected arguments: %v", name, rest)
	}
	if len(rest) > 1 {
		return false, "", usagef("runs %s: expected at most one run id, got %v", name, rest)
	}
	if len(rest) == 1 && rest[0] != "latest" {
		if !validRunID(rest[0]) {
			return false, "", usagef("runs %s: %q is not a run id (vani runs list shows them)", name, truncateRunes(rest[0], 60))
		}
		id = rest[0]
	}
	return *j, id, nil
}

// runRef is one run found for the CLI: its record and where it came from.
type runRef struct {
	ID string
	// Source is "last" (runs/last.json) or "history" (runs/history/<id>.json).
	Source string
	Path   string
	Rec    runRecord
}

// findRun resolves id to a record. "" is the latest run, last.json. A named
// run is the history's file for it, or last.json when that is the run (a run
// whose history write failed, or one still going). A file that exists but
// cannot be read is an error naming why, never "no such run".
func (e *env) findRun(id string) (runRef, error) {
	if id == "" {
		r, err := readRunRecord(e.stateDir)
		switch {
		case err != nil:
			return runRef{}, fmt.Errorf("the latest run's record: %w", err)
		case r == nil:
			return runRef{}, errors.New("no run has been recorded yet")
		}
		return runRef{ID: runID(*r), Source: "last", Path: runRecordPath(e.stateDir), Rec: *r}, nil
	}
	path := runHistoryPath(e.stateDir, id)
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return runRef{}, fmt.Errorf("run %s: its history entry is not a regular file", id)
		}
		r, err := readRunRecordFile(path)
		if err != nil {
			return runRef{}, fmt.Errorf("run %s: %w", id, err)
		}
		return runRef{ID: id, Source: "history", Path: path, Rec: *r}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return runRef{}, fmt.Errorf("run %s: %w", id, err)
	}
	if r, err := readRunRecord(e.stateDir); err == nil && r != nil && runID(*r) == id {
		return runRef{ID: id, Source: "last", Path: runRecordPath(e.stateDir), Rec: *r}, nil
	}
	return runRef{}, fmt.Errorf("no run %s (vani runs list shows the runs it has)", id)
}

// runRow is one run in `runs list`.
type runRow struct {
	ID string `json:"id"`
	// Source is where the row was read: "history", or "last" for a run not in
	// the history (still going, never finished, or its history write failed).
	Source string `json:"source"`
	// Latest marks the run last.json holds, the one `status` reports.
	Latest       bool      `json:"latest,omitempty"`
	State        string    `json:"state"`
	Trigger      string    `json:"trigger,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at,omitzero"`
	DurationSec  *float64  `json:"duration_sec,omitempty"`
	FailedBoards int       `json:"failed_boards"`
	HasFailures  bool      `json:"has_failures"`
	// Error is the run's own error line (not a board's), bounded.
	Error string `json:"error,omitempty"`
}

// runProblem is a history file that could not be listed as a run. It is
// reported, never dropped: a missing run and an unreadable one differ.
type runProblem struct {
	// ID is the file's run id, or "latest" for last.json.
	ID    string `json:"id"`
	Error string `json:"error"`
}

type runsListJSON struct {
	Version    int          `json:"version"`
	StateDir   string       `json:"state_dir"`
	HistoryDir string       `json:"history_dir"`
	Runs       []runRow     `json:"runs"`
	Problems   []runProblem `json:"problems"`
	// Unlisted counts history files beyond maxRunHistoryScan, which the
	// listing did not read.
	Unlisted int `json:"unlisted,omitempty"`
}

func rowOf(id, source string, r runRecord, at time.Time) runRow {
	row := runRow{ID: id, Source: source, State: runState(r, at), Trigger: r.Trigger, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		HasFailures: runHasFailures(r, at), Error: oneLine(r.Error, maxReportLine)}
	if r.Boards != nil {
		row.FailedBoards = r.Boards.Failed
	}
	if !r.FinishedAt.IsZero() {
		d := max(r.FinishedAt.Sub(r.StartedAt).Seconds(), 0)
		row.DurationSec = &d
	}
	return row
}

// runHasFailures says whether a run has anything to report: a failed or
// interrupted run, a run error, a board that failed to list, or a backend
// error. The Mac app's button follows the same rule (LastRun.hasFailures); a
// contract test holds the two together.
func runHasFailures(r runRecord, at time.Time) bool {
	st := runState(r, at)
	if st == outcomeFailed || st == "interrupted" || r.Error != "" {
		return true
	}
	if r.Boards != nil && (r.Boards.Failed > 0 || failedBoardItems(r.Boards) > 0) {
		return true
	}
	b := r.BackendErrors
	return b != nil && (len(b.Causes) > 0 || b.Unrecorded > 0)
}

func failedBoardItems(b *boardsReport) int {
	n := 0
	for _, it := range b.Items {
		if boardDidNotList(it.Outcome) {
			n++
		}
	}
	return n
}

// boardDidNotList is whether a board item is a failure the report names: a
// board that failed, or one a host's rate limit stopped (it counts in Failed
// too, see cmd/vani/boards.go).
func boardDidNotList(outcome string) bool {
	return outcome == boardFailed || outcome == boardRateLimited
}

func (e *env) runsList(args []string) error {
	asJSON, _, err := e.runsArgs("list", args, false)
	if err != nil {
		return err
	}
	at := now()
	out := runsListJSON{Version: runsVersion, StateDir: e.stateDir, HistoryDir: filepath.Join(e.stateDir, runRecordDir, runHistoryDir),
		Runs: []runRow{}, Problems: []runProblem{}}
	files, more, lerr := listRunHistory(e.stateDir, maxRunHistoryScan)
	if lerr != nil {
		return fmt.Errorf("read the run history: %w", lerr)
	}
	out.Unlisted = more
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.ID] = true
		if !f.Regular {
			out.Problems = append(out.Problems, runProblem{ID: f.ID, Error: "not a regular file"})
			continue
		}
		r, rerr := readRunRecordFile(runHistoryPath(e.stateDir, f.ID))
		if rerr != nil {
			out.Problems = append(out.Problems, runProblem{ID: f.ID, Error: oneLine(rerr.Error(), 300)})
			continue
		}
		out.Runs = append(out.Runs, rowOf(f.ID, "history", *r, at))
	}
	last, lastErr := readRunRecord(e.stateDir)
	switch {
	case lastErr != nil:
		out.Problems = append(out.Problems, runProblem{ID: "latest", Error: oneLine(lastErr.Error(), 300)})
	case last != nil:
		id := runID(*last)
		if seen[id] {
			for i := range out.Runs {
				if out.Runs[i].ID == id {
					out.Runs[i].Latest = true
				}
			}
		} else {
			row := rowOf(id, "last", *last, at)
			row.Latest = true
			out.Runs = append(out.Runs, row)
		}
	}
	// ids sort in time order; the newest first, as the history lists them.
	sortRows(out.Runs)
	return e.jobsReply(asJSON, out, nil, func() {
		if len(out.Runs) == 0 && len(out.Problems) == 0 {
			fmt.Fprintln(e.stdout, "no run has been recorded yet")
			return
		}
		for _, r := range out.Runs {
			mark := " "
			if r.Latest {
				mark = "*"
			}
			fails := ""
			if r.FailedBoards > 0 {
				fails = fmt.Sprintf("  %d board(s) failed", r.FailedBoards)
			}
			fmt.Fprintf(e.stdout, "%s %s  %-11s %-8s%s\n", mark, r.ID, r.State, r.Trigger, fails)
		}
		for _, p := range out.Problems {
			fmt.Fprintf(e.stdout, "! %s  unreadable: %s\n", p.ID, p.Error)
		}
		if out.Unlisted > 0 {
			fmt.Fprintf(e.stdout, "(%d older history file(s) not listed)\n", out.Unlisted)
		}
	})
}

func sortRows(rows []runRow) {
	sort.SliceStable(rows, func(i, j int) bool { return idNewer(rows[i].ID, rows[j].ID) })
}

// runShowJSON is `runs show --json`: the whole record, as written.
type runShowJSON struct {
	Version int       `json:"version"`
	ID      string    `json:"id"`
	Source  string    `json:"source"`
	Path    string    `json:"path"`
	State   string    `json:"state"`
	Record  runRecord `json:"record"`
}

func (e *env) runsShow(args []string) error {
	asJSON, id, err := e.runsArgs("show", args, true)
	if err != nil {
		return err
	}
	ref, err := e.findRun(id)
	if err != nil {
		return e.jobsReply(asJSON, nil, err, nil)
	}
	at := now()
	return e.jobsReply(asJSON, runShowJSON{Version: runsVersion, ID: ref.ID, Source: ref.Source, Path: ref.Path, State: runState(ref.Rec, at), Record: ref.Rec}, nil, func() {
		fmt.Fprint(e.stdout, e.runReportOf(ref, at, true).Text)
	})
}

// runFailuresJSON is `runs failures --json`. Text is always the report, so a
// client that copies it never copies nothing: a run with no failures says so.
type runFailuresJSON struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	HasFailures bool   `json:"has_failures"`
	Text        string `json:"text"`
	// CutLines and ShortenedLines are what the bounds took out of Text; both
	// are also said in its last line.
	CutLines       int `json:"cut_lines,omitempty"`
	ShortenedLines int `json:"shortened_lines,omitempty"`
}

func (e *env) runsFailures(args []string) error {
	asJSON, id, err := e.runsArgs("failures", args, true)
	if err != nil {
		return err
	}
	ref, err := e.findRun(id)
	if err != nil {
		return e.jobsReply(asJSON, nil, err, nil)
	}
	rep := e.runReportOf(ref, now(), false)
	return e.jobsReply(asJSON, runFailuresJSON{Version: runsVersion, ID: ref.ID, Source: ref.Source, HasFailures: rep.HasFailures,
		Text: rep.Text, CutLines: rep.CutLines, ShortenedLines: rep.ShortenedLines}, nil, func() {
		fmt.Fprint(e.stdout, rep.Text)
	})
}

// runReport is the report text and what bounding it took out.
type runReport struct {
	Text           string
	HasFailures    bool
	CutLines       int
	ShortenedLines int
}

// reportBuilder collects the report's lines under the bounds.
type reportBuilder struct {
	lines     []string
	bytes     int
	shortened int
}

// line cleans s to one line, hides secrets, and cuts it at maxReportLine.
func (b *reportBuilder) clean(s string) string {
	full := oneLine(redactSecrets(s), 1<<20)
	if utf8.RuneCountInString(full) > maxReportLine {
		b.shortened++
		return oneLine(full, maxReportLine)
	}
	return full
}

func (b *reportBuilder) add(s string) {
	b.lines = append(b.lines, s)
	b.bytes += len(s) + 1
}

// Secrets are not expected in a run record (errors name boards, status codes
// and request ids; Manvi redacts provider keys), so this is the second line of
// defence for text that is about to be pasted somewhere else: a header or
// token-looking value, and a key-looking string, are replaced.
var (
	secretKV     = regexp.MustCompile(`(?i)\b(authorization|x-api-key|api[-_ ]?key|apikey|access[-_ ]?token|secret|password|passwd)(["']?\s*[:=]\s*["']?)(?:bearer\s+|basic\s+)?[^\s"',;&]+`)
	secretBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	secretKey    = regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`)
)

func redactSecrets(s string) string {
	s = secretKV.ReplaceAllString(s, "${1}${2}[redacted]")
	s = secretBearer.ReplaceAllString(s, "${1} [redacted]")
	return secretKey.ReplaceAllString(s, "[redacted]")
}

// reportLogs are the logs a run may have written, as they exist now. runs/
// manual.log is the app's last manual run (each app-started run replaces it,
// and the app keeps the replaced ones in runs/history/); logs/vani.*.log are
// the daemon's under launchd, which holds every run it made. Nothing here says
// which log is this run's: it names what exists and when it was last written.
func (e *env) reportLogs() []string {
	var out []string
	for _, p := range []string{
		filepath.Join(e.stateDir, runRecordDir, "manual.log"),
		filepath.Join(e.stateDir, "logs", "vani.err.log"),
		filepath.Join(e.stateDir, "logs", "vani.out.log"),
	} {
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		out = append(out, fmt.Sprintf("Log: %s (%d bytes, last written %s)", p, st.Size(), st.ModTime().UTC().Format(time.RFC3339)))
	}
	return out
}

// runReportOf is the one formatter of a run's failures. detail adds the run's
// ledger and advance lines (`runs show`); the copy text (`runs failures`)
// leaves them out.
func (e *env) runReportOf(ref runRef, at time.Time, detail bool) runReport {
	r := ref.Rec
	state := runState(r, at)
	has := runHasFailures(r, at)
	b := &reportBuilder{}

	// Header: kept whole.
	b.add("Vani run report")
	b.add(b.clean("Run: " + ref.ID))
	trigger := r.Trigger
	if trigger == "" {
		trigger = "unknown"
	}
	if r.Options != nil {
		if s := optionsSummary(*r.Options); s != "" {
			trigger += " (" + s + ")"
		}
	}
	b.add(b.clean("Trigger: " + trigger))
	b.add("Started: " + r.StartedAt.UTC().Format(time.RFC3339))
	if r.FinishedAt.IsZero() {
		b.add("Finished: not recorded")
	} else {
		b.add(fmt.Sprintf("Finished: %s (took %s)", r.FinishedAt.UTC().Format(time.RFC3339), max(r.FinishedAt.Sub(r.StartedAt), 0).Round(time.Second)))
	}
	b.add(b.clean("Outcome: " + state))
	if r.Stage != "" {
		b.add(b.clean("Failed at: " + r.Stage))
	}
	if r.Error != "" {
		b.add(b.clean("Error: " + r.Error))
	}
	if r.Cause != "" {
		b.add(b.clean("Cause: " + r.Cause))
	}
	if detail {
		if r.Boards != nil {
			b.add(b.clean(fmt.Sprintf("Boards: %d of %d read, %d failed, %d skipped, %d not reached; %d postings listed",
				r.Boards.Read, r.Boards.Configured, r.Boards.Failed, r.Boards.Skipped, r.Boards.NotReached, r.Boards.Listed)))
		}
		if a := r.Advance; a != nil {
			b.add(b.clean(fmt.Sprintf("Advance: %d of %d eligible attempted (cap %d), %d unread, %d failed, %d deferred; stopped: %s",
				a.Attempted, a.Eligible, a.Limit, a.Unread, a.Failed, a.Deferred, orNone(a.Stopped))))
		}
		if r.CountsBefore != nil && r.CountsAfter != nil {
			b.add(fmt.Sprintf("Ledger: %d -> %d postings", sumCounts(r.CountsBefore), sumCounts(r.CountsAfter)))
		}
		if r.DigestWarning != "" {
			b.add(b.clean("Digest warning: " + r.DigestWarning))
		}
	}
	b.add(b.clean("Run record: " + ref.Path))
	for _, l := range e.reportLogs() {
		b.add(b.clean(l))
	}

	// Failures: cut from the end when they do not fit.
	var detailLines []string
	if r.Boards != nil {
		failed := 0
		for _, it := range r.Boards.Items {
			if !boardDidNotList(it.Outcome) {
				continue
			}
			failed++
			detailLines = append(detailLines, b.clean(boardLine(it)))
		}
		if hidden := r.Boards.Failed - failed; hidden > 0 {
			detailLines = append(detailLines, fmt.Sprintf("(%d more failed board(s) are not in the record: it keeps a bounded number)", hidden))
		}
		if len(detailLines) > 0 {
			detailLines = append([]string{fmt.Sprintf("Failed boards (%d of %d):", r.Boards.Failed, r.Boards.Configured)}, detailLines...)
		}
	}
	if be := r.BackendErrors; be != nil && (len(be.Causes) > 0 || be.Unrecorded > 0) {
		calls := be.Unrecorded
		for _, c := range be.Causes {
			calls += c.Count
		}
		detailLines = append(detailLines, fmt.Sprintf("Backend errors (%d call(s), %d cause(s) recorded):", calls, len(be.Causes)))
		for _, c := range be.Causes {
			detailLines = append(detailLines, b.clean(fmt.Sprintf("%s (%d call(s)): %s", c.Backend, c.Count, c.Example)))
		}
		if be.Unrecorded > 0 {
			detailLines = append(detailLines, fmt.Sprintf("(%d more call(s) failed with causes not recorded)", be.Unrecorded))
		}
	}
	if len(detailLines) == 0 && !has {
		b.add("No failures in this run.")
	}

	budget := maxReportBytes - reportFooterReserve - b.bytes
	cut := 0
	for i, l := range detailLines {
		if budget-(len(l)+1) < 0 {
			cut = len(detailLines) - i
			break
		}
		budget -= len(l) + 1
		b.add(l)
	}
	if cut > 0 || b.shortened > 0 {
		var parts []string
		if b.shortened > 0 {
			parts = append(parts, fmt.Sprintf("%d line(s) shortened to %d characters", b.shortened, maxReportLine))
		}
		if cut > 0 {
			parts = append(parts, fmt.Sprintf("%d line(s) cut at %d KiB", cut, maxReportBytes>>10))
		}
		b.add("[" + strings.Join(parts, "; ") + "; vani runs show --json " + ref.ID + " has the whole record]")
	}
	return runReport{Text: strings.Join(b.lines, "\n") + "\n", HasFailures: has, CutLines: cut, ShortenedLines: b.shortened}
}

// boardLine is one failed board as "ats:token (Company): error", the shape the
// app's per-board "Copy error" also copies (a contract test holds them equal).
func boardLine(it boardReport) string {
	name := it.ATS + ":" + it.Token
	if it.Company != "" {
		name += " (" + it.Company + ")"
	}
	return name + ": " + orNone(it.Error)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// optionsSummary words a manual run's overrides, as the app does ("3 boards;
// advance cap 50; no digest").
func optionsSummary(o runOptions) string {
	var parts []string
	switch n := len(o.Boards); {
	case n == 1:
		parts = append(parts, "1 board")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d boards", n))
	}
	if o.ListOnly {
		parts = append(parts, "list only")
	} else if o.Advance > 0 {
		parts = append(parts, fmt.Sprintf("advance cap %d", o.Advance))
	}
	if o.NoDigest {
		parts = append(parts, "no digest")
	}
	return strings.Join(parts, "; ")
}
