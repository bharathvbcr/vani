package location

import (
	"slices"
	"strings"
	"testing"
)

// Finding 2 and 5: each option of a comma list is judged on its own, and the
// country list comes from the same verdicts, so InUS and Countries agree.
func TestReviewOptionsOfAList(t *testing.T) {
	for _, c := range []struct {
		loc       string
		allUS     bool     // every option is in the US
		countries []string // must all be listed (order free)
	}{
		{"US & Canada", false, []string{"United States", "Canada"}},
		{"Toronto, Austin", false, []string{"United States", "Canada"}},
		{"Toronto, Austin, Montreal", false, []string{"United States", "Canada"}},
		{"London, Houston, New York, Singapore", false, []string{"United States", "Singapore"}},
		{"Mexico City, Monterrey, Houston", false, []string{"United States", "Mexico"}},
		{"Bengaluru, Delhi, Columbus", false, []string{"United States"}},
		{"Toronto, Montreal, New York", false, []string{"United States", "Canada"}},
		// Parse reads the trailing "New York" as the state of Mexico (Mexico,
		// NY is a town): Canada, or Mexico, NY. Either way a US option and
		// Canada.
		{"Canada, Mexico, New York", false, []string{"United States", "Canada"}},
		{"Austin, Texas, United States", true, []string{"United States"}},
		{"US, CA, Santa Clara", true, []string{"United States"}},
		{"Mountain View, California (HQ)", true, []string{"United States"}},
		{"New York, NY", true, []string{"United States"}},
		{"Washington, DC", true, []string{"United States"}},
	} {
		l := Parse(c.loc, false)
		if !InUS(l) {
			t.Errorf("%q: not read as US", c.loc)
		}
		all := len(l.Places) > 0
		for _, p := range l.Places {
			for _, o := range PlaceOptions(p) {
				all = all && o.Verdict == VerdictUS
			}
		}
		if all != c.allUS {
			t.Errorf("%q: every option US = %v, want %v (options %+v)", c.loc, all, c.allUS, optionsOf(l))
		}
		got := Countries(l)
		for _, w := range c.countries {
			if !slices.Contains(got, w) {
				t.Errorf("%q: countries %v, want %s among them", c.loc, got, w)
			}
		}
	}
	// "Georgia" is the country beside foreign places, unjudged at worst.
	for _, loc := range []string{"London, Berlin, Georgia", "Lagos, Nairobi, Georgia"} {
		if InUS(Parse(loc, false)) {
			t.Errorf("%q read as US: Georgia beside foreign cities is not the state", loc)
		}
	}
}

func optionsOf(l Location) [][]Option {
	var out [][]Option
	for _, p := range l.Places {
		out = append(out, PlaceOptions(p))
	}
	return out
}

// TestPlaceVerdictsAgree: for every place, PlaceInUS, the option verdicts and
// the country list say the same thing, so --us-first and --country "United
// States" can never disagree about one place.
func TestPlaceVerdictsAgree(t *testing.T) {
	var locs []string
	for _, set := range oracleSets {
		for _, r := range readOracle(t, set.path, set.minRows) {
			locs = append(locs, r.Location)
		}
	}
	locs = append(locs, "London, Houston, New York, Singapore", "Toronto, Austin", "Santa Clara, Cuba",
		"US & Canada", "Remote - outside US", "Georgia", "Tbilisi, Georgia", "Pune, MH, IN", "Berlin, DE")
	for _, loc := range locs {
		for _, remote := range []bool{false, true} {
			l := Parse(loc, remote)
			for _, p := range l.Places {
				us := PlaceInUS(p)
				anyOpt := slices.ContainsFunc(PlaceOptions(p), func(o Option) bool { return o.Verdict == VerdictUS })
				if us != anyOpt {
					t.Errorf("%q place %+v: PlaceInUS %v, an option US %v", loc, p, us, anyOpt)
				}
				if inC := slices.Contains(placeCountries(p), "United States"); inC != us {
					t.Errorf("%q place %+v: PlaceInUS %v, United States in countries %v", loc, p, us, inC)
				}
			}
			usScope := false
			for _, s := range strings.Split(l.RemoteScope, ",") {
				switch strings.TrimSpace(s) {
				case "US", "North America", "Americas", "Anywhere":
					usScope = true
				}
			}
			if InUS(l) != (slices.Contains(Countries(l), "United States") || usScope) {
				t.Errorf("%q (remote %v): InUS %v, countries %v, scope %q", loc, remote, InUS(l), Countries(l), l.RemoteScope)
			}
		}
	}
}
