package pipeline

// The death rule, checked over random sequences of reads of one to three boards
// sharing a ledger, under default or random settings, with clocks that are
// sometimes wrong for one read. An oracle written from the owner's text alone
// (it reads the ledger only for each posting's status, keeps its own copy of
// every record's stamps and last event, and uses none of liveness.go's
// functions or constants) predicts every read, and the reconciler must match
// it exactly: which postings die, are held, wait a read or are missed, and
// whether the read froze. On top of that, in TRUE time:
//
//	(a) no posting dies before its second miss, nor before its hold (20 hours,
//	    or the mass hold once any listing in its absence episode was a mass
//	    absence) has passed since the read that stamped its first miss, less
//	    only the clock errors the rule documents it cannot see: a stamp taken
//	    under a clock set back (by the set-back), and a death read under a clock
//	    ahead that the gap guard did not catch (by that error);
//	(b) a posting continuously absent dies at the first read that ages it at or
//	    after its first miss + hold, unless that read comes more than 3 days
//	    after its last event (it then waits exactly one read) or the read froze
//	    on events ahead of its clock;
//	(c) a read whose clock is more than 3 days ahead never kills a posting
//	    whose last event was written under a right clock;
//	(d) an upper bound in true time that does not lean on the oracle's
//	    stamps: at a read under a right clock that froze nothing and came at
//	    most 3 days after the posting's last event, a posting is dead once its
//	    hold plus an hour has passed since the first read under a right clock
//	    that missed it after its last miss under a wrong one (or since the
//	    read at which its first sighting, taken under a clock that ran ahead,
//	    first read as past). A rule that kept restarting an episode would
//	    keep it alive past this.
//
// The owner's text, as the oracle reads it: a posting absent from a full
// listing is missed; its first miss is stamped; a stamp more than an hour
// ahead of the clock, or before the posting was first seen, restarts at the
// clock. A full listing that leaves at least the share of the board's open
// postings (distinct keys, before the listing) absent with at least the floor
// open, or shows none of at least 2 open, or (on a board under the floor)
// leaves more than half of its open postings and at least 2 absent, is a mass absence and marks every
// absent posting until it reappears. A listing ages nothing while the ledger
// holds events between 1 hour and 3 days ahead of its clock on one of the
// board's own records, or on at least 3 records anywhere, or events between 3
// days and 30 days ahead on at least 3 of the board's own records.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const (
	fuzzSequences = 250 // VANI_FUZZ_SEQUENCES=2000 is the full run
	fuzzReads     = 40
	specWindow    = 72 * time.Hour // the 3 days the owner's text takes on trust
	// specSetBackCap is how far ahead a quorum of the board's own events
	// still reads as a clock set back by days (a month).
	specSetBackCap = 30 * 24 * time.Hour
)

// fuzzGaps are the spacings between reads: hourly retries, daily runs, a
// laptop asleep for days, a board left unread for two weeks.
var fuzzGaps = []time.Duration{
	time.Hour, 3 * time.Hour, 12 * time.Hour, 19 * time.Hour, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour,
	30 * time.Hour, 48 * time.Hour, 5 * 24 * time.Hour, 15 * 24 * time.Hour,
}

// fuzzSkews are the clock errors of a wrong read: short and long, both ways.
var fuzzSkews = []time.Duration{
	2 * time.Hour, 20 * time.Hour, 2 * 24 * time.Hour, 4 * 24 * time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour,
	-2 * time.Hour, -20 * time.Hour, -2 * 24 * time.Hour, -4 * 24 * time.Hour, -30 * 24 * time.Hour, -365 * 24 * time.Hour,
}

type fuzzRead struct {
	board   int
	at      time.Time     // true time
	off     time.Duration // the clock reads at+off
	listed  []int
	partial bool
	short   bool // the board counted more rows than it listed (ReconcileShort)
}

// fuzzRule is a sequence's settings: Liveness for the reconciler, and the same
// values as the oracle reads them (the share as a fraction).
type fuzzRule struct {
	lv       Liveness
	num, den int
	floor    int
	hold     time.Duration
}

func fuzzRuleOf(rng *rand.Rand) fuzzRule {
	if rng.IntN(2) == 0 {
		return fuzzRule{num: 1, den: 4, floor: 10, hold: specMassHold} // the defaults, left unset
	}
	shares := [][2]int{{1, 10}, {1, 4}, {1, 2}, {1, 1}}
	sh := shares[rng.IntN(len(shares))]
	floor := []int{1, 3, 10, 20}[rng.IntN(4)]
	hold := []time.Duration{specOrdinaryHold, 72 * time.Hour, specMassHold, 30 * 24 * time.Hour}[rng.IntN(4)]
	return fuzzRule{lv: Liveness{MassShare: float64(sh[0]) / float64(sh[1]), MassMinOpen: floor, MassHold: hold},
		num: sh[0], den: sh[1], floor: floor, hold: hold}
}

// fuzzBoardState is one board's listing story.
type fuzzBoardState struct {
	universe, next int
	cur            []int
}

// fuzzSequence builds the reads of nb boards from rng: each read picks a board
// and is a healthy listing, a few missing, a mass drop, an empty answer, a
// renumbered board, repeated rows or a partial listing. One read in eight on a
// board is under a wrong clock, never two in a row on the same board (a clock
// wrong for one read, then right).
func fuzzSequence(rng *rand.Rand, nb int) []fuzzRead {
	st := make([]fuzzBoardState, nb)
	for i := range st {
		u := 2 + rng.IntN(29)
		st[i] = fuzzBoardState{universe: u, next: 1000 * (i + 1)}
		for id := range u {
			st[i].cur = append(st[i].cur, id)
		}
	}
	skewedLast := make([]bool, nb)
	firstRead := make([]bool, nb)
	at := t0
	out := make([]fuzzRead, 0, fuzzReads)
	for range fuzzReads {
		bi := rng.IntN(nb)
		s := &st[bi]
		var ids []int
		partial := false
		switch m := rng.IntN(20); {
		case m < 9: // healthy, a role or two closes
			if len(s.cur) > 0 && rng.IntN(3) == 0 {
				i := rng.IntN(len(s.cur))
				s.cur = append(s.cur[:i:i], s.cur[i+1:]...)
			}
			ids = append(ids, s.cur...)
		case m < 11: // a few missing this read only
			for _, id := range s.cur {
				if rng.IntN(10) != 0 {
					ids = append(ids, id)
				}
			}
		case m < 13: // a mass drop that may or may not recover
			ids = append(ids, s.cur[:rng.IntN(len(s.cur)+1)]...)
		case m < 14: // empty answer
		case m < 15: // renumbered: the old ids never come back
			s.cur = s.cur[:0]
			for range s.universe {
				s.cur = append(s.cur, s.next)
				s.next++
			}
			ids = append(ids, s.cur...)
		case m < 16: // one row repeated
			if len(s.cur) > 0 {
				id := s.cur[rng.IntN(len(s.cur))]
				for range 1 + rng.IntN(20) {
					ids = append(ids, id)
				}
			}
		case m < 17: // a partial listing: ages nothing
			partial = true
			for _, id := range s.cur {
				if rng.IntN(2) == 0 {
					ids = append(ids, id)
				}
			}
		default: // the board is back at full strength
			ids = append(ids, s.cur...)
		}
		rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		at = at.Add(fuzzGaps[rng.IntN(len(fuzzGaps))])
		var off time.Duration
		if !skewedLast[bi] && rng.IntN(8) == 0 {
			off = fuzzSkews[rng.IntN(len(fuzzSkews))]
		}
		// A board's first read writes an event per posting, so one under a clock
		// days ahead leaves a burst of the board's own records days ahead (what
		// a clock set back later looks like): the days-ahead quorum's case.
		if !firstRead[bi] {
			firstRead[bi] = true
			if rng.IntN(4) == 0 {
				off = []time.Duration{4 * 24 * time.Hour, 10 * 24 * time.Hour, 30 * 24 * time.Hour}[rng.IntN(3)]
			}
		}
		skewedLast[bi] = off != 0
		// One full read in eight is short: it lists what it lists but is not the
		// board's whole word, so every posting it leaves out is held as a mass
		// absence, with a cause of its own (the owner's text: a short listing).
		short := !partial && rng.IntN(8) == 0
		out = append(out, fuzzRead{board: bi, at: at, off: off, listed: ids, partial: partial, short: short})
	}
	return out
}

// fuzzPost is the oracle's copy of one record: its clock-time stamps as the
// ledger holds them, and the true time and clock error of the read that set
// its current first miss.
type fuzzPost struct {
	board           int
	firstSeen, last time.Time // clock times
	lastOff         time.Duration
	misses          int
	first           time.Time // clock time of the current first miss
	firstTrue       time.Time // true time of the read that set first
	firstOff        time.Duration
	massed          bool
	shortOnly       bool // massed, and only short listings marked it in this episode
	// firstSeenOff is the clock error of the read that first saw it; seenPast
	// that a read under a right clock has seen that sighting as past;
	// cleanStart is (d)'s start (true time), zero when none.
	firstSeenOff time.Duration
	seenPast     bool
	cleanStart   time.Time
}

// fuzzTally is what the sequences reached.
type fuzzTally struct {
	deaths, held, massReads, deferred, frozen, skewed, settings, multi atomic.Int64
	// shortReads are short full listings with something absent, shortDeaths
	// deaths of postings only short listings held, setBackFrozen reads frozen
	// by the wider (days ahead) quorum (maybe with other evidence), upgrades short marks a true mass
	// absence made mass.
	shortReads, shortDeaths, setBackFrozen, upgrades atomic.Int64
}

func TestDeathRuleBoundsFuzz(t *testing.T) {
	n := fuzzSequences
	if testing.Short() {
		n = 60
	}
	if v, err := strconv.Atoi(os.Getenv("VANI_FUZZ_SEQUENCES")); err == nil && v > 0 {
		n = min(v, 20000) // 2000 is the full run
	}
	base := t.TempDir()
	var tally fuzzTally
	const shards = 8
	t.Run("shards", func(t *testing.T) {
		for sh := 1; sh <= shards; sh++ {
			t.Run(fmt.Sprint(sh), func(t *testing.T) {
				t.Parallel()
				for seed := sh; seed <= n; seed += shards {
					fuzzOne(t, base, seed, &tally)
				}
			})
		}
	})
	if t.Failed() {
		return
	}
	// The sequences reached what they are for.
	for name, v := range map[string]int64{"deaths": tally.deaths.Load(), "held reads": tally.held.Load(), "mass reads": tally.massReads.Load(),
		"deferred deaths": tally.deferred.Load(), "frozen reads": tally.frozen.Load(), "skewed reads": tally.skewed.Load(),
		"non-default settings": tally.settings.Load(), "multi-board sequences": tally.multi.Load(),
		"short listings that left something out": tally.shortReads.Load(), "short deaths": tally.shortDeaths.Load(),
		"reads under the days-ahead quorum": tally.setBackFrozen.Load(), "short marks upgraded by a true mass absence": tally.upgrades.Load()} {
		if v == 0 {
			t.Errorf("the sequences reached no %s", name)
		}
	}
	t.Logf("%d sequences x %d reads: %d deaths, %d held posting-reads, %d mass reads, %d deferred, %d frozen reads, %d skewed reads",
		n, fuzzReads, tally.deaths.Load(), tally.held.Load(), tally.massReads.Load(), tally.deferred.Load(), tally.frozen.Load(), tally.skewed.Load())
}

// fuzzOne runs one sequence against the oracle.
func fuzzOne(t *testing.T, base string, seed int, tally *fuzzTally) {
	rng := rand.New(rand.NewPCG(uint64(seed), 99))
	nb := 1 + rng.IntN(3)
	rule := fuzzRuleOf(rng)
	reads := fuzzSequence(rng, nb)
	if rule.lv != (Liveness{}) {
		tally.settings.Add(1)
	}
	if nb > 1 {
		tally.multi.Add(1)
	}
	boards := make([]sources.Board, nb)
	for i := range boards {
		boards[i] = probeBoard(fmt.Sprintf("fuzz%d", i))
	}
	l, err := ledger.Open(filepath.Join(base, fmt.Sprint(seed)))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	known := map[posting.Key]*fuzzPost{}
	failf := func(i int, format string, a ...any) {
		t.Helper()
		t.Fatalf("seed %d read %d: %s", seed, i, fmt.Sprintf(format, a...))
	}
	isClosed := func(s posting.Status) bool { return s == "dead" || s == "duplicate" || s == "dropped" }
	for i, r := range reads {
		b := boards[r.board]
		clock := r.at.Add(r.off)
		if r.off != 0 {
			tally.skewed.Add(1)
		}
		var listed []posting.Posting
		distinct := map[posting.Key]bool{}
		for _, id := range r.listed {
			listed = append(listed, probePost(t, b, id))
			distinct[probeKey(t, b, id)] = true
		}
		// The ledger before the listing, as the owner's text reads it: the
		// board's open postings, and every record's last event.
		var open []posting.Key
		for _, rec := range l.All() {
			if known[rec.Key] != nil && known[rec.Key].board == r.board && !isClosed(rec.Status) {
				open = append(open, rec.Key)
			}
		}
		lo, hi := clock.Add(time.Hour), clock.Add(specWindow)
		inWindow, own, setBack := 0, false, 0
		for _, p := range known {
			if p.last.After(lo) && !p.last.After(hi) {
				inWindow++
				own = own || p.board == r.board
			}
			// The owner's text: at least 3 of the board's own records with an
			// event more than 3 days and at most 30 days ahead.
			if p.board == r.board && p.last.After(hi) && !p.last.After(clock.Add(specSetBackCap)) {
				setBack++
			}
		}

		rc := NewReconciler(l)
		if err := rc.SetLiveness(rule.lv); err != nil {
			failf(i, "settings %+v: %v", rule.lv, err)
		}
		var s Summary
		if r.partial {
			s, err = rc.ReconcilePartial(ctx, b, listed, clock)
		} else if r.short {
			s, err = rc.ReconcileShort(ctx, b, listed, clock)
		} else {
			s, err = rc.Reconcile(ctx, b, listed, clock)
		}
		if err != nil {
			failf(i, "reconcile: %v", err)
		}

		// The listing: a new posting is first seen now; a listed posting with
		// misses, or dead, has reappeared and its episode ends.
		for k := range distinct {
			p := known[k]
			if p == nil {
				known[k] = &fuzzPost{board: r.board, firstSeen: clock, last: clock, lastOff: r.off, firstSeenOff: r.off}
				continue
			}
			if p.misses > 0 || p.dead() {
				*p = fuzzPost{board: p.board, firstSeen: p.firstSeen, firstSeenOff: p.firstSeenOff, seenPast: p.seenPast, last: clock, lastOff: r.off}
			}
		}
		if r.partial {
			continue
		}
		var absent []posting.Key
		for _, k := range open {
			if !distinct[k] {
				absent = append(absent, k)
			}
		}
		if s.Open != len(open) || s.Absent != len(absent) {
			failf(i, "open %d absent %d, oracle %d %d", s.Open, s.Absent, len(open), len(absent))
		}
		frozen := len(absent) > 0 && (own || inWindow >= 3 || setBack >= 3)
		if s.ClockBehind != frozen {
			failf(i, "ClockBehind %v, oracle %v (own %v, %d records in the window, clock %+v)", s.ClockBehind, frozen, own, inWindow, r.off)
		}
		if frozen {
			tally.frozen.Add(1)
			if setBack >= 3 {
				tally.setBackFrozen.Add(1)
			}
			for _, k := range absent {
				if rec, _ := l.Get(k); rec.MissedRuns != known[k].misses || rec.Status == posting.Dead {
					failf(i, "%s was aged by a frozen read: %+v", k, rec)
				}
			}
			continue
		}
		nOpen, nAbsent := len(open), len(absent)
		// A short listing is a true mass absence only when it shows none of at
		// least 2 open postings, never by share (its cause is short).
		trueMass := (nOpen >= 2 && nAbsent == nOpen) || (!r.short && ((nOpen >= rule.floor && nAbsent*rule.den >= rule.num*nOpen) ||
			(nOpen < rule.floor && nAbsent >= 2 && 2*nAbsent > nOpen)))
		mass := r.short || trueMass // a short listing marks every absent posting mass
		if mass && nAbsent > 0 {
			tally.massReads.Add(1)
		}
		if r.short && nAbsent > 0 {
			tally.shortReads.Add(1)
		}
		var want Summary
		for _, k := range absent {
			p := known[k]
			if p.misses == 0 {
				p.massed, p.shortOnly = false, false
				p.cleanStart = time.Time{}
			}
			// (d)'s start: the first read under a right clock that missed it since
			// the last miss under a wrong one, moved to the read at which its first
			// sighting, taken under a clock that ran ahead, first reads as past.
			if r.off != 0 {
				p.cleanStart = time.Time{}
			} else {
				seenPast := !p.firstSeen.After(clock.Add(time.Hour))
				if p.cleanStart.IsZero() || (seenPast && !p.seenPast && p.firstSeenOff > 0) {
					p.cleanStart = r.at
				}
				p.seenPast = p.seenPast || seenPast
			}
			p.misses++
			if p.misses == 1 || p.first.After(clock.Add(time.Hour)) || (!p.firstSeen.After(clock.Add(time.Hour)) && p.first.Before(p.firstSeen.Add(-time.Hour))) {
				p.first, p.firstTrue, p.firstOff = clock, r.at, r.off
			}
			if mass && !p.massed {
				want.MassMarked++
				p.shortOnly = !trueMass
			} else if trueMass {
				if p.shortOnly && !r.short {
					tally.upgrades.Add(1)
				}
				p.shortOnly = false
			}
			p.massed = p.massed || mass
			hold := specOrdinaryHold
			if p.massed {
				hold = rule.hold
			}
			due := p.misses >= 2 && !clock.Before(p.first.Add(hold))
			gap := clock.Sub(p.last)
			waits := due && gap > specWindow
			lastOff := p.lastOff
			p.last, p.lastOff = clock, r.off
			rec, _ := l.Get(k)
			switch {
			case !due && !waits && r.off == 0 && !p.cleanStart.IsZero() && r.at.Sub(p.cleanStart) >= hold+time.Hour:
				failf(i, "(d) %s alive %s (true) after its first miss under a right clock, hold %s: a restart or freeze the rule does not allow", k, r.at.Sub(p.cleanStart), hold)
			case due && !waits:
				if rec.Status != posting.Dead {
					failf(i, "(b) %s alive with %d misses, %s (clock) after its first miss, hold %s", k, p.misses, clock.Sub(p.first), hold)
				}
				// (a) in true time, less the errors the rule cannot see.
				allow := max(0, -p.firstOff) + max(0, r.off)
				if r.at.Sub(p.firstTrue) < hold-allow {
					failf(i, "(a) %s died %s (true) after the read that stamped its first miss, hold %s, clock errors stamp %s death %s",
						k, r.at.Sub(p.firstTrue), hold, p.firstOff, r.off)
				}
				// (c) a clock far ahead never kills against a last event written
				// under a right clock.
				if r.off > specWindow && lastOff == 0 {
					failf(i, "(c) %s died under a clock %s ahead", k, r.off)
				}
				want.Dead++
				if p.massed && p.shortOnly {
					want.ShortDead++
					tally.shortDeaths.Add(1)
				} else if p.massed {
					want.MassDead++
				}
				p.misses = -1 // dead: the next listing of it is a reappearance
				tally.deaths.Add(1)
			case due:
				if rec.Status == posting.Dead {
					failf(i, "(b) %s died though %s passed since its last event", k, gap)
				}
				want.Held++
				want.Deferred++
				tally.deferred.Add(1)
				tally.held.Add(1)
			case p.misses >= 2:
				if rec.Status == posting.Dead {
					failf(i, "(a) %s died inside its hold: %d misses, %s (clock) after its first miss, hold %s", k, p.misses, clock.Sub(p.first), hold)
				}
				want.Held++
				tally.held.Add(1)
			default:
				if rec.Status == posting.Dead {
					failf(i, "(a) %s died on its first miss", k)
				}
				want.Missed++
			}
		}
		if s.Missed != want.Missed || s.Held != want.Held || s.Deferred != want.Deferred || s.Dead != want.Dead ||
			s.MassDead != want.MassDead || s.ShortDead != want.ShortDead || s.MassMarked != want.MassMarked || s.Mass != (mass && nAbsent > 0) {
			failf(i, "summary %+v, oracle missed %d held %d deferred %d dead %d mass dead %d short dead %d marked %d mass %v",
				s, want.Missed, want.Held, want.Deferred, want.Dead, want.MassDead, want.ShortDead, want.MassMarked, mass)
		}
		for k := range distinct { // everything the listing showed is open or revived
			if rec, _ := l.Get(k); rec.Status == posting.Dead {
				failf(i, "%s was listed and is dead", k)
			}
		}
	}
}

// dead says the oracle holds the posting as dead (misses -1).
func (p *fuzzPost) dead() bool { return p.misses < 0 }
