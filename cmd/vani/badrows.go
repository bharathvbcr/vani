package main

import (
	"github.com/bharathvbcr/Vani/internal/sources"
)

// maxBadRowNote bounds the skipped row's error in the run log.
const maxBadRowNote = 160

// badRowSink is the srcutil.BadRowSink of a run: an adapter that skipped rows
// it could not read reports how many, and the board's listing is then short by
// that many (boardFetch.Short), so reconcileBoard folds it as a short one: the
// postings behind the skipped rows, and any other stored posting the listing
// leaves out, are held as a mass absence rather than aged by the ordinary
// rule. The error text may hold the row's own content, which is untrusted: it
// is bounded and kept to one line before it reaches the log, and is never
// stored.
func (w *wired) badRowSink(b sources.Board, skipped int, first string) {
	w.warnf("board %s/%s: skipped %d row(s) that could not be read (first: %q)", b.ATS, b.Token, skipped, oneLine(first, maxBadRowNote))
	w.noteBoardFetch(b, boardFetch{Short: skipped, BadRows: skipped})
}
