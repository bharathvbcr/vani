// Package jobinsight derives display-only facts about a retrieved posting for
// `vani jobs`: how fresh it is, a relevance and a priority score to sort and
// plot by, and the skill keywords its text mentions. Everything here is
// deterministic and local: no model, no network, and nothing is written back
// to the ledger. No engine rule reads these values (Rule 7: they only help a
// person browse; dropping a posting stays an engine rule on triage readings).
package jobinsight

import (
	"math"
	"time"
)

// FreshSource says which date Fresh used.
type FreshSource string

const (
	// FromPosted is the board's own posted date.
	FromPosted FreshSource = "posted"
	// FromSeen is when Vani first saw the posting: an upper bound on its age
	// for boards that give no posted date (or a date that cannot be right).
	FromSeen FreshSource = "seen"
)

// clockSkew is how far in the future a board's posted date may be before it is
// treated as wrong (time zones, a board stamping the end of the day).
const clockSkew = 36 * time.Hour

// earliest is the oldest posted date believed; anything earlier is a
// placeholder (Go's zero time, Unix 0, 1900-01-01).
var earliest = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Fresh returns the date a posting's age is measured from. The board's posted
// date wins when it is plausible: after 2000, not later than firstSeen (plus
// clock skew) and not in the future. Otherwise the first-seen date is used,
// and with neither the zero time and FromSeen.
func Fresh(posted, firstSeen, now time.Time) (time.Time, FreshSource) {
	limit := now
	if !firstSeen.IsZero() && firstSeen.Before(limit) {
		limit = firstSeen
	}
	if !posted.IsZero() && posted.After(earliest) && !posted.After(limit.Add(clockSkew)) {
		return posted, FromPosted
	}
	return firstSeen, FromSeen
}

// FreshAge is the one rule for a posting's freshness date and whether its age
// is known. boardFirstCrawl is the earliest first-seen among the posting's
// board's records: the pipeline stamps every posting a board lists in one
// Reconcile call with the same instant, so a posting first seen at exactly that
// instant was listed in the board's first crawl and may be arbitrarily old.
//
//   - A plausible posted date (see Fresh) is known, and is never later than
//     firstSeen: a posting cannot look fresher than when Vani saw it.
//   - Otherwise a firstSeen after the board's first crawl is known: the posting
//     was put up at most one run before Vani saw it.
//   - Otherwise (listed in the first crawl, undated, or nothing at all) the age
//     is unknown; at is firstSeen, which may be the zero time.
func FreshAge(posted, firstSeen, boardFirstCrawl, now time.Time) (at time.Time, src FreshSource, known bool) {
	if p, s := Fresh(posted, firstSeen, now); s == FromPosted {
		if !firstSeen.IsZero() && p.After(firstSeen) {
			p = firstSeen
		}
		return p, FromPosted, true
	}
	if !firstSeen.IsZero() && !firstSeen.Equal(boardFirstCrawl) {
		return firstSeen, FromSeen, true
	}
	return firstSeen, FromSeen, false
}

// BoardOf is the board part of a posting key: everything before its last
// component ("gh:acme" for "gh:acme:123", "wd:tenant:site" for a Workday key).
func BoardOf(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i]
		}
	}
	return key
}

// AgeDays is whole days from at to now, never negative. A zero at gives -1:
// the age is unknown.
func AgeDays(at, now time.Time) int {
	if at.IsZero() {
		return -1
	}
	d := now.Sub(at)
	if d < 0 {
		return 0
	}
	return int(d / (24 * time.Hour))
}

// Basis says what a relevance score rests on.
type Basis string

const (
	// BasisRank is DevPrism's must-have coverage (the ledger's rank).
	BasisRank Basis = "rank"
	// BasisFit is triage's role_fit reading.
	BasisFit Basis = "fit"
	// BasisNone means neither: only keyword overlap, at a neutral base.
	BasisNone Basis = "none"
)

// fitBase maps the jdtriage fixture's role_fit options to a base relevance.
// The order matches cmd/vani's roleFitOrder; the spacing is a display choice.
var fitBase = map[string]float64{
	"strong_fit":         0.85,
	"partial_fit":        0.65,
	"adjacent":           0.45,
	"mismatch_seniority": 0.20,
	"mismatch_domain":    0.10,
}

// MaxHalfLifeDays bounds the half-life a caller may ask for.
const MaxHalfLifeDays = 365.0

func clamp01(v float64) float64 { return clamp(v, 0, 1) }

func clamp(v, lo, hi float64) float64 {
	switch {
	case math.IsNaN(v) || v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}

// round1 rounds to one decimal, half up. The nudge absorbs binary
// representation error (0.75*0.85*100 is 63.7499...), far below display
// precision.
func round1(v float64) float64 { return math.Round(v*10+1e-9) / 10 }
