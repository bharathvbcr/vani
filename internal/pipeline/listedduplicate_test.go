package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A listed posting stored as a duplicate is counted in ListedDuplicate and in
// Duplicate; a record another listing marks as its twin is in Duplicate only.
// So a caller can account for every listed posting (New, ListedDuplicate and
// the known outcomes) without mistaking marked records for listed ones.
func TestListedDuplicateIsCountedApartFromMarkedTwins(t *testing.T) {
	l := openLedger(t)
	gh := ghPost(t, "1", "jd") // Title "Engineer 1"
	if _, err := Reconcile(ctx, l, board, []posting.Posting{gh}, t0); err != nil {
		t.Fatal(err)
	}
	twin := pagePost(t, "https://acme.example/careers/1", "Engineer 1")
	fresh := pagePost(t, "https://acme.example/careers/2", "Engineer 2")
	s, err := NewReconciler(l).ReconcilePartial(ctx, pageBoard, []posting.Posting{twin, fresh}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.New != 1 || s.ListedDuplicate != 1 || s.Duplicate != 1 {
		t.Fatalf("page listing: %+v, want 1 new, 1 listed duplicate, 1 duplicate", s)
	}

	// An ATS posting arriving after its page twin marks the page record: a
	// duplicate, but not a listed one.
	l2 := openLedger(t)
	if _, err := Reconcile(ctx, l2, pageBoard, []posting.Posting{twin}, t0); err != nil {
		t.Fatal(err)
	}
	s, err = Reconcile(ctx, l2, board, []posting.Posting{gh}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.New != 1 || s.Duplicate != 1 || s.ListedDuplicate != 0 {
		t.Fatalf("ATS listing: %+v, want 1 new, 1 marked duplicate, 0 listed", s)
	}

	// The page listed again: the stored duplicate is known, not listed anew.
	s, err = NewReconciler(l2).ReconcilePartial(ctx, pageBoard, []posting.Posting{twin}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if s.ListedDuplicate != 0 || s.Unchanged+s.Revived != 1 {
		t.Fatalf("relisted duplicate: %+v", s)
	}
}
