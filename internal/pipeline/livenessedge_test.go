package pipeline

// Edge cases of the death rule that the probe table and the fuzz do not reach:
// the ledger as it stands at install, a clock that is wrong in either direction,
// a pass that is cancelled, an empty ledger, a board that comes back, settings.

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// seedBoard reconciles n postings of b at t0 and returns them.
func seedBoard(t *testing.T, l ledger.Ledger, b sources.Board, n int) []posting.Posting {
	t.Helper()
	var ps []posting.Posting
	for i := 0; i < n; i++ {
		ps = append(ps, probePost(t, b, i))
	}
	if _, err := Reconcile(ctx, l, b, ps, t0); err != nil {
		t.Fatal(err)
	}
	return ps
}

// TestRecordAlreadyMissedOnceAtInstallDoesNotDieEarly: the live ledger holds
// records at missed_runs=1 with no miss stamp (the old rule). The first run
// after install is their second miss; with no stamp their episode restarts
// there, so they cannot die on that run, and die once 20 hours have passed.
func TestRecordAlreadyMissedOnceAtInstallDoesNotDieEarly(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("legacy")
	ps := seedBoard(t, l, b, 20)
	legacy := ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: ps[0].Key, Status: posting.Live,
		Annotations: map[string]string{ledger.MissedRunsKey: "1"}, Reason: "absent from board (1)"}
	if err := l.Append(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, ps[0].Key); r.MissedRuns != 1 || r.Annotations[MissedSinceKey] != "" {
		t.Fatalf("setup: %+v", r)
	}
	run := func(h int) {
		t.Helper()
		if _, err := Reconcile(ctx, l, b, ps[1:], t0.Add(time.Duration(h)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	run(100) // first run after install: second miss, no stamp
	r := rec(t, l, ps[0].Key)
	if r.Status == posting.Dead || r.MissedRuns != 2 {
		t.Fatalf("died on the first post-install run: %+v", r)
	}
	if r.Annotations[MissedSinceKey] != t0.Add(100*time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("episode did not restart at the first post-install run: %q", r.Annotations[MissedSinceKey])
	}
	run(119)
	if rec(t, l, ps[0].Key).Status == posting.Dead {
		t.Fatal("died 19 hours into its restarted episode")
	}
	run(120)
	if rec(t, l, ps[0].Key).Status != posting.Dead {
		t.Fatal("still alive 20 hours into its restarted episode")
	}
}

// TestFutureEventsFreezeOnlyWhatIsNotAClockAheadBurst: events in the ledger's
// future decide whether a full listing ages anything. Bounded both ways: events
// further ahead than ClockWindow (a clock that ran ahead) do not freeze the
// board however many runs wrote them, and events within it do, only until real
// time passes them. Events of another board count once there are ClockQuorum of
// them: a clock set back puts a run's whole burst ahead, one stray event does
// not stop every board.
//
// Changed 2026-11-12 (review F1, F2): "far-future burst spread over a day" was
// behind (two runs' burst read as a clock set back, which froze aging until
// real time reached it, a month here, a year in the review's case); "two days
// ahead, one burst" on another board was behind (one stray event froze every
// board). Both now age; the same events on the board's own records, or on
// three records elsewhere, still freeze it.
func TestFutureEventsFreezeOnlyWhatIsNotAClockAheadBurst(t *testing.T) {
	cases := []struct {
		name   string
		events []time.Duration // event times, after t0
		own    bool            // the events are on the read board's own records
		now    time.Duration   // the read, after t0
		behind bool
	}{
		{"one far-future burst, history before it", []time.Duration{5 * 365 * 24 * time.Hour}, false, 24 * time.Hour, false},
		{"far-future burst spread over a day", []time.Duration{30 * 24 * time.Hour, 31 * 24 * time.Hour}, false, 24 * time.Hour, false},
		{"far-future burst on the board's own records", []time.Duration{30 * 24 * time.Hour, 31 * 24 * time.Hour}, true, 24 * time.Hour, false},
		{"two days ahead, one stray event on another board", []time.Duration{72 * time.Hour}, false, 24 * time.Hour, false},
		{"two days ahead, on the board's own record: a clock set back", []time.Duration{72 * time.Hour}, true, 24 * time.Hour, true},
		{"two days ahead, a burst of three elsewhere: a clock set back", []time.Duration{72 * time.Hour, 72 * time.Hour, 73 * time.Hour}, false, 24 * time.Hour, true},
		{"three days ahead exactly, own record", []time.Duration{96 * time.Hour}, true, 24 * time.Hour, true},
		{"just past the window, own record", []time.Duration{96*time.Hour + time.Second}, true, 24 * time.Hour, false},
		{"the event is now in the past", []time.Duration{72 * time.Hour}, true, 74 * time.Hour, false},
		{"within the one-hour allowance", []time.Duration{24*time.Hour + 30*time.Minute}, true, 24 * time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			b := probeBoard("future")
			ps := seedBoard(t, l, b, 20)
			other := probeBoard("elsewhere")
			if tc.own {
				other = b
			}
			op := probePost(t, other, 1)
			for i, d := range tc.events {
				op.Key = probeKey(t, other, 100+i)
				if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(d), Key: op.Key, Status: posting.Live, Posting: &op}); err != nil {
					t.Fatal(err)
				}
			}
			s, err := Reconcile(ctx, l, b, ps[1:], t0.Add(tc.now))
			if err != nil {
				t.Fatal(err)
			}
			if s.ClockBehind != tc.behind {
				t.Fatalf("ClockBehind = %v, want %v (%+v)", s.ClockBehind, tc.behind, s)
			}
			r := rec(t, l, ps[0].Key)
			if tc.behind && r.MissedRuns != 0 {
				t.Fatalf("a listing under a clock behind aged a posting: %+v", r)
			}
			if !tc.behind && r.MissedRuns != 1 {
				t.Fatalf("a listing under a sound clock did not miss the absent posting: %+v", r)
			}
		})
	}
}

// TestCancelledPassReportsNothingWritten: a pass cancelled before it writes
// reports no misses, holds, deaths or marks, and the ledger is as it was.
func TestCancelledPassReportsNothingWritten(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("cancel")
	ps := seedBoard(t, l, b, 20)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	s, err := NewReconciler(l).Reconcile(cctx, b, nil, t0.Add(24*time.Hour))
	if err == nil {
		t.Fatal("a cancelled pass reported success")
	}
	if s.Missed+s.Held+s.Dead+s.MassMarked != 0 {
		t.Fatalf("counted work that was never written: %+v", s)
	}
	for _, p := range ps {
		if r := rec(t, l, p.Key); r.MissedRuns != 0 || r.Status != posting.Live {
			t.Fatalf("a cancelled pass changed %s: %+v", p.Key, r)
		}
	}
}

// TestFirstReadsAndAReAddedBoard: an unknown board's first listing (empty or
// not) ages nothing; a board off the watchlist for months and added back starts
// its absence episodes at its first read, and a renumbered one is a mass
// absence held two weeks from there.
func TestFirstReadsAndAReAddedBoard(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("readded")
	if s, err := Reconcile(ctx, l, b, nil, t0); err != nil || s.Open != 0 || s.Absent != 0 || s.Mass || s.Dead != 0 {
		t.Fatalf("first-ever empty read: %+v %v", s, err)
	}
	ps := seedBoard(t, l, b, 20)
	// Ninety days with the board off the watchlist: nothing reads it.
	back := t0.Add(90 * 24 * time.Hour)
	var renumbered []posting.Posting
	for i := 500; i < 520; i++ {
		renumbered = append(renumbered, probePost(t, b, i))
	}
	s, err := Reconcile(ctx, l, b, renumbered, back)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Mass || s.MassMarked != 20 || s.Dead != 0 {
		t.Fatalf("re-added renumbered board: %+v", s)
	}
	if _, err := Reconcile(ctx, l, b, renumbered, back.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if rec(t, l, p.Key).Status == posting.Dead {
			t.Fatalf("%s died a day into a mass hold", p.Key)
		}
	}
	// Changed 2026-11-12 (review F3): the read 14 days on comes 13 days after the
	// postings' last event, more than ClockWindow; the hold is over only by
	// counting that whole unwitnessed gap (a clock that jumped looks the same),
	// so the deaths wait one more read, which kills them.
	s, err = Reconcile(ctx, l, b, renumbered, back.Add(14*24*time.Hour))
	if err != nil || s.Dead != 0 || s.Deferred != 20 {
		t.Fatalf("the read after a 13-day gap: %+v %v", s, err)
	}
	if _, err := Reconcile(ctx, l, b, renumbered, back.Add(14*24*time.Hour+time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if rec(t, l, p.Key).Status != posting.Dead {
			t.Fatalf("%s still alive after its 14 day hold", p.Key)
		}
	}
}

// TestOversizedDuplicateListingIsCountedAsASet: five thousand rows naming one
// posting list one posting, whatever the row count.
func TestOversizedDuplicateListingIsCountedAsASet(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("rows")
	ps := seedBoard(t, l, b, 20)
	rows := make([]posting.Posting, 5000)
	for i := range rows {
		rows[i] = ps[0]
	}
	s, err := Reconcile(ctx, l, b, rows, t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Open != 20 || s.Absent != 19 || !s.Mass || s.MassMarked != 19 {
		t.Fatalf("%+v", s)
	}
}

// TestClosedPostingsDoNotDiluteTheOpenCount: the share is of the board's open
// postings. Thirty dropped ones the ledger keeps for the board are not open, so
// three of ten live ones absent is 30%, a mass absence, not 3 of 40.
func TestClosedPostingsDoNotDiluteTheOpenCount(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("diluted")
	var all []posting.Posting
	for i := 0; i < 40; i++ {
		all = append(all, probePost(t, b, i))
	}
	if _, err := Reconcile(ctx, l, b, all, t0); err != nil {
		t.Fatal(err)
	}
	for _, p := range all[10:] { // thirty of them are dropped by the owner's rules
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: p.Key, Status: posting.Dropped}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Reconcile(ctx, l, b, append(append([]posting.Posting{}, all[3:10]...), all[10:]...), t0.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Open != 10 || s.Absent != 3 || !s.Mass || s.MassMarked != 3 {
		t.Fatalf("%+v", s)
	}
}

// TestTwoRunsAnHourApartCannotKill: death needs time as well as misses; and a
// malformed stamp restarts the episode at the read that finds it.
func TestTwoRunsAnHourApartCannotKill(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("hourly")
	ps := seedBoard(t, l, b, 20)
	for h := 1; h <= 19; h++ { // a run every hour for 19 hours
		if _, err := Reconcile(ctx, l, b, ps[1:], t0.Add(time.Duration(h)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if r := rec(t, l, ps[0].Key); r.Status == posting.Dead || r.MissedRuns != 19 {
		t.Fatalf("an hourly run killed it inside its hold: %+v", r)
	}
	if _, err := Reconcile(ctx, l, b, ps[1:], t0.Add(21*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rec(t, l, ps[0].Key).Status != posting.Dead {
		t.Fatal("alive 20 hours after its first miss")
	}

	// A malformed stamp is unreadable: the episode restarts there.
	l2 := openLedger(t)
	ps2 := seedBoard(t, l2, probeBoard("garbled"), 20)
	bad := ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: ps2[0].Key, Status: posting.Live,
		Annotations: map[string]string{ledger.MissedRunsKey: "1", MissedSinceKey: "garbage"}}
	if err := l2.Append(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ctx, l2, probeBoard("garbled"), ps2[1:], t0.Add(100*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l2, ps2[0].Key); r.Status == posting.Dead || r.Annotations[MissedSinceKey] != t0.Add(100*time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("a malformed stamp did not restart the episode: %+v", r)
	}
}

// TestLivenessSettings: the settings are used, and an invalid one is refused
// before it can weaken the rule.
func TestLivenessSettings(t *testing.T) {
	bad := []Liveness{
		{MassShare: -0.1}, {MassShare: 1.5}, {MassShare: math.NaN()}, {MassMinOpen: -1},
		{MassHold: time.Hour}, {MassHold: OrdinaryHold - time.Second}, {MassHold: 400 * 24 * time.Hour},
	}
	for _, lv := range bad {
		if err := lv.Validate(); err == nil {
			t.Errorf("accepted %+v", lv)
		}
		rc := NewReconciler(openLedger(t))
		if err := rc.SetLiveness(lv); err == nil || rc.live != (Liveness{}) {
			t.Errorf("SetLiveness took %+v (%v)", lv, err)
		}
	}
	for _, lv := range []Liveness{{}, DefaultLiveness(), {MassShare: 1, MassMinOpen: 1, MassHold: OrdinaryHold}, {MassHold: MaxMassHold}} {
		if err := lv.Validate(); err != nil {
			t.Errorf("refused %+v: %v", lv, err)
		}
	}

	// Ten open, four absent: 40%, a mass absence by default; with the share at
	// one half it is ordinary; with the share at a third and a 48h hold it is
	// mass, and dies on the second miss at 48h.
	run := func(lv Liveness, hours ...int) (Summary, []ledger.Record) {
		t.Helper()
		l := openLedger(t)
		b := probeBoard("settings" + strconv.Itoa(int(lv.MassShare*100)))
		ps := seedBoard(t, l, b, 10)
		var s Summary
		for _, h := range hours {
			rc := NewReconciler(l)
			if err := rc.SetLiveness(lv); err != nil {
				t.Fatal(err)
			}
			var err error
			if s, err = rc.Reconcile(ctx, b, ps[4:], t0.Add(time.Duration(h)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		var out []ledger.Record
		for _, p := range ps[:4] {
			out = append(out, rec(t, l, p.Key))
		}
		return s, out
	}
	if s, _ := run(Liveness{}, 24); !s.Mass || s.MassMarked != 4 {
		t.Fatalf("default: %+v", s)
	}
	if s, _ := run(Liveness{MassShare: 0.5}, 24); s.Mass {
		t.Fatalf("share 0.5: %+v", s)
	}
	lv := Liveness{MassShare: 0.3, MassHold: 48 * time.Hour}
	if s, rs := run(lv, 24, 71); !s.Mass || s.Dead != 0 || rs[0].Status == posting.Dead {
		t.Fatalf("hold 48h, 47h in: %+v", s)
	}
	if s, rs := run(lv, 24, 72); s.Dead != 4 || rs[0].Status != posting.Dead {
		t.Fatalf("hold 48h, 48h in: %+v", s)
	}
}
