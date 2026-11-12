// Package doctor is the pipeline doctor: a report-only engine that has Claude
// (the configured Manvi provider, Opus by default) read what the daily runs
// left behind and say where the pipeline is failing in silence.
//
// The split is fixed: Go computes, the model interprets. Everything a verdict
// rests on (per-board signals across the run history, the distinct error lines
// of the run logs, a seeded stratified sample of dropped postings) is derived
// here deterministically and sent as compact tables; the model only classifies
// and explains. Its answers are a report and nothing else: no ledger status,
// annotation, drop rule or watchlist entry is changed by anything in this
// package (Rule 7).
//
// A board or a dropped posting the model was not asked about, or did not
// answer, is reported as not examined, never as healthy or correct.
package doctor

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Outcomes of one board in one run, as the run record writes them.
const (
	OutcomeRead        = "read"
	OutcomeFailed      = "failed"
	OutcomeRateLimited = "rate_limited"
	OutcomeSkipped     = "skipped"
)

// BoardRun is one board in one run: the subset of the run record's board
// entry the signals read.
type BoardRun struct {
	RunID   string
	At      time.Time
	Board   string // "ats:token"
	Company string
	Outcome string
	Listed  *int // nil when the board did not list (failed, skipped)
	New     int
	Missed  int
	Dead    int
	Error   string
	// FromLog is a read recovered from the run's own log (LogBoardRuns)
	// because the run's record left the board unnamed.
	FromLog bool
}

// RunSummary is one run's board totals.
type RunSummary struct {
	ID         string
	At         time.Time
	Configured int
	Read       int
	Failed     int
	Unrecorded int // tried boards the run record left out of its items
	Listed     int
	FailOpen   map[string]int
}

// Signal kinds. Each is a deterministic rule over one board's history; the
// model is told the rule, not asked to find it.
const (
	// SigZeroEveryRun: read at least minRuns times and listed nothing every time.
	SigZeroEveryRun = "zero_every_run"
	// SigZeroAfterNonzero: a read that listed nothing between reads that listed
	// some: an empty reply accepted as the board's whole listing.
	SigZeroAfterNonzero = "zero_after_nonzero"
	// SigSharpDrop: the latest read listed under half the board's median.
	SigSharpDrop = "sharp_drop"
	// SigMassMissed: one read marked at least half of the previous listing missed.
	SigMassMissed = "mass_missed"
	// SigChronicFailure: failed or rate limited in at least half its runs.
	SigChronicFailure = "chronic_failure"
	// SigFailingNow: the latest run that tried it did not read it.
	SigFailingNow = "failing_now"
	// SigNeverRecorded: on the watchlist, but in no run record's items.
	SigNeverRecorded = "never_recorded"
)

const (
	minRuns        = 3   // reads before a board's history means anything
	sharpDropShare = 0.5 // latest under this share of the median
	sharpDropMin   = 10  // median below this is too small to call a drop
	massMissedMin  = 5
	maxSeries      = 30 // listed values kept per board
	maxErrorRunes  = 200
)

// BoardSignal is one board that tripped at least one signal, with the
// evidence a reader (or the model) needs to judge it.
type BoardSignal struct {
	Board     string   `json:"board"`
	Company   string   `json:"company,omitempty"`
	Signals   []string `json:"signals"`
	Runs      int      `json:"runs"`                    // runs whose record or log names it
	FromLog   int      `json:"runs_from_log,omitempty"` // of which recovered from the run's log (the record left it unnamed)
	Reads     int      `json:"reads"`                   // of which read
	ZeroReads int      `json:"zero_reads"`              // of which listed nothing
	Failures  int      `json:"failures"`                // failed or rate limited
	Median    int      `json:"median_listed"`
	Latest    *int     `json:"latest_listed,omitempty"`
	Series    []int    `json:"listed_series,omitempty"` // oldest first; -1 is a run that did not read it
	MaxMissed int      `json:"max_missed_in_one_run,omitempty"`
	LastError string   `json:"last_error,omitempty"`
	// Details are the board's raw listing lines from the newest run logs
	// (Details), newest first: Listed before the prefilter, Prefiltered,
	// Queries, Requests. Empty when no log read names the board.
	Details []string `json:"raw_details,omitempty"`
	// anomalous are the run ids of the reads that listed nothing or tripped
	// mass_missed, oldest first (AttachDetails reads their log lines first).
	anomalous []string
}

// Signals derives every board's signals from its history. watchlist is every
// configured board ("ats:token"); a board on it that no run's items name is
// SigNeverRecorded. The result is sorted by board, and every board in it has at
// least one signal.
func Signals(history []BoardRun, watchlist []string) []BoardSignal {
	byBoard := map[string][]BoardRun{}
	for _, br := range history {
		if br.Board == "" {
			continue
		}
		byBoard[br.Board] = append(byBoard[br.Board], br)
	}
	var out []BoardSignal
	for board, runs := range byBoard {
		slices.SortStableFunc(runs, func(a, b BoardRun) int {
			if c := a.At.Compare(b.At); c != 0 {
				return c
			}
			return cmp.Compare(a.RunID, b.RunID)
		})
		if s, ok := boardSignal(board, runs); ok {
			out = append(out, s)
		}
	}
	seen := map[string]bool{}
	for _, br := range history {
		seen[br.Board] = true
	}
	for _, b := range uniqueSorted(watchlist) {
		if !seen[b] {
			out = append(out, BoardSignal{Board: b, Signals: []string{SigNeverRecorded}})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Board < out[j].Board })
	return out
}

func boardSignal(board string, runs []BoardRun) (BoardSignal, bool) {
	s := BoardSignal{Board: board, Runs: len(runs)}
	var listed []int
	prev := -1 // the previous read's listing
	sawNonzeroBefore := false
	zeroAfterNonzero := false
	for i, r := range runs {
		if r.Company != "" {
			s.Company = r.Company
		}
		if r.FromLog {
			s.FromLog++
		}
		if r.Outcome == OutcomeFailed || r.Outcome == OutcomeRateLimited {
			s.Failures++
		}
		if r.Error != "" {
			s.LastError = cut(r.Error, maxErrorRunes)
		}
		if r.Outcome != OutcomeRead || r.Listed == nil {
			s.Series = append(s.Series, -1)
			continue
		}
		n := *r.Listed
		s.Reads++
		listed = append(listed, n)
		s.Series = append(s.Series, n)
		if n == 0 || (prev > 0 && r.Missed >= massMissedMin && 2*r.Missed >= prev) {
			s.anomalous = append(s.anomalous, r.RunID)
		}
		if n == 0 {
			s.ZeroReads++
			// A zero followed later by a nonzero read, or preceded by one.
			if sawNonzeroBefore || laterNonzero(runs[i+1:]) {
				zeroAfterNonzero = true
			}
		} else {
			sawNonzeroBefore = true
		}
		if r.Missed > s.MaxMissed {
			s.MaxMissed = r.Missed
		}
		if prev > 0 && r.Missed >= massMissedMin && 2*r.Missed >= prev {
			s.Signals = appendOnce(s.Signals, SigMassMissed)
		}
		prev = n
	}
	if len(s.Series) > maxSeries {
		s.Series = s.Series[len(s.Series)-maxSeries:]
	}
	s.Median = median(listed)
	last := runs[len(runs)-1]
	if last.Outcome == OutcomeRead && last.Listed != nil {
		v := *last.Listed
		s.Latest = &v
	}
	if s.Reads >= minRuns && s.ZeroReads == s.Reads {
		s.Signals = appendOnce(s.Signals, SigZeroEveryRun)
	}
	if zeroAfterNonzero {
		s.Signals = appendOnce(s.Signals, SigZeroAfterNonzero)
	}
	if s.Latest != nil && s.Reads >= minRuns && s.Median >= sharpDropMin && float64(*s.Latest) < sharpDropShare*float64(s.Median) {
		s.Signals = appendOnce(s.Signals, SigSharpDrop)
	}
	if len(runs) >= 2 && 2*s.Failures >= len(runs) {
		s.Signals = appendOnce(s.Signals, SigChronicFailure)
	}
	if last.Outcome == OutcomeFailed || last.Outcome == OutcomeRateLimited {
		s.Signals = appendOnce(s.Signals, SigFailingNow)
	}
	if len(s.Signals) == 0 {
		return BoardSignal{}, false
	}
	sort.Strings(s.Signals)
	return s, true
}

func laterNonzero(rest []BoardRun) bool {
	for _, r := range rest {
		if r.Outcome == OutcomeRead && r.Listed != nil && *r.Listed > 0 {
			return true
		}
	}
	return false
}

// median of xs (the lower middle for an even count); 0 for none.
func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	c := slices.Clone(xs)
	slices.Sort(c)
	return c[(len(c)-1)/2]
}

func appendOnce(xs []string, s string) []string {
	if slices.Contains(xs, s) {
		return xs
	}
	return append(xs, s)
}

func uniqueSorted(xs []string) []string {
	c := slices.Clone(xs)
	slices.Sort(c)
	return slices.Compact(c)
}

// cut is s on one line, at most n runes, with an ellipsis when cut.
func cut(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// RunTrend is the run-level picture: per run, how much of the watchlist the
// record names and the fail-open counts, oldest first.
func RunTrend(runs []RunSummary) []string {
	c := slices.Clone(runs)
	slices.SortStableFunc(c, func(a, b RunSummary) int { return a.At.Compare(b.At) })
	out := make([]string, 0, len(c))
	for _, r := range c {
		fo := make([]string, 0, len(r.FailOpen))
		for k, v := range r.FailOpen {
			if v != 0 {
				fo = append(fo, fmt.Sprintf("%s=%d", k, v))
			}
		}
		sort.Strings(fo)
		out = append(out, fmt.Sprintf("%s configured=%d read=%d failed=%d unrecorded=%d listed=%d fail_open[%s]",
			r.At.UTC().Format("2006-01-02T15:04Z"), r.Configured, r.Read, r.Failed, r.Unrecorded, r.Listed, strings.Join(fo, " ")))
	}
	return out
}
