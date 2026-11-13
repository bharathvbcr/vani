package pipeline

// Pending-reopen expiry counts read time (pendingreopen.go SightingReopen.expire),
// not calendar time since the mark: a clock that jumped, a board that failed
// for days and a board whose aging is frozen for events ahead of the clock do
// not clear entries; an entry that did expire unseen is not marked again by
// the exclusion pass until a full listing shows it.

import (
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// leaveOut reads the board in full each day for n days from start, listing
// only other, and returns the last sighting.
func leaveOut(t *testing.T, l ledger.Ledger, s *PendingReopens, p DropPolicy, start time.Time, n int, other posting.Posting) *SightingReopen {
	t.Helper()
	var so *SightingReopen
	for d := 0; d < n; d++ {
		_, so = sighting(t, l, s, p, start.Add(time.Duration(d)*24*time.Hour), true, other)
	}
	return so
}

// A clock that jumped 20 days ahead for one read clears nothing: the read adds
// at most ClockWindow of read time. (Before: one read past the 14 days from the
// mark cleared every entry on the board.)
func TestPendingReopenSurvivesAClockJump(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	other := ghPost(t, "2", "other jd")
	s := NewPendingReopens()
	sc := deferScope(s)
	at := t0.Add(time.Hour)
	if _, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil || s.Len() != 1 {
		t.Fatalf("mark: %d (%v)", s.Len(), err)
	}
	sighting(t, l, s, loosened, at.Add(24*time.Hour), true, other) // a right read starts its count
	for _, jump := range []time.Duration{20 * 24 * time.Hour, 365 * 24 * time.Hour} {
		if _, so := sighting(t, l, s, loosened, at.Add(24*time.Hour+jump), true, other); so.Expired != 0 || s.Len() != 1 {
			t.Fatalf("a read %s after the last one cleared the entry: %+v", jump, so)
		}
	}
	if rec(t, l, k).Status != posting.Dropped {
		t.Fatal("reopened")
	}
	// The jump does not stall the entry: right-clock daily reads count again
	// (the first, behind the jump's read, counts nothing) and it expires.
	var expired int
	for d := 2; d < 40 && s.Len() == 1; d++ {
		_, so := sighting(t, l, s, loosened, at.Add(time.Duration(d)*24*time.Hour), true, other)
		expired += so.Expired
	}
	if expired != 1 || s.GoneLen() != 1 {
		t.Fatalf("after the jump the entry never expired through reads: %d expired, %d pending, %d gone", expired, s.Len(), s.GoneLen())
	}
}

// A board that failed for two weeks counts nothing: one read afterwards adds at
// most ClockWindow, so the entry waits another 14 days of reads.
func TestPendingReopenExpiryCountsOnlyReadTime(t *testing.T) {
	l := openLedger(t)
	excludedDrop(t, l, "1", "London", excludeLocation("London"))
	other := ghPost(t, "2", "other jd")
	s := NewPendingReopens()
	sc := deferScope(s)
	at := t0.Add(time.Hour)
	if _, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil {
		t.Fatal(err)
	}
	so := leaveOut(t, l, s, loosened, at.Add(24*time.Hour), 3, other)
	// No reads for 30 days, then daily ones.
	resume := at.Add(30 * 24 * time.Hour)
	if so = leaveOut(t, l, s, loosened, resume, 1, other); so.Expired != 0 || s.Len() != 1 {
		t.Fatalf("after a 30-day outage: %+v, %d pending", so, s.Len())
	}
	if so = leaveOut(t, l, s, loosened, resume.Add(24*time.Hour), 8, other); so.Expired != 0 {
		t.Fatalf("expired with about 13 days of read time: %+v", so)
	}
	var expired int
	for d := 9; d < 20 && s.Len() == 1; d++ {
		_, so = sighting(t, l, s, loosened, resume.Add(time.Duration(d)*24*time.Hour), true, other)
		expired += so.Expired
	}
	if expired != 1 || s.Len() != 0 {
		t.Fatalf("never expired through daily reads: %d expired, %d pending", expired, s.Len())
	}
}

// A board whose aging is frozen for events stamped ahead of the clock does not
// expire its pending reopens: the clock is not trusted to age postings, so it
// is not trusted to clear them either. Once the clock passes the events, the
// count goes on.
func TestPendingReopenDoesNotExpireOnAFrozenBoard(t *testing.T) {
	l := openLedger(t)
	excludedDrop(t, l, "1", "London", excludeLocation("London"))
	other := ghPost(t, "2", "other jd")
	s := NewPendingReopens()
	sc := deferScope(s)
	at := t0.Add(time.Hour)
	if _, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil {
		t.Fatal(err)
	}
	leaveOut(t, l, s, loosened, at.Add(24*time.Hour), 2, other)
	// Every read finds an event of the board's own 10 hours ahead of its clock.
	start := at.Add(30 * 24 * time.Hour)
	ahead := ghPost(t, "3", "ahead jd")
	for d := 0; d < 20; d++ {
		now := start.Add(time.Duration(d) * 24 * time.Hour)
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: now.Add(10 * time.Hour), Key: ahead.Key,
			Status: posting.Live, Posting: &ahead}); err != nil {
			t.Fatal(err)
		}
		if _, so := sighting(t, l, s, loosened, now, true, other, ahead); so.Expired != 0 || s.Len() != 1 {
			t.Fatalf("day %d: a frozen board expired an entry: %+v", d, so)
		}
	}
	// The clock is right again: the count resumes and the entry expires.
	var expired int
	for d := 20; d < 40 && s.Len() == 1; d++ {
		_, so := sighting(t, l, s, loosened, start.Add(time.Duration(d)*24*time.Hour), true, other, ahead)
		expired += so.Expired
	}
	if expired != 1 {
		t.Fatalf("the entry never expired once the board was not frozen: %d pending", s.Len())
	}
}

// An entry that expired unseen is not marked again by the exclusion pass (its
// board has not listed it for two weeks); it is counted, and a full listing
// that shows it brings it back to be judged at once.
func TestExpiredExclusionIsNotMarkedAgainUntilListed(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	other := ghPost(t, "2", "other jd")
	s := NewPendingReopens()
	sc := deferScope(s)
	at := t0.Add(time.Hour)
	if _, err := ApplyExclusions(ctx, l, at, loosened, nil, nil, &sc); err != nil {
		t.Fatal(err)
	}
	var expired int
	for d := 1; d <= 20 && s.Len() == 1; d++ {
		_, so := sighting(t, l, s, loosened, at.Add(time.Duration(d)*24*time.Hour), true, other)
		expired += so.Expired
	}
	if expired != 1 || s.Len() != 0 || s.GoneLen() != 1 {
		t.Fatalf("setup: %d expired, %d pending, %d gone", expired, s.Len(), s.GoneLen())
	}
	// The next passes, however many, mark nothing.
	for i := 0; i < 3; i++ {
		later := at.Add(time.Duration(30+14*i) * 24 * time.Hour)
		ch, err := ApplyExclusions(ctx, l, later, loosened, nil, nil, &sc)
		if err != nil || ch.Deferred != 0 || ch.Gone != 1 || s.Len() != 0 || rec(t, l, k).Status != posting.Dropped {
			t.Fatalf("pass %d: %+v (%v), %d pending", i, ch, err, s.Len())
		}
	}
	// It survives the file.
	path := t.TempDir() + "/pending.json"
	if err := s.Save(path, at); err != nil {
		t.Fatal(err)
	}
	back, err := LoadPendingReopens(path, at)
	if err != nil || back.GoneLen() != 1 {
		t.Fatalf("round trip: %d gone (%v)", back.GoneLen(), err)
	}
	// Listed again, it is judged against the loosened exclusions at once.
	sum, _ := sighting(t, l, s, loosened, at.Add(90*24*time.Hour), true, listedAgain(t, l, k), other)
	if sum.Reopened != 1 || s.GoneLen() != 0 || rec(t, l, k).Status != posting.Live {
		t.Fatalf("sighting after expiry: %+v, %d gone, %s", sum, s.GoneLen(), rec(t, l, k).Status)
	}
}

// Exclusion marks cannot fill the set: past MaxPendingExclusions they are
// OverCap, and a title or model mark still finds room.
func TestExclusionMarksCannotStarveTitleAndModelMarks(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	for i := range MaxPendingExclusions {
		kk, _ := posting.GreenhouseKey("filler", fmt.Sprint(i))
		s.Items[kk] = PendingReopen{Class: reopenClassExclusion, At: t0}
	}
	sc := deferScope(s)
	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), loosened, nil, nil, &sc)
	if err != nil || ch.OverCap != 1 || ch.Deferred != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("%+v (%v)", ch, err)
	}
	// Room is left for the other classes.
	if s.Len() >= MaxPendingReopens-100 {
		t.Fatalf("exclusions take %d of %d", s.Len(), MaxPendingReopens)
	}
	_, _, mk := modelDrop(t, l, "9", staffAns(0.95), staffRules)
	got, err := RetriagePolicyDrops(ctx, l, t0.Add(2*time.Hour), pol(keptStaff), deferScope(s), false)
	if err != nil || got.OverCap != 0 || len(got.Deferred) != 1 || got.Deferred[0] != mk {
		t.Fatalf("a model mark after the exclusion marks: %+v (%v)", got, err)
	}
}
