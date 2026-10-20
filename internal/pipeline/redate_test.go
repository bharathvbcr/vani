package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// An adapter that starts reading a board's posted date (Greenhouse's
// first_published, 2026-10-19) lists postings the ledger stored undated. The
// stored posting takes the date, so the advance order can rank it by
// freshness, and keeps its status and annotations: the content is the same.
func TestUndatedPostingTakesALaterListedDate(t *testing.T) {
	posted := time.Date(2026, 9, 16, 13, 11, 57, 0, time.UTC)
	for _, st := range []posting.Status{posting.Live, posting.Triaged, posting.Digested} {
		t.Run(string(st), func(t *testing.T) {
			l := openLedger(t)
			p := ghPost(t, "1", "Build the thing.")
			digestedWith(t, l, p)
			if st != posting.Digested {
				if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: st, Reason: "set"}); err != nil {
					t.Fatal(err)
				}
			}
			p2 := p
			p2.PostedAt = posted
			s, err := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			r := rec(t, l, p.Key)
			if s.Redated != 1 || s.Unchanged != 0 || s.Changed != 0 || r.Status != st {
				t.Fatalf("undated posting not re-dated: %+v status %s", s, r.Status)
			}
			if !r.Posting.PostedAt.Equal(posted) {
				t.Fatalf("stored posted_at %s, want %s", r.Posting.PostedAt, posted)
			}
			if !r.FirstSeen.Equal(rec(t, l, p.Key).FirstSeen) || r.FirstSeen.After(t0) {
				t.Fatalf("first_seen moved to %s", r.FirstSeen)
			}
			keepsDerived(t, r)
			// The next run lists the same dated posting: nothing to write.
			before := r.LastEvent
			s, _ = Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(2*time.Hour))
			if s.Unchanged != 1 || s.Redated != 0 || !rec(t, l, p.Key).LastEvent.Equal(before) {
				t.Fatalf("second run wrote an event: %+v", s)
			}
		})
	}
}

// A scraped batch (jobsIngest, ReconcilePartial) re-dates the same way: the
// case is in the listing loop both entry points share, so a job first sent
// undated and later sent with its date takes the date and keeps its status.
func TestReconcilePartialRedatesAnUndatedScrape(t *testing.T) {
	l := openLedger(t)
	rc := NewReconciler(l)
	p := scraped(t, "https://careers.acme.com/jobs/7", "Platform Engineer")
	if _, err := rc.ReconcilePartial(ctx, partialBoard(), []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	posted := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	p2 := p
	p2.PostedAt = posted
	s, err := rc.ReconcilePartial(ctx, partialBoard(), []posting.Posting{p2}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := rec(t, l, p.Key)
	if s.Redated != 1 || s.Unchanged != 0 || s.Changed != 0 || r.Status != posting.Live {
		t.Fatalf("scraped undated posting not re-dated: %+v status %s", s, r.Status)
	}
	if !r.Posting.PostedAt.Equal(posted) {
		t.Fatalf("stored posted_at %s, want %s", r.Posting.PostedAt, posted)
	}
}

// A stored date is never moved by a later listing: relative phrases
// ("Posted 3 Days Ago") drift by a day between runs, and rewriting the
// posting each run would grow the ledger for nothing. Nor does a listing
// that lost its date clear one.
func TestStoredPostedDateIsKept(t *testing.T) {
	posted := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	for name, next := range map[string]time.Time{
		"later date":  posted.AddDate(0, 0, 1),
		"no date now": {},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			p := ghPost(t, "1", "Build the thing.")
			p.PostedAt = posted
			digestedWith(t, l, p)
			before := rec(t, l, p.Key).LastEvent
			p2 := p
			p2.PostedAt = next
			s, _ := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
			r := rec(t, l, p.Key)
			if s.Unchanged != 1 || s.Redated != 0 || !r.LastEvent.Equal(before) || !r.Posting.PostedAt.Equal(posted) {
				t.Fatalf("stored date rewritten: %+v posted_at %s", s, r.Posting.PostedAt)
			}
		})
	}
}

// A dropped posting is shown nowhere and is never advanced, so a new date
// writes no event: its last event dates the drop the digest footer counts.
func TestDroppedUndatedPostingIsNotRedated(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "Build the thing.")
	Reconcile(ctx, l, board, []posting.Posting{p}, t0)
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Dropped, Reason: "rule"}); err != nil {
		t.Fatal(err)
	}
	before := rec(t, l, p.Key).LastEvent
	p2 := p
	p2.PostedAt = time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	s, _ := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
	if r := rec(t, l, p.Key); s.Unchanged != 1 || s.Redated != 0 || !r.LastEvent.Equal(before) || !r.Posting.PostedAt.IsZero() {
		t.Fatalf("dropped posting re-dated: %+v %+v", s, r)
	}
}

// Many undated postings on one board are re-dated in one listing, each once,
// and duplicates in the listing count once.
func TestRedateManyAndDuplicateListing(t *testing.T) {
	l := openLedger(t)
	var first, listed []posting.Posting
	for _, id := range []string{"1", "2", "3"} {
		p := ghPost(t, id, "Build "+id)
		first = append(first, p)
		p.PostedAt = time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
		listed = append(listed, p, p)
	}
	if _, err := Reconcile(ctx, l, board, first, t0); err != nil {
		t.Fatal(err)
	}
	s, err := Reconcile(ctx, l, board, listed, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Redated != 3 || s.Unchanged != 0 {
		t.Fatalf("summary %+v, want 3 re-dated", s)
	}
}
