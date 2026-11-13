package config

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The review of 9f1db69, from the exclusions' side. exclude.locations drops a
// posting only when EVERY option it lists matches; only_locations keeps it
// when SOME option does. A string the parser cannot judge is kept by both.

// Finding 2: exclude.locations ["United States"] drops only a posting whose
// every option is in the US. 9f1db69 read a comma list with one US city as
// wholly US and dropped it.
func TestExcludeLocationsUSJudgesEachOption(t *testing.T) {
	x := Exclude{Locations: []string{"United States"}}
	for _, c := range []struct {
		loc    string
		remote bool
		drop   bool
	}{
		{"US & Canada", false, false},
		{"Toronto, Austin", false, false},
		{"Toronto, Austin, Montreal", false, false},
		{"London, Houston, New York, Singapore", false, false},
		{"Mexico City, Monterrey, Houston", false, false},
		{"Bengaluru, Delhi, Columbus", false, false},
		{"Toronto, Montreal, New York", false, false},
		{"Canada, Mexico, New York", false, false},
		{"Austin, TX; London", false, false},
		// Wholly US: dropped, as before.
		{"Austin, TX", false, true},
		{"San Francisco", false, true},
		{"US, CA, Santa Clara", false, true},
		{"Austin, Texas, United States", false, true},
		{"New York, NY", false, true},
		{"Remote - US", true, true},
		{"Austin, TX; Seattle, WA", false, true},
		// Unjudged, foreign or negated: kept.
		{"Cambridge", false, false},
		{"San Jose", false, false},
		{"Santa Clara, Cuba", false, false},
		{"Remote - outside US", true, false},
		{"Contact us for locations", false, false},
		{"Global locations", false, false},
	} {
		_, got := x.Match(posting.Posting{Location: c.loc, Remote: c.remote})
		if got != c.drop {
			t.Errorf("exclude.locations [United States] on %q (remote %v): dropped %v, want %v", c.loc, c.remote, got, c.drop)
		}
	}
}

// Every entry, not only the US, is judged per option: excluding Toronto does
// not drop a role also listed in Austin.
func TestExcludeLocationsEntryJudgesEachOption(t *testing.T) {
	x := Exclude{Locations: []string{"Toronto"}}
	if _, got := x.Match(posting.Posting{Location: "Toronto, Austin"}); got {
		t.Error(`exclude.locations [Toronto] dropped "Toronto, Austin"`)
	}
	if _, got := x.Match(posting.Posting{Location: "Toronto, ON"}); !got {
		t.Error(`exclude.locations [Toronto] kept "Toronto, ON"`)
	}
	if _, got := x.Match(posting.Posting{Location: "Toronto, Ontario, Canada"}); !got {
		t.Error(`exclude.locations [Toronto] kept "Toronto, Ontario, Canada"`)
	}
	y := Exclude{Locations: []string{"Canada"}}
	if _, got := y.Match(posting.Posting{Location: "Toronto, Ontario, Canada"}); !got {
		t.Error(`exclude.locations [Canada] kept "Toronto, Ontario, Canada"`)
	}
	if _, got := y.Match(posting.Posting{Location: "US & Canada"}); got {
		t.Error(`exclude.locations [Canada] dropped "US & Canada"`)
	}
}

// Findings 1, 3, 4, 6, 7, 8 and 9 from only_locations ["United States"]: what
// it must keep (a US option, or nothing it can judge) and what it must drop.
func TestOnlyLocationsUSReview(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"United States"}}
	for _, c := range []struct {
		loc    string
		remote bool
		drop   bool
	}{
		// 1: placeholders naming the world name no place.
		{"Global locations", false, false},
		{"Multiple Global Locations", false, false},
		{"Various locations worldwide", false, false},
		{"Multiple Locations Worldwide", false, false},
		// 2: a US option keeps the list.
		{"US & Canada", false, false},
		{"Toronto, Austin", false, false},
		{"London, Houston, New York, Singapore", false, false},
		{"Bengaluru, Delhi, Columbus", false, false},
		// 3: not the US.
		{"South America", false, true},
		{"Remote - South America", true, true},
		{"Americas - Remote (excluding US)", true, true},
		{"Remote - outside US", true, true},
		{"Remote - Not US", true, true},
		{"Remote (Americas, non-US)", true, true},
		{"Remote - US (no Canada)", true, false},
		// 4: US names abroad.
		{"Santa Clara, Cuba", false, true},
		{"Houston, Scotland", false, true},
		{"Bay Area, Ontario", false, true},
		{"Austin, Victoria", false, true},
		{"SF Express, Shenzhen", false, true},
		{"San Jose", false, false},
		{"Santa Clara, Villa Clara", false, false},
		// 6: the pronoun.
		{"Contact us for locations", false, false},
		// 7: ambiguous with affixes.
		{"Cambridge HQ", false, false},
		{"Greater Portland Area", false, false},
		{"Portland Metro", false, false},
		{"Richmond Area", false, false},
		// 8: US regions; worldwide words.
		{"New England", false, false},
		{"Pacific Northwest", false, false},
		{"DMV Area", false, false},
		{"Remote - East Coast", true, false},
		{"San Francisco, North America", false, false},
		{"Remote - NA", true, false},
		{"Anywhere", false, false},
		{"Worldwide", false, false},
		// 9: "(X only)" and LATAM beside the Americas.
		{"Remote - Americas (LATAM only)", true, true},
		{"Remote - North America (Canada only)", true, true},
		{"Remote - LATAM / Americas", true, true},
		{"Remote (Continental US + Hawaii + Canada Only)", true, false},
		// Kept on purpose: a remote role open across the Americas can be done
		// from the US, whatever office the board also names.
		{"Mexico City; Remote - Americas", true, false},
	} {
		_, got := x.Match(posting.Posting{Location: c.loc, Remote: c.remote})
		if got != c.drop {
			t.Errorf("only_locations [United States] on %q (remote %v): dropped %v, want %v", c.loc, c.remote, got, c.drop)
		}
	}
}
