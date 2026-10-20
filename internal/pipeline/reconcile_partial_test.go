package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func partialBoard() sources.Board {
	return sources.Board{ATS: posting.Page, Token: "careers-acme-com", Company: "Acme"}
}

func scraped(t *testing.T, url, title string) posting.Posting {
	t.Helper()
	k, err := posting.PageKey("careers-acme-com", url)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: k, ATS: posting.Page, Company: "Acme", Title: title, Location: "Remote", URL: url,
		Description: "scraped snippet", SeenAt: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
}

// A partial listing lands new postings Live, like a full one.
func TestReconcilePartialLandsNewPostingsLive(t *testing.T) {
	l := openLedger(t)
	p := scraped(t, "https://careers.acme.com/jobs/1", "Platform Engineer")
	s, err := NewReconciler(l).ReconcilePartial(context.Background(), partialBoard(), []posting.Posting{p}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.New != 1 {
		t.Fatalf("summary %+v, want one new", s)
	}
	if r, ok := l.Get(p.Key); !ok || r.Status != posting.Live {
		t.Fatalf("record %+v ok=%v, want live", r, ok)
	}
}

// The reason ReconcilePartial exists: a scrape is not the board's whole
// listing, so a record the batch lacks must not be missed, aged or killed,
// however many batches come. Reconcile, given the same input, ages it.
func TestReconcilePartialNeverAgesAbsentRecords(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t)
	a := scraped(t, "https://careers.acme.com/jobs/1", "Platform Engineer")
	b := scraped(t, "https://careers.acme.com/jobs/2", "Data Engineer")
	rc := NewReconciler(l)
	if _, err := rc.ReconcilePartial(ctx, partialBoard(), []posting.Posting{a, b}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MissesToDead+1; i++ {
		s, err := rc.ReconcilePartial(ctx, partialBoard(), []posting.Posting{a}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if s.Missed != 0 || s.Dead != 0 {
			t.Fatalf("round %d: summary %+v aged an absent record", i, s)
		}
	}
	r, _ := l.Get(b.Key)
	if r.Status != posting.Live || r.MissedRuns != 0 {
		t.Fatalf("absent record is %s with %d misses, want live and untouched", r.Status, r.MissedRuns)
	}

	// Control: the full-listing path does age the same record.
	full, err := rc.Reconcile(ctx, partialBoard(), []posting.Posting{a}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if full.Missed != 1 {
		t.Fatalf("Reconcile summary %+v, want the absent record missed once", full)
	}
}

// A scraped posting that repeats an ATS-keyed role is the duplicate.
func TestReconcilePartialMarksATSTwinsDuplicate(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t)
	rc := NewReconciler(l)
	gh := posting.Posting{ATS: posting.Greenhouse, Company: "Acme", Title: "Platform Engineer", Location: "Remote",
		URL: "https://boards.greenhouse.io/acme/jobs/9", SeenAt: time.Now()}
	gh.Key, _ = posting.GreenhouseKey("acme", "9")
	if _, err := rc.Reconcile(ctx, sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}, []posting.Posting{gh}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := scraped(t, "https://careers.acme.com/jobs/1", "Platform Engineer")
	s, err := rc.ReconcilePartial(ctx, partialBoard(), []posting.Posting{p}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Duplicate != 1 || s.New != 0 {
		t.Fatalf("summary %+v, want one duplicate and no new", s)
	}
	r, _ := l.Get(p.Key)
	if r.Status != posting.Duplicate || r.Annotations[DuplicateOfKey] != string(gh.Key) {
		t.Fatalf("record %s %v, want duplicate of %s", r.Status, r.Annotations, gh.Key)
	}
	if g, _ := l.Get(gh.Key); g.Status != posting.Live {
		t.Fatalf("the ATS record is %s, want live", g.Status)
	}
}

// The ATS role arriving after the scrape demotes the weak record instead.
func TestReconcilePartialRecordIsDemotedByLaterATSListing(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t)
	rc := NewReconciler(l)
	p := scraped(t, "https://careers.acme.com/jobs/1", "Platform Engineer")
	if _, err := rc.ReconcilePartial(ctx, partialBoard(), []posting.Posting{p}, time.Now()); err != nil {
		t.Fatal(err)
	}
	gh := posting.Posting{ATS: posting.Greenhouse, Company: "Acme", Title: "Platform Engineer", Location: "Remote",
		URL: "https://boards.greenhouse.io/acme/jobs/9", SeenAt: time.Now()}
	gh.Key, _ = posting.GreenhouseKey("acme", "9")
	if _, err := rc.Reconcile(ctx, sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}, []posting.Posting{gh}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r, _ := l.Get(p.Key); r.Status != posting.Duplicate {
		t.Fatalf("scraped record is %s, want duplicate", r.Status)
	}
}

// Folding the same batch again changes nothing.
func TestReconcilePartialIsIdempotent(t *testing.T) {
	ctx := context.Background()
	l := openLedger(t)
	batch := []posting.Posting{
		scraped(t, "https://careers.acme.com/jobs/1", "Platform Engineer"),
		scraped(t, "https://careers.acme.com/jobs/2", "Data Engineer"),
	}
	if _, err := NewReconciler(l).ReconcilePartial(ctx, partialBoard(), batch, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := l.All()
	s, err := NewReconciler(l).ReconcilePartial(ctx, partialBoard(), batch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.New+s.Changed+s.Rerendered+s.Reappeared+s.Duplicate+s.Missed+s.Dead != 0 || s.Unchanged != 2 {
		t.Fatalf("second fold %+v, want two unchanged", s)
	}
	after := l.All()
	if len(after) != len(before) {
		t.Fatalf("%d records after, %d before", len(after), len(before))
	}
	for i := range before {
		if before[i].Status != after[i].Status || !before[i].LastEvent.Equal(after[i].LastEvent) {
			t.Fatalf("record %s changed on re-fold: %+v -> %+v", before[i].Key, before[i], after[i])
		}
	}
}

// A posting keyed outside the board is counted invalid and never written.
func TestReconcilePartialRefusesForeignKeys(t *testing.T) {
	l := openLedger(t)
	p := scraped(t, "https://careers.acme.com/jobs/1", "Platform Engineer")
	p.Key, _ = posting.PageKey("someone-else", p.URL)
	s, err := NewReconciler(l).ReconcilePartial(context.Background(), partialBoard(), []posting.Posting{p}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Invalid != 1 || len(l.All()) != 0 {
		t.Fatalf("summary %+v, %d records; want one invalid and nothing written", s, len(l.All()))
	}
}
