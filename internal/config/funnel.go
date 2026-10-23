package config

import (
	"math"
)

// Defaults for Funnel. Each is chosen to spend less, never to hide a posting:
// a posting below a floor is kept and listed, it only goes without a paid call.
const (
	// DefaultMinDescriptionRunes: a description under this many non-space
	// characters is too thin for a model to read (a title restated, a "see
	// careers page" stub). The paid triage call is skipped and the posting is
	// kept as unsure, never dropped.
	DefaultMinDescriptionRunes = 40
	// MaxMinDescriptionRunes bounds the setting.
	MaxMinDescriptionRunes = 5000
	// DefaultNotesMinRelevance is the relevance floor for a "why this role" note.
	// Relevance is jobinsight Result.Relevance, the age-free 0-100 number the
	// Jobs views and the app show (not DevPrism coverage, which is exactly 0
	// on about 22% of postings). Measured 2026-10-20 over the 2267 digested
	// postings of the live ledger with the integrated ranking (confidence-
	// weighted parts, one mismatch penalty): median 16.7, p75 29.3, p80 34.4,
	// p90 47.9; the floor 32 (about p78) admits about 22% of them. (Under the
	// pre-integration scoring the same share sat at 20.) Below it a posting is
	// listed without a note; the digest's top is well above it.
	DefaultNotesMinRelevance = 32.0
	// DefaultNotesDailyUSD is the notes' own daily cap, separate from
	// decide.daily_usd.openrouter so notes can never starve triage; spent, notes
	// wait for the next UTC day. Measured 2026-10-20 from the live ledger and
	// budget/jev.json: all OpenRouter calls cost about $0.00011 each (10-19:
	// $0.495 over 4654 triage+notes calls; 10-20: $0.161 over 1342); the cost
	// is recorded per day, not per call, so notes alone are unmeasured. The
	// peak day wrote 1864 notes ungated; gated at the floor that is about 400,
	// about $0.05 at the measured rate and $0.09 at twice it. 0.15 leaves
	// headroom over both.
	DefaultNotesDailyUSD = 0.15
)

// Funnel tunes the triage funnel's spend. Every field is optional; the zero
// value means the documented default.
type Funnel struct {
	// MinDescriptionRunes: postings whose description has fewer non-space
	// characters than this skip the paid triage call and are kept as unsure.
	// Zero means DefaultMinDescriptionRunes.
	MinDescriptionRunes int `json:"min_description_runes,omitempty"`
	// NotesMinRelevance is the lowest relevance (0-100) that gets a note. nil
	// means DefaultNotesMinRelevance; an explicit 0 notes every posting whose
	// relevance is known.
	NotesMinRelevance *float64 `json:"notes_min_relevance,omitempty"`
	// NotesDailyUSD is the notes' daily OpenRouter cap, drawn separately from
	// decide.daily_usd.openrouter. nil means DefaultNotesDailyUSD.
	NotesDailyUSD *float64 `json:"notes_daily_usd,omitempty"`
	// ScoreAfterTriage puts DevPrism back after triage. By default DevPrism
	// runs before triage, so coverage is known when a posting is triaged;
	// coverage is never a gate either way.
	ScoreAfterTriage bool `json:"score_after_triage,omitempty"`
}

// DescriptionMin is MinDescriptionRunes with zero read as the default.
func (f Funnel) DescriptionMin() int {
	if f.MinDescriptionRunes == 0 {
		return DefaultMinDescriptionRunes
	}
	return f.MinDescriptionRunes
}

// NotesFloor is NotesMinRelevance with unset read as the default.
func (f Funnel) NotesFloor() float64 {
	if f.NotesMinRelevance == nil {
		return DefaultNotesMinRelevance
	}
	return *f.NotesMinRelevance
}

// NotesCapUSD is NotesDailyUSD with unset read as the default.
func (f Funnel) NotesCapUSD() float64 {
	if f.NotesDailyUSD == nil {
		return DefaultNotesDailyUSD
	}
	return *f.NotesDailyUSD
}

func (f Funnel) validate(add func(string, ...any)) {
	if f.MinDescriptionRunes < 0 || f.MinDescriptionRunes > MaxMinDescriptionRunes {
		add("funnel.min_description_runes %d must be within 0..%d (0 means %d)", f.MinDescriptionRunes, MaxMinDescriptionRunes, DefaultMinDescriptionRunes)
	}
	if v := f.NotesFloor(); math.IsNaN(v) || v < 0 || v > 200 {
		add("funnel.notes_min_relevance %v must be within 0..200", v)
	}
	if v := f.NotesCapUSD(); math.IsNaN(v) || v <= 0 || v > MaxDailyUSD {
		add("funnel.notes_daily_usd %v must be above 0 and at most %v", v, MaxDailyUSD)
	}
}
