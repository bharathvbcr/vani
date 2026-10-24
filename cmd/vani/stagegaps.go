package main

import (
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// explain modes (explainNote.Mode). A list-only pass still warns and does not
// record one: it was never going to ask a model.
const (
	explainOff    = "off"    // no catalog, a stale one, or no model configured
	explainPaused = "paused" // the catalog or the model could not be used this run
)

// noteExplain records that the digest's notes did not run. A list-only pass
// leaves the record alone. The line is the same one warnf prints, bounded.
func (w *wired) noteExplain(mode, msg string) {
	if !w.recordGaps {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.explain = &explainNote{Mode: mode, Note: oneLine(msg, maxStageNote)}
}

// noteShadow records why a configured shadow never attached. A local-server
// start failure is already shadow.Local; this does not repeat it. A list-only
// pass leaves the record alone.
func (w *wired) noteShadow(msg string) {
	if !w.recordGaps {
		return
	}
	line := oneLine(msg, maxStageNote)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.shadow != nil && w.shadow.Local != "" && strings.Contains(line, "local decision model") {
		return
	}
	if w.shadow == nil {
		w.shadow = &shadowNote{}
	}
	if w.shadow.Note == "" {
		w.shadow.Note = line
		return
	}
	if strings.Contains(w.shadow.Note, line) {
		return
	}
	w.shadow.Note = oneLine(w.shadow.Note+"; "+line, maxStageNote)
}

// noteShadowMiss records shadow calls that failed and sampled postings the
// shadow skipped while it was busy. Zeroes leave an existing note alone.
func (w *wired) noteShadowMiss(errors, busy int) {
	if errors < 0 {
		errors = 0
	}
	if busy < 0 {
		busy = 0
	}
	if errors == 0 && busy == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.shadow == nil {
		w.shadow = &shadowNote{}
	}
	w.shadow.Errors = errors
	w.shadow.BusySkips = busy
}

// noteSimilarity records a near-duplicate cosine that did not run (off) or a
// cache that could not be used or saved (a note; the cosine still ran).
func (w *wired) noteSimilarity(off bool, msg string) {
	line := oneLine(msg, maxStageNote)
	w.mu.Lock()
	defer w.mu.Unlock()
	if off {
		w.simOff = line
		return
	}
	if w.simNote == "" {
		w.simNote = line
		return
	}
	w.simNote = oneLine(w.simNote+"; "+line, maxStageNote)
}

// finishSimilarity copies the cosine notes onto a near-dup summary.
func (w *wired) finishSimilarity(s *nearDupSummary) {
	if s == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s.SimilarityOff = w.simOff
	s.SimilarityNote = w.simNote
}

// boardFetch is one board's detail and pay counts. None of it is a posting.
type boardFetch struct {
	Deferred   int
	PayFailed  int
	PaySkipped int
	Gone       int
	PayError   string
}

type boardKey struct {
	ATS   string
	Token string
}

func fetchKey(ats posting.ATS, token string) boardKey {
	return boardKey{ATS: string(ats), Token: oneLine(token, maxBoardNameRunes)}
}

// noteBoardFetch keeps one board's fetch counts. A second report for the same
// board adds; the first pay error is the one kept.
func (w *wired) noteBoardFetch(b sources.Board, f boardFetch) {
	if f.PayError != "" {
		f.PayError = oneLine(f.PayError, maxStageNote)
	}
	key := fetchKey(b.ATS, b.Token)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fetches == nil {
		w.fetches = map[boardKey]boardFetch{}
	}
	prev := w.fetches[key]
	prev.Deferred += f.Deferred
	prev.PayFailed += f.PayFailed
	prev.PaySkipped += f.PaySkipped
	prev.Gone += f.Gone
	if prev.PayError == "" {
		prev.PayError = f.PayError
	}
	w.fetches[key] = prev
}

// applyFetchesLocked copies fetch counts onto the board report. Totals count
// every board, including one the item list had to drop. The caller holds w.mu.
func (w *wired) applyFetchesLocked(rep *boardsReport) {
	if rep == nil || len(w.fetches) == 0 {
		return
	}
	seen := map[boardKey]bool{}
	for i := range rep.Items {
		it := &rep.Items[i]
		f, ok := w.fetches[boardKey{it.ATS, it.Token}]
		if !ok {
			continue
		}
		seen[boardKey{it.ATS, it.Token}] = true
		it.Deferred = f.Deferred
		it.PayFailed = f.PayFailed
		it.PaySkipped = f.PaySkipped
		it.Gone = f.Gone
		it.PayError = f.PayError
		addFetch(rep, f)
	}
	for k, f := range w.fetches {
		if seen[k] {
			continue
		}
		addFetch(rep, f)
	}
}

func addFetch(rep *boardsReport, f boardFetch) {
	rep.Deferred += f.Deferred
	rep.PayFailed += f.PayFailed
	rep.PaySkipped += f.PaySkipped
	rep.Gone += f.Gone
	if rep.PayError == "" {
		rep.PayError = f.PayError
	}
}
