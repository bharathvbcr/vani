package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// AnnNotesDeferred marks a scored posting whose note was held back, with why
// (unknown relevance, or the notes budget was spent). It is not "explained":
// the posting stays Scored and a later run asks again.
const AnnNotesDeferred = "notes.deferred"

// AnnNotesSkipped marks a posting explained without a note because its
// relevance was below the floor. The reason is the annotation's value.
const AnnNotesSkipped = "notes.skipped"

// Deferral reasons carried by AnnNotesDeferred.
const (
	DeferUnknownRelevance = "relevance unknown"
	DeferBudget           = "notes budget spent"
)

// NotesDeferred reports whether r is a scored posting whose note is waiting.
func NotesDeferred(r ledger.Record) bool {
	return r.Status == posting.Scored && r.Annotations[AnnNotesDeferred] != ""
}

// NotesCounts is what the gate did this run.
type NotesCounts struct {
	// Below is postings explained without a note: relevance under the floor.
	Below int `json:"below_floor,omitempty"`
	// UnknownRelevance is postings whose note was deferred: no usable rank.
	UnknownRelevance int `json:"unknown_relevance,omitempty"`
	// BudgetSpent is postings whose note was deferred: the notes budget was spent.
	BudgetSpent int `json:"budget_spent,omitempty"`
}

// NotesGate decides, before the paid notes call, whether a scored posting gets
// a note. Notes run only for relevance at or above MinRelevance, and draw on
// their own daily budget so they cannot starve triage.
type NotesGate struct {
	// MinRelevance is the relevance floor, 0-100.
	MinRelevance float64
	// Relevance reads the age-free relevance the Jobs ranking uses
	// (jobinsight.Relevance); ok false means it cannot be computed. nil reads
	// every posting as unknown, so a gate built without one defers rather
	// than guesses. DevPrism coverage is not read here: a coverage of 0 is
	// only an input to relevance, never "unknown".
	Relevance func(ledger.Record) (v float64, ok bool)
	// Spend is the notes' own daily cap, checked before each call. nil means
	// the notes backend meters itself.
	Spend decide.Spend
	// SpendKey is the key Spend is asked about.
	SpendKey string

	mu        sync.Mutex
	c         NotesCounts
	exhausted bool
}

// Counts is what the gate did so far.
func (g *NotesGate) Counts() NotesCounts {
	if g == nil {
		return NotesCounts{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.c
}

type notesVerdict int

const (
	notesRun   notesVerdict = iota // ask for the note
	notesBelow                     // explained without a note
	notesDefer                     // stays scored, asked again later
)

// relevance reads r's relevance through the gate's reader; a missing reader or
// a non-finite value reads as unknown.
func (g *NotesGate) relevance(r ledger.Record) (float64, bool) {
	if g.Relevance == nil {
		return 0, false
	}
	v, ok := g.Relevance(r)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// check returns the verdict for r and the reason to record. An error means the
// budget could not be read: the note is not asked for and the stage fails.
func (g *NotesGate) check(ctx context.Context, r ledger.Record) (notesVerdict, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rel, known := g.relevance(r)
	switch {
	case !known:
		g.c.UnknownRelevance++
		return notesDefer, DeferUnknownRelevance, nil
	case rel < g.MinRelevance:
		g.c.Below++
		return notesBelow, fmt.Sprintf("relevance %.1f below notes floor %.1f", rel, g.MinRelevance), nil
	case g.exhausted:
		g.c.BudgetSpent++
		return notesDefer, DeferBudget, nil
	case g.Spend == nil:
		return notesRun, "", nil
	}
	err := g.Spend.Allow(ctx, g.SpendKey)
	switch {
	case err == nil:
		return notesRun, "", nil
	case errors.Is(err, decide.ErrBudget):
		g.exhausted = true
		g.c.BudgetSpent++
		return notesDefer, DeferBudget, nil
	}
	return notesRun, "", fmt.Errorf("notes budget: %w", err)
}

// gateNotes runs the gate for a scored record. handled is true when the
// posting needs no notes call this time: it was explained without a note
// (below the floor) or its note was deferred.
func (r *Runner) gateNotes(ctx context.Context, rec ledger.Record) (handled bool, err error) {
	if r.Notes == nil {
		return false, nil
	}
	v, reason, err := r.Notes.check(ctx, rec)
	if err != nil {
		return false, err
	}
	switch v {
	case notesBelow:
		return true, r.emit(ctx, rec.Key, posting.Explained, "notes skipped: "+reason,
			map[string]string{AnnNotesSkipped: reason, AnnNotesDeferred: ""})
	case notesDefer:
		if rec.Annotations[AnnNotesDeferred] == reason {
			return true, nil // already recorded; do not grow the ledger every run
		}
		return true, r.emit(ctx, rec.Key, posting.Scored, "notes deferred: "+reason, map[string]string{AnnNotesDeferred: reason})
	}
	return false, nil
}
