package pipeline

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func unreadableWhenBlank(p posting.Posting) bool { return p.Location == "" }

// A Live posting an exclusion rule could not read (a blank location under a
// location rule) is kept and reported in Unread, so the run counts it as
// checked-by-nothing; marked, past-triage, matched and readable ones are not.
func TestExclusionChangesReportsUnreadableLivePostings(t *testing.T) {
	mk := func(k string, st posting.Status, title, loc string) ledger.Record {
		return ledger.Record{Key: posting.Key(k), Status: st, Posting: posting.Posting{Key: posting.Key(k), Title: title, Location: loc}}
	}
	recs := []ledger.Record{
		mk("gh:a:1", posting.Live, "Engineer", ""),        // unreadable, counted
		mk("gh:a:2", posting.Live, "Engineer", "Austin"),  // readable
		mk("gh:a:3", posting.Live, "Marked", ""),          // the owner marked it
		mk("gh:a:4", posting.Digested, "Engineer", ""),    // already past triage
		mk("gh:a:5", posting.Live, "Engineer Intern", ""), // dropped by the title rule, not kept
	}
	p := DropPolicy{Exclude: excludeTitle("Intern"), ExcludeUnreadable: unreadableWhenBlank}
	ch := ExclusionChanges(recs, p, func(k posting.Key) bool { return k == "gh:a:3" })
	if len(ch.Unread) != 1 || ch.Unread[0].Key != "gh:a:1" {
		t.Fatalf("Unread = %v, want only gh:a:1", ch.Unread)
	}
	if len(ch.Drop) != 1 || ch.Drop[0].Key != "gh:a:5" {
		t.Fatalf("Drop = %v", ch.Drop)
	}
	if got := ExclusionChanges(recs, DropPolicy{Exclude: excludeTitle("Intern")}, nil); len(got.Unread) != 0 {
		t.Errorf("a policy with no unreadable test reported %d", len(got.Unread))
	}
}

// ApplyExclusions notes each unreadable posting on the fail-open counter, once
// per posting per pass, and a nil counter counts nothing.
func TestApplyExclusionsCountsUnreadableFailOpen(t *testing.T) {
	r, _, l, _ := excludeRunner(t, "Engineer", "Engineer II")
	p := DropPolicy{Exclude: excludeTitle("Intern"), ExcludeUnreadable: unreadableWhenBlank}
	fo := &FailOpen{}
	if _, err := ApplyExclusions(context.Background(), l, t0, p, nil, fo); err != nil {
		t.Fatal(err)
	}
	// ghPost gives postings a location; blank it through a policy that calls
	// every posting unreadable.
	all := DropPolicy{Exclude: excludeTitle("Intern"), ExcludeUnreadable: func(posting.Posting) bool { return true }}
	if _, err := ApplyExclusions(context.Background(), l, t0, all, nil, fo); err != nil {
		t.Fatal(err)
	}
	if got := fo.Counts(); got.LocationUnreadable != 2 || got.Total() != 2 {
		t.Fatalf("counts %+v, want 2 location_unreadable", got)
	}
	if _, err := ApplyExclusions(context.Background(), l, t0, all, nil, nil); err != nil {
		t.Fatalf("a nil counter: %v", err)
	}
	_ = r
}
