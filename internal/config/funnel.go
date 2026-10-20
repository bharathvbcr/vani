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
	DefaultMinDescriptionRunes = 20
	// MaxMinDescriptionRunes bounds the setting.
	MaxMinDescriptionRunes = 5000
	// DefaultNotesMinRank is the relevance floor for a "why this role" note.
	// Relevance is the rank annotation: DevPrism must-have coverage (0-100)
	// plus the preferred-employer boost. 40 means a posting whose must-haves
	// are at least 40% covered gets a note; weaker ones are listed without
	// one. Conservative on purpose: notes were one paid call per scored
	// posting, and a note on a weak match is the least useful one. Lower it
	// to widen notes, set 0.01 for nearly all (zero rank is "unknown" and is
	// always deferred, never noted).
	DefaultNotesMinRank = 40.0
	// DefaultNotesDailyUSD is the notes' own daily cap. It is separate from
	// decide.daily_usd.openrouter so notes can never starve triage; when it is
	// spent, notes wait for the next UTC day and triage is unaffected.
	DefaultNotesDailyUSD = 0.10
)

// Funnel tunes the triage funnel's spend. Every field is optional; the zero
// value means the documented default.
type Funnel struct {
	// MinDescriptionRunes: postings whose description has fewer non-space
	// characters than this skip the paid triage call and are kept as unsure.
	// Zero means DefaultMinDescriptionRunes.
	MinDescriptionRunes int `json:"min_description_runes,omitempty"`
	// NotesMinRank is the lowest rank (0-100, plus the employer boost) that
	// gets a note. nil means DefaultNotesMinRank; an explicit 0 notes every
	// posting whose relevance is known.
	NotesMinRank *float64 `json:"notes_min_rank,omitempty"`
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

// NotesFloor is NotesMinRank with unset read as the default.
func (f Funnel) NotesFloor() float64 {
	if f.NotesMinRank == nil {
		return DefaultNotesMinRank
	}
	return *f.NotesMinRank
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
		add("funnel.notes_min_rank %v must be within 0..200", v)
	}
	if v := f.NotesCapUSD(); math.IsNaN(v) || v <= 0 || v > MaxDailyUSD {
		add("funnel.notes_daily_usd %v must be above 0 and at most %v", v, MaxDailyUSD)
	}
}
