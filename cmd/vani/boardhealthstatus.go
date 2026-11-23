package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// boardHealthFacet is what status says about the boards a run reads: the
// stale ones (board health, boardhealth.go), the watched ones nothing tracks,
// and the ones that used to list and now list none (boardsnone.go, from the
// last run's report). Reporting only. Additive on status --json.
type boardHealthFacet struct {
	facet
	// Tracked is false when no run has written board health yet; the stale
	// list is then empty because nothing is known, not because nothing is wrong.
	Tracked bool `json:"tracked"`
	// Updated is when a run last folded its boards in.
	Updated        time.Time    `json:"updated,omitzero"`
	Stale          []staleBoard `json:"stale,omitempty"`
	StaleCount     int          `json:"stale_count,omitempty"`
	Untracked      []string     `json:"untracked,omitempty"`
	UntrackedCount int          `json:"untracked_count,omitempty"`
	Empty          []emptyBoard `json:"empty,omitempty"`
	EmptyCount     int          `json:"empty_count,omitempty"`
	// ListsNothing are watched boards whose good reads keep listing 0 with
	// nothing of theirs in the ledger (boardszero.go); ListsNothingNote says the
	// check could not run (the ledger could not be read).
	ListsNothing      []zeroBoard `json:"lists_nothing,omitempty"`
	ListsNothingCount int         `json:"lists_nothing_count,omitempty"`
	ListsNothingNote  string      `json:"lists_nothing_note,omitempty"`
}

// boardHealthFacet reads board health and the last run's report as they stand,
// judged at at. It writes nothing.
func (e *env) boardHealthFacet(cfg config.Config, at time.Time) boardHealthFacet {
	return e.boardHealthFacetFrom(cfg, at, nil)
}

// boardHealthFacetFrom is boardHealthFacet over a ledger fold the caller
// already holds (nil: read it if a board needs it).
func (e *env) boardHealthFacetFrom(cfg config.Config, at time.Time, folded *foldedLedger) boardHealthFacet {
	v := readBoardHealthView(e.stateDir, cfg.Watchlist, at)
	f := boardHealthFacet{facet: facet{OK: true}, Tracked: v.Tracked, Updated: v.Updated, Stale: v.Stale, StaleCount: v.StaleCount,
		Untracked: v.Untracked, UntrackedCount: v.UntrackedN}
	f.ListsNothing, f.ListsNothingCount, f.ListsNothingNote = e.zeroBoardsOf(v.ZeroCandidates, folded)
	if v.Warn != "" {
		f.OK, f.Reason = false, oneLine(v.Warn, 300)
	}
	if r, err := readRunRecordFile(runRecordPath(e.stateDir)); err == nil && r != nil && r.Boards != nil {
		f.Empty, f.EmptyCount = r.Boards.Empty, r.Boards.EmptyCount
	}
	return f
}

// boardHealthLines are the status lines for f. A board nothing tracks, a health
// file no run has refreshed and a missing file are said, never read as "no
// stale board".
func boardHealthLines(f boardHealthFacet, at time.Time) []string {
	var out []string
	switch {
	case f.Reason != "":
		out = append(out, "board health: "+f.Reason)
	case !f.Tracked:
		out = append(out, "board health: no run has recorded it yet, so stale boards are not known")
	case !f.Updated.IsZero() && at.Sub(f.Updated) > boardStaleAfter:
		out = append(out, fmt.Sprintf("board health: last updated %s ago; no run has refreshed it, so it may be out of date",
			at.Sub(f.Updated).Round(time.Hour)))
	}
	if f.StaleCount > 0 {
		out = append(out, fmt.Sprintf("stale boards (%d): no good read for over %s, so their postings are not refreshed:", f.StaleCount, boardStaleAfter))
		for _, s := range f.Stale {
			out = append(out, "  "+oneLine(staleBoardLine(s, at), 400))
		}
		if hidden := f.StaleCount - len(f.Stale); hidden > 0 {
			out = append(out, fmt.Sprintf("  (%d more)", hidden))
		}
	}
	if f.UntrackedCount > 0 {
		out = append(out, fmt.Sprintf("boards not tracked yet (%d, no board health entry): %s", f.UntrackedCount, strings.Join(f.Untracked, ", ")))
	}
	if f.EmptyCount > 0 {
		out = append(out, fmt.Sprintf("boards that list nothing now though they listed before (%d):", f.EmptyCount))
		for _, b := range f.Empty {
			out = append(out, "  "+oneLine(emptyBoardLine(b), 400))
		}
		if hidden := f.EmptyCount - len(f.Empty); hidden > 0 {
			out = append(out, fmt.Sprintf("  (%d more)", hidden))
		}
	}
	return append(out, zeroBoardLines(f.ListsNothing, f.ListsNothingCount, f.ListsNothingNote)...)
}
