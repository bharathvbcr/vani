package pipeline

import (
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

var lvBoard = sources.Board{ATS: posting.Lever, Token: "acmelv", Company: "Acme"}

func ghJob(t *testing.T, id, title, loc, url string) posting.Posting {
	t.Helper()
	k, err := posting.GreenhouseKey("acme", id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: title, Location: loc, URL: url, Description: "d"}
}

func lvJob(t *testing.T, id, title, loc, url string) posting.Posting {
	t.Helper()
	k, err := posting.LeverKey("acmelv", id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: k, ATS: posting.Lever, Company: "Acme", Title: title, Location: loc, URL: url, Description: "d"}
}

func reconcileOne(t *testing.T, l ledger.Ledger, b sources.Board, now time.Time, ps ...posting.Posting) Summary {
	t.Helper()
	s, err := Reconcile(ctx, l, b, ps, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func wantDuplicate(t *testing.T, l ledger.Ledger, dup, of posting.Key) {
	t.Helper()
	r := rec(t, l, dup)
	if r.Status != posting.Duplicate || r.Annotations[DuplicateOfKey] != string(of) {
		t.Fatalf("%s: %s %v, want a duplicate of %s", dup, r.Status, r.Annotations, of)
	}
}

func wantLive(t *testing.T, l ledger.Ledger, ks ...posting.Key) {
	t.Helper()
	for _, k := range ks {
		if r := rec(t, l, k); r.Status != posting.Live {
			t.Fatalf("%s: %s %v, want live", k, r.Status, r.Annotations)
		}
	}
}

// One role listed on two ATSes is one job: the later-ranked record is the
// duplicate, whichever board ran first.
func TestSameRoleOnTwoATSesIsOneDuplicate(t *testing.T) {
	gh := func(t *testing.T) posting.Posting { return ghJob(t, "1", "Staff Engineer", "Remote", "") }
	lv := func(t *testing.T) posting.Posting { return lvJob(t, "a1", "Staff Engineer", "Remote", "") }
	t.Run("gh then lv, one shared time", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, board, t0, gh(t))
		s := reconcileOne(t, l, lvBoard, t0, lv(t))
		if s.Duplicate != 1 || s.New != 0 {
			t.Fatalf("summary %+v", s)
		}
		wantDuplicate(t, l, lv(t).Key, gh(t).Key)
		wantLive(t, l, gh(t).Key)
	})
	t.Run("lv then gh, one shared time", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, lvBoard, t0, lv(t))
		s := reconcileOne(t, l, board, t0, gh(t))
		if s.Duplicate != 1 || s.New != 1 {
			t.Fatalf("summary %+v", s)
		}
		wantDuplicate(t, l, lv(t).Key, gh(t).Key)
		wantLive(t, l, gh(t).Key)
	})
	t.Run("the earlier first seen survives", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, lvBoard, t0, lv(t))
		reconcileOne(t, l, board, t0.Add(time.Hour), gh(t))
		wantDuplicate(t, l, gh(t).Key, lv(t).Key)
		wantLive(t, l, lv(t).Key)
	})
	t.Run("rerun is idempotent", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, lvBoard, t0, lv(t))
		reconcileOne(t, l, board, t0, gh(t))
		before := l.All()
		for run := 1; run <= 2; run++ {
			now := t0.Add(time.Duration(run) * time.Hour)
			a := reconcileOne(t, l, board, now, gh(t))
			b := reconcileOne(t, l, lvBoard, now, lv(t))
			if a.Duplicate+b.Duplicate != 0 {
				t.Fatalf("rerun %d marked again: %+v %+v", run, a, b)
			}
		}
		if !reflect.DeepEqual(l.All(), before) {
			t.Fatalf("rerun changed the ledger\nbefore %+v\nafter  %+v", before, l.All())
		}
	})
}

// A role is a multi-city requisition, not a duplicate, when the places differ;
// and two ids on one ATS are two requisitions even at one place.
func TestSameTitleOtherLocationOrSameATSStaysSeparate(t *testing.T) {
	l := openLedger(t)
	ny := ghJob(t, "1", "Staff Engineer", "New York", "")
	ny2 := ghJob(t, "2", "Staff Engineer", "New York", "") // same ATS: another requisition
	sf := lvJob(t, "a1", "Staff Engineer", "San Francisco", "")
	nyLv := lvJob(t, "a2", "Staff Engineer", "New York", "")
	reconcileOne(t, l, board, t0, ny, ny2)
	s := reconcileOne(t, l, lvBoard, t0, sf, nyLv)
	if s.Duplicate != 1 || s.New != 1 {
		t.Fatalf("summary %+v", s)
	}
	wantLive(t, l, ny.Key, ny2.Key, sf.Key)
	wantDuplicate(t, l, nyLv.Key, ny.Key)
}

// Only a live, unmarked record of another ATS can be demoted by a later
// arrival; a dead record never absorbs a new one.
func TestCrossATSTwinSparesProtectedDigestedAndDeadRecords(t *testing.T) {
	gh := func(t *testing.T) posting.Posting { return ghJob(t, "1", "Staff Engineer", "Remote", "") }
	lv := func(t *testing.T) posting.Posting { return lvJob(t, "a1", "Staff Engineer", "Remote", "") }
	t.Run("marked", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, lvBoard, t0, lv(t))
		rc := NewReconciler(l)
		rc.Protect(func(k posting.Key) bool { return k == lv(t).Key })
		if _, err := rc.Reconcile(ctx, board, []posting.Posting{gh(t)}, t0); err != nil {
			t.Fatal(err)
		}
		wantLive(t, l, lv(t).Key, gh(t).Key)
	})
	t.Run("digested", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, lvBoard, t0, lv(t))
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: lv(t).Key, Status: posting.Digested}); err != nil {
			t.Fatal(err)
		}
		reconcileOne(t, l, board, t0, gh(t))
		if r := rec(t, l, lv(t).Key); r.Status != posting.Digested {
			t.Fatalf("digested record re-marked: %s", r.Status)
		}
		wantLive(t, l, gh(t).Key)
	})
	t.Run("dead", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, board, t0, gh(t))
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: gh(t).Key, Status: posting.Dead}); err != nil {
			t.Fatal(err)
		}
		s := reconcileOne(t, l, lvBoard, t0, lv(t))
		if s.Duplicate != 0 {
			t.Fatalf("a dead record absorbed a live posting: %+v", s)
		}
		wantLive(t, l, lv(t).Key)
	})
}

// The same posting link, however decorated, is the same job.
func TestSameURLIsADuplicate(t *testing.T) {
	const canonical = "https://boards.greenhouse.io/acme/jobs/4012345"
	for _, tc := range []struct{ name, url string }{
		{"tracking params", canonical + "?utm_source=li&ref=abc&utm_campaign=x"},
		{"http", "http://boards.greenhouse.io/acme/jobs/4012345"},
		{"job-boards host", "https://job-boards.greenhouse.io/acme/jobs/4012345/"},
		{"all of it", "HTTP://Job-Boards.Greenhouse.io/acme/jobs/4012345?utm_medium=x#apply"},
		{"embed form", "https://boards.greenhouse.io/embed/job_app?for=acme&token=4012345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			gh := ghJob(t, "4012345", "Staff Engineer", "Remote", canonical)
			other := lvJob(t, "a1", "Platform Lead", "Berlin", tc.url) // a different tuple: only the link ties them
			reconcileOne(t, l, board, t0, gh)
			s := reconcileOne(t, l, lvBoard, t0.Add(time.Hour), other)
			if s.Duplicate != 1 {
				t.Fatalf("summary %+v", s)
			}
			wantDuplicate(t, l, other.Key, gh.Key)
			wantLive(t, l, gh.Key)
		})
	}
	t.Run("apply url", func(t *testing.T) {
		l := openLedger(t)
		gh := ghJob(t, "4012345", "Staff Engineer", "Remote", canonical)
		other := lvJob(t, "a1", "Platform Lead", "Berlin", "https://jobs.lever.co/acmelv/a1")
		other.ApplyURL = canonical + "?gh_src=x"
		reconcileOne(t, l, board, t0, gh)
		reconcileOne(t, l, lvBoard, t0.Add(time.Hour), other)
		wantDuplicate(t, l, other.Key, gh.Key)
	})
	t.Run("a careers page posting that links the ATS posting", func(t *testing.T) {
		l := openLedger(t)
		gh := ghJob(t, "4012345", "Staff Engineer", "Remote", canonical)
		pg := pagePost(t, "https://acme.example/careers/9", "Engineering Manager")
		pg.ApplyURL = canonical
		reconcileOne(t, l, board, t0, gh)
		reconcileOne(t, l, pageBoard, t0.Add(time.Hour), pg)
		wantDuplicate(t, l, pg.Key, gh.Key)
	})
}

func TestURLThatIsAmbiguousOrNotAPostingIsNotIdentity(t *testing.T) {
	const shared = "https://boards.greenhouse.io/acme/jobs/4012345"
	t.Run("claimed by two records", func(t *testing.T) {
		l := openLedger(t)
		a := ghJob(t, "1", "Staff Engineer", "Remote", shared)
		b := ghJob(t, "2", "Staff Designer", "Remote", shared) // same listing: neither sees the other
		reconcileOne(t, l, board, t0, a, b)
		c := lvJob(t, "a1", "Platform Lead", "Berlin", shared+"?utm_source=x")
		s := reconcileOne(t, l, lvBoard, t0.Add(time.Hour), c)
		if s.Duplicate != 0 {
			t.Fatalf("an ambiguous link marked a duplicate: %+v", s)
		}
		wantLive(t, l, a.Key, b.Key, c.Key)
	})
	for _, home := range []string{
		"https://acme.example/careers",
		"https://acme.example/careers/?utm_source=x",
		"https://acme.example/",
		"https://acme.example/jobs/v2/web3",
		"https://acme.example/careers/en-us",
	} {
		t.Run("homepage "+home, func(t *testing.T) {
			l := openLedger(t)
			a := ghJob(t, "1", "Staff Engineer", "Remote", home)
			b := lvJob(t, "a1", "Platform Lead", "Berlin", home)
			reconcileOne(t, l, board, t0, a)
			reconcileOne(t, l, lvBoard, t0.Add(time.Hour), b)
			wantLive(t, l, a.Key, b.Key)
		})
	}
	for _, bad := range []string{"", "   ", "not a url", "ftp://acme.example/jobs/4012345", "https://", "javascript:alert(1)//4012345", "https://acme.example/jobs/4012345 x"} {
		t.Run("unusable "+bad, func(t *testing.T) {
			l := openLedger(t)
			a := ghJob(t, "1", "Staff Engineer", "Remote", bad)
			b := lvJob(t, "a1", "Platform Lead", "Berlin", bad)
			reconcileOne(t, l, board, t0, a)
			reconcileOne(t, l, lvBoard, t0.Add(time.Hour), b)
			wantLive(t, l, a.Key, b.Key)
		})
	}
	t.Run("a dead record does not claim", func(t *testing.T) {
		l := openLedger(t)
		a := ghJob(t, "1", "Staff Engineer", "Remote", shared)
		reconcileOne(t, l, board, t0, a)
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: a.Key, Status: posting.Dead}); err != nil {
			t.Fatal(err)
		}
		b := lvJob(t, "a1", "Platform Lead", "Berlin", shared)
		reconcileOne(t, l, lvBoard, t0.Add(time.Hour), b)
		wantLive(t, l, b.Key)
	})
}

func TestIdentityURL(t *testing.T) {
	for raw, want := range map[string]string{
		"http://job-boards.greenhouse.io/acme/jobs/4012345?utm_source=x": "https://boards.greenhouse.io/acme/jobs/4012345",
		"https://acme.example/careers?gh_jid=77&utm_source=x":            "https://acme.example/careers?gh_jid=77",
		"https://acme.example/careers":                                   "",
		"https://acme.example/jobs/12":                                   "", // two digits name no posting
		"":                                                               "",
	} {
		got, ok := identityURL(raw)
		if got != want || ok != (want != "") {
			t.Errorf("identityURL(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
}
