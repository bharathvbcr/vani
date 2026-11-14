package config

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// only_locations drops a name the tables do not know the way it always has, but
// says so; a place the tables know abroad is dropped without the mark.
func TestOnlyLocationsNamesTheUnrecognisedCauseOfADrop(t *testing.T) {
	x := Exclude{OnlyLocations: []string{"United States"}}
	for _, c := range []struct {
		loc          string
		dropped      bool
		unrecognised bool
	}{
		{"Zzyzxville", true, true},
		{"Quillmont Heights", true, true},
		{"Paris, France", true, false},
		{"Toronto, Canada", true, false},
		{"Paris, France; Zzyzxville", true, true}, // one unknown option is enough: the drop may be wrong
		{"Austin, TX", false, false},
		{"Zzyzxville; Austin, TX", false, false}, // some US option keeps it
		{"", false, false},
	} {
		why, ok := x.Match(posting.Posting{Title: "Engineer", Location: c.loc})
		if ok != c.dropped {
			t.Errorf("%q: dropped %v, want %v (%s)", c.loc, ok, c.dropped, why)
			continue
		}
		if got := strings.Contains(why, UnrecognisedLocationMark); got != c.unrecognised {
			t.Errorf("%q: reason %q, unrecognised %v, want %v", c.loc, why, got, c.unrecognised)
		}
		if ok && !strings.HasPrefix(why, "location is outside only_locations") {
			t.Errorf("%q: the reason's start changed: %q", c.loc, why)
		}
	}
}

// exclude.locations is the owner's own text: an entry that happens to say
// "unrecognised location" never makes a drop read as one.
func TestExcludeLocationsEntryNeverReadsAsUnrecognised(t *testing.T) {
	x := Exclude{Locations: []string{"unrecognised location"}}
	why, ok := x.Match(posting.Posting{Title: "T", Location: "Paris, France"})
	if ok {
		t.Fatalf("matched: %s", why)
	}
	x = Exclude{Locations: []string{"Paris"}}
	why, ok = x.Match(posting.Posting{Title: "T", Location: "Paris, France"})
	if !ok || strings.Contains(why, UnrecognisedLocationMark) {
		t.Errorf("reason %q ok %v", why, ok)
	}
}
