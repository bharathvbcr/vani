package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// A board's backlog and its ongoing problems, as reporting.
//
// Rows past a board's detail cap (srcutil.MaxDetails) are deferred: they are
// not stored, so no row carries a first-deferred time and the age of the
// oldest cannot be read from the ledger. What board health keeps instead is
// DeferredSince, when the board's current run of good reads that each deferred
// something began. The oldest deferred row is no older than that, so it is an
// upper bound, and a backlog that never empties shows as an age that only
// grows. ShortSince is the same for a listing that counted more rows than it
// listed: such a board holds what it left out as a mass absence (reconcileBoard), so one that is short on
// every run is named rather than left to look healthy.
//
// None of it fails a run. A run counts as failed only when postings die or a
// board fails; an ongoing problem is an attention line (boardAttentionLine).

const (
	// maxBacklogLines bounds the per-board "Deferred backlog" lines of runs show.
	maxBacklogLines = 10
	// maxAttentionNames bounds the boards one attention line names per kind;
	// the counts stay exact.
	maxAttentionNames = 5
)

// trackBacklog folds one good read's fetch counts into r's streaks: a deferred
// count starts DeferredSince when none is open and a read with none closes it;
// the same for a short listing and ShortSince. An attempt that was not a good
// read says nothing about either, so r is returned as it is.
func trackBacklog(r boardHealthRec, e boardReport, f boardFetch, at time.Time) boardHealthRec {
	if !goodRead(e) {
		return r
	}
	r.DeferredSince = streak(r.DeferredSince, f.Deferred > 0, at)
	r.ShortSince = streak(r.ShortSince, f.Short > 0, at)
	return r
}

// streak is a streak's start after a read: kept while it goes on, begun at at
// when it starts, zero when the read ends it.
func streak(since time.Time, on bool, at time.Time) time.Time {
	switch {
	case !on:
		return time.Time{}
	case since.IsZero():
		return at
	}
	return since
}

// fetchSnapshot is this run's per-board fetch counts, copied.
func (w *wired) fetchSnapshot() map[boardKey]boardFetch {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[boardKey]boardFetch, len(w.fetches))
	for k, f := range w.fetches {
		out[k] = f
	}
	return out
}

// applyBacklogLocked copies the streak starts board health holds onto the run
// record's board items. h is nil when there is no state dir. The caller holds
// w.mu.
func applyBacklogLocked(rep *boardsReport, h *boardHealth) {
	if rep == nil || h == nil {
		return
	}
	for i := range rep.Items {
		it := &rep.Items[i]
		if it.Outcome != boardRead {
			continue
		}
		r, ok := h.doc.Boards[boardHealthKey(it.ATS, it.Token)]
		if !ok {
			continue
		}
		if it.Deferred > 0 && !r.DeferredSince.IsZero() {
			t := r.DeferredSince
			it.DeferredSince = &t
		}
		if it.Short > 0 && !r.ShortSince.IsZero() {
			t := r.ShortSince
			it.ShortSince = &t
		}
	}
}

// runReportAt is the time a run's report measures ages from: when the run
// finished, else at (a run still going).
func runReportAt(r runRecord, at time.Time) time.Time {
	if !r.FinishedAt.IsZero() {
		return r.FinishedAt
	}
	return at
}

// streakAge is how long a streak that began at since has gone on at at, never
// negative (a start a little ahead of the clock reads as new).
func streakAge(since *time.Time, at time.Time) time.Duration {
	if since == nil || since.After(at) {
		return 0
	}
	return at.Sub(*since)
}

// ageText is d for a person: whole days from a day up, else whole hours.
func ageText(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

// deferredBacklogLines names each read board that deferred new rows past its
// detail cap and how long its backlog has been open, at most maxBacklogLines of
// them. Not a failure: deferred rows are fetched on later runs and, not being
// stored, age nothing. The age is an upper bound on the oldest deferred row
// (DeferredSince); a board without one (the first run that deferred, or no
// health file) says the backlog is new.
func deferredBacklogLines(b *boardsReport, at time.Time) []string {
	if b == nil || b.Deferred <= 0 {
		return nil
	}
	var lines []string
	more := 0
	for _, it := range b.Items {
		if it.Deferred <= 0 {
			continue
		}
		if len(lines) == maxBacklogLines {
			more++
			continue
		}
		since := "backlog opened this run"
		if it.DeferredSince != nil {
			since = fmt.Sprintf("backlog open since %s (oldest deferred row is at most %s old)",
				it.DeferredSince.UTC().Format("2006-01-02"), ageText(streakAge(it.DeferredSince, at)))
		}
		lines = append(lines, fmt.Sprintf("Deferred: %s: %d new row(s) past the detail cap, not stored yet; %s", boardName(it), it.Deferred, since))
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("Deferred: %d more board(s) with a backlog", more))
	}
	return lines
}

// boardAttentionLine is the attention line for boards with an ongoing problem
// that is not a failure: boards with no good read for over boardStaleAfter
// (when withStale; the digest lists them in a section of their own), boards
// skipped this run, and boards whose listing has been short for over
// boardStaleAfter: what they leave out is held for the mass hold from each
// posting's own first miss, then dies (Summary.ShortDead). "" when there are none. Names
// are bounded (maxAttentionNames per kind); the counts are exact.
func boardAttentionLine(b *boardsReport, at time.Time, withStale bool) string {
	if b == nil {
		return ""
	}
	var parts []string
	if withStale && b.StaleCount > 0 {
		var names []string
		for _, s := range b.Stale {
			names = append(names, boardNameOf(s.ATS, s.Token, s.Company)+" "+ageText(max(at.Sub(s.Since), 0)))
		}
		parts = append(parts, kindPart(b.StaleCount, "stale (no good read for over "+boardStaleAfter.String()+")", names))
	}
	var skipped, long []string
	for _, it := range b.Items {
		switch {
		case it.Outcome == boardSkipped:
			skipped = append(skipped, boardName(it))
		case it.Outcome == boardRead && it.Short > 0 && streakAge(it.ShortSince, at) > boardStaleAfter:
			long = append(long, boardName(it)+" "+ageText(streakAge(it.ShortSince, at)))
		}
	}
	// The record keeps a bounded number of boards; the totals are exact.
	if n := max(b.Skipped, len(skipped)); n > 0 {
		parts = append(parts, kindPart(n, "skipped this run", skipped))
	}
	if len(long) > 0 {
		parts = append(parts, kindPart(len(long), "listing short for over "+boardStaleAfter.String()+", so the postings they leave out are held "+ageText(b.massHold())+" from their first miss and then die", long))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Boards not being read (attention, not a failure): " + strings.Join(parts, "; ")
}

// massHold is the mass hold the run was configured with (MassHoldHours), or the
// default for a record that predates the field.
func (b *boardsReport) massHold() time.Duration {
	if b != nil && b.MassHoldHours > 0 {
		return time.Duration(b.MassHoldHours) * time.Hour
	}
	return pipeline.DefaultMassHold
}

// kindPart is "N kind (a, b, ... and M more)" for one kind of board.
func kindPart(n int, kind string, names []string) string {
	s := fmt.Sprintf("%d %s", n, kind)
	if len(names) == 0 {
		return s
	}
	shown := names
	if len(shown) > maxAttentionNames {
		shown = shown[:maxAttentionNames]
	}
	s += ": " + strings.Join(shown, ", ")
	if rest := n - len(shown); rest > 0 {
		s += fmt.Sprintf(" and %d more", rest)
	}
	return s
}

// boardNameOf is "ats:token (Company)", boardName's form for the parts a stale
// board carries.
func boardNameOf(ats, token, company string) string {
	return boardName(boardReport{ATS: ats, Token: token, Company: company})
}
