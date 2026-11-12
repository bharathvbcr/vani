package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// When a posting dies. Reconcile is the one owner of this rule; board health
// (cmd/vani) only reports and never changes what it does.
//
//   - A posting absent from a full listing of its board misses a run. The first
//     miss of an absence episode is stamped (MissedSinceKey). Reappearing ends
//     the episode and clears every miss mark.
//   - An ordinary absence kills after MissesToDead misses and OrdinaryHold since
//     the episode's first miss.
//   - A full listing is a mass absence when it leaves at least MassShare of the
//     board's open postings absent (distinct keys, counted before the listing)
//     with at least MassMinOpen open. An empty answer is one too on a board of
//     any size from EmptyListingMinOpen open (a named extension of the owner's
//     rule: a small board that shows none of its postings is the classic wrong
//     tenant or broken read, and keeping is the safe error). Every posting such
//     a listing leaves absent is mass marked (MassMissedSinceKey). The mark only
//     upgrades: a later ordinary listing never clears it; only the posting
//     reappearing does.
//   - A mass-marked posting dies only after MissesToDead misses and MassHold
//     since its own first miss.
//   - A partial listing ages nothing. A full listing taken while the clock reads
//     behind the ledger's own history ages nothing either (Summary.ClockBehind):
//     a clock behind would date a first miss in the past and kill early. That
//     errs toward keeping, and is reported.
//   - A stamp more than MaxStampAhead past now is unreadable (a clock that ran
//     ahead) and restarts the episode at now.
//
// The rule keeps its state in pipeline-owned annotations beside missed_runs, not
// in ledger fields.
const (
	// MissedSinceKey stamps the first miss of the current absence episode
	// (RFC 3339, UTC). Read only while missed_runs > 0. A record that has
	// missed runs but no stamp (the ledger before this rule) restarts its
	// episode at the first listing that misses it.
	MissedSinceKey = "missed_since"
	// MassMissedSinceKey marks a posting a mass absence left absent in the
	// current episode, with when it was first so marked. Read only while
	// missed_runs > 0.
	MassMissedSinceKey = "mass_missed_since"
	// OrdinaryHold is how long an ordinary absence lasts, from its first miss,
	// before it can kill: a daily run kills on its second miss, two runs an hour
	// apart cannot.
	OrdinaryHold = 20 * time.Hour
	// MaxStampAhead is how far past now a stamp may sit and still be read, and
	// how far the clock may sit behind the ledger's newest event before a
	// listing is refused for aging.
	MaxStampAhead = time.Hour
	// EmptyListingMinOpen is the fewest open postings from which a listing that
	// shows none of them is a mass absence whatever the floor.
	EmptyListingMinOpen = 2
	// ClockPollutionSkew and ClockPollutionSpan tell a clock that ran AHEAD for
	// one run from a clock now BEHIND. Events in the ledger's future are read as
	// the first only when the newest is at least ClockPollutionSkew past now and
	// all of them fall within ClockPollutionSpan of each other (one run's burst);
	// a clock set back leaves the whole history since the jump in the future,
	// which spans days. Anything else is a clock behind, and a behind clock ages
	// nothing: a clock set back less than ClockPollutionSkew, or events ahead
	// over a longer span, freeze aging only until real time passes them.
	ClockPollutionSkew = 7 * 24 * time.Hour
	ClockPollutionSpan = 6 * time.Hour
	// MaxMassHold bounds the mass hold setting.
	MaxMassHold = 365 * 24 * time.Hour
)

// Defaults for Liveness; config.Liveness holds the owner's settings with the
// same defaults, which cmd/vani's TestLivenessDefaultsAgree pins equal.
const (
	// DefaultMassShare: measured 2026-11-12 (docs/RUNTIME.md "Mass absence") over
	// the 30 stored run records (11,317 board reads), with each read's open count
	// taken from the ledger's own status history (closed = dead, duplicate or
	// dropped). 1,484 reads with at least 10 open left something absent: 71 left
	// 10% or more, 32 left 15%, 20 left 20% (the highest ordinary ones, 8 of 35
	// and 7 of 33, are 23% and 21%), 10 left 25% or more, 6 left 30% or more.
	// The 10 are six boards' events over the 7 days: Snapchat's empty answer,
	// Cursor, Luma AI, Mirage, Shield AI and Sentry. 0.25 sits just above the
	// ordinary tail. Inferred, not verified: that those six were outages rather
	// than real closures; the ledger cannot say.
	DefaultMassShare = 0.25
	// DefaultMassMinOpen: below 10 open postings one or two closed roles are
	// already 10-20%, so a small board leans on the ordinary rule (an empty
	// answer is still mass by EmptyListingMinOpen).
	DefaultMassMinOpen = 10
	// DefaultMassHold is the owner's choice (2026-11-12): a two-week outage or
	// migration loses nothing; a genuine mass closure lingers, named, two weeks.
	DefaultMassHold = 14 * 24 * time.Hour
)

// Liveness tunes the mass-absence rule. A zero field means its default.
type Liveness struct {
	MassShare   float64
	MassMinOpen int
	MassHold    time.Duration
}

// DefaultLiveness returns the documented defaults.
func DefaultLiveness() Liveness {
	return Liveness{MassShare: DefaultMassShare, MassMinOpen: DefaultMassMinOpen, MassHold: DefaultMassHold}
}

// Validate rejects a setting that would weaken the rule or make no sense: a
// share outside (0,1], a floor under 1, a hold shorter than the ordinary hold
// (a mass mark would then shorten a posting's life) or past MaxMassHold. A zero
// field is the default and passes.
func (lv Liveness) Validate() error {
	var errs []error
	if lv.MassShare != 0 && !(lv.MassShare > 0 && lv.MassShare <= 1) { // also rejects NaN
		errs = append(errs, fmt.Errorf("mass share %v must be within (0, 1]", lv.MassShare))
	}
	if lv.MassMinOpen < 0 || lv.MassMinOpen > math.MaxInt32 {
		errs = append(errs, fmt.Errorf("mass floor %d must be at least 1", lv.MassMinOpen))
	}
	if lv.MassHold != 0 && (lv.MassHold < OrdinaryHold || lv.MassHold > MaxMassHold) {
		errs = append(errs, fmt.Errorf("mass hold %s must be within %s..%s", lv.MassHold, OrdinaryHold, MaxMassHold))
	}
	return errors.Join(errs...)
}

// resolved fills the zero fields with the defaults. Validate has already
// refused the rest.
func (lv Liveness) resolved() Liveness {
	d := DefaultLiveness()
	if lv.MassShare != 0 {
		d.MassShare = lv.MassShare
	}
	if lv.MassMinOpen != 0 {
		d.MassMinOpen = lv.MassMinOpen
	}
	if lv.MassHold != 0 {
		d.MassHold = lv.MassHold
	}
	return d
}

// SetLiveness sets the mass-absence settings; unset fields keep the defaults.
// A setting Validate refuses is an error and leaves the Reconciler as it was.
func (r *Reconciler) SetLiveness(lv Liveness) error {
	if err := lv.Validate(); err != nil {
		return fmt.Errorf("liveness: %w", err)
	}
	r.live = lv
	return nil
}

// Liveness returns the settings the Reconciler ages with, defaults filled in.
func (r *Reconciler) Liveness() Liveness { return r.live.resolved() }

// massAbsence reports whether absent of open is a mass absence under lv.
func massAbsence(lv Liveness, open, absent int) bool {
	if open >= EmptyListingMinOpen && absent == open {
		return true
	}
	return open >= lv.MassMinOpen && float64(absent) >= lv.MassShare*float64(open)-1e-9
}

// clearMissMarks adds to ann the forgetting of every miss mark: a listing that
// shows the posting ends its absence episode. ann may be nil.
func clearMissMarks(ann map[string]string) map[string]string {
	if ann == nil {
		ann = map[string]string{}
	}
	ann[MissedSinceKey] = ""
	ann[MassMissedSinceKey] = ""
	return ann
}

// readStamp parses a stamp annotation; ok is false when it is missing,
// malformed or more than MaxStampAhead past now.
func readStamp(r ledger.Record, key string, now time.Time) (time.Time, bool) {
	v := r.Annotations[key]
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.After(now.Add(MaxStampAhead)) {
		return time.Time{}, false
	}
	return t, true
}

func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// clockBehind reports whether now reads behind the ledger's history (events
// sorted, as the index holds them): some event lies more than MaxStampAhead in
// the future and they are not the single burst of a run taken under a clock
// that ran ahead (ClockPollutionSkew, ClockPollutionSpan) with history before it
// that agrees with now.
func clockBehind(events []time.Time, now time.Time) bool {
	cut := now.Add(MaxStampAhead)
	i := sort.Search(len(events), func(i int) bool { return events[i].After(cut) })
	if i == len(events) {
		return false
	}
	if i == 0 {
		return true // the whole history is ahead of now: no witness that the clock is right
	}
	oldest, newest := events[i], events[len(events)-1]
	return !(newest.Sub(now) >= ClockPollutionSkew && newest.Sub(oldest) <= ClockPollutionSpan)
}

// absence is what one full listing's absence pass did.
type absence struct {
	open, absent       int
	missed, held, dead int
	massMarked         int // postings this listing newly marked
	mass, clockBehind  bool
}

// ageAbsent is the absence pass of a full listing: open is the board's open
// keys before the listing, seen the keys it listed, behind whether the clock
// reads behind the ledger. It writes the miss, hold and death events in one
// batched append; its counts say what was written, so a pass that failed
// reports none of them.
func ageAbsent(ctx context.Context, l ledger.Ledger, lv Liveness, open []posting.Key, seen map[posting.Key]bool, behind bool, now time.Time) (absence, error) {
	lv = lv.resolved()
	var a absence
	a.open = len(open)
	var absent []posting.Key
	for _, k := range open {
		if !seen[k] {
			absent = append(absent, k)
		}
	}
	a.absent = len(absent)
	if len(absent) == 0 {
		return a, nil
	}
	if behind {
		a.clockBehind = true
		return a, nil
	}
	mass := massAbsence(lv, len(open), len(absent))

	var w absence
	evs := make([]ledger.Event, 0, len(absent))
	for _, k := range absent {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		cur, ok := l.Get(k)
		if !ok || closed(cur.Status) {
			continue
		}
		e, outcome, newMark := absenceEvent(lv, cur, mass, now)
		switch outcome {
		case absentDead:
			w.dead++
		case absentHeld:
			w.held++
		default:
			w.missed++
		}
		if newMark {
			w.massMarked++
		}
		evs = append(evs, e)
	}
	if _, err := appendBatched(ctx, l, evs); err != nil {
		return a, err
	}
	a.missed, a.held, a.dead, a.massMarked, a.mass = w.missed, w.held, w.dead, w.massMarked, mass
	return a, nil
}

type absentOutcome int

const (
	absentMissed absentOutcome = iota // below MissesToDead
	absentHeld                        // enough misses, hold not yet over
	absentDead
)

// absenceEvent is the event one more full-listing absence writes for cur, and
// whether it newly mass-marks the posting.
func absenceEvent(lv Liveness, cur ledger.Record, massListing bool, now time.Time) (ledger.Event, absentOutcome, bool) {
	misses := cur.MissedRuns + 1
	ongoing := cur.MissedRuns > 0
	first, ok := readStamp(cur, MissedSinceKey, now)
	if !ongoing || !ok {
		first = now // a new episode, or an unreadable stamp: restart (later, never earlier)
	}
	// The mass mark persists through the episode; any value counts, so a
	// mangled mark still holds. A new episode starts unmarked.
	massSince, newMark := "", false
	if ongoing && cur.Annotations[MassMissedSinceKey] != "" {
		massSince = cur.Annotations[MassMissedSinceKey]
		if _, ok := readStamp(cur, MassMissedSinceKey, now); !ok {
			massSince = stampOf(now)
		}
	}
	if massListing && massSince == "" {
		massSince, newMark = stampOf(now), true
	}
	hold := OrdinaryHold
	if massSince != "" {
		hold = lv.MassHold
	}
	ann := map[string]string{
		ledger.MissedRunsKey: strconv.Itoa(misses),
		MissedSinceKey:       stampOf(first),
		MassMissedSinceKey:   massSince, // "" forgets a stale mark of an earlier episode
	}
	e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: cur.Key, Status: cur.Status, Annotations: ann,
		Reason: fmt.Sprintf("absent from board (%d)", misses)}
	if massSince != "" {
		e.Reason = fmt.Sprintf("absent from board (%d), mass absence", misses)
	}
	switch {
	case misses >= MissesToDead && !now.Before(first.Add(hold)):
		e.Status = posting.Dead
		ann[DiedFromKey] = string(cur.Status)
		if massSince != "" {
			e.Reason = fmt.Sprintf("absent from board %d times over %s, mass absence", misses, hold)
		} else {
			e.Reason = fmt.Sprintf("absent from board %d times over %s", misses, OrdinaryHold)
		}
		return e, absentDead, newMark
	case misses >= MissesToDead:
		e.Reason += fmt.Sprintf(", held until %s", first.Add(hold).UTC().Format(time.RFC3339))
		return e, absentHeld, newMark
	}
	return e, absentMissed, newMark
}
