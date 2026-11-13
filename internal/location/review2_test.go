package location

import (
	"strings"
	"testing"
	"time"
)

// The review of 2e347f4 (the per-option verdict), finding by finding. Each
// want is what a person reads the string as, under the exclusions' cost
// model: a US role read as foreign is the costly miss, so a string that
// cannot be resolved is ambiguous (kept unjudged), never "not US".

// F1: a US city confirmed by its state code stays a US option when a foreign
// word follows; the foreign word is an option of its own, as it already was
// after a state written in full ("New York, NY (Europe)", "Austin, Texas
// (Mexico team)"). A city of the foreign lists, or a name nothing places,
// before a code and a country is the foreign place: the code is that
// country's subdivision (Madrid MD = Comunidad de Madrid, Westport MO = Mayo,
// Hoofddorp NH = Noord-Holland). Every string of that shape in the live
// ledger (2026-11-12) is foreign.
func TestReview2USCityStateThenAbroad(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"San Francisco, CA (Remote - Canada OK)", false, wantUS},
		{"Seattle, WA (open to Canada)", false, wantUS},
		{"Seattle, WA, Canada", false, wantUS},
		{"Austin, TX - Mexico", false, wantUS},
		{"Austin, TX (EMEA hours)", false, wantUS},
		{"Boston, MA (UK hours)", false, wantUS},
		{"Miami, FL (LATAM)", false, wantUS},
		{"Austin, TX (Canada only)", false, wantUS},
		{"New York, NY (Europe)", false, wantUS},
		{"Austin, Texas (Mexico team)", false, wantUS},
		// A town no table holds, placed by its state code: a note naming a
		// country or a region after it ("Canada OK", "EMEA hours", "LATAM")
		// is an option of its own. Only a country written as a whole part
		// is an address's country (below).
		{"Bentonville, AR (Canada OK)", false, wantUS},
		{"Bentonville, AR (EMEA hours)", false, wantUS},
		{"Smallville, KS (LATAM)", false, wantUS},
		{"Smallville, KS (Europe)", false, wantUS},
		// A name as likely US as not, placed by its state code.
		{"Portland, OR (Canada OK)", false, wantUS},
		{"Cambridge, MA, UK hours", false, wantUS},
		// The foreign address shape: city, subdivision code, country.
		{"Richmond, CA, Canada", false, wantNotUS},
		{"Madrid, MD, Spain", false, wantNotUS},
		{"Chennai, TN, India", false, wantNotUS},
		{"Westport, MO, Ireland", false, wantNotUS},
		{"Cork, CO, Ireland", false, wantNotUS},
		{"Hoofddorp, NH, Netherlands", false, wantNotUS},
		{"Curitiba, PR, Brazil", false, wantNotUS},
		{"Perth, WA, Australia", false, wantNotUS},
		// A code that is also the country's ISO code names the country.
		{"Netanya, IL, Israel", false, wantNotUS},
		{"Cali, CO, Colombia", false, wantNotUS},
	})
}

// F3: "not in" and "not in the" before a place negate it.
func TestReview2NotIn(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Not in US", false, wantNotUS},
		{"Not in US", true, wantNotUS},
		{"Not in the US", false, wantNotUS},
		{"Not in the US", true, wantNotUS},
		{"Remote, but not in the US", false, wantNotUS},
		{"Remote, but not in the US", true, wantNotUS},
		{"Remote - outside CONUS", true, wantNotUS},
		{"Not based in the US", false, wantNotUS},
		{"Remote - not based in the US", true, wantNotUS},
		{"Not within the US", false, wantNotUS},
		{"Not located in the US", false, wantNotUS},
		{"Not based in office - Austin, TX", false, wantUS},
		// A negated work mode is still the mode, not a place.
		{"Not in office - Austin, TX", false, wantUS},
		{"Austin, TX (not on site)", false, wantUS},
	})
}

// F2: US names a board writes that the tables did not hold. A US state name
// ending a regional phrase is that state; the region codes AMER and NAMER
// take in the US like Americas and North America, kept unjudged; US-only names
// (NoVA, Long Island, Orange County, CONUS) are the US; a US time zone or
// "Nationwide" is as likely the US as not, so it is kept unjudged.
func TestReview2USNamesOutsideTheTables(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Upstate New York", false, wantUS},
		{"Northern New Jersey", false, wantUS},
		{"Western Washington", false, wantUS},
		{"South Florida", false, wantUS},
		{"Central Texas", false, wantUS},
		{"North Texas", false, wantUS},
		{"Northern Virginia", false, wantUS},
		{"Western North Carolina", false, wantUS},
		{"Greater New York", false, wantUS},
		{"Remote - Must be located in Georgia", false, wantUS},
		{"Remote - Must be located in Georgia", true, wantUS},
		{"NoVA", false, wantUS},
		{"Nova Scotia", false, wantNotUS},
		{"Alberta; Nova Scotia; Ontario", false, wantNotUS},
		{"Halifax, Nova Scotia", false, wantNotUS},
		{"Long Island", false, wantUS},
		{"Orange County", false, wantUS},
		{"CONUS", false, wantUS},
		{"Remote - CONUS", true, wantUS},
		// A wide region written as a place is kept unjudged, as "Americas"
		// is; not a Parse scope (salesRegions).
		{"Remote - AMER", true, wantAmbiguous},
		{"Remote - NAMER", true, wantAmbiguous},
		{"Remote, California, United States, AMER", false, wantUS},
		{"AMER", false, wantAmbiguous},
		{"NAMER", false, wantAmbiguous},
		{"Americas", false, wantAmbiguous},
		{"Nationwide", false, wantAmbiguous},
		{"Remote EST", false, wantAmbiguous},
		{"Remote EST", true, wantAmbiguous},
		{"CST", false, wantAmbiguous},
		{"Pacific Time", false, wantAmbiguous},
		{"Remote - Pacific Time", true, wantAmbiguous},
		// Distractors: whole state names, foreign regions and states that
		// share a time zone's letters stay what they were.
		{"West Virginia", false, wantUS},
		{"North Carolina", false, wantUS},
		{"Hartford, CT", false, wantUS},
		{"Bozeman, MT", false, wantUS},
		{"Baja California", false, wantNotUS},
		{"Tijuana, Baja California, Mexico", false, wantNotUS},
		{"Republic of Georgia", false, wantNotUS},
		{"Western Australia", false, wantNotUS},
		{"New South Wales", false, wantNotUS},
		{"Northern Ireland", false, wantNotUS},
		// Georgia beside foreign places stays unjudged.
		{"London, Berlin, Georgia", false, wantAmbiguous},
	})
}

// F7, the owner's decision (2026-11-12): a bare Paris is ambiguous (kept by
// only_locations, never US); with a country or a French region it is France;
// with a US state it is the US.
func TestReview2Paris(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Paris", false, wantAmbiguous},
		{"Paris", true, wantAmbiguous},
		{"Paris / London", false, wantAmbiguous},
		{"Paris, France", false, wantNotUS},
		{"Paris, FR", false, wantNotUS},
		{"Paris, Île-de-France", false, wantNotUS},
		{"Paris, Île-de-France, France", false, wantNotUS},
		{"Paris, TX", false, wantUS},
		{"Paris, Texas", false, wantUS},
		{"Paris, Kentucky", false, wantUS},
	})
}

// F4: forms 9f1db69 read as US.
func TestReview2USForms(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Plano TX 75024", false, wantUS},
		{"Dallas-Fort Worth Metroplex", false, wantUS},
		{"Raleigh-Durham", false, wantUS},
		{"Raleigh-Durham-Chapel Hill Area", false, wantUS},
		// A code alone after a foreign name, with no zip, is not split.
		{"Pune MH", false, wantNotUS},
	})
}

// F5: "Washington" before DC is the city, one option in DC.
func TestReview2WashingtonDC(t *testing.T) {
	for _, c := range []struct {
		loc   string
		state string
	}{
		{"Washington, D.C.", "DC"},
		{"Washington, DC", "DC"},
		{"Washington, DC Metro Area", "DC"},
		{"Washington DC", "DC"},
	} {
		l := Parse(c.loc, false)
		var opts []Option
		for _, p := range l.Places {
			opts = append(opts, PlaceOptions(p)...)
		}
		if len(opts) != 1 || opts[0].State != c.state || opts[0].Verdict != VerdictUS {
			t.Errorf("%q: options %+v, want one US option in %s", c.loc, opts, c.state)
		}
	}
	// Two states written in full are a list.
	l := Parse("New York, New Jersey", false)
	var states []string
	for _, p := range l.Places {
		for _, o := range PlaceOptions(p) {
			states = append(states, o.State)
		}
	}
	if strings.Join(states, ",") != "NY,NJ" {
		t.Errorf(`"New York, New Jersey": option states %v, want NY,NJ`, states)
	}
}

// F6: a negated US state is not a place; it says the role is in the US
// elsewhere. "Remote, USA (non-California)" parses as it did before the
// per-option change: no place, remote within the US.
func TestReview2NegatedState(t *testing.T) {
	for _, c := range []struct {
		loc    string
		remote bool
	}{
		{"Remote, USA (non-California)", true},
		{"Remote, USA (non-California)", false},
		{"Remote (not available in CA)", true},
		{"Remote (not available in CA)", false},
		{"Remote - US, excluding Hawaii", true},
	} {
		l := Parse(c.loc, c.remote)
		if len(l.Places) != 0 || l.RemoteScope != "US" || !l.Remote {
			t.Errorf("%q (remote %v): places %+v, scope %q, remote %v; want no place, remote scope US", c.loc, c.remote, l.Places, l.RemoteScope, l.Remote)
		}
	}
	// A negated country or region is left to the reader of the place.
	if l := Parse("Remote - US (no Canada)", true); !InUS(l) {
		t.Errorf(`"Remote - US (no Canada)" not US: %+v`, l)
	}
}

// The inputs are capped and the reading linear: a 1 MB hostile string runs
// through Parse and PlaceOptions in bounded time.
func TestReview2MegabyteInput(t *testing.T) {
	for _, s := range []string{
		strings.Repeat("Not in the ", 100000),
		strings.Repeat("Upstate Northern Western ", 45000),
		strings.Repeat("Seattle, WA, Canada, ", 50000),
		strings.Repeat("Washington, ", 90000) + "D.C.",
		strings.Repeat("non-", 260000),
	} {
		start := time.Now()
		l := Parse(s, true)
		n := 0
		for _, p := range l.Places {
			n += len(PlaceOptions(p))
		}
		PlaceOptions(Place{Area: s, Region: s})
		d := time.Since(start)
		if d > 2*time.Second {
			t.Errorf("%d-byte input took %v", len(s), d)
		}
		t.Logf("%d-byte input: Parse and PlaceOptions in %v (%d options)", len(s), d, n)
		if len(l.Places) > MaxPlaces {
			t.Errorf("%d places, cap %d", len(l.Places), MaxPlaces)
		}
	}
}
