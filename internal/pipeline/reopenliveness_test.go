package pipeline

// The pending reopen and the death rule meet where a dropped posting that was
// in an absence episode (missed, maybe mass held) when it was dropped is listed
// again with a pending reopen. A listing that shows a posting ends its absence
// episode (liveness.go), whichever way the reopen goes: reopened, its miss marks
// are gone; judged again and left dropped, the listing still records that it
// reappeared. Otherwise a later reopen would carry the old episode, and the
// first listing that missed it again would kill it at once.

import (
	"strconv"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// heldWhenDropped records on k, dropped, the miss state of an absence episode
// that began at first: missed runs, its stamp, and a mass mark when mass.
func heldWhenDropped(t *testing.T, l ledger.Ledger, k posting.Key, misses int, first time.Time, mass bool) {
	t.Helper()
	ann := map[string]string{ledger.MissedRunsKey: strconv.Itoa(misses), MissedSinceKey: stampOf(first)}
	if mass {
		ann[MassMissedSinceKey] = stampOf(first)
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: first.Add(20 * time.Minute), Key: k, Status: posting.Dropped,
		Annotations: ann, Reason: "absent from board, then dropped"}); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, k); r.Status != posting.Dropped || r.MissedRuns != misses {
		t.Fatalf("setup: %+v", r)
	}
}

func noMissMarks(t *testing.T, l ledger.Ledger, k posting.Key) {
	t.Helper()
	r := rec(t, l, k)
	if r.MissedRuns != 0 || r.Annotations[MissedSinceKey] != "" || r.Annotations[MassMissedSinceKey] != "" {
		t.Fatalf("the listing that showed %s did not end its absence episode: %+v", k, r)
	}
}

// Reopened at the sighting while mass held: no miss marks, no mass hold, and a
// later absence is a new, ordinary episode stamped at its own first miss.
func TestReopenAtSightingOfAMassHeldDropEndsItsEpisode(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	heldWhenDropped(t, l, k, 3, t0.Add(10*time.Minute), true)
	s := NewPendingReopens()
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false); err != nil || s.Len() != 1 {
		t.Fatalf("retriage: %d pending, %v", s.Len(), err)
	}
	if sum, so := sighting(t, l, s, pol(keptStaff), t0.Add(2*time.Hour), true, listedAgain(t, l, k)); sum.Reopened != 1 || so.Applied != 1 {
		t.Fatalf("sighting: %+v %+v", sum, so)
	}
	noMissMarks(t, l, k)
	if held := MassHeld(l.All(), Liveness{}, t0.Add(2*time.Hour)); len(held) != 0 {
		t.Fatalf("a reopened posting is still reported in a mass hold: %+v", held)
	}
	other := ghPost(t, "2", "other jd")
	at := t0.Add(3 * time.Hour)
	if _, err := NewReconciler(l).Reconcile(ctx, board, []posting.Posting{other}, at); err != nil {
		t.Fatal(err)
	}
	r := rec(t, l, k)
	if r.Status == posting.Dead || r.MissedRuns != 1 || r.Annotations[MissedSinceKey] != stampOf(at) || r.Annotations[MassMissedSinceKey] != "" {
		t.Fatalf("the next absence is not a new ordinary episode: %+v", r)
	}
}

// Judged again at the sighting and left dropped: the listing still ends the
// episode. A later reopen (here the retriage that reopens at once) then
// starts from no misses, so the next listing that leaves it out is its first
// miss, not its second 20 hours after a stale stamp.
func TestDropLeftDroppedAtSightingStillEndsItsEpisode(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	heldWhenDropped(t, l, k, 1, t0.Add(10*time.Minute), false)
	s := NewPendingReopens()
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false); err != nil || s.Len() != 1 {
		t.Fatalf("retriage: %d pending, %v", s.Len(), err)
	}
	sum, so := sighting(t, l, s, pol(staffRules), t0.Add(2*time.Hour), true, listedAgain(t, l, k))
	if sum.Reopened != 0 || so.Cleared != 1 || sum.Reappeared != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("sighting: %+v %+v", sum, so)
	}
	noMissMarks(t, l, k)
	if got, err := RetriagePolicyDrops(ctx, l, t0.Add(3*time.Hour), pol(keptStaff), reviewScope(), false); err != nil || len(got.Model) != 1 {
		t.Fatalf("reopen at once: %+v %v", got, err)
	}
	other := ghPost(t, "2", "other jd")
	if _, err := NewReconciler(l).Reconcile(ctx, board, []posting.Posting{other}, t0.Add(21*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, k); r.Status == posting.Dead || r.MissedRuns != 1 {
		t.Fatalf("listed at hour 2, killed by its first miss at hour 21: %+v", r)
	}
}
