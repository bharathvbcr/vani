package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// removedBoardRun is one board taken off the watchlist whose open postings a
// run closed (pipeline.CloseRemovedBoards). Its closings are not deaths: they
// count in no absence total and fail nothing.
type removedBoardRun struct {
	Board  string `json:"board"`
	Closed int    `json:"closed"`
	// KeptIngested is postings under its prefix first seen after the removal
	// (ingested, never listed by it), left open.
	KeptIngested int `json:"kept_ingested,omitempty"`
	// KeptWatched is postings under its prefix that a board still watched
	// covers, left to that board's listing.
	KeptWatched int `json:"kept_watched,omitempty"`
}

// closeRemovedBoards closes the open postings of the boards `vani watchlist
// remove` took off the watchlist, before the listing. The run holds the
// ledger lock. Tombstones are judged against the watchlist this run read from
// the config file (wired.watch), never the last one applied when the file
// could not be read, nor a run's cut. A tombstone directory that cannot be
// read closes nothing and is said; only a ledger error fails the run.
func (w *wired) closeRemovedBoards(ctx context.Context, now time.Time) ([]removedBoardRun, error) {
	tombs, over, problems, err := pipeline.LoadRemovedBoards(w.stateDir)
	if err != nil {
		w.warnf("%v; no removed board's postings are closed this run", err)
		return nil, nil
	}
	for _, p := range problems {
		w.warnf("%v; left as it is, it closes nothing", p)
	}
	if over > 0 {
		w.warnf("removed boards: %d more than the %d a run reads wait for a later run", over, pipeline.MaxRemovedBoards)
	}
	if len(tombs) == 0 {
		return nil, nil
	}
	w.mu.Lock()
	whole, read := slices.Clone(w.watched), w.watchedRead
	w.mu.Unlock()
	if len(whole) == 0 || read.IsZero() {
		w.warnf("removed boards: %d wait: this run read no watchlist from the config file to judge them against", len(tombs))
		return nil, nil
	}
	rep, err := pipeline.CloseRemovedBoards(ctx, w.led, tombs, pipeline.WatchedBy(whole), read, now)
	out := make([]removedBoardRun, 0, len(rep.Closed))
	for _, c := range rep.Closed {
		out = append(out, removedBoardRun{Board: c.Board, Closed: c.Closed, KeptIngested: c.KeptIngested, KeptWatched: c.KeptWatched})
		w.warnf("%s", removedBoardLine(out[len(out)-1]))
		if c.DeleteErr != nil {
			w.warnf("removed board %s: its tombstone stays (%v); the next run finds its postings closed", c.Board, c.DeleteErr)
		}
	}
	for _, p := range rep.Problems {
		w.warnf("%v", p)
	}
	if err != nil {
		return out, fmt.Errorf("removed boards: %w", err)
	}
	return out, nil
}

// removedBoardLine says what a run did with one removed board's postings.
func removedBoardLine(r removedBoardRun) string {
	s := fmt.Sprintf("removed board %s: closed %d open posting(s)", r.Board, r.Closed)
	if r.KeptIngested > 0 {
		s += fmt.Sprintf(", left open %d first seen after the removal (ingested)", r.KeptIngested)
	}
	if r.KeptWatched > 0 {
		s += fmt.Sprintf(", left open %d a watched board lists", r.KeptWatched)
	}
	return s + "; adding the board back reopens them"
}
