package config

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestOnlyLocationsUSReadsTheGazetteer: an entry naming the US keeps every
// place location.PlaceInUS reads as the US, not only places whose state Parse
// found. Before, "San Francisco" and "US, CA, Santa Clara" were dropped.
func TestOnlyLocationsUSReadsTheGazetteer(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"United States"}}
	for _, c := range []struct {
		loc    string
		remote bool
		drop   bool
	}{
		{"San Francisco", false, false},
		{"SF Office", true, false},
		{"New York City", false, false},
		{"US, CA, Santa Clara; US, TX, Austin", false, false},
		{"California - Palo Alto", false, false},
		{"Mountain View, California (HQ)", false, false},
		{"Watertown, MA USA", false, false},
		{"New York, London, Singapore", false, false},
		{"US locations", false, false},
		{"Remote - US", true, false},
		{"Remote (world)", false, false},
		{"Remote - Anywhere", true, false},
		{"Remote - North America", true, false},
		{"BLANK,BLANK,Multiple Locations", false, false},
		{"Remote - EMEA", true, true},
		{"London", false, true},
		{"Remote - India", true, true},
		{"San Jose, Costa Rica", true, true},
		{"Madrid, MD, Spain", false, true},
		{"Vancouver, BC", false, true},
		{"Cambridge, UK", false, true},
	} {
		_, got := x.Match(posting.Posting{Location: c.loc, Remote: c.remote})
		if got != c.drop {
			t.Errorf("only_locations [United States] on %q (remote %v): dropped %v, want %v", c.loc, c.remote, got, c.drop)
		}
	}
}

// TestOnlyLocationsAmbiguous: a bare name as likely US as foreign, or a region
// that takes in the US, is kept and counted as not judged (location_unreadable),
// never dropped. A place beside it that matches decides as before.
func TestOnlyLocationsAmbiguous(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"United States"}}
	for _, loc := range []string{"Cambridge", "Portland", "Vancouver", "North America", "Americas or APAC"} {
		p := posting.Posting{Location: loc}
		if _, dropped := x.Match(p); dropped {
			t.Errorf("%q dropped; an ambiguous place is kept", loc)
		}
		if !x.LocationUnreadable(p) {
			t.Errorf("%q kept but not counted as unread", loc)
		}
	}
	p := posting.Posting{Location: "Cambridge; Austin, TX"}
	if _, dropped := x.Match(p); dropped || x.LocationUnreadable(p) {
		t.Errorf("Cambridge beside Austin: dropped %v, unread %v; want kept and judged", dropped, x.LocationUnreadable(p))
	}
	// exclude.locations is untouched by ambiguity: it drops only when every
	// option matches, and an ambiguous place matches no entry.
	l := Exclude{Locations: []string{"United Kingdom"}}
	if _, dropped := l.Match(posting.Posting{Location: "Cambridge"}); dropped {
		t.Error("exclude.locations [United Kingdom] dropped a bare Cambridge")
	}
}

// TestExcludeLocationsUSAgreesWithOnly: exclude.locations ["United States"]
// reads the same US places as only_locations does, so the two lists cannot
// disagree about a place.
func TestExcludeLocationsUSAgreesWithOnly(t *testing.T) {
	ex := Exclude{Locations: []string{"United States"}}
	for _, loc := range []string{"San Francisco", "US, CA, Santa Clara", "California - Palo Alto"} {
		if _, dropped := ex.Match(posting.Posting{Location: loc}); !dropped {
			t.Errorf("exclude.locations [United States] kept %q, a US place", loc)
		}
	}
	if _, dropped := ex.Match(posting.Posting{Location: "San Francisco; London"}); dropped {
		t.Error("exclude.locations [United States] dropped a role also listed in London")
	}
	if _, dropped := ex.Match(posting.Posting{Location: "Remote - Anywhere", Remote: true}); dropped {
		t.Error("exclude.locations [United States] dropped a worldwide remote role")
	}
}
