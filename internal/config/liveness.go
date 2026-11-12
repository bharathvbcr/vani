package config

import (
	"math"
	"time"
)

// Defaults for Liveness, the mass-absence rule of internal/pipeline
// (liveness.go, which holds the reasoning and the measurement behind them).
// They are the owner's settings and mirror the pipeline's own defaults;
// cmd/vani's TestLivenessDefaultsAgree pins the two sets equal, since this
// package cannot import pipeline.
const (
	DefaultMassShare     = 0.25
	DefaultMassMinOpen   = 10
	DefaultMassHoldHours = 14 * 24
	// MinMassHoldHours is the ordinary absence hold (pipeline.OrdinaryHold): a
	// mass hold shorter than it would let a mass mark shorten a posting's life.
	MinMassHoldHours = 20
	// MaxMassHoldHours bounds the setting at a year (pipeline.MaxMassHold).
	MaxMassHoldHours = 365 * 24
	// MaxMassMinOpen bounds the floor: past it no real board is ever mass.
	MaxMassMinOpen = 100_000
)

// Liveness tunes when a posting a board stops listing is declared dead. Every
// field is optional; unset means the documented default. A setting outside its
// range fails `vani config check` and the run, it is never silently replaced.
type Liveness struct {
	// MassShare is the fraction of a board's open postings (0 to 1, above 0)
	// that one full listing may leave absent before the absence counts as a
	// mass absence (an outage, a moved board, a renumbering) rather than roles
	// closing. nil means DefaultMassShare.
	MassShare *float64 `json:"mass_share,omitempty"`
	// MassMinOpen is how many open postings a board needs before the share
	// applies; a smaller board leans on the ordinary rule. 0 means
	// DefaultMassMinOpen.
	MassMinOpen int `json:"mass_min_open,omitempty"`
	// MassHoldHours is how long a posting a mass absence left absent is kept,
	// from its own first miss, before it can die. 0 means DefaultMassHoldHours;
	// at least MinMassHoldHours, at most MaxMassHoldHours.
	MassHoldHours int `json:"mass_hold_hours,omitempty"`
}

// Share is MassShare with unset read as the default.
func (l Liveness) Share() float64 {
	if l.MassShare == nil {
		return DefaultMassShare
	}
	return *l.MassShare
}

// MinOpen is MassMinOpen with zero read as the default.
func (l Liveness) MinOpen() int {
	if l.MassMinOpen == 0 {
		return DefaultMassMinOpen
	}
	return l.MassMinOpen
}

// Hold is MassHoldHours with zero read as the default.
func (l Liveness) Hold() time.Duration {
	if l.MassHoldHours == 0 {
		return DefaultMassHoldHours * time.Hour
	}
	return time.Duration(l.MassHoldHours) * time.Hour
}

func (l Liveness) validate(add func(string, ...any)) {
	if l.MassShare != nil {
		if s := *l.MassShare; math.IsNaN(s) || s <= 0 || s > 1 {
			add("liveness.mass_share %v must be above 0 and at most 1", s)
		}
	}
	if l.MassMinOpen < 0 || l.MassMinOpen > MaxMassMinOpen {
		add("liveness.mass_min_open %d must be within 1..%d (0 is the default)", l.MassMinOpen, MaxMassMinOpen)
	}
	if l.MassHoldHours != 0 && (l.MassHoldHours < MinMassHoldHours || l.MassHoldHours > MaxMassHoldHours) {
		add("liveness.mass_hold_hours %d must be within %d..%d (0 is the default %d)",
			l.MassHoldHours, MinMassHoldHours, MaxMassHoldHours, DefaultMassHoldHours)
	}
}
