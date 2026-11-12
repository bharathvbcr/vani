package location

import (
	"strings"
	"testing"
)

// TestInUSRecognition pins the forms the US gazetteer and the foreign-country
// check read, and the ones they must not.
func TestInUSRecognition(t *testing.T) {
	for _, c := range []struct {
		in     string
		remote bool
		want   bool
	}{
		// Bare US cities, abbreviations and office suffixes.
		{"San Francisco", false, true},
		{"sf", false, true},
		{"SF Office", true, true},
		{"South San Francisco", false, true},
		{"Fremont Office", false, true},
		{"Wichita Metro Area", false, true},
		{"TARRYTOWN; SLEEPY HOLLOW", false, true},
		{"Kansas City", false, true},
		// Workday and similar forms.
		{"US, California, Santa Clara; US, Texas, Austin; US, Oregon, Hillsboro", false, true},
		{"USA-Colorado-Fort Collins-4380 Ziegler Road", false, true},
		{"California - Palo Alto", false, true},
		{"New York, NY (HQ)", false, true},
		// Placeholders that still name the US.
		{"US locations", false, true},
		{"Multiple U.S. Data Center Locations", false, true},
		{"Multiple Locations", false, false},
		// A comma list of cities with a US option.
		{"New York, London, Singapore", false, true},
		{"London, Houston, New York, Singapore", false, true},
		// A foreign country decides it.
		{"San Jose, Costa Rica", true, false},
		{"Madrid, MD, Spain", false, false},
		{"Curitiba, PR, Brazil", true, false},
		{"Westport, MO, Ireland", false, false},
		{"GB-London", false, false},
		{"IND-Pune", false, false},
		{"Novi Sad,Vojvodina,Serbia", false, false},
		// Remote scope.
		{"Remote - US", false, true},
		{"Remote - India", false, false},
		// Same name, either side.
		{"Paris, TX", false, true},
		{"Paris", false, false},
		{"Paris, France", false, false},
		{"London, ON", false, false},
		{"London", false, false},
		{"Atlanta, Georgia", false, true},
		{"Tbilisi, Georgia", false, false},
		// Unicode.
		{"São Paulo, Brasil", false, false},
		{"Zürich, Switzerland", false, false},
		{"Bogotá, CO", false, false},
		// Ambiguous bare names are not US.
		{"Cambridge", false, false},
		{"Portland", false, false},
		{"Vancouver", false, false},
		// Nothing.
		{"", false, false},
		{"   ", false, false},
	} {
		if got := InUS(Parse(c.in, c.remote)); got != c.want {
			t.Errorf("InUS(Parse(%q, %v)) = %v, want %v (parsed %q)", c.in, c.remote, got, c.want, Parse(c.in, c.remote).String())
		}
	}
}

func TestWashingtonStateAndDC(t *testing.T) {
	if got := Parse("Washington", false).States(); len(got) != 1 || got[0] != "WA" {
		t.Errorf("Washington: states %v, want [WA]", got)
	}
	if got := Parse("Washington, DC", false).States(); len(got) != 1 || got[0] != "DC" {
		t.Errorf("Washington, DC: states %v, want [DC]", got)
	}
}

// TestLoneGeorgiaIsTheState pins the existing reading: a lone "Georgia" is the
// US state (these are US-centred boards); the country needs a city or
// "Republic of Georgia".
func TestLoneGeorgiaIsTheState(t *testing.T) {
	if got := Parse("Georgia", false).States(); len(got) != 1 || got[0] != "GA" {
		t.Errorf("Georgia: states %v, want [GA]", got)
	}
}

func TestAmbiguousPlace(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"Cambridge", true},
		{"Portland", true},
		{"Vancouver", true},
		{"North America", true},
		{"Americas", true},
		{"Cambridge, MA", false},
		{"Cambridge, UK", false},
		{"Vancouver, BC", false},
		{"Paris", false},
		{"London", false},
		{"San Francisco", false},
		{"Europe", false},
	} {
		l := Parse(c.in, false)
		got := len(l.Places) == 1 && AmbiguousPlace(l.Places[0])
		if got != c.want {
			t.Errorf("AmbiguousPlace(%q) = %v, want %v (places %#v)", c.in, got, c.want, l.Places)
		}
	}
}

// TestInUSBounded feeds oversized and many-location strings: the parse stays
// bounded and the answer is still right.
func TestInUSBounded(t *testing.T) {
	var us, foreign []string
	for i := 0; i < 60; i++ {
		us = append(us, "Austin, TX")
		foreign = append(foreign, "Berlin, Germany")
	}
	l := Parse(strings.Join(us, "; "), false)
	if !InUS(l) || len(l.Places) > MaxPlaces {
		t.Errorf("60 US locations: InUS %v, %d places (max %d)", InUS(l), len(l.Places), MaxPlaces)
	}
	if InUS(Parse(strings.Join(foreign, "; "), false)) {
		t.Error("60 foreign locations read as US")
	}
	commas := "London, " + strings.Repeat("Berlin, ", 200) + "New York"
	_ = InUS(Parse(commas, false)) // bounded by MaxInputRunes; must not hang or panic
	// Oversized, but each place whole: a city matches as a whole part, not as
	// the prefix of a longer one (one run of "San Francisco San Francisco ..."
	// is no place).
	huge := strings.Repeat("San Francisco; ", 10000)
	if !InUS(Parse(huge, false)) {
		t.Error("oversized San Francisco string not read as US")
	}
	if InUS(Parse(strings.Repeat("San Francisco ", 10000), false)) {
		t.Error("one oversized run of words read as San Francisco")
	}
}
