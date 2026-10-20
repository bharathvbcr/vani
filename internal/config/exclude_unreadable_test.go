package config

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A location rule cannot judge a posting whose location names nothing (or, for
// only_locations, is remote with no place and no scope): the posting is kept,
// and LocationUnreadable says the check could not run.
func TestExcludeLocationUnreadable(t *testing.T) {
	both := Exclude{Locations: []string{"London"}, OnlyLocations: []string{"United States"}}
	for _, tc := range []struct {
		name string
		x    Exclude
		p    posting.Posting
		want bool
	}{
		{"blank, locations rule", Exclude{Locations: []string{"London"}}, posting.Posting{Location: ""}, true},
		{"blank, only_locations rule", Exclude{OnlyLocations: []string{"United States"}}, posting.Posting{Location: "  "}, true},
		{"unparseable", Exclude{Locations: []string{"London"}}, posting.Posting{Location: "TBD"}, true},
		{"remote with no place or scope, only_locations", Exclude{OnlyLocations: []string{"United States"}}, posting.Posting{Location: "Remote", Remote: true}, true},
		{"remote with no place or scope, locations only", Exclude{Locations: []string{"London"}}, posting.Posting{Location: "Remote", Remote: true}, false},
		{"readable place", both, posting.Posting{Location: "Austin, TX"}, false},
		{"readable remote scope", both, posting.Posting{Location: "Remote (US)"}, false},
		{"no location rule at all", Exclude{Titles: []string{"intern"}}, posting.Posting{Location: ""}, false},
		{"empty exclude", Exclude{}, posting.Posting{Location: ""}, false},
	} {
		if got := tc.x.LocationUnreadable(tc.p); got != tc.want {
			t.Errorf("%s: LocationUnreadable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
