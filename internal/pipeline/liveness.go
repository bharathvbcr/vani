package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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
//     any size from EmptyListingMinOpen open, and on a board under MassMinOpen
//     open so is a non-empty listing that leaves out more than half of the open
//     postings and at least SmallBoardMinAbsent of them (a named extension of the owner's
//     rule: a small board that shows none of its postings is the classic wrong
//     tenant or broken read, and keeping is the safe error). Every posting such
//     a listing leaves absent is mass marked (MassMissedSinceKey). The mark only
//     upgrades: a later ordinary listing never clears it; only the posting
//     reappearing does.
//   - A mass-marked posting dies only after MissesToDead misses and MassHold
//     since its own first miss.
//   - A partial listing ages nothing.
//
// The clock. The rule measures holds with the clock of each read, so a clock
// that is wrong in a way the ledger cannot see could end a hold early. Three
// guards, each of which only ever keeps a posting longer:
//
//   - Events ahead (clockEvidence). A full listing ages nothing
//     (Summary.ClockBehind) while the ledger holds events stamped between
//     MaxStampAhead and ClockWindow past now on one of the board's own records,
//     or on at least ClockQuorum records anywhere. Stamping a first miss under
//     a clock set back would date it before the posting's own history. The
//     freeze is bounded: events further than ClockWindow ahead are
//     not read as a clock behind at all (a clock that ran ahead wrote them, or
//     the clock is set back by more than ClockWindow, which the next guard
//     handles), so a freeze lasts from when real time comes within ClockWindow
//     of the events until it passes them (ClockWindow plus the span of the runs
//     that wrote them), and names the newest event it saw (Summary.ClockAhead).
//     One or two stray records on another board do not freeze this one.
//     A second, wider freeze covers a clock set back by days: at least
//     ClockQuorum of the board's OWN records with events more than ClockWindow
//     and at most SetBackCap ahead freeze it the same way (a run's burst of
//     misses is what a set-back leaves ahead). Without it aging goes on under
//     the set-back clock, stamps first misses early, and ends a mass hold early
//     by the set-back once the clock is right. One or two own records, records
//     on other boards and events past SetBackCap freeze nothing, and the freeze
//     ends when real time passes the newest event; the cost is that a run under
//     a clock that ran ahead by up to SetBackCap, which touched that many of a
//     board's postings, freezes the board until real time catches up.
//   - Unreadable stamps. A stamp more than MaxStampAhead past now, or before
//     more than MaxStampAhead before the posting was first seen (a clock set
//     back past its history), is
//     unreadable and restarts the episode at now: later, never earlier. A
//     first sighting more than MaxStampAhead past now is not compared (it was
//     taken under a clock that ran ahead and would restart every read).
//   - Unwitnessed gaps. A death due at a read more than ClockWindow after the
//     posting's last event (its previous miss) waits one more read
//     (Summary.Deferred): a clock that jumped ahead between two reads, or came
//     back from being set back, looks exactly like a long gap, and the next
//     read either confirms the time (the posting dies then) or shows it was
//     the clock (the jump's stamps are then unreadable or its events ahead).
//     A board read after a gap that long (a laptop asleep, a board failing for
//     days) pays one read.
//
// What remains, and is not detectable from the ledger: a clock error of at most
// ClockWindow that does not put now behind any recorded event (a clock jumping
// ahead by that little between two reads, or set back by less than the time
// since the board's last write) is indistinguishable from reads that far apart,
// and can end a hold early by up to that error. An ordinary hold (20h) is
// shorter than ClockWindow, so a jump of 20 hours or more between two reads an
// hour apart ends it. A clock set back, with nothing in the ledger within
// ClockWindow ahead of it, stamps first misses early by the set-back for
// postings first seen longer ago than that (a younger posting's stamp predates
// it and restarts); if the set-back is more than ClockWindow its correction is
// a long gap and costs them one read, no more. A clock that stays wrong after
// jumping ahead is seen for one read only.
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
	// MassShortKey is "short" on a mass-marked posting whose every mass
	// listing in the current episode was a short listing (a board that counted
	// more rows than it listed), never a true mass absence. Its death is an
	// expected closure, reported (Summary.ShortDead) but not a failure; a
	// posting with no value (the ledger before this key, or one a true mass
	// absence ever marked) counts as a mass death. Read only while
	// mass_missed_since is set; forgotten with it.
	MassShortKey = "mass_short"
	// massShortValue and massUnreadableValue are MassShortKey's values. The
	// second is a posting a short listing that skipped unreadable rows marked: it
	// is held for as long as any listing skips rows, and its death (on a listing
	// that skipped none) is a mass death, a failure naming the board
	// (Summary.UnreadableDead). An older build reads it as "not short": a mass
	// death, which also fails the run.
	massShortValue      = "short"
	massUnreadableValue = "unreadable"
	// OrdinaryHold is how long an ordinary absence lasts, from its first miss,
	// before it can kill: a daily run kills on its second miss, two runs an hour
	// apart cannot.
	OrdinaryHold = 20 * time.Hour
	// MaxStampAhead is how far past now a stamp or an event may sit and still
	// agree with the clock.
	MaxStampAhead = time.Hour
	// EmptyListingMinOpen is the fewest open postings from which a listing that
	// shows none of them is a mass absence whatever the floor.
	EmptyListingMinOpen = 2
	// SmallBoardMinAbsent is the fewest absent postings from which a listing of
	// a board under the floor that leaves out more than half of its open
	// postings is a mass absence (closes FOLLOWUPS A2-4).
	SmallBoardMinAbsent = 2
	// ClockWindow is how much time the rule takes on trust: events further
	// ahead than this are not read as a clock behind (so a clock that once ran
	// ahead freezes nothing for longer than this), and a death that needs more
	// than this much of the gap since the posting's last event waits one more
	// read. The two halves meet: a clock set back by more than ClockWindow is
	// not frozen for, and its correction is a gap of more than ClockWindow.
	// Longer than a day's run plus a missed one, so daily runs are never
	// deferred.
	ClockWindow = 72 * time.Hour
	// SetBackCap bounds the second clock guard: a quorum (ClockQuorum) of the
	// board's own records with events more than ClockWindow and at most
	// SetBackCap ahead of a read freezes the board's aging, as events within
	// ClockWindow do. Such a quorum is what a clock set back by days leaves
	// behind (the last run's burst of misses is that far ahead), and aging under
	// it would stamp first misses early and end a mass hold early by the
	// set-back once the clock is right. A month covers a mistyped date or day;
	// further ahead is read as a clock that ran ahead, as before, because the
	// ledger cannot tell the two apart and every day of the cap is a day a
	// ran-ahead run's new postings can freeze their board: the freeze lasts from
	// the read until real time passes the newest of them, never past the cap.
	SetBackCap = 30 * 24 * time.Hour
	// ClockQuorum is how many records anywhere in the ledger must carry events
	// within ClockWindow ahead for a board none of whose own records do to be
	// frozen: a clock set back puts the last run's whole burst ahead; one or two
	// stray events (a hand-written event, a write under another clock) do not
	// stop every board.
	ClockQuorum = 3
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
	if open < lv.MassMinOpen {
		// A board under the floor leans on the ordinary rule for a posting or
		// two, but a listing that is missing more than half of its open
		// postings (and at least 2) is a wrong tenant or a broken read.
		return absent >= SmallBoardMinAbsent && 2*absent > open
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
	ann[MassShortKey] = ""
	return ann
}

// readStamp parses a stamp annotation; ok is false when it is missing,
// malformed, more than MaxStampAhead past now, or more than MaxStampAhead
// before the record was first seen (no posting is missed before it exists:
// the stamp was taken under a clock set back past the posting's history; the
// hour is the same jitter allowed everywhere, so a miss stamped just before an
// ahead-stamped sighting does not restart). The first sighting is itself a
// stamp: one more than MaxStampAhead past now was taken under a clock that ran
// ahead and says nothing, so it is not compared (else every stamp would read
// as before it, and the episode would restart until real time passed it).
func readStamp(r ledger.Record, key string, now time.Time) (time.Time, bool) {
	v := r.Annotations[key]
	if v == "" {
		return time.Time{}, false
	}
	cut := now.Add(MaxStampAhead)
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.After(cut) || (!r.FirstSeen.After(cut) && t.Before(r.FirstSeen.Add(-MaxStampAhead))) {
		return time.Time{}, false
	}
	return t, true
}

func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// clockEvidence is what the ledger's history says about a full listing taken
// at now: behind when it holds events that read as a clock behind, and ahead the
// newest event that says so. Aging resumes once the clock passes it.
type clockEvidence struct {
	behind bool
	// setBack: behind only by a quorum of the board's own events further ahead than
	// ClockWindow (a clock set back by days); none lies within the window.
	setBack bool
	ahead  time.Time
}

// clockEvidence reads the board under prefix (liveness.go "The clock"): an
// event of the board's own records, or of at least ClockQuorum records
// anywhere, more than MaxStampAhead and at most ClockWindow past now. A ledger
// whose every event lies further ahead is no evidence either: a run under a
// clock that ran ahead may have touched every record of a small ledger, and
// a clock set back past the whole history stamps nothing readable (a stamp
// before a posting was first seen restarts).
func (x *ledgerIndex) clockEvidence(prefix string, now time.Time) clockEvidence {
	lo, hi := now.Add(MaxStampAhead), now.Add(ClockWindow)
	ev := x.events
	i := sort.Search(len(ev), func(i int) bool { return ev[i].After(lo) })
	if i == len(ev) {
		return clockEvidence{}
	}
	var c clockEvidence
	if j := sort.Search(len(ev), func(j int) bool { return ev[j].After(hi) }); j-i >= ClockQuorum {
		c = clockEvidence{behind: true, ahead: ev[j-1]}
	}
	capHi := now.Add(SetBackCap)
	var far int
	var farNewest time.Time
	for _, k := range x.board(prefix) {
		t := x.info[k].last
		switch {
		case t.After(lo) && !t.After(hi):
			if t.After(c.ahead) {
				c = clockEvidence{behind: true, ahead: t}
			}
		case t.After(hi) && !t.After(capHi):
			far++
			if t.After(farNewest) {
				farNewest = t
			}
		}
	}
	// A quorum of the board's own records further ahead than ClockWindow but
	// within SetBackCap: a clock set back by days (liveness.go "The clock").
	if far >= ClockQuorum && !c.behind {
		c = clockEvidence{behind: true, ahead: farNewest, setBack: true}
	} else if far >= ClockQuorum && farNewest.After(c.ahead) {
		c.ahead = farNewest
	}
	return c
}

// absence is what one full listing's absence pass did.
type absence struct {
	open, absent       int
	missed, held, dead int
	massMarked         int // postings this listing newly marked
	deferred           int // deaths that waited a read for an unwitnessed gap (in held)
	massDead           int // deaths of mass-marked postings a true mass absence marked (in dead)
	shortDead          int // deaths of postings only short listings marked (in dead, not in massDead)
	unreadableDead     int // deaths of postings an unreadable-row listing held (in massDead)
	mass, clockBehind  bool
	clockSetBack       bool
	clockAhead         time.Time
}

// ageAbsent is the absence pass of a full listing: open is the board's open
// keys before the listing, seen the keys it listed, clock what the ledger says
// about now. It writes the miss, hold and death events in one batched append;
// its counts say what was written, so a pass that failed reports none of them.
func ageAbsent(ctx context.Context, l ledger.Ledger, lv Liveness, open []posting.Key, seen map[posting.Key]bool, clock clockEvidence, now time.Time, kind shortKind) (absence, error) {
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
	if clock.behind {
		a.clockBehind, a.clockAhead, a.clockSetBack = true, clock.ahead, clock.setBack
		return a, nil
	}
	// Only a listing that is the board's whole word can be a true mass absence
	// by share: a short listing leaves out what it leaves out because it is
	// short, however much that is, so what it holds dies as a short death
	// (MassShortKey). A listing that shows none of the board's open postings is
	// one whether or not it is short (the Snap case: an empty read).
	short := kind != shortNone
	unreadable := kind == shortUnreadable
	trueMass := massAbsence(lv, len(open), len(absent)) && (!short || len(absent) == len(open))
	mass := short || trueMass

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
		e, o := absenceEvent(lv, cur, mass, trueMass, unreadable, now)
		switch o.outcome {
		case absentDead:
			w.dead++
			switch {
			case o.massed && o.shortOnly:
				w.shortDead++
			case o.massed:
				w.massDead++
				if o.unreadable {
					w.unreadableDead++
				}
			}
		case absentHeld:
			w.held++
			if o.deferred {
				w.deferred++
			}
		default:
			w.missed++
		}
		if o.newMark {
			w.massMarked++
		}
		evs = append(evs, e)
	}
	if _, err := appendBatched(ctx, l, evs); err != nil {
		return a, err
	}
	a.missed, a.held, a.dead, a.massMarked, a.mass = w.missed, w.held, w.dead, w.massMarked, mass
	a.deferred, a.massDead, a.shortDead, a.unreadableDead = w.deferred, w.massDead, w.shortDead, w.unreadableDead
	return a, nil
}

type absentOutcome int

const (
	absentMissed absentOutcome = iota // below MissesToDead
	absentHeld                        // enough misses, hold not yet over (or deferred)
	absentDead
)

// absenceResult is what one absence did to a posting.
type absenceResult struct {
	outcome   absentOutcome
	newMark   bool // this listing mass-marked it
	massed    bool // it is mass marked (after this listing)
	shortOnly bool // ... and only short listings ever marked it in this episode
	// unreadable: ... and a listing that skipped unreadable rows marked it, and no
	// true mass absence since (MassShortKey "unreadable"): a death is a failing one.
	unreadable bool
	deferred   bool // its death was due only by an unwitnessed gap, and waits a read
}

// absenceEvent is the event one more full-listing absence writes for cur.
// massListing says the listing marks its absent postings mass; trueMass that it
// does so by the mass rule itself, not only because it was a short listing;
// unreadable that the listing skipped rows it could not read, which may be
// the posting itself: such a listing kills nothing (the posting is held), and
// what it marked dies only on a later listing that skipped nothing.
func absenceEvent(lv Liveness, cur ledger.Record, massListing, trueMass, unreadable bool, now time.Time) (ledger.Event, absenceResult) {
	misses := cur.MissedRuns + 1
	ongoing := cur.MissedRuns > 0
	first, ok := readStamp(cur, MissedSinceKey, now)
	if !ongoing || !ok {
		first = now // a new episode, or an unreadable stamp: restart (later, never earlier)
	}
	// The mass mark persists through the episode; any value counts, so a
	// mangled mark still holds. A new episode starts unmarked.
	massSince, newMark := "", false
	shortOnly, unread := false, false
	if ongoing && cur.Annotations[MassMissedSinceKey] != "" {
		massSince = cur.Annotations[MassMissedSinceKey]
		shortOnly = cur.Annotations[MassShortKey] == massShortValue
		unread = cur.Annotations[MassShortKey] == massUnreadableValue
		if _, ok := readStamp(cur, MassMissedSinceKey, now); !ok {
			massSince = stampOf(now)
		}
	}
	if massListing && massSince == "" {
		massSince, newMark = stampOf(now), true
		shortOnly = !trueMass && !unreadable // marked by a short listing alone
	} else if trueMass || unreadable {
		shortOnly = false // a true mass absence, or unreadable rows, upgrade the cause: it fails the run if they die
	}
	// Unreadable rows mark the episode until a true mass absence takes it over;
	// the cause is kept through later short or complete listings.
	unread = (unread || (unreadable && massSince != "")) && !trueMass
	short := ""
	switch {
	case massSince != "" && shortOnly:
		short = massShortValue
	case massSince != "" && unread:
		short = massUnreadableValue
	}
	hold := OrdinaryHold
	if massSince != "" {
		hold = lv.MassHold
	}
	ann := map[string]string{
		ledger.MissedRunsKey: strconv.Itoa(misses),
		MissedSinceKey:       stampOf(first),
		MassMissedSinceKey:   massSince, // "" forgets a stale mark of an earlier episode
		MassShortKey:         short,
	}
	e := ledger.Event{V: ledger.SchemaVersion, At: now, Key: cur.Key, Status: cur.Status, Annotations: ann,
		Reason: fmt.Sprintf("absent from board (%d)", misses)}
	if massSince != "" {
		e.Reason = fmt.Sprintf("absent from board (%d), mass absence", misses)
	}
	res := absenceResult{newMark: newMark, massed: massSince != "", shortOnly: short == massShortValue, unreadable: short == massUnreadableValue}
	due := misses >= MissesToDead && !now.Before(first.Add(hold))
	unreadableHeld := due && unreadable
	if unreadableHeld {
		due = false // the posting may be the row this listing could not read
	}
	// An unwitnessed gap: more than ClockWindow since the posting's last event
	// (its previous miss). A clock that jumped ahead, or came back from being set
	// back, looks the same as that long a gap, so the next read decides: this
	// one writes the miss, and that read's gap is its own.
	gap := now.Sub(cur.LastEvent)
	if due && !cur.LastEvent.IsZero() && gap > ClockWindow {
		due, res.deferred = false, true
	}
	switch {
	case due:
		e.Status = posting.Dead
		ann[DiedFromKey] = string(cur.Status)
		if massSince != "" {
			e.Reason = fmt.Sprintf("absent from board %d times over %s, mass absence", misses, hold)
		} else {
			e.Reason = fmt.Sprintf("absent from board %d times over %s", misses, OrdinaryHold)
		}
		res.outcome = absentDead
	case unreadableHeld:
		e.Reason += ", held: this listing skipped rows it could not read, and the posting may be one of them"
		res.outcome = absentHeld
	case res.deferred:
		e.Reason += fmt.Sprintf(", held one more read: %s since its last event is more than the %s the rule takes on trust (a clock that jumped looks the same)",
			gap.Round(time.Hour), ClockWindow)
		res.outcome = absentHeld
	case misses >= MissesToDead:
		e.Reason += fmt.Sprintf(", held until %s", first.Add(hold).UTC().Format(time.RFC3339))
		res.outcome = absentHeld
	default:
		res.outcome = absentMissed
	}
	return e, res
}

// MassHold is one board's open postings that a mass absence marked and that
// are still absent: waiting out the mass hold.
type MassHold struct {
	Prefix string // the board's key prefix (sources.Board.KeyPrefix)
	Held   int
	// Until is when the first of them can die, at the earliest: its first miss
	// plus the mass hold. Last is the same for the last of them. A death also
	// needs a read that ages the board then.
	Until, Last time.Time
}

// MassHeld lists the boards in recs that hold mass-marked postings, by prefix,
// with when their holds end under lv. A posting whose miss stamp is unreadable
// at now is counted from now: its episode restarts at its next miss.
func MassHeld(recs []ledger.Record, lv Liveness, now time.Time) []MassHold {
	lv = lv.resolved()
	byPrefix := map[string]*MassHold{}
	for _, r := range recs {
		if closed(r.Status) || r.MissedRuns == 0 || r.Annotations[MassMissedSinceKey] == "" {
			continue
		}
		k := string(r.Key)
		i := strings.LastIndexByte(k, ':')
		if i < 0 {
			continue
		}
		first, ok := readStamp(r, MissedSinceKey, now)
		if !ok {
			first = now
		}
		end := first.Add(lv.MassHold)
		h := byPrefix[k[:i+1]]
		if h == nil {
			h = &MassHold{Prefix: k[:i+1], Until: end, Last: end}
			byPrefix[h.Prefix] = h
		}
		h.Held++
		if end.Before(h.Until) {
			h.Until = end
		}
		if end.After(h.Last) {
			h.Last = end
		}
	}
	out := make([]MassHold, 0, len(byPrefix))
	for _, h := range byPrefix {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}
