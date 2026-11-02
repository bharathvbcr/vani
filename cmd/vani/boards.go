package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Bounds on the run record's board report. Totals stay exact past them; only
// the per-board entries are cut, and the cut is counted (Unrecorded).
const (
	// maxRecordedBoards bounds boardsReport.Items.
	maxRecordedBoards = 100
	// maxBoardsBytes bounds the compact JSON of boardsReport.Items, so a
	// worst-case report cannot push the record past maxRunRecord.
	maxBoardsBytes = 32 << 10
	// maxBoardErrorRunes bounds one board's error line.
	maxBoardErrorRunes = 300
	// maxBoardNameRunes bounds a board's token and company as recorded.
	maxBoardNameRunes = 120
)

// reconcileFunc folds one board's listing into the ledger
// (pipeline.Reconciler.Reconcile).
type reconcileFunc func(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (pipeline.Summary, error)

// listBoards lists every board in watchlist order and reconciles each
// listing. A board with no source is skipped (pageErr, when set, is why the
// careers-page source is disabled); a board whose listing fails is left
// unchanged in the ledger, because an unread board must never count as "every
// posting disappeared". Both are warnings, not a failed run. It returns an
// error only for cancellation or a reconcile (ledger) failure, and the report
// says what happened to every board tried before that, however it ended.
//
// The listing is bounded as a whole, not only board by board: every board's
// waiting to retry is charged to one run budget (srcutil.RunRetryBudget), and a
// host that kept answering 429 is remembered, within the run and, through
// cooldownFile, from one run to the next (srcutil.HostThrottle). The next run
// sends such a host one probe request and stops at once if it is still
// throttled. An empty cooldownFile keeps the memory to this run. None of this
// reaches the ledger: a board that is not asked is a board that failed.
func listBoards(ctx context.Context, watchlist []sources.Board, srcs map[posting.ATS]sources.Source, pageErr error,
	reconcile reconcileFunc, warnf func(string, ...any), cooldownFile string) (boardsReport, error) {
	throttle, saveThrottle := openHostThrottle(cooldownFile, warnf)
	defer saveThrottle() // also when the run is cancelled part-way: the 429s were seen
	ctx = srcutil.WithHostThrottle(srcutil.WithRunRetryBudget(ctx, srcutil.RunRetryBudget), throttle)
	tried := make([]boardReport, 0, len(watchlist))
	report := func() boardsReport { return boardsReportOf(len(watchlist), tried) }
	total := len(watchlist)
	for i, b := range watchlist {
		index := i + 1
		e := boardReport{ATS: string(b.ATS), Token: oneLine(b.Token, maxBoardNameRunes), Company: oneLine(b.Company, maxBoardNameRunes)}
		src, ok := srcs[b.ATS]
		if !ok {
			why := fmt.Sprintf("no adapter for %s", b.ATS)
			if b.ATS == posting.Page {
				why = "careers-page source disabled this run"
				if pageErr != nil {
					why += ": " + pageErr.Error()
				}
			}
			warnf("board %s/%s: %s; skipped", b.ATS, b.Token, why)
			e.Outcome, e.Error = boardSkipped, oneLine(why, maxBoardErrorRunes)
			tried = append(tried, e)
			emitListing(ctx, progressPhaseDone, index, total, b, tried)
			continue
		}
		// Said before the fetch: a slow board is otherwise silent until it ends.
		warnf("board %s/%s: reading (%d of %d)", b.ATS, b.Token, index, total)
		emitListing(ctx, progressPhaseListing, index, total, b, tried)
		// One retry budget per board: waiting out a host's 429s is bounded for
		// the board as a whole, not only for each of its requests.
		listed, err := src.List(srcutil.WithRetryBudget(ctx, srcutil.BoardRetryBudget), b)
		if err != nil {
			e.Outcome, e.Error = boardFailed, oneLine(err.Error(), maxBoardErrorRunes)
			next := "left unchanged"
			if errors.Is(err, srcutil.ErrRateLimited) {
				// The host is throttling, not broken: say so. The postings are
				// untouched and the board is tried again next run.
				e.Outcome = boardRateLimited
				next = "left unchanged, will retry next run"
			}
			tried = append(tried, e)
			emitListing(ctx, progressPhaseDone, index, total, b, tried)
			if ctx.Err() != nil {
				return report(), ctx.Err()
			}
			warnf("board %s/%s: %v; %s", b.ATS, b.Token, err, next)
			continue
		}
		n := len(listed)
		e.Listed = &n
		emitListing(ctx, progressPhaseReconciling, index, total, b, tried)
		sum, err := reconcile(ctx, b, listed, time.Now())
		if err != nil {
			e.Outcome, e.Error = boardFailed, oneLine("reconcile: "+err.Error(), maxBoardErrorRunes)
			tried = append(tried, e)
			emitListing(ctx, progressPhaseDone, index, total, b, tried)
			return report(), fmt.Errorf("reconcile %s/%s: %w", b.ATS, b.Token, err)
		}
		e.Outcome, e.Reconcile = boardRead, reconcileCountsOf(sum)
		tried = append(tried, e)
		emitListing(ctx, progressPhaseDone, index, total, b, tried)
		warnf("board %s/%s: %+v", b.ATS, b.Token, sum)
	}
	return report(), nil
}

// listingTick is one moment of listBoards, carried on the context so the
// existing callers keep their signature. Counts exclude the board still in
// flight: they are tallied from the boards already appended.
type listingTick struct {
	Phase                                            string
	Index, Total                                     int
	Board                                            sources.Board
	Done, Read, Failed, RateLimited, Skipped, Listed int
	// Outcome is set only for phase done, and only when the last appended
	// report is this board. Empty while the board is still in flight.
	Outcome string
}

type listingTickFunc func(listingTick)

type listingTickKey struct{}

func withListingTick(ctx context.Context, fn listingTickFunc) context.Context {
	if fn == nil || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, listingTickKey{}, fn)
}

func noteListing(ctx context.Context, tk listingTick) {
	if ctx == nil {
		return
	}
	fn, _ := ctx.Value(listingTickKey{}).(listingTickFunc)
	if fn != nil {
		fn(tk)
	}
}

func tallyTried(tried []boardReport) (done, read, failed, limited, skipped, listed int) {
	for _, e := range tried {
		done++
		switch e.Outcome {
		case boardRead:
			read++
			if e.Listed != nil {
				listed += *e.Listed
			}
		case boardFailed:
			failed++
		case boardRateLimited:
			failed++
			limited++
		case boardSkipped:
			skipped++
		}
	}
	return
}

func emitListing(ctx context.Context, phase string, index, total int, b sources.Board, tried []boardReport) {
	done, read, failed, limited, skipped, listed := tallyTried(tried)
	tk := listingTick{
		Phase: phase, Index: index, Total: total, Board: b,
		Done: done, Read: read, Failed: failed, RateLimited: limited, Skipped: skipped, Listed: listed,
	}
	if phase == progressPhaseDone && len(tried) > 0 {
		last := tried[len(tried)-1]
		if last.ATS == string(b.ATS) && last.Token == oneLine(b.Token, maxBoardNameRunes) {
			tk.Outcome = last.Outcome
		}
	}
	noteListing(ctx, tk)
}

// boardsClock is the time the host cooldowns are read and stamped by; a
// variable only so tests can set it.
var boardsClock = time.Now

// openHostThrottle loads the cooldowns the last run left in path (none when path
// is empty) and returns the run's throttle and the function that writes the
// cooldowns back. A file that cannot be used is a warning, not a failed run, and
// the write replaces it. Nothing is written when nothing changed.
func openHostThrottle(path string, warnf func(string, ...any)) (*srcutil.HostThrottle, func()) {
	var prior map[string]srcutil.HostCooldown
	var loadWarn string
	if path != "" {
		prior, loadWarn = srcutil.LoadCooldowns(path, boardsClock())
		if loadWarn != "" {
			warnf("%s", loadWarn)
		}
	}
	throttle := srcutil.NewHostThrottle(prior, boardsClock)
	return throttle, func() {
		if path == "" {
			return
		}
		after := throttle.Cooldowns()
		same := maps.EqualFunc(prior, after, func(a, b srcutil.HostCooldown) bool { return a.Runs == b.Runs && a.Last.Equal(b.Last) })
		if same && loadWarn == "" {
			return
		}
		if err := srcutil.SaveCooldowns(path, after, boardsClock()); err != nil {
			warnf("host cooldowns not saved: %v", err)
		}
	}
}

func reconcileCountsOf(s pipeline.Summary) *reconcileCounts {
	return &reconcileCounts{New: s.New, Changed: s.Changed, Rerendered: s.Rerendered, Kept: s.Kept, Redated: s.Redated,
		Refreshed: s.Refreshed, Revived: s.Revived, Unchanged: s.Unchanged,
		Reappeared: s.Reappeared, Missed: s.Missed, Dead: s.Dead, Duplicate: s.Duplicate, Invalid: s.Invalid}
}

// boardsReportOf totals the tried boards out of configured and keeps as many
// entries as the bounds allow: failed and skipped boards first (they are why
// the report exists), then read ones, each group in watchlist order. Items
// keeps watchlist order.
func boardsReportOf(configured int, tried []boardReport) boardsReport {
	r := boardsReport{Configured: configured, NotReached: max(configured-len(tried), 0), Items: []boardReport{}}
	for _, e := range tried {
		switch e.Outcome {
		case boardRead:
			r.Read++
			if e.Listed != nil {
				r.Listed += *e.Listed
			}
		case boardFailed:
			r.Failed++
		case boardRateLimited:
			r.Failed++
			r.RateLimited++
		case boardSkipped:
			r.Skipped++
		}
	}
	// The first entry that does not fit ends the selection, so a read board is
	// never kept while a failed or skipped one is cut.
	keep := make([]bool, len(tried))
	kept, size := 0, 0
fill:
	for _, readPass := range []bool{false, true} {
		for i, e := range tried {
			if (e.Outcome == boardRead) != readPass {
				continue
			}
			raw, err := json.Marshal(e)
			if err != nil || kept >= maxRecordedBoards || size+len(raw)+1 > maxBoardsBytes {
				break fill // the rest are counted below as unrecorded
			}
			keep[i], kept, size = true, kept+1, size+len(raw)+1
		}
	}
	for i, e := range tried {
		if keep[i] {
			r.Items = append(r.Items, e)
		} else {
			r.Unrecorded++
		}
	}
	return r
}
