package pipeline

// The exclusion pass lets back an excluded posting only through the pending
// reopen set (pendingreopen.go): a dropped posting is closed, so loosening an
// exclusion must not return every record it frees to Live at once. Each waits,
// still dropped, until its board's full listing shows it again.

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var londonOnly = DropPolicy{OpenRouter: "d1-ultra", Exclude: excludeLocation("London")}

// loosened is the policy after the owner removed the exclusion.
var loosened = DropPolicy{OpenRouter: "d1-ultra"}

func TestApplyExclusionsMarksALoosenedExclusionPendingAndSightingLetsItBack(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	sc := deferScope(s)

	at := t0.Add(time.Hour)
	ch, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc)
	if err != nil || ch.Reopened != 0 || ch.Deferred != 1 || len(ch.Reopen) != 1 {
		t.Fatalf("pass: %+v (%v)", ch, err)
	}
	if got := rec(t, l, k); got.Status != posting.Dropped || !got.LastEvent.Before(at) {
		t.Fatalf("after the pass: %s, last event %v: it must wait, dropped and untouched", got.Status, got.LastEvent)
	}
	if e := s.Items[k]; e.Class != reopenClassExclusion || !e.At.Equal(at) {
		t.Fatalf("pending %+v", s.Items)
	}

	// Marking again adds nothing and keeps the first mark's time.
	ch, err = ApplyExclusions(ctx, l, at.Add(time.Hour), loosened, nil, nil, &sc)
	if err != nil || ch.Deferred != 0 || ch.AlreadyPending != 1 || !s.Items[k].At.Equal(at) {
		t.Fatalf("second pass: %+v (%v)", ch, err)
	}

	// A partial listing is not the board's word.
	if sum, so := sighting(t, l, s, loosened, at.Add(2*time.Hour), false, listedAgain(t, l, k)); sum.Reopened != 0 || so.Applied != 0 || s.Len() != 1 {
		t.Fatalf("partial listing: %+v %+v, %d pending", sum, so, s.Len())
	}
	// The full listing is.
	sum, so := sighting(t, l, s, loosened, at.Add(3*time.Hour), true, listedAgain(t, l, k))
	after := rec(t, l, k)
	if sum.Reopened != 1 || so.Applied != 1 || s.Len() != 0 || after.Status != posting.Live || Excluded(after) {
		t.Fatalf("sighting: %+v %+v, %d pending, %s excluded=%v", sum, so, s.Len(), after.Status, Excluded(after))
	}
}

// Absent from every listing for the whole window, it never comes back: the
// pending reopen is cleared and counted, the record stays dropped. The pass
// then marks it again (the exclusions still do not match it), so a posting its
// board lists later still reopens at that sighting, never before.
func TestApplyExclusionsPendingReopenExpiresAfterFifteenDaysOfAbsence(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	other := ghPost(t, "2", "other jd")
	s := NewPendingReopens()
	sc := deferScope(s)
	at := t0.Add(time.Hour)
	if _, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil || s.Len() != 1 {
		t.Fatalf("mark: %d pending (%v)", s.Len(), err)
	}
	// Listings that leave it out, up to the last minute of the window.
	if _, so := sighting(t, l, s, loosened, at.Add(PendingReopenExpiry-time.Minute), true, other); so.Expired != 0 || s.Len() != 1 {
		t.Fatalf("inside the window: %+v, %d pending", so, s.Len())
	}
	_, so := sighting(t, l, s, loosened, at.Add(15*24*time.Hour), true, other)
	if so.Expired != 1 || s.Len() != 0 || so.Applied != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("after 15 days: %+v, %d pending, %s", so, s.Len(), rec(t, l, k).Status)
	}
	// Marked again by the next pass; reopens only if its board lists it.
	at = at.Add(16 * 24 * time.Hour)
	if ch, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil || ch.Deferred != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("re-mark: %+v (%v)", ch, err)
	}
	if sum, _ := sighting(t, l, s, loosened, at.Add(time.Hour), true, listedAgain(t, l, k)); sum.Reopened != 1 {
		t.Fatalf("sighting after the re-mark: %+v", sum)
	}
}

// The rule edited back and forth: restored before the board lists the posting,
// the pass takes it off the set; restored after the pass marked it and before a
// listing, the sighting judges it again and leaves it dropped.
func TestApplyExclusionsRuleEditedBackAndForth(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	sc := deferScope(s)
	at := t0.Add(time.Hour)

	apply := func(p DropPolicy) ExclusionChange {
		t.Helper()
		at = at.Add(time.Hour)
		ch, err := ApplyExclusions(ctx, l, at, p, nil, nil, &sc)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	if ch := apply(loosened); ch.Deferred != 1 {
		t.Fatalf("loosen: %+v", ch)
	}
	if ch := apply(londonOnly); ch.Unqueued != 1 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("restore: %+v, %d pending, %s", ch, s.Len(), rec(t, l, k).Status)
	}
	if ch := apply(loosened); ch.Deferred != 1 || s.Len() != 1 {
		t.Fatalf("loosen again: %+v", ch)
	}
	// Restored after the mark, before the pass sees it: the board lists it.
	sum, so := sighting(t, l, s, londonOnly, at.Add(time.Hour), true, listedAgain(t, l, k))
	if sum.Reopened != 0 || so.Cleared != 1 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("sighting under the restored rule: %+v %+v, %d pending, %s", sum, so, s.Len(), rec(t, l, k).Status)
	}
}

// The posting's text moved after the mark: the sighting judges the listing's.
func TestApplyExclusionsSightingJudgesTheListedText(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	sc := deferScope(s)
	if _, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), loosened, nil, nil, &sc); err != nil {
		t.Fatal(err)
	}
	// The exclusion is back, and the board's text is still what it excludes.
	if sum, so := sighting(t, l, s, londonOnly, t0.Add(2*time.Hour), true, listedAgain(t, l, k)); sum.Reopened != 0 || so.Cleared != 1 {
		t.Fatalf("%+v %+v", sum, so)
	}
	// A different exclusion that the listed text (now Paris) matches.
	if _, err := ApplyExclusions(ctx, l, t0.Add(3*time.Hour), loosened, nil, nil, &sc); err != nil {
		t.Fatal(err)
	}
	if sum, so := sighting(t, l, s, DropPolicy{Exclude: excludeLocation("Paris")}, t0.Add(4*time.Hour), true, moved(t, l, k, "Paris")); sum.Reopened != 0 || so.Cleared != 1 {
		t.Fatalf("listed text still excluded: %+v %+v", sum, so)
	}
}

// A stale stored location must not keep the pass marking the same drop every
// run: stored as London under a rule that now excludes Paris, the board lists
// Paris. The sighting keeps it dropped on the listed text and stores Paris, so
// the next pass sees an exclusion that matches and marks nothing.
func TestApplyExclusionsDoesNotRemarkADropWhoseListedFieldsNowMatch(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	sc := deferScope(s)
	paris := DropPolicy{Exclude: excludeLocation("Paris")}
	if ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), paris, nil, nil, &sc); err != nil || ch.Deferred != 1 {
		t.Fatalf("mark: %+v (%v)", ch, err)
	}
	if sum, so := sighting(t, l, s, paris, t0.Add(2*time.Hour), true, moved(t, l, k, "Paris")); sum.Reopened != 0 || so.Cleared != 1 || s.Len() != 0 {
		t.Fatalf("sighting: %+v %+v, %d pending", sum, so, s.Len())
	}
	ch, err := ApplyExclusions(ctx, l, t0.Add(3*time.Hour), paris, nil, nil, &sc)
	if err != nil || len(ch.Reopen) != 0 || ch.Deferred != 0 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("next pass: %+v (%v), %d pending", ch, err, s.Len())
	}
}

// A posting on a board no longer watched has no listing that will ever show
// it: it stays dropped and nothing is marked. A weakly keyed one (an inbox
// page posting) has no full listing to wait for and comes back at once.
func TestApplyExclusionsUnwatchedStaysDroppedAndWeakKeyReopensAtOnce(t *testing.T) {
	l := openLedger(t)
	watchedKey := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	weak, err := posting.FallbackKey("Acme", "Engineer", "London")
	if err != nil {
		t.Fatal(err)
	}
	wp := posting.Posting{Key: weak, ATS: posting.Other, Company: "Acme", Title: "Engineer", Location: "London", Description: "jd w"}
	for _, e := range []ledger.Event{
		{V: ledger.SchemaVersion, At: t0, Key: weak, Status: posting.Live, Reason: "new", Posting: &wp},
		{V: ledger.SchemaVersion, At: t0.Add(time.Minute), Key: weak, Status: posting.Dropped, Annotations: map[string]string{AnnExcluded: "excluded: location matches London"}, Reason: "excluded"},
	} {
		if err := l.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	s := NewPendingReopens()
	sc := deferScope(s)
	sc.Watched = func(posting.Key) bool { return false }
	sc.Defer = sc.Watched
	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), loosened, nil, nil, &sc)
	if err != nil || ch.Stale != 1 || ch.Reopened != 1 || ch.Deferred != 0 || s.Len() != 0 {
		t.Fatalf("%+v (%v), %d pending", ch, err, s.Len())
	}
	if rec(t, l, watchedKey).Status != posting.Dropped || rec(t, l, weak).Status != posting.Live {
		t.Fatalf("statuses %s %s", rec(t, l, watchedKey).Status, rec(t, l, weak).Status)
	}
}

// With no scope there is no listing to wait for: the pass lets back at once.
func TestApplyExclusionsWithoutAScopeReopensAtOnce(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), loosened, nil, nil, nil)
	if err != nil || ch.Reopened != 1 || rec(t, l, k).Status != posting.Live {
		t.Fatalf("%+v (%v), %s", ch, err, rec(t, l, k).Status)
	}
}

// A scope with nowhere to keep the marks is refused before anything is written.
func TestApplyExclusionsRefusesAScopeWithoutAPendingSet(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	sc := reviewScope()
	sc.Defer = sc.Watched
	if _, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), loosened, nil, nil, &sc); err == nil {
		t.Fatal("a deferring scope with no pending set was accepted")
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("the refused pass reopened a record")
	}
}

// The set is bounded: a mark beyond it is counted, never lost silently, and
// one already in the set is not over the bound.
func TestApplyExclusionsPendingSetIsBounded(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	for i := range MaxPendingReopens {
		kk, _ := posting.GreenhouseKey("filler", fmt.Sprint(i))
		s.Items[kk] = PendingReopen{Class: reopenClassTitle, At: t0}
	}
	sc := deferScope(s)
	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), loosened, nil, nil, &sc)
	if err != nil || ch.OverCap != 1 || ch.Deferred != 0 || s.Len() != MaxPendingReopens || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("%+v (%v), %d pending", ch, err, s.Len())
	}
}

// Dropped by an exclusion after a model kept it: the model's reading is
// evidence on the drop. It waits like any other, and the sighting returns it
// with the triage forgotten, to be read again.
func TestApplyExclusionsPendingReopenOfADropThatKeepsAModelReading(t *testing.T) {
	r, _, l, keys := excludeRunner(t, "Data Intern")
	r.Policy.Exclude = nil
	if err := r.Advance(ctx, keys[0]); err != nil {
		t.Fatal(err)
	}
	s := NewPendingReopens()
	sc := deferScope(s)
	if ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), DropPolicy{Exclude: excludeTitle("Intern")}, nil, nil, &sc); err != nil || len(ch.Drop) != 1 {
		t.Fatalf("drop: %+v (%v)", ch, err)
	}
	if got := rec(t, l, keys[0]); !Excluded(got) || got.Annotations["triage.role_fit"] != "strong_fit" {
		t.Fatalf("setup: %v", got.Annotations)
	}
	if ch, err := ApplyExclusions(ctx, l, t0.Add(2*time.Hour), DropPolicy{}, nil, nil, &sc); err != nil || ch.Deferred != 1 {
		t.Fatalf("loosen: %+v (%v)", ch, err)
	}
	if sum, _ := sighting(t, l, s, DropPolicy{}, t0.Add(3*time.Hour), true, listedAgain(t, l, keys[0])); sum.Reopened != 1 {
		t.Fatalf("sighting: %+v", sum)
	}
	if got := rec(t, l, keys[0]); got.Status != posting.Live || got.Annotations["triage.role_fit"] != "" {
		t.Fatalf("after: %s %v", got.Status, got.Annotations)
	}
}

// The new class survives the set's file round trip.
func TestPendingReopenExclusionClassRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	k, _ := posting.GreenhouseKey("acme", "1")
	s := NewPendingReopens()
	s.Items[k] = PendingReopen{Class: reopenClassExclusion, At: t0}
	if err := s.Save(path, t0); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPendingReopens(path, t0.Add(time.Hour))
	if err != nil || got.Items[k].Class != reopenClassExclusion {
		t.Fatalf("%+v (%v)", got, err)
	}
}
