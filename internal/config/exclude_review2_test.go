package config

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The review of 2e347f4, from the exclusions' side. Each row says whether
// only_locations ["United States"] and exclude.locations ["United States"]
// drop the posting, written from the cost model: a wrong drop is the worst
// error, and a string that cannot be judged is kept by both.
func TestExcludeReview2USRules(t *testing.T) {
	only := Exclude{OnlyLocations: []string{"United States"}}
	ex := Exclude{Locations: []string{"United States"}}
	for _, c := range []struct {
		loc              string
		remote           bool
		onlyDrop, exDrop bool
	}{
		// F1: a US city and its state, with a foreign word after it. Kept by
		// only_locations (it is US); kept by exclude.locations too, since
		// the board names somewhere outside the US beside it.
		{"San Francisco, CA (Remote - Canada OK)", false, false, false},
		{"Seattle, WA (open to Canada)", false, false, false},
		{"Seattle, WA, Canada", false, false, false},
		{"Austin, TX - Mexico", false, false, false},
		{"Austin, TX (EMEA hours)", false, false, false},
		{"Boston, MA (UK hours)", false, false, false},
		{"Miami, FL (LATAM)", false, false, false},
		{"Austin, TX (Canada only)", false, false, false},
		{"New York, NY (Europe)", false, false, false},
		{"Austin, Texas (Mexico team)", false, false, false},
		// The foreign address shape is foreign.
		{"Madrid, MD, Spain", false, true, false},
		{"Westport, MO, Ireland", false, true, false},
		// F3: not in the US.
		{"Not in US", false, true, false},
		{"Not in the US", true, true, false},
		{"Remote, but not in the US", true, true, false},
		{"Not based in the US", false, true, false},
		{"Not within the US", false, true, false},
		{"Bentonville, AR (Canada OK)", false, false, false},
		{"Bentonville, AR (EMEA hours)", false, false, false},
		// F2: US names outside the tables are kept by only_locations.
		{"Upstate New York", false, false, true},
		{"Northern New Jersey", false, false, true},
		{"Western Washington", false, false, true},
		{"South Florida", false, false, true},
		{"Central Texas", false, false, true},
		{"North Texas", false, false, true},
		{"Remote - Must be located in Georgia", true, false, true},
		{"Northern Virginia", false, false, true},
		{"NoVA", false, false, true},
		{"Long Island", false, false, true},
		{"Orange County", false, false, true},
		{"CONUS", false, false, true},
		{"Remote - AMER", true, false, false},
		{"Remote, California, United States, AMER", false, false, true},
		{"Remote - NAMER", true, false, false},
		{"AMER", false, false, false},
		{"NAMER", false, false, false},
		{"Americas", false, false, false},
		{"Nationwide", false, false, false},
		{"Remote EST", true, false, false},
		{"CST", false, false, false},
		{"Pacific Time", false, false, false},
		// F7, the owner's decision: a bare Paris is kept unjudged by both.
		{"Paris", true, false, false},
		{"Paris", false, false, false},
		{"Paris / London", false, false, false},
		{"Paris, France", false, true, false},
		{"Paris, Île-de-France", false, true, false},
		{"Paris, TX", false, false, true},
		// F4.
		{"Plano TX 75024", false, false, true},
		{"Dallas-Fort Worth Metroplex", false, false, true},
		{"Raleigh-Durham", false, false, true},
		{"Raleigh-Durham-Chapel Hill Area", false, false, true},
		// F5.
		{"Washington, D.C.", false, false, true},
		{"Washington, DC Metro Area", false, false, true},
		// F6: remote within the US but outside one state.
		{"Remote, USA (non-California)", true, false, true},
		{"Remote (not available in CA)", true, false, true},
	} {
		p := posting.Posting{Location: c.loc, Remote: c.remote}
		if _, got := only.Match(p); got != c.onlyDrop {
			t.Errorf("only_locations [United States] on %q (remote %v): dropped %v, want %v", c.loc, c.remote, got, c.onlyDrop)
		}
		if _, got := ex.Match(p); got != c.exDrop {
			t.Errorf("exclude.locations [United States] on %q (remote %v): dropped %v, want %v", c.loc, c.remote, got, c.exDrop)
		}
	}
}

// A state entry is the state, not a town named for it: "Washington" is not
// Washington, D.C., and excluding California does not drop a role that says
// it is not available there.
func TestExcludeReview2StateEntries(t *testing.T) {
	for _, c := range []struct {
		x    Exclude
		loc  string
		drop bool
	}{
		{Exclude{OnlyLocations: []string{"Washington"}}, "Washington, D.C.", true},
		{Exclude{OnlyLocations: []string{"DC"}}, "Washington, D.C.", false},
		{Exclude{Locations: []string{"Washington"}}, "Washington, D.C.", false},
		{Exclude{Locations: []string{"California"}}, "Remote (not available in CA)", false},
		{Exclude{Locations: []string{"California"}}, "Remote, USA (non-California)", false},
		{Exclude{OnlyLocations: []string{"Texas"}}, "Plano TX 75024", false},
		{Exclude{OnlyLocations: []string{"Virginia"}}, "Northern Virginia", false},
	} {
		_, got := c.x.Match(posting.Posting{Location: c.loc})
		if got != c.drop {
			t.Errorf("%+v on %q: dropped %v, want %v", c.x, c.loc, got, c.drop)
		}
	}
}
