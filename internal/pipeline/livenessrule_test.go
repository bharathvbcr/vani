package pipeline

// The death rule, from its text (docs/RUNTIME.md "When a posting dies"):
//
//   - A posting absent from a FULL listing of its board misses a run. Its first
//     miss in an absence episode is stamped (missed_since). Reappearing ends the
//     episode and clears every miss mark.
//   - An ordinary absence kills after at least 2 misses AND at least 20 hours
//     since the episode's first miss.
//   - A full listing is a MASS absence when it leaves at least S of the board's
//     open postings (distinct keys, counted before the listing) absent and the
//     board had at least the floor open, or when it shows none of a board's open
//     postings and at least 2 were open. Every posting it leaves absent is mass
//     marked; the mark only clears when the posting reappears.
//   - A mass-marked posting dies only after at least 2 misses AND the mass hold
//     (14 days by default) since its own first miss.
//   - A partial listing ages nothing. A listing taken with the clock behind the
//     ledger ages nothing.
//
// Each probe below is a case a reviewer found that killed postings early under
// an earlier design. The expectation is written from the text above, not from
// the implementation's constants: hours are spelled out.

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const (
	specOrdinaryHold = 20 * time.Hour
	specMassHold     = 14 * 24 * time.Hour
)

// probeRead is one attempt to read a board: at hours after t0, a full listing
// of ids (repeats allowed), a partial one, or nothing at all (a failed read,
// which never reaches reconcile).
type probeRead struct {
	at      float64 // hours after t0
	ids     []int
	partial bool
	failed  bool
	clock   time.Duration // added to the true time to make the clock wrong
}

func probeKey(t *testing.T, b sources.Board, id int) posting.Key {
	t.Helper()
	k, err := posting.GreenhouseKey(b.Token, fmt.Sprint(id))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func probePost(t *testing.T, b sources.Board, id int) posting.Posting {
	t.Helper()
	return posting.Posting{Key: probeKey(t, b, id), ATS: posting.Greenhouse, Company: b.Company,
		Title: fmt.Sprintf("Role %d", id), Location: "Remote", Description: fmt.Sprintf("the work of role %d", id)}
}

func idRange(lo, hi int) []int {
	out := make([]int, 0, hi-lo)
	for i := lo; i < hi; i++ {
		out = append(out, i)
	}
	return out
}

func repeatID(id, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = id
	}
	return out
}

// runProbe applies reads in order with one Reconciler per read (as one run per
// read does) and returns, per id, the true time it died (zero if alive).
func runProbe(t *testing.T, b sources.Board, reads []probeRead) map[int]time.Time {
	t.Helper()
	l := openLedger(t)
	died := map[int]time.Time{}
	ids := map[int]bool{}
	for _, r := range reads {
		for _, id := range r.ids {
			ids[id] = true
		}
	}
	for _, r := range reads {
		if r.failed {
			continue
		}
		at := t0.Add(time.Duration(r.at * float64(time.Hour)))
		var listed []posting.Posting
		for _, id := range r.ids {
			listed = append(listed, probePost(t, b, id))
		}
		rc := NewReconciler(l)
		rc.SetLiveness(DefaultLiveness())
		var err error
		if r.partial {
			_, err = rc.ReconcilePartial(ctx, b, listed, at.Add(r.clock))
		} else {
			_, err = rc.Reconcile(ctx, b, listed, at.Add(r.clock))
		}
		if err != nil {
			t.Fatalf("read at %vh: %v", r.at, err)
		}
		for id := range ids {
			got, ok := l.Get(probeKey(t, b, id))
			if ok && got.Status == posting.Dead && died[id].IsZero() {
				died[id] = at
			}
			if ok && got.Status != posting.Dead {
				delete(died, id) // relisted
			}
		}
	}
	return died
}

// daily returns full reads every 24h from hour start for n days, listing ids.
func daily(start float64, n int, ids []int) []probeRead {
	var out []probeRead
	for d := 0; d < n; d++ {
		out = append(out, probeRead{at: start + float64(24*d), ids: ids})
	}
	return out
}

func probeBoard(token string) sources.Board {
	return sources.Board{ATS: posting.Greenhouse, Token: token, Company: token}
}

// TestDeathRuleProbes: every reviewer probe that killed postings early under
// the count-band design is a non-event here.
func TestDeathRuleProbes(t *testing.T) {
	type expect struct {
		ids      []int
		notUntil time.Duration // none of ids may die before t0 + notUntil
		deadBy   time.Duration // all of ids dead by t0 + deadBy (0: no claim)
	}
	cases := []struct {
		name   string
		reads  []probeRead
		expect []expect
	}{
		{
			// F1: steady at 400, four days of empty answers, then a short read
			// of 100. The 300 never relisted were first missed at hour 24 and
			// are mass absences (an empty answer shows none of 400 open), so
			// they live until hour 24 + 14 days.
			name: "400 then empty then 100",
			reads: append(append(daily(0, 1, idRange(0, 400)), daily(24, 4, nil)...),
				daily(24*5, 20, idRange(0, 100))...),
			expect: []expect{
				{ids: idRange(100, 400), notUntil: 24*time.Hour + specMassHold, deadBy: 24*time.Hour + specMassHold + 24*time.Hour},
				{ids: idRange(0, 100), notUntil: 1000 * time.Hour},
			},
		},
		{
			// F3: a renumbered board (or a token moved to another tenant):
			// every old id is absent at once, new ids appear. The old ids are
			// a mass absence from hour 24 and are held 14 days.
			name:  "renumbered board",
			reads: append(daily(0, 1, idRange(0, 20)), daily(24, 20, idRange(1000, 1020))...),
			expect: []expect{
				{ids: idRange(0, 20), notUntil: 24*time.Hour + specMassHold, deadBy: 24*time.Hour + specMassHold + 24*time.Hour},
			},
		},
		{
			// F5: a small board (under the floor) read once, then answering
			// empty: showing none of its open postings is a mass absence.
			name:  "small board empties",
			reads: append(daily(0, 1, idRange(0, 6)), daily(24, 20, nil)...),
			expect: []expect{
				{ids: idRange(0, 6), notUntil: 24*time.Hour + specMassHold, deadBy: 24*time.Hour + specMassHold + 24*time.Hour},
			},
		},
		{
			// F6: one posting repeated 20 times in place of 20 distinct ones.
			// Counted as a set, 19 of 20 open are absent: a mass absence.
			name:  "duplicate rows",
			reads: append(daily(0, 1, idRange(0, 20)), daily(24, 20, repeatID(0, 20))...),
			expect: []expect{
				{ids: idRange(1, 20), notUntil: 24*time.Hour + specMassHold},
				{ids: []int{0}, notUntil: 1000 * time.Hour},
			},
		},
		{
			// The clock five years behind for one read, then corrected an hour
			// later. The behind read ages nothing; the first miss is the
			// corrected read's, so posting 0 lives at least 20 hours past it.
			name: "clock behind then corrected",
			reads: []probeRead{
				{at: 0, ids: idRange(0, 20)},
				{at: 24, ids: idRange(1, 20), clock: -5 * 365 * 24 * time.Hour},
				{at: 25, ids: idRange(1, 20)},
				{at: 26, ids: idRange(1, 20)},
				{at: 30, ids: idRange(1, 20)},
				{at: 46, ids: idRange(1, 20)},
			},
			expect: []expect{{ids: []int{0}, notUntil: 25*time.Hour + specOrdinaryHold, deadBy: 46 * time.Hour}},
		},
		{
			// The clock five years ahead for one read, then corrected: the
			// far-future stamp is unreadable and the episode restarts at the
			// corrected read (hour 25), so the posting dies at the first read 20
			// hours later. The far-future events the bad read wrote are a single
			// burst with history before it, so they do not freeze aging.
			name: "clock ahead then corrected",
			reads: []probeRead{
				{at: 0, ids: idRange(0, 20)},
				{at: 24, ids: idRange(1, 20), clock: 5 * 365 * 24 * time.Hour},
				{at: 25, ids: idRange(1, 20)},
				{at: 30, ids: idRange(1, 20)},
				{at: 46, ids: idRange(1, 20)},
			},
			expect: []expect{{ids: []int{0}, notUntil: 25*time.Hour + specOrdinaryHold, deadBy: 46 * time.Hour}},
		},
		{
			// A rate-limit spell (reads that never list) then a short read of
			// 100 of 400: 300 of 400 open absent is a mass absence.
			name: "rate limited then short",
			reads: append(append(daily(0, 1, idRange(0, 400)),
				probeRead{at: 24, failed: true}, probeRead{at: 48, failed: true}, probeRead{at: 72, failed: true}),
				daily(96, 20, idRange(0, 100))...),
			expect: []expect{{ids: idRange(100, 400), notUntil: 96*time.Hour + specMassHold}},
		},
		{
			// Flapping: absent, back, absent, back every 12 hours for days.
			// Each return ends the episode, so no posting ever reaches 20 hours
			// of continuous absence.
			name: "flapping",
			reads: func() []probeRead {
				var out []probeRead
				for i := 0; i < 40; i++ {
					ids := idRange(0, 20)
					if i%2 == 1 {
						ids = idRange(1, 20)
					}
					out = append(out, probeRead{at: float64(12 * i), ids: ids})
				}
				return out
			}(),
			expect: []expect{{ids: []int{0}, notUntil: 10000 * time.Hour}},
		},
		{
			// A partial listing (a page the user scraped) ages nothing.
			name:   "partial listings",
			reads:  append(daily(0, 1, idRange(0, 20)), probeRead{at: 24, ids: idRange(0, 2), partial: true}, probeRead{at: 48, ids: idRange(0, 2), partial: true}, probeRead{at: 72, ids: idRange(0, 2), partial: true}),
			expect: []expect{{ids: idRange(2, 20), notUntil: 10000 * time.Hour}},
		},
		{
			// The ordinary rule still works: one closed role on a healthy
			// board dies at the first full read 20h or more past its first miss.
			name:   "one role closes",
			reads:  append(daily(0, 1, idRange(0, 40)), daily(24, 5, idRange(1, 40))...),
			expect: []expect{{ids: []int{0}, notUntil: 24*time.Hour + specOrdinaryHold, deadBy: 48 * time.Hour}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := probeBoard("probe")
			died := runProbe(t, b, tc.reads)
			for _, e := range tc.expect {
				for _, id := range e.ids {
					at, dead := died[id]
					if dead && at.Before(t0.Add(e.notUntil)) {
						t.Fatalf("posting %d died at +%v, before +%v", id, at.Sub(t0), e.notUntil)
					}
					if e.deadBy > 0 && (!dead || at.After(t0.Add(e.deadBy))) {
						t.Fatalf("posting %d not dead by +%v (died %v)", id, e.deadBy, dead)
					}
				}
			}
		})
	}
}

// TestBoardScopedReadLeavesOtherBoardsAlone: a run cut to one board reconciles
// that board only; another board's postings are never aged by it.
func TestBoardScopedReadLeavesOtherBoardsAlone(t *testing.T) {
	l := openLedger(t)
	a, other := probeBoard("alpha"), probeBoard("beta")
	var la, lb []posting.Posting
	for i := 0; i < 10; i++ {
		la = append(la, probePost(t, a, i))
		lb = append(lb, probePost(t, other, i))
	}
	if _, err := Reconcile(ctx, l, a, la, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ctx, l, other, lb, t0); err != nil {
		t.Fatal(err)
	}
	for d := 1; d <= 30; d++ {
		rc := NewReconciler(l)
		rc.SetLiveness(DefaultLiveness())
		if _, err := rc.Reconcile(ctx, a, la, t0.Add(time.Duration(d)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range lb {
		r, _ := l.Get(p.Key)
		if r.Status != posting.Live || r.MissedRuns != 0 {
			t.Fatalf("%s aged by another board's reads: %s missed %d", p.Key, r.Status, r.MissedRuns)
		}
	}
}

// TestMassMarkIsStickyAndClearsOnReturn: a mass mark set by one listing
// survives later ordinary listings, and only the posting's return clears it
// (with its miss stamp).
func TestMassMarkIsStickyAndClearsOnReturn(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("sticky")
	all := make([]posting.Posting, 0, 20)
	for i := 0; i < 20; i++ {
		all = append(all, probePost(t, b, i))
	}
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	recon := func(h int, ps []posting.Posting) Summary {
		rc := NewReconciler(l)
		rc.SetLiveness(DefaultLiveness())
		s, err := rc.Reconcile(ctx, b, ps, at(h))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	recon(0, all)
	s := recon(24, all[:5]) // 15 of 20 absent: mass
	if s.MassMarked != 15 {
		t.Fatalf("mass marked %d, want 15 (%+v)", s.MassMarked, s)
	}
	// The board recovers to 15 listed; postings 15..19 stay absent in what is
	// now an ordinary listing (5 of 20 open absent is 25%: still mass by S, so
	// list 17 to make it ordinary: 3 of 20).
	recon(48, all[:17])
	for i := 17; i < 20; i++ {
		r := rec(t, l, all[i].Key)
		if r.Annotations[MassMissedSinceKey] == "" || r.Status == posting.Dead {
			t.Fatalf("posting %d lost its mass mark or died: %s %v", i, r.Status, r.Annotations)
		}
	}
	// Posting 19 returns: every miss mark clears.
	recon(72, append(slices.Clone(all[:17]), all[19]))
	r := rec(t, l, all[19].Key)
	if r.MissedRuns != 0 || r.Annotations[MassMissedSinceKey] != "" || r.Annotations[MissedSinceKey] != "" {
		t.Fatalf("returned posting kept miss marks: missed %d %v", r.MissedRuns, r.Annotations)
	}
	// Absent again in an ordinary listing: a fresh episode with no mass mark,
	// so the ordinary hold applies.
	recon(96, all[:17])
	recon(120, all[:17])
	if got := rec(t, l, all[19].Key); got.Status != posting.Dead {
		t.Fatalf("posting 19 should die on the ordinary rule after its new episode: %s %v", got.Status, got.Annotations)
	}
	if got := rec(t, l, all[18].Key); got.Status == posting.Dead {
		t.Fatalf("posting 18 (mass marked since hour 24) died before its hold")
	}
}

// TestMissStampsAreAnnotations: the rule keeps its state in pipeline-owned
// annotations, not ledger fields.
func TestMissStampsAreAnnotations(t *testing.T) {
	l := openLedger(t)
	b := probeBoard("ann")
	ps := []posting.Posting{probePost(t, b, 1), probePost(t, b, 2)}
	if _, err := Reconcile(ctx, l, b, ps, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(ctx, l, b, ps[:1], t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := rec(t, l, ps[1].Key)
	if r.Annotations[MissedSinceKey] != t0.Add(time.Hour).UTC().Format(time.RFC3339) {
		t.Fatalf("missed_since = %q", r.Annotations[MissedSinceKey])
	}
	var _ ledger.Record = r
}
