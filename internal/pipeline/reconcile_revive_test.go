package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func reviveDay(n int) time.Time { return t0.Add(time.Duration(n) * 24 * time.Hour) }

// reviveWorld lists one role on two ATSes at t0: the Lever record is the
// duplicate of the Greenhouse one. The Lever record also carries near-duplicate
// annotations, as AnnotateNearDuplicates leaves on one it demoted.
func reviveWorld(t *testing.T) (l *ledger.File, gh, lv posting.Posting) {
	t.Helper()
	l = openLedger(t)
	gh = ghJob(t, "1", "Staff Engineer", "Remote", "")
	lv = lvJob(t, "a1", "Staff Engineer", "Remote", "")
	reconcileOne(t, l, board, t0, gh)
	reconcileOne(t, l, lvBoard, t0, lv)
	wantDuplicate(t, l, lv.Key, gh.Key)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: lv.Key, Status: posting.Duplicate,
		Annotations: map[string]string{AnnDupMethod: DupMethodShingle, AnnDupScore: "0.95"}, Reason: "near-duplicate"}); err != nil {
		t.Fatal(err)
	}
	return l, gh, lv
}

func wantRevived(t *testing.T, l ledger.Ledger, k posting.Key, survivor posting.Key) {
	t.Helper()
	r := rec(t, l, k)
	if r.Status != posting.Live {
		t.Fatalf("%s: %s, want live", k, r.Status)
	}
	for _, a := range []string{DuplicateOfKey, AnnDupMethod, AnnDupScore} {
		if v, ok := r.Annotations[a]; ok {
			t.Fatalf("%s: annotation %s=%q not cleared", k, a, v)
		}
	}
	if r.Annotations[DescHashKey] == "" {
		t.Fatalf("%s: lost %s", k, DescHashKey)
	}
}

// A survivor that dies while its duplicate is still listed leaves the job
// visible: the duplicate comes back live with its duplicate annotations gone.
func TestDuplicateRevivesWhenSurvivorDies(t *testing.T) {
	l, gh, lv := reviveWorld(t)
	for i := 1; i <= MissesToDead; i++ {
		reconcileOne(t, l, board, reviveDay(i)) // the survivor is no longer listed
		s := reconcileOne(t, l, lvBoard, reviveDay(i), lv)
		if i < MissesToDead {
			if s.Revived != 0 || s.Unchanged != 1 {
				t.Fatalf("run %d, survivor not dead yet: %+v", i, s)
			}
			wantDuplicate(t, l, lv.Key, gh.Key)
			continue
		}
		if r := rec(t, l, gh.Key); r.Status != posting.Dead {
			t.Fatalf("survivor %s, want dead", r.Status)
		}
		if s.Revived != 1 || s.Unchanged != 0 {
			t.Fatalf("run %d: %+v", i, s)
		}
	}
	wantRevived(t, l, lv.Key, gh.Key)
	if r := rec(t, l, lv.Key); r.LastEvent.IsZero() || r.MissedRuns != 0 {
		t.Fatalf("revived record: %+v", r)
	}
}

// The survivor is not in the ledger at all (a rekey or a pruned ledger): the
// duplicate is revived all the same.
func TestDuplicateRevivesWhenSurvivorMissing(t *testing.T) {
	l := openLedger(t)
	lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: lv.Key, Status: posting.Duplicate, Posting: &lv,
		Annotations: map[string]string{DuplicateOfKey: "greenhouse:acme:404"}, Reason: "duplicate of greenhouse:acme:404"}); err != nil {
		t.Fatal(err)
	}
	s := reconcileOne(t, l, lvBoard, reviveDay(1), lv)
	if s.Revived != 1 {
		t.Fatalf("%+v", s)
	}
	wantRevived(t, l, lv.Key, "")
}

func TestDuplicateStaysWhileSurvivorStands(t *testing.T) {
	for _, st := range []posting.Status{posting.Live, posting.Triaged, posting.Digested, posting.Dropped, posting.Duplicate} {
		t.Run(string(st), func(t *testing.T) {
			l, gh, lv := reviveWorld(t)
			if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: reviveDay(1), Key: gh.Key, Status: st, Reason: "test"}); err != nil {
				t.Fatal(err)
			}
			before := rec(t, l, lv.Key)
			s := reconcileOne(t, l, lvBoard, reviveDay(2), lv)
			if s.Revived != 0 || s.Unchanged != 1 {
				t.Fatalf("%+v", s)
			}
			if after := rec(t, l, lv.Key); after.Status != posting.Duplicate || after.LastEvent != before.LastEvent {
				t.Fatalf("changed: %+v -> %+v", before, after)
			}
		})
	}
}

// Only a listed duplicate is revived: absence says nothing about a record the
// board no longer shows, and a closed record is never aged.
func TestDuplicateNotListedStaysDuplicate(t *testing.T) {
	l, gh, lv := reviveWorld(t)
	for i := 1; i <= MissesToDead; i++ {
		reconcileOne(t, l, board, reviveDay(i))
	}
	if r := rec(t, l, gh.Key); r.Status != posting.Dead {
		t.Fatalf("survivor %s, want dead", r.Status)
	}
	before := rec(t, l, lv.Key)
	s := reconcileOne(t, l, lvBoard, reviveDay(5)) // lv is not listed
	if s.Revived != 0 {
		t.Fatalf("%+v", s)
	}
	if after := rec(t, l, lv.Key); after.Status != posting.Duplicate || after.LastEvent != before.LastEvent || after.Annotations[DuplicateOfKey] != string(gh.Key) {
		t.Fatalf("changed: %+v -> %+v", before, after)
	}
}

func TestReviveIsIdempotent(t *testing.T) {
	l, gh, lv := reviveWorld(t)
	for i := 1; i <= MissesToDead; i++ {
		reconcileOne(t, l, board, reviveDay(i))
	}
	reconcileOne(t, l, lvBoard, reviveDay(5), lv)
	wantRevived(t, l, lv.Key, gh.Key)
	first := rec(t, l, lv.Key)
	events := len(l.All())
	for i := 6; i <= 8; i++ {
		s := reconcileOne(t, l, lvBoard, reviveDay(i), lv)
		if s.Revived != 0 || s.Unchanged != 1 || s.Duplicate != 0 {
			t.Fatalf("rerun %d: %+v", i, s)
		}
	}
	if after := rec(t, l, lv.Key); after.Status != posting.Live || after.LastEvent != first.LastEvent || len(l.All()) != events {
		t.Fatalf("rerun changed the record: %+v -> %+v", first, after)
	}
}

// Another live record is the same job: the revived record is marked against
// it in the same event, never against the dead survivor.
func TestReviveFindsAnotherLiveTwin(t *testing.T) {
	setup := func(t *testing.T) (*ledger.File, posting.Posting, posting.Posting, posting.Posting) {
		l := openLedger(t)
		gh1 := ghJob(t, "1", "Staff Engineer", "Remote", "")
		gh2 := ghJob(t, "2", "Staff Engineer", "Remote", "")
		lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
		reconcileOne(t, l, board, t0, gh1, gh2)
		reconcileOne(t, l, lvBoard, reviveDay(1), lv)
		wantDuplicate(t, l, lv.Key, gh1.Key)
		for i := 2; i <= MissesToDead+1; i++ { // gh1 leaves the board, gh2 stays
			reconcileOne(t, l, board, reviveDay(i), gh2)
		}
		if r := rec(t, l, gh1.Key); r.Status != posting.Dead {
			t.Fatalf("survivor %s, want dead", r.Status)
		}
		return l, gh1, gh2, lv
	}
	t.Run("retargeted", func(t *testing.T) {
		l, gh1, gh2, lv := setup(t)
		s := reconcileOne(t, l, lvBoard, reviveDay(6), lv)
		if s.Revived != 1 {
			t.Fatalf("%+v", s)
		}
		wantDuplicate(t, l, lv.Key, gh2.Key)
		if r := rec(t, l, lv.Key); r.Annotations[DuplicateOfKey] == string(gh1.Key) {
			t.Fatalf("still points at the dead survivor: %+v", r)
		}
		s = reconcileOne(t, l, lvBoard, reviveDay(7), lv)
		if s.Revived != 0 || s.Unchanged != 1 {
			t.Fatalf("rerun %+v", s)
		}
		wantDuplicate(t, l, lv.Key, gh2.Key)
	})
	t.Run("user-marked record is not demoted", func(t *testing.T) {
		l, _, _, lv := setup(t)
		rc := NewReconciler(l)
		rc.Protect(func(k posting.Key) bool { return k == lv.Key })
		s, err := rc.Reconcile(ctx, lvBoard, []posting.Posting{lv}, reviveDay(6))
		if err != nil || s.Revived != 1 {
			t.Fatalf("%+v %v", s, err)
		}
		wantRevived(t, l, lv.Key, "")
	})
}

// A user-marked record that is live stays untouched by the rule.
func TestReviveLeavesLiveRecordsAlone(t *testing.T) {
	l := openLedger(t)
	gh := ghJob(t, "1", "Staff Engineer", "Remote", "")
	reconcileOne(t, l, board, t0, gh)
	before := rec(t, l, gh.Key)
	s := reconcileOne(t, l, board, reviveDay(1), gh)
	if s.Revived != 0 || s.Unchanged != 1 {
		t.Fatalf("%+v", s)
	}
	if after := rec(t, l, gh.Key); after.LastEvent != before.LastEvent || after.Status != posting.Live {
		t.Fatalf("changed: %+v -> %+v", before, after)
	}
}

// seedDuplicate files p as a duplicate of a survivor the ledger lacks, first
// seen at `at`, with the given annotations on top of duplicate_of.
func seedDuplicate(t *testing.T, l ledger.Ledger, p posting.Posting, at time.Time, ann map[string]string) {
	t.Helper()
	a := map[string]string{DuplicateOfKey: "greenhouse:acme:404", DescHashKey: p.DescriptionHash()}
	for k, v := range ann {
		a[k] = v
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: p.Key, Status: posting.Duplicate, Posting: &p,
		Annotations: a, Reason: "seed"}); err != nil {
		t.Fatal(err)
	}
}

// A listing whose text changed since the duplicate was written invalidates the
// triage and score results the record carries; an unchanged text keeps them.
func TestReviveStaleAnnotationsFollowTheDescription(t *testing.T) {
	derived := map[string]string{ledger.AnnTriagePrefix + "role": "match", "score.fit": "0.8", "gap.skills": "go", ledger.AnnRank: "3"}
	for _, tc := range []struct {
		name, desc string
		keep       bool
	}{
		{"changed description clears them", "an entirely different job text", false},
		{"unchanged description keeps them", "d", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
			seedDuplicate(t, l, lv, t0, derived)
			listed := lv
			listed.Description = tc.desc
			if s := reconcileOne(t, l, lvBoard, reviveDay(1), listed); s.Revived != 1 {
				t.Fatalf("%+v", s)
			}
			r := rec(t, l, lv.Key)
			for k, v := range derived {
				got, has := r.Annotations[k]
				if tc.keep && got != v || !tc.keep && has {
					t.Fatalf("%s: keep=%v, got %q (present %v), want %q", k, tc.keep, got, has, v)
				}
			}
			if r.Annotations[DescHashKey] != listed.DescriptionHash() {
				t.Fatalf("desc hash %q", r.Annotations[DescHashKey])
			}
		})
	}
}

// A revived record does what a newly written live one does: the live records
// it outranks become its duplicates in the same Reconcile.
func TestReviveDemotesTheLiveTwinsItOutranks(t *testing.T) {
	t.Run("cross-ATS twin", func(t *testing.T) {
		l := openLedger(t)
		lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
		gh := ghJob(t, "1", "Staff Engineer", "Remote", "")
		seedDuplicate(t, l, lv, t0, nil) // first seen before gh
		reconcileOne(t, l, board, reviveDay(1), gh)
		wantLive(t, l, gh.Key)
		s := reconcileOne(t, l, lvBoard, reviveDay(2), lv)
		if s.Revived != 1 || s.Duplicate != 1 {
			t.Fatalf("%+v", s)
		}
		wantLive(t, l, lv.Key)
		wantDuplicate(t, l, gh.Key, lv.Key)
		// The next run finds nothing left to do.
		if s := reconcileOne(t, l, lvBoard, reviveDay(3), lv); s.Revived != 0 || s.Duplicate != 0 {
			t.Fatalf("rerun %+v", s)
		}
		wantDuplicate(t, l, gh.Key, lv.Key)
	})
	t.Run("a twin that ranks first is left alone", func(t *testing.T) {
		l := openLedger(t)
		lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
		gh := ghJob(t, "1", "Staff Engineer", "Remote", "")
		reconcileOne(t, l, board, t0, gh)
		seedDuplicate(t, l, lv, reviveDay(1), nil) // first seen after gh
		s := reconcileOne(t, l, lvBoard, reviveDay(2), lv)
		if s.Revived != 1 {
			t.Fatalf("%+v", s)
		}
		wantDuplicate(t, l, lv.Key, gh.Key) // retargeted at the live twin
		wantLive(t, l, gh.Key)
	})
	t.Run("weak twin", func(t *testing.T) {
		l := openLedger(t)
		gh := ghJob(t, "1", "Staff Engineer", "Remote", "")
		fk, err := posting.FallbackKey("Acme", "Staff Engineer", "Remote")
		if err != nil {
			t.Fatal(err)
		}
		weak := posting.Posting{Key: fk, ATS: posting.Other, Company: "Acme", Title: "Staff Engineer", Location: "Remote", Description: "d"}
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: reviveDay(1), Key: fk, Status: posting.Live, Posting: &weak, Reason: "seed"}); err != nil {
			t.Fatal(err)
		}
		seedDuplicate(t, l, gh, t0, nil)
		s := reconcileOne(t, l, board, reviveDay(2), gh)
		if s.Revived != 1 || s.Duplicate != 1 {
			t.Fatalf("%+v", s)
		}
		wantLive(t, l, gh.Key)
		wantDuplicate(t, l, fk, gh.Key)
	})
}
