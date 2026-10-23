package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
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
func listBoards(ctx context.Context, watchlist []sources.Board, srcs map[posting.ATS]sources.Source, pageErr error,
	reconcile reconcileFunc, warnf func(string, ...any)) (boardsReport, error) {
	tried := make([]boardReport, 0, len(watchlist))
	report := func() boardsReport { return boardsReportOf(len(watchlist), tried) }
	for _, b := range watchlist {
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
			continue
		}
		listed, err := src.List(ctx, b)
		if err != nil {
			e.Outcome, e.Error = boardFailed, oneLine(err.Error(), maxBoardErrorRunes)
			tried = append(tried, e)
			if ctx.Err() != nil {
				return report(), ctx.Err()
			}
			warnf("board %s/%s: %v; left unchanged", b.ATS, b.Token, err)
			continue
		}
		n := len(listed)
		e.Listed = &n
		sum, err := reconcile(ctx, b, listed, time.Now())
		if err != nil {
			e.Outcome, e.Error = boardFailed, oneLine("reconcile: "+err.Error(), maxBoardErrorRunes)
			tried = append(tried, e)
			return report(), fmt.Errorf("reconcile %s/%s: %w", b.ATS, b.Token, err)
		}
		e.Outcome, e.Reconcile = boardRead, reconcileCountsOf(sum)
		tried = append(tried, e)
		warnf("board %s/%s: %+v", b.ATS, b.Token, sum)
	}
	return report(), nil
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
