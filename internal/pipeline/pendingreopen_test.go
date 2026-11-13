package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// decide2 is a decision-model answer reading role_fit as roleFit (mid level).
func decide2(roleFit string) decide.Answer {
	return decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"seniority": jev("mid", 0.95), "role_fit": jev(roleFit, 0.95)}}
}

// deferScope is the scope the retriage command passes: every test board is
// watched, so each reopen waits for the board's next full listing, kept in s.
func deferScope(s *PendingReopens) ReopenScope {
	sc := reviewScope()
	sc.Defer = sc.Watched
	sc.Pending = s
	return sc
}

// sighting reconciles board's full listing of ps at at, applying the pending
// reopens in s under p.
func sighting(t *testing.T, l ledger.Ledger, s *PendingReopens, p DropPolicy, at time.Time, full bool, ps ...posting.Posting) (Summary, *SightingReopen) {
	t.Helper()
	rc := NewReconciler(l)
	so := &SightingReopen{Policy: p, Scope: reviewScope(), Pending: s}
	rc.ReopenOnSighting(so)
	var sum Summary
	var err error
	if full {
		sum, err = rc.Reconcile(ctx, board, ps, at)
	} else {
		sum, err = rc.ReconcilePartial(ctx, board, ps, at)
	}
	if err != nil {
		t.Fatal(err)
	}
	return sum, so
}

// The posting a board lists again for k: its stored posting.
func listedAgain(t *testing.T, l ledger.Ledger, k posting.Key) posting.Posting {
	t.Helper()
	return ledger.Full(l, rec(t, l, k)).Posting
}

// The point of the pending reopen: a drop the rules no longer make is not put
// back at once (a closed posting would be advanced, scored and paid for
// before any listing could age it); it waits, still dropped, for its board to
// list it again, and comes back then.
func TestPendingReopenWaitsForItsBoardToListItAgain(t *testing.T) {
	l := openLedger(t)
	_, ft, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	s := NewPendingReopens()

	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false)
	if err != nil || !slices.Equal(got.Model, []posting.Key{k}) || !slices.Equal(got.Deferred, []posting.Key{k}) {
		t.Fatalf("retriage: %+v (%v)", got, err)
	}
	if st := rec(t, l, k).Status; st != posting.Dropped || s.Len() != 1 {
		t.Fatalf("after marking: status %s, %d pending; want dropped and 1 pending", st, s.Len())
	}

	// Marking again adds nothing.
	got, err = RetriagePolicyDrops(ctx, l, t0.Add(2*time.Hour), pol(keptStaff), deferScope(s), false)
	if err != nil || got.AlreadyPending != 1 || len(got.Deferred) != 0 {
		t.Fatalf("second mark: %+v (%v)", got, err)
	}

	// A partial listing (a scraped page) is not the board's word: nothing applies.
	if sum, so := sighting(t, l, s, pol(keptStaff), t0.Add(3*time.Hour), false, listedAgain(t, l, k)); sum.Reopened != 0 || so.Applied != 0 {
		t.Fatalf("partial listing applied a reopen: %+v %+v", sum, so)
	}
	if rec(t, l, k).Status != posting.Dropped || s.Len() != 1 {
		t.Fatal("partial listing changed the pending reopen")
	}

	// The board lists it: it comes back on its stored reading, no model asked.
	sum, so := sighting(t, l, s, pol(keptStaff), t0.Add(4*time.Hour), true, listedAgain(t, l, k))
	if sum.Reopened != 1 || so.Applied != 1 || s.Len() != 0 || !so.Changed() {
		t.Fatalf("sighting: %+v %+v, %d pending", sum, so, s.Len())
	}
	if after := rec(t, l, k); after.Status != posting.Triaged || after.Annotations["triage.seniority"] != "staff_plus" {
		t.Fatalf("after sighting: %s %v", after.Status, after.Annotations)
	}
	if ft.calls != 1 {
		t.Fatalf("%d backend calls, want the original 1", ft.calls)
	}
}

// A posting that never comes back costs nothing and stays dropped; once its
// board has listed without it for PendingReopenExpiry, the pending reopen is
// cleared and counted.
func TestPendingReopenExpiresWhenItsBoardNoLongerListsIt(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	other := ghPost(t, "2", "other jd")
	s := NewPendingReopens()
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false); err != nil {
		t.Fatal(err)
	}
	// Daily listings without it: the first starts the count, so 14 days of read
	// time need the read 14 days after it. Inside the window: still pending.
	for d := 1; d <= 14; d++ {
		if _, so := sighting(t, l, s, pol(keptStaff), t0.Add(time.Hour+time.Duration(d)*24*time.Hour), true, other); so.Expired != 0 || s.Len() != 1 {
			t.Fatalf("day %d, inside the window: %+v, %d pending", d, so, s.Len())
		}
	}
	// Past it: cleared, counted, still dropped.
	_, so := sighting(t, l, s, pol(keptStaff), t0.Add(time.Hour+15*24*time.Hour), true, other)
	if so.Expired != 1 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("past the window: %+v, %d pending, status %s", so, s.Len(), rec(t, l, k).Status)
	}
}

// The reopen is judged again when the board lists the posting: a rule put
// back between the mark and the sighting keeps it dropped (and clears the
// pending reopen), as a fresh triage would.
func TestPendingReopenIsJudgedAgainAtTheSighting(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	s := NewPendingReopens()
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false); err != nil {
		t.Fatal(err)
	}
	sum, so := sighting(t, l, s, pol(staffRules), t0.Add(2*time.Hour), true, listedAgain(t, l, k))
	if sum.Reopened != 0 || so.Cleared != 1 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("rule restored: %+v %+v, %d pending, status %s", sum, so, s.Len(), rec(t, l, k).Status)
	}
}

// A pending model drop whose listing moved its title is put back to Live for
// a fresh read at the sighting, not on a reading of other text.
func TestPendingReopenWithMovedTextGoesLive(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	s := NewPendingReopens()
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false); err != nil {
		t.Fatal(err)
	}
	p := listedAgain(t, l, k)
	p.Location = "Austin, TX"
	sum, _ := sighting(t, l, s, pol(keptStaff), t0.Add(2*time.Hour), true, p)
	after := rec(t, l, k)
	if sum.Reopened != 1 || after.Status != posting.Live || after.Annotations["triage.seniority"] != "" || after.Posting.Location != "Austin, TX" {
		t.Fatalf("moved text: %+v, %s %v %q", sum, after.Status, after.Annotations, after.Posting.Location)
	}
}

// A title drop waits too, and goes back to Live at the sighting.
func TestPendingReopenOfATitleDrop(t *testing.T) {
	r, _, l, k := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	kept := r.Policy
	kept.Rules = keptStaff
	s := NewPendingReopens()
	sc := deferScope(s)
	sc.TitleOnly = true
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, sc, false)
	if err != nil || !slices.Equal(got.Title, []posting.Key{k}) || len(got.Deferred) != 1 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("mark: %+v (%v)", got, err)
	}
	if sum, _ := sighting(t, l, s, kept, t0.Add(2*time.Hour), true, listedAgain(t, l, k)); sum.Reopened != 1 || rec(t, l, k).Status != posting.Live {
		t.Fatalf("sighting: %+v, status %s", sum, rec(t, l, k).Status)
	}
}

// An inbox posting has no full listing to wait for: it is reopened at once,
// as before (finding A), even under a deferring scope.
func TestPendingReopenInboxPostingReopensAtOnce(t *testing.T) {
	l := openLedger(t)
	r, k := inboxTitleDrop(t, l, "Staff Software Engineer")
	kept := r.Policy
	kept.Rules = keptStaff
	s := NewPendingReopens()
	sc := deferScope(s)
	sc.Watched = func(posting.Key) bool { return false }
	sc.Defer = sc.Watched
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), kept, sc, false)
	if err != nil || !slices.Equal(got.Title, []posting.Key{k}) || len(got.Deferred) != 0 || s.Len() != 0 {
		t.Fatalf("inbox: %+v (%v), %d pending", got, err, s.Len())
	}
	if rec(t, l, k).Status != posting.Live {
		t.Fatalf("status %s, want live", rec(t, l, k).Status)
	}
}

// The owner's re-read: a model drop whose reading cannot be judged (made
// under another model id) and that no current rule would drop on its value,
// were the reading trusted, goes back for a fresh read. One a current rule
// would still drop on its value is left alone.
func TestRefreshUnjudgedRereadsOnlyWhatTheRulesNoLongerName(t *testing.T) {
	l := openLedger(t)
	_, _, staff := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	_, _, domain := modelDrop(t, l, "2", decide2("mismatch_domain"), staffRules)
	for _, k := range []posting.Key{staff, domain} {
		rewriteReading(t, l, k, map[string]string{AnnTriageModel: "old-model"})
	}
	sc := reviewScope()
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), sc, true)
	if err != nil || got.Unjudged != 2 || len(got.Refresh) != 0 {
		t.Fatalf("without the flag: %+v (%v)", got, err)
	}
	sc.RefreshUnjudged = true
	got, err = RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), sc, false)
	if err != nil || !slices.Equal(got.Refresh, []posting.Key{staff}) || got.Unjudged != 1 {
		t.Fatalf("refresh: %+v (%v)", got, err)
	}
	if rec(t, l, staff).Status != posting.Live || rec(t, l, domain).Status != posting.Dropped {
		t.Fatalf("statuses %s %s", rec(t, l, staff).Status, rec(t, l, domain).Status)
	}
}

// The re-read is capped: at most MaxRefreshUnjudged per run, the rest counted.
func TestRefreshUnjudgedIsCapped(t *testing.T) {
	l := openLedger(t)
	keys := manyDrops(t, l, MaxRefreshUnjudged+3)
	for _, k := range keys {
		rewriteReading(t, l, k, map[string]string{AnnTriageModel: "old-model"})
	}
	sc := reviewScope()
	sc.RefreshUnjudged = true
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), sc, true)
	if err != nil || len(got.Refresh) != MaxRefreshUnjudged || got.OverCap != 3 {
		t.Fatalf("refresh %d, over cap %d (%v)", len(got.Refresh), got.OverCap, err)
	}
}

// The pending set is bounded: past MaxPendingReopens nothing more is added,
// and the overflow is counted.
func TestPendingReopensAreBounded(t *testing.T) {
	l := openLedger(t)
	keys := manyDrops(t, l, 5)
	s := NewPendingReopens()
	for i := range MaxPendingReopens - 2 {
		s.Items[posting.Key(fmt.Sprintf("gh:other:%d", i))] = PendingReopen{Class: "model", At: t0}
	}
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false)
	if err != nil || len(got.Deferred) != 2 || got.OverCap != 3 || s.Len() != MaxPendingReopens {
		t.Fatalf("deferred %d, over cap %d, pending %d (%v); keys %d", len(got.Deferred), got.OverCap, s.Len(), err, len(keys))
	}
}

// A dry run reports what it would defer and changes neither the ledger nor
// the pending set.
func TestPendingReopenDryRunWritesNothing(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	s := NewPendingReopens()
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), true)
	if err != nil || !slices.Equal(got.Deferred, []posting.Key{k}) || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("dry run: %+v (%v), %d pending", got, err, s.Len())
	}
}

// A deferring scope with nowhere to keep the pending reopens is refused, not
// run as an immediate reopen.
func TestPendingReopenNeedsAStore(t *testing.T) {
	sc := reviewScope()
	sc.Defer = sc.Watched
	if _, err := RetriagePolicyDrops(ctx, openLedger(t), t0, pol(keptStaff), sc, false); err == nil {
		t.Fatal("a deferring scope without a store ran")
	}
}

// Cancelled part way through a sighting listing: what was applied is applied,
// the rest stays pending.
func TestPendingReopenSightingCancelled(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	s := NewPendingReopens()
	if _, err := RetriagePolicyDrops(ctx, l, t0.Add(time.Hour), pol(keptStaff), deferScope(s), false); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	rc := NewReconciler(l)
	rc.ReopenOnSighting(&SightingReopen{Policy: pol(keptStaff), Scope: reviewScope(), Pending: s})
	if _, err := rc.Reconcile(c, board, []posting.Posting{listedAgain(t, l, k)}, t0.Add(2*time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}
	if rec(t, l, k).Status != posting.Dropped || s.Len() != 1 {
		t.Fatalf("after cancel: %s, %d pending", rec(t, l, k).Status, s.Len())
	}
}

// The store round-trips, keeps old entries on load whatever the clock, refuses an oversized or malformed file, and
// treats a missing file as empty.
func TestPendingReopensFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reopen", "pending.json")
	s, err := LoadPendingReopens(path, t0)
	if err != nil || s.Len() != 0 {
		t.Fatalf("missing file: %d (%v)", s.Len(), err)
	}
	s.Items["gh:acme:1"] = PendingReopen{Class: "model", At: t0, Reason: "r"}
	s.Items["gh:acme:2"] = PendingReopen{Class: "title", At: t0.Add(-3 * PendingReopenExpiry), Reason: "old"}
	if err := s.Save(path, t0); err != nil {
		t.Fatal(err)
	}
	// Changed 2026-11-12: nothing is cleared for age on load, whatever the clock
	// says (a clock 28+ days ahead used to wipe the whole set, ledger-wide).
	back, err := LoadPendingReopens(path, t0.Add(365*24*time.Hour))
	if err != nil || back.Len() != 2 || back.Expired != 0 || back.Items["gh:acme:1"].Class != "model" {
		t.Fatalf("round trip: %+v (%v)", back, err)
	}
	// A board that left the watchlist is cleared, clock-free.
	if n := back.ClearUnwatched(func(k posting.Key) bool { return k != "gh:acme:2" }); n != 1 || back.Len() != 1 || back.Expired != 1 {
		t.Fatalf("ClearUnwatched: %d cleared, %+v", n, back)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPendingReopens(path, t0); err == nil {
		t.Fatal("malformed file loaded")
	}
	if err := os.WriteFile(path, make([]byte, maxPendingReopenBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPendingReopens(path, t0); err == nil {
		t.Fatal("oversized file loaded")
	}
	bad := NewPendingReopens()
	bad.Items["gh:acme:1"] = PendingReopen{Class: "bogus", At: t0}
	if err := bad.Save(path, t0); err == nil {
		t.Fatal("saved an entry of an unknown class")
	}
}
