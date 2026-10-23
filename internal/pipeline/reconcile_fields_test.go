package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// fieldsJob is a stored-shape posting with every display field set.
func fieldsJob(t *testing.T) posting.Posting {
	t.Helper()
	p := ghPost(t, "1", "Build the thing.")
	p.Location, p.Department = "US, CA, Santa Clara", "Engineering"
	p.URL, p.ApplyURL = "https://boards.greenhouse.io/acme/jobs/1", "https://boards.greenhouse.io/acme/jobs/1#app"
	p.PostedAt = t0.Add(-48 * time.Hour)
	return p
}

func reconcileFull(t *testing.T, l ledger.Ledger, at time.Time, ps ...posting.Posting) Summary {
	t.Helper()
	s, err := Reconcile(ctx, l, board, ps, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func reconcilePart(t *testing.T, l ledger.Ledger, at time.Time, ps ...posting.Posting) Summary {
	t.Helper()
	s, err := NewReconciler(l).ReconcilePartial(ctx, board, ps, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An adapter that learns a posting's other places or its remote flag (Workday
// additionalLocations, Jibe Home Office, Eightfold work site, Google's note)
// changes no board text: the stored posting takes the new fields and keeps
// its status, triage and explanation, and the next run sees no change.
func TestFieldOnlyChangeRefreshesStoredPosting(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	digestedWith(t, l, p)
	p2 := p
	p2.Location = "US, CA, Santa Clara; US, CA, Remote"
	p2.Remote = true
	p2.Department = "Silicon"
	p2.URL = "https://job-boards.greenhouse.io/acme/jobs/1"
	p2.ApplyURL = "https://job-boards.greenhouse.io/acme/jobs/1#app"
	p2.Company = "Acme Inc"
	p2.PostedAt = t0 // a drifted relative date must not move the stored one
	p2.SeenAt = t0.Add(time.Hour)
	s := reconcileFull(t, l, t0.Add(time.Hour), p2)
	r := rec(t, l, p.Key)
	if s.Refreshed != 1 || s.Unchanged != 0 || s.Changed != 0 || s.Rerendered != 0 || r.Status != posting.Digested {
		t.Fatalf("summary %+v status %s", s, r.Status)
	}
	keepsDerived(t, r)
	got := r.Posting
	if got.Location != p2.Location || !got.Remote || got.Department != "Silicon" || got.URL != p2.URL ||
		got.ApplyURL != p2.ApplyURL || got.Company != "Acme Inc" || !got.PostedAt.Equal(p.PostedAt) || got.Title != p.Title {
		t.Fatalf("stored posting %+v", got)
	}
	if s := reconcileFull(t, l, t0.Add(2*time.Hour), p2); s.Unchanged != 1 || s.Refreshed != 0 {
		t.Fatalf("second run: %+v", s)
	}
}

// Title is read by triage and the title drop rules, so a title-only change
// does not refresh the stored posting under a standing triage; a changed
// UpdatedAt or SeenAt alone writes nothing (they move on every re-save).
func TestTitleAndTimestampOnlyChangesAreUnchanged(t *testing.T) {
	for name, edit := range map[string]func(*posting.Posting){
		"title":      func(p *posting.Posting) { p.Title = "Staff Engineer 1" },
		"updated at": func(p *posting.Posting) { p.UpdatedAt = t0.Add(time.Hour) },
		"seen at":    func(p *posting.Posting) { p.SeenAt = t0.Add(time.Hour) },
		"posted at":  func(p *posting.Posting) { p.PostedAt = t0 },
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			p := fieldsJob(t)
			digestedWith(t, l, p)
			before := rec(t, l, p.Key)
			p2 := p
			edit(&p2)
			s := reconcileFull(t, l, t0.Add(time.Hour), p2)
			after := rec(t, l, p.Key)
			if s.Unchanged != 1 || s.Refreshed != 0 || after.Posting.Title != p.Title || !after.LastEvent.Equal(before.LastEvent) {
				t.Fatalf("summary %+v, stored %+v", s, after.Posting)
			}
		})
	}
}

// A listing that leaves a field empty says nothing about it: the stored value
// stays, and a listing that differs only by such blanks is unchanged.
func TestEmptyListedFieldsNeverBlankStoredOnes(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	digestedWith(t, l, p)
	p2 := p
	p2.Location, p2.Department, p2.URL, p2.ApplyURL, p2.Company = "", " ", "", "", ""
	if s := reconcileFull(t, l, t0.Add(time.Hour), p2); s.Unchanged != 1 || s.Refreshed != 0 {
		t.Fatalf("blanks alone: %+v", s)
	}
	// Blanks beside a real change: the change lands, the blanks do not.
	p2.Remote = true
	s := reconcileFull(t, l, t0.Add(2*time.Hour), p2)
	got := rec(t, l, p.Key).Posting
	if s.Refreshed != 1 || !got.Remote || got.Location != p.Location || got.Department != p.Department ||
		got.URL != p.URL || got.ApplyURL != p.ApplyURL || got.Company != p.Company {
		t.Fatalf("summary %+v stored %+v", s, got)
	}
}

// A full listing is the board's word on Remote, so true can become false; a
// partial one (a scraped page) can set it but never clear it.
func TestRemoteAuthority(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	p.Remote = true
	digestedWith(t, l, p)
	off := p
	off.Remote = false
	if s := reconcilePart(t, l, t0.Add(time.Hour), off); s.Unchanged != 1 || !rec(t, l, p.Key).Posting.Remote {
		t.Fatalf("partial listing cleared remote: %+v", s)
	}
	if s := reconcileFull(t, l, t0.Add(2*time.Hour), off); s.Refreshed != 1 || rec(t, l, p.Key).Posting.Remote {
		t.Fatalf("full listing did not clear remote: %+v", s)
	}
	if s := reconcilePart(t, l, t0.Add(3*time.Hour), p); s.Refreshed != 1 || !rec(t, l, p.Key).Posting.Remote {
		t.Fatalf("partial listing did not set remote: %+v", s)
	}
}

// A dropped posting is shown nowhere and gets no event; a posting listed
// twice in one run is folded once.
func TestRefreshSkipsDroppedAndRepeats(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	reconcileFull(t, l, t0, p)
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Dropped, Reason: "drop"}); err != nil {
		t.Fatal(err)
	}
	p2 := p
	p2.Location = "Elsewhere"
	if s := reconcileFull(t, l, t0.Add(time.Hour), p2); s.Unchanged != 1 || rec(t, l, p.Key).Posting.Location != p.Location {
		t.Fatalf("dropped posting refreshed: %+v", s)
	}

	l = openLedger(t)
	digestedWith(t, l, p)
	p3 := p
	p3.Location = "Third"
	s := reconcileFull(t, l, t0.Add(time.Hour), p2, p3)
	if s.Refreshed != 1 || rec(t, l, p.Key).Posting.Location != "Elsewhere" {
		t.Fatalf("repeat in one run: %+v %q", s, rec(t, l, p.Key).Posting.Location)
	}
}

// A user-marked (protected) record is refreshed like any other: protection
// only spares it demotion to Duplicate.
func TestProtectedRecordIsRefreshed(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	digestedWith(t, l, p)
	rc := NewReconciler(l)
	rc.Protect(func(posting.Key) bool { return true })
	p2 := p
	p2.Location = "US, CA, Remote"
	s, err := rc.Reconcile(ctx, board, []posting.Posting{p2}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, p.Key); s.Refreshed != 1 || r.Status != posting.Digested || r.Posting.Location != p2.Location {
		t.Fatalf("summary %+v record %s %q", s, r.Status, r.Posting.Location)
	}
}

// The same-content paths that store the listed posting (re-rendered,
// reappeared after a miss or death, re-dated) keep stored values the listing
// left empty and never move a stored posted date.
func TestSameContentPathsKeepStoredFields(t *testing.T) {
	blanked := func(p posting.Posting) posting.Posting {
		p.Location, p.Department, p.URL, p.ApplyURL, p.Company = "", "", "", "", ""
		p.PostedAt = t0
		return p
	}
	check := func(t *testing.T, l ledger.Ledger, p posting.Posting) {
		t.Helper()
		got := rec(t, l, p.Key).Posting
		if got.Location != p.Location || got.Department != p.Department || got.URL != p.URL ||
			got.ApplyURL != p.ApplyURL || got.Company != p.Company || !got.PostedAt.Equal(p.PostedAt) {
			t.Fatalf("stored %+v, want fields of %+v", got, p)
		}
	}
	t.Run("re-rendered", func(t *testing.T) {
		l := openLedger(t)
		p := fieldsJob(t)
		digestedWith(t, l, p)
		p2 := blanked(p)
		p2.Description = "Pay range: USD 1 – 2 per year\n\n" + p.Description
		if s := reconcileFull(t, l, t0.Add(time.Hour), p2); s.Rerendered != 1 {
			t.Fatalf("%+v", s)
		}
		check(t, l, p)
	})
	t.Run("reappeared after a miss", func(t *testing.T) {
		l := openLedger(t)
		p := fieldsJob(t)
		digestedWith(t, l, p)
		reconcileFull(t, l, t0.Add(time.Hour))
		if s := reconcileFull(t, l, t0.Add(2*time.Hour), blanked(p)); s.Reappeared != 1 {
			t.Fatalf("%+v", s)
		}
		check(t, l, p)
	})
	t.Run("reappeared after death", func(t *testing.T) {
		l := openLedger(t)
		p := fieldsJob(t)
		digestedWith(t, l, p)
		reconcileFull(t, l, t0.Add(time.Hour))
		reconcileFull(t, l, t0.Add(2*time.Hour))
		if r := rec(t, l, p.Key); r.Status != posting.Dead {
			t.Fatalf("not dead: %s", r.Status)
		}
		if s := reconcileFull(t, l, t0.Add(3*time.Hour), blanked(p)); s.Reappeared != 1 {
			t.Fatalf("%+v", s)
		}
		check(t, l, p)
	})
	t.Run("re-dated", func(t *testing.T) {
		l := openLedger(t)
		p := fieldsJob(t)
		p.PostedAt = time.Time{}
		digestedWith(t, l, p)
		p2 := blanked(p)
		p2.Location = "US, CA, Remote"
		if s := reconcileFull(t, l, t0.Add(time.Hour), p2); s.Redated != 1 {
			t.Fatalf("%+v", s)
		}
		got := rec(t, l, p.Key).Posting
		if !got.PostedAt.Equal(t0) || got.Location != "US, CA, Remote" || got.URL != p.URL || got.Company != p.Company {
			t.Fatalf("stored %+v", got)
		}
	})
}
