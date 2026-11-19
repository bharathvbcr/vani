package config

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestOnlyLocationsStateEntryKeepsBareUSCity: with only_locations naming a US
// state, a US place whose state the board did not write ("Austin") cannot be
// judged against the list, so it is kept and counted as location unreadable
// (LocationUnreadable feeds pipeline.CauseLocationUnreadable). A place read as
// another state, or as a foreign country, is still dropped.
func TestOnlyLocationsStateEntryKeepsBareUSCity(t *testing.T) {
	tx := Exclude{OnlyLocations: []string{"TX"}}
	for _, c := range []struct {
		name       string
		x          Exclude
		loc        string
		remote     bool
		drop       bool
		unreadable bool
	}{
		{"bare US city", tx, "Austin", false, false, true},
		{"bare US city, another", tx, "Seattle", false, false, true},
		{"city and state", tx, "Austin, TX", false, false, false},
		{"duplicate bare city", tx, "Austin; Austin", false, false, true},
		{"bare city beside a matching place", tx, "Seattle; Austin, TX", false, false, false},
		{"other state read", tx, "Denver, CO", false, true, false},
		{"bare city beside another state", tx, "Austin; Denver, CO", false, false, true},
		{"foreign, with province", tx, "Toronto, ON", false, true, false},
		{"foreign country", tx, "London, United Kingdom", false, true, false},
		{"full state name entry, bare city", Exclude{OnlyLocations: []string{"Texas"}}, "Austin", false, false, true},
		{"Washington state entry vs DC", Exclude{OnlyLocations: []string{"Washington"}}, "Washington, DC", false, true, false},
		{"Washington state entry vs WA", Exclude{OnlyLocations: []string{"Washington"}}, "Seattle, WA", false, false, false},
		{"country entry keeps a bare US city as a match", Exclude{OnlyLocations: []string{"United States"}}, "Austin", false, false, false},
		{"state and country entries", Exclude{OnlyLocations: []string{"TX", "United States"}}, "Austin", false, false, false},
		{"blank", tx, "", false, false, true},
		{"whitespace", tx, "   ", false, false, true},
		{"remote, no scope", tx, "Remote", true, false, true},
		{"unparseable", tx, "TBD", false, false, true},
		{"oversized", tx, strings.Repeat("Austin ", 1500), false, false, true},
	} {
		p := posting.Posting{Location: c.loc, Remote: c.remote}
		_, dropped := c.x.Match(p)
		if dropped != c.drop {
			t.Errorf("%s (%.20q): dropped %v, want %v", c.name, c.loc, dropped, c.drop)
		}
		if got := c.x.LocationUnreadable(p); got != c.unreadable {
			t.Errorf("%s (%.20q): LocationUnreadable %v, want %v", c.name, c.loc, got, c.unreadable)
		}
	}
	// Garbage unicode no table knows is the existing unrecognised class: it is
	// not a place read, and keeps its own marked drop (see
	// TestOnlyLocationsNamesTheUnrecognisedCauseOfADrop); it must never panic.
	for _, loc := range []string{"\u202e\U0001F9EA\x00", "ΩΩΩ", "東京"} {
		tx.Match(posting.Posting{Location: loc})
		tx.LocationUnreadable(posting.Posting{Location: loc})
	}
}
