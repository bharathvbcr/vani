package main

import (
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/apple"
	"github.com/bharathvbcr/Vani/internal/sources/avature"
	"github.com/bharathvbcr/Vani/internal/sources/eightfold"
	"github.com/bharathvbcr/Vani/internal/sources/ibm"
	"github.com/bharathvbcr/Vani/internal/sources/oraclehcm"
	"github.com/bharathvbcr/Vani/internal/sources/rippling"
	"github.com/bharathvbcr/Vani/internal/sources/smartrecruiters"
	"github.com/bharathvbcr/Vani/internal/sources/meta"
	"github.com/bharathvbcr/Vani/internal/sources/talentbrew"
	"github.com/bharathvbcr/Vani/internal/sources/workable"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
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
	// Unanswered is details whose request failed transiently and were left for a
	// later run (the adapters' Stats.Unanswered): a new row is not returned, a
	// stored one is returned as stored.
	Unanswered int
	// Prefiltered is new listed rows the drop policy rejected before any
	// detail request (the adapter's Stats.Prefiltered): never stored.
	Prefiltered int
	// BadRows is rows the adapter skipped because it could not read them
	// (srcutil.BadRowSink); they are also in Short. A listing with any is
	// reconciled so that it never kills what it leaves out (ReconcileUnreadable).
	BadRows int
	// Short is rows the board counted but did not list (workday.Stats.Short).
	Short int
}

type boardKey struct {
	ATS   string
	Token string
}

func fetchKey(ats posting.ATS, token string) boardKey {
	return boardKey{ATS: string(ats), Token: oneLine(token, maxBoardNameRunes)}
}

// The adapters' counts as the run record keeps them: one function each, so a
// count an adapter reports and the record drops fails a test
// (TestAdapterStatsReachTheBoardFetch) rather than an inline closure.
func workdayFetch(st workday.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Prefiltered: st.Prefiltered, Short: st.Short, Unanswered: st.Unanswered}
}

func smartRecruitersFetch(st smartrecruiters.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Prefiltered: st.Prefiltered, Short: st.Short, Unanswered: st.Unanswered}
}

func oracleFetch(st oraclehcm.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Short: st.Short, Unanswered: st.Unanswered}
}

func ripplingFetch(st rippling.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Prefiltered: st.Prefiltered, Unanswered: st.Unanswered}
}

func workableFetch(st workable.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Prefiltered: st.Prefiltered, Unanswered: st.Unanswered}
}

func talentBrewFetch(st talentbrew.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Prefiltered: st.Prefiltered, Unanswered: st.Unanswered}
}

func metaFetch(st meta.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Unanswered: st.Unanswered}
}

func eightfoldFetch(st eightfold.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Prefiltered: st.Prefiltered, Unanswered: st.Unanswered}
}

func avatureFetch(st avature.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Prefiltered: st.Prefiltered, Unanswered: st.Unanswered}
}

func appleFetch(st apple.Stats) boardFetch {
	return boardFetch{Deferred: st.Deferred, Gone: st.Gone, Unanswered: st.Unanswered}
}

func ibmFetch(st ibm.Stats) boardFetch { return boardFetch{Short: st.Short} }

// noteBoardFetch keeps one board's fetch counts. A second report for the same
// board adds; the first pay error is the one kept. A listing that counted rows
// it did not list (Short) is said in the run log too.
func (w *wired) noteBoardFetch(b sources.Board, f boardFetch) {
	if f.Short > 0 {
		w.warnf("board %s/%s: listed %d job(s) fewer than it counts; the listing is kept, and stored postings it left out are held as a mass absence, not aged by the ordinary rule", b.ATS, b.Token, f.Short)
	}
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
	prev.Prefiltered += f.Prefiltered
	prev.Short += f.Short
	prev.BadRows += f.BadRows
	prev.Unanswered += f.Unanswered
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
		// A listing's short rows age stored postings only when the board was
		// read and reconciled; an adapter may report them before a detail
		// fails the board, so a board that did not read keeps none.
		if it.Outcome != boardRead {
			f.Short = 0
		}
		it.Deferred = f.Deferred
		it.PayFailed = f.PayFailed
		it.PaySkipped = f.PaySkipped
		it.Gone = f.Gone
		it.Prefiltered = f.Prefiltered
		it.Short = f.Short
		it.Unanswered = f.Unanswered
		it.PayError = f.PayError
		addFetch(rep, f)
	}
	// A board Items left out is counted whole: its outcome is not at hand.
	// The cut keeps failed and skipped boards before read ones
	// (boardsReportOf), so it was read unless those alone overflow the bound.
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
	rep.Prefiltered += f.Prefiltered
	rep.Short += f.Short
	rep.Unanswered += f.Unanswered
	if rep.PayError == "" {
		rep.PayError = f.PayError
	}
}
