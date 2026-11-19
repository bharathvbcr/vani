package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A posting with several bare US options is one unread posting, not one per
// option, and a dropped one is not also counted.
func TestAttackBareUSCountsOncePerPosting(t *testing.T) {
	x := config.Exclude{OnlyLocations: []string{"TX"}}
	mk := func(k, loc string) ledger.Record {
		return ledger.Record{Key: posting.Key(k), Status: posting.Live, Posting: posting.Posting{Key: posting.Key(k), Title: "Engineer", Location: loc}}
	}
	recs := []ledger.Record{
		mk("gh:a:1", "Austin; Seattle; Boston; Austin"),
		mk("gh:a:2", "Denver, CO"),
		mk("gh:a:3", "Austin, TX"),
		mk("gh:a:4", "Austin; Denver, CO"),
	}
	ch := ExclusionChanges(recs, DropPolicy{Exclude: x.Match, ExcludeUnreadable: x.LocationUnreadable}, nil)
	if len(ch.Unread) != 2 || ch.Unread[0].Key != "gh:a:1" || ch.Unread[1].Key != "gh:a:4" {
		t.Fatalf("Unread = %v, want gh:a:1 and gh:a:4 once each", ch.Unread)
	}
	if len(ch.Drop) != 1 || ch.Drop[0].Key != "gh:a:2" {
		t.Fatalf("Drop = %v, want only gh:a:2", ch.Drop)
	}
}
