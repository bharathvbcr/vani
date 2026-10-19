package location

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParse(t *testing.T) {
	cases := []struct {
		raw         string
		boardRemote bool
		places      []Place
		remote      bool
		scope       string
		show        string
	}{
		{raw: "", show: ""},
		{raw: "El Paso, TX", places: []Place{{Area: "El Paso", State: "TX"}}, show: "El Paso, TX"},
		{raw: "El Paso TX", places: []Place{{Area: "El Paso", State: "TX"}}, show: "El Paso, TX"},
		{raw: "Las Cruces, New Mexico", places: []Place{{Area: "Las Cruces", State: "NM"}}, show: "Las Cruces, NM"},
		{raw: "Austin, Texas, United States", places: []Place{{Area: "Austin", State: "TX"}}, show: "Austin, TX"},
		{raw: "Texas", places: []Place{{State: "TX"}}, show: "TX"},
		{raw: "Washington, DC", places: []Place{{Area: "Washington", State: "DC"}}, show: "Washington, DC"},
		{raw: "Washington DC", places: []Place{{Area: "Washington", State: "DC"}}, show: "Washington, DC"},
		{raw: "Seattle, Washington", places: []Place{{Area: "Seattle", State: "WA"}}, show: "Seattle, WA"},
		{raw: "Winston-Salem, NC", places: []Place{{Area: "Winston-Salem", State: "NC"}}, show: "Winston-Salem, NC"},
		// Lowercase two-letter words are never states.
		{raw: "Bloomington, in", places: []Place{{Area: "Bloomington", Region: "in"}}, show: "Bloomington, in"},
		{raw: "Toronto, ON", places: []Place{{Area: "Toronto", Region: "ON"}}, show: "Toronto, ON"},
		{raw: "Toronto, ON, Canada", places: []Place{{Area: "Toronto", Region: "ON, Canada"}}, show: "Toronto, ON, Canada"},
		{raw: "London, United Kingdom", places: []Place{{Area: "London", Region: "United Kingdom"}}, show: "London, United Kingdom"},
		{raw: "Ciudad Juárez, Chihuahua, Mexico", places: []Place{{Area: "Ciudad Juárez", Region: "Chihuahua, Mexico"}}, show: "Ciudad Juárez, Chihuahua, Mexico"},
		{raw: "Belgium; Canada", places: []Place{{Area: "Belgium"}, {Region: "Canada"}}, show: "Belgium; Canada"},
		{raw: "San Francisco, CA; New York, NY", places: []Place{{Area: "San Francisco", State: "CA"}, {Area: "New York", State: "NY"}},
			show: "San Francisco, CA; New York, NY"},
		{raw: "San Francisco, CA | San Francisco, CA", places: []Place{{Area: "San Francisco", State: "CA"}}, show: "San Francisco, CA"},
		// Remote, with and without a scope.
		{raw: "Remote", remote: true, show: "Remote"},
		{raw: "Remote - US", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "US-Remote", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "Remote (Canada)", remote: true, scope: "Canada", show: "Remote (Canada)"},
		{raw: "Remote (Anywhere)", remote: true, scope: "Anywhere", show: "Remote (Anywhere)"},
		{raw: "Remote within the US", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "100% Remote, USA only", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "Remote - Texas", remote: true, scope: "US", places: []Place{{State: "TX"}}, show: "TX · Remote (US)"},
		{raw: "New York, NY / Remote", remote: true, places: []Place{{Area: "New York", State: "NY"}}, show: "New York, NY · Remote"},
		{raw: "Work from home", remote: true, show: "Remote"},
		// The board's flag counts; a country-only location scopes it.
		{raw: "United States", boardRemote: true, remote: true, scope: "US", places: []Place{{Region: "United States"}}, show: "United States · Remote (US)"},
		{raw: "Austin, TX", boardRemote: true, remote: true, places: []Place{{Area: "Austin", State: "TX"}}, show: "Austin, TX · Remote"},
		{raw: "", boardRemote: true, remote: true, show: "Remote"},
		// Hybrid and negations are not remote.
		{raw: "Hybrid - Austin, TX", places: []Place{{Area: "Austin", State: "TX"}}, show: "Austin, TX"},
		{raw: "Hybrid in Boston, MA", places: []Place{{Area: "Boston", State: "MA"}}, show: "Boston, MA"},
		{raw: "Non-remote, Austin, TX", places: []Place{{Area: "Austin", State: "TX"}}, show: "Austin, TX"},
		// "or" offers both, so remote is allowed.
		{raw: "Remote or hybrid, Denver, CO", remote: true, places: []Place{{Area: "Denver", State: "CO"}}, show: "Denver, CO · Remote"},
		// A trailing code in capitals is the state even when it spells a
		// work-mode or weak word ("or", "in"); in the middle it stays the word.
		{raw: "Portland, OR", places: []Place{{Area: "Portland", State: "OR"}}, show: "Portland, OR"},
		{raw: "Portland OR", places: []Place{{Area: "Portland", State: "OR"}}, show: "Portland, OR"},
		{raw: "Hybrid - Portland, OR", places: []Place{{Area: "Portland", State: "OR"}}, show: "Portland, OR"},
		{raw: "Evansville IN", places: []Place{{Area: "Evansville", State: "IN"}}, show: "Evansville, IN"},
		{raw: "Office in Austin, TX", places: []Place{{Area: "Austin", State: "TX"}}, show: "Austin, TX"},
		{raw: "Remote OR Denver, CO", remote: true, scope: "US", places: []Place{{Area: "Denver", State: "CO"}}, show: "Denver, CO · Remote (US)"},
		{raw: "On-site, Austin, TX", places: []Place{{Area: "Austin", State: "TX"}}, show: "Austin, TX"},
		{raw: "In office - Chicago, IL", places: []Place{{Area: "Chicago", State: "IL"}}, show: "Chicago, IL"},
		// Shapes seen on real boards (2026-10-18 nonprofit run).
		{raw: "Mountain View, CA / Remote (Continental US + Hawaii + Canada Only)", remote: true, scope: "US, Canada",
			places: []Place{{Area: "Mountain View", State: "CA"}}, show: "Mountain View, CA · Remote (US, Canada)"},
		{raw: "San Mateo, CA/ Remote (Continental US + Hawaii + Canada only)", remote: true, scope: "US, Canada",
			places: []Place{{Area: "San Mateo", State: "CA"}}, show: "San Mateo, CA · Remote (US, Canada)"},
		{raw: "Remote (US-Based), some travel required", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "Remote (in the U.S.)", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "Remote, Lower 48 States", remote: true, scope: "US", show: "Remote (US)"},
		{raw: "Washington D.C.", places: []Place{{Area: "Washington", State: "DC"}}, show: "Washington, DC"},
		{raw: "Chicago, IL (Onsite)", places: []Place{{Area: "Chicago", State: "IL"}}, show: "Chicago, IL"},
		{raw: "United States", places: []Place{{Region: "United States"}}, show: "United States"},
		// Placeholders name no place.
		{raw: "2 Locations", show: ""},
		{raw: "Multiple Locations", show: ""},
		{raw: "Various", show: ""},
		// Slash-joined places share what follows the last one; ';' groups do not.
		{raw: "Dallas/Fort Worth, TX", places: []Place{{Area: "Dallas", State: "TX"}, {Area: "Fort Worth", State: "TX"}},
			show: "Dallas, TX; Fort Worth, TX"},
		{raw: "Toronto/Vancouver, Canada", places: []Place{{Area: "Toronto", Region: "Canada"}, {Area: "Vancouver", Region: "Canada"}},
			show: "Toronto, Canada; Vancouver, Canada"},
		{raw: "Austin, TX/Berlin", places: []Place{{Area: "Austin", State: "TX"}, {Area: "Berlin"}}, show: "Austin, TX; Berlin"},
		{raw: "Berlin; Austin, TX", places: []Place{{Area: "Berlin"}, {Area: "Austin", State: "TX"}}, show: "Berlin; Austin, TX"},
		{raw: "New York/San Francisco", places: []Place{{Area: "New York", State: "NY"}, {Area: "San Francisco"}}, show: "New York, NY; San Francisco"},
		{raw: "New York", places: []Place{{Area: "New York", State: "NY"}}, show: "New York, NY"},
		{raw: "Buffalo, New York", places: []Place{{Area: "Buffalo", State: "NY"}}, show: "Buffalo, NY"},
		{raw: "N/A", show: ""},
		{raw: "n/a", boardRemote: true, remote: true, show: "Remote"},
		{raw: "TBD", show: ""},
		{raw: " — ", show: ""},
		// Georgia the country: one of its own cities says so. A US token in the
		// same location wins, and with neither it is the state.
		{raw: "Tbilisi, Georgia", places: []Place{{Area: "Tbilisi", Region: "Georgia"}}, show: "Tbilisi, Georgia"},
		{raw: "Batumi, GEORGIA", places: []Place{{Area: "Batumi", Region: "GEORGIA"}}, show: "Batumi, GEORGIA"},
		{raw: "Tbilisi/Kutaisi, Georgia", places: []Place{{Area: "Tbilisi", Region: "Georgia"}, {Area: "Kutaisi", Region: "Georgia"}},
			show: "Tbilisi, Georgia; Kutaisi, Georgia"},
		{raw: "Tbilisi, Georgia; Atlanta, GA", places: []Place{{Area: "Tbilisi", Region: "Georgia"}, {Area: "Atlanta", State: "GA"}},
			show: "Tbilisi, Georgia; Atlanta, GA"},
		{raw: "Remote - Tbilisi, Georgia", remote: true, places: []Place{{Area: "Tbilisi", Region: "Georgia"}}, show: "Tbilisi, Georgia · Remote"},
		{raw: "Tbilisi, Georgia, USA", places: []Place{{Area: "Tbilisi", State: "GA"}}, show: "Tbilisi, GA"},
		{raw: "Tbilisi, GA", places: []Place{{Area: "Tbilisi", State: "GA"}}, show: "Tbilisi, GA"},
		{raw: "Atlanta, Georgia", places: []Place{{Area: "Atlanta", State: "GA"}}, show: "Atlanta, GA"},
		{raw: "Alpharetta, Georgia", places: []Place{{Area: "Alpharetta", State: "GA"}}, show: "Alpharetta, GA"},
		{raw: "Atlanta, Georgia, United States", places: []Place{{Area: "Atlanta", State: "GA"}}, show: "Atlanta, GA"},
		{raw: "Georgia", places: []Place{{State: "GA"}}, show: "GA"},
		{raw: "Remote - Georgia", remote: true, scope: "US", places: []Place{{State: "GA"}}, show: "GA · Remote (US)"},
		// Two-letter codes that are both USPS and ISO 3166 (D13).
		{raw: "Berlin, DE", places: []Place{{Area: "Berlin", Region: "DE"}}, show: "Berlin, DE"},
		{raw: "Munich, Bavaria, DE", places: []Place{{Area: "Munich", Region: "Bavaria, DE"}}, show: "Munich, Bavaria, DE"},
		{raw: "Bangalore, IN", places: []Place{{Area: "Bangalore", Region: "IN"}}, show: "Bangalore, IN"},
		{raw: "Bengaluru IN", places: []Place{{Area: "Bengaluru", Region: "IN"}}, show: "Bengaluru, IN"},
		{raw: "Toronto, CA", places: []Place{{Area: "Toronto", Region: "CA"}}, show: "Toronto, CA"},
		{raw: "Tel Aviv, IL", places: []Place{{Area: "Tel Aviv", Region: "IL"}}, show: "Tel Aviv, IL"},
		{raw: "Remote - Montréal, CA", remote: true, places: []Place{{Area: "Montréal", Region: "CA"}}, show: "Montréal, CA · Remote"},
		{raw: "Berlin, DE, USA", places: []Place{{Area: "Berlin", State: "DE"}}, show: "Berlin, DE"},
		{raw: "Toronto, CA, United States", places: []Place{{Area: "Toronto", State: "CA"}}, show: "Toronto, CA"},
		{raw: "Smallville, DE", places: []Place{{Area: "Smallville", State: "DE"}}, show: "Smallville, DE"},
		{raw: "Wilmington, DE", places: []Place{{Area: "Wilmington", State: "DE"}}, show: "Wilmington, DE"},
		{raw: "Indianapolis, IN", places: []Place{{Area: "Indianapolis", State: "IN"}}, show: "Indianapolis, IN"},
		{raw: "Chicago, IL", places: []Place{{Area: "Chicago", State: "IL"}}, show: "Chicago, IL"},
		{raw: "Berlin, Delaware", places: []Place{{Area: "Berlin", State: "DE"}}, show: "Berlin, DE"},
		{raw: "Libreville, GA", places: []Place{{Area: "Libreville", State: "GA"}}, show: "Libreville, GA"},
		{raw: "Berlin, DE; Austin, TX", places: []Place{{Area: "Berlin", Region: "DE"}, {Area: "Austin", State: "TX"}},
			show: "Berlin, DE; Austin, TX"},
		// No other state name is a country's: Jersey is not New Jersey.
		{raw: "St Helier, Jersey", places: []Place{{Area: "St Helier", Region: "Jersey"}}, show: "St Helier, Jersey"},
		{raw: "Jersey City, New Jersey", places: []Place{{Area: "Jersey City", State: "NJ"}}, show: "Jersey City, NJ"},
	}
	for _, c := range cases {
		got := Parse(c.raw, c.boardRemote)
		if !reflect.DeepEqual(got.Places, c.places) || got.Remote != c.remote || got.RemoteScope != c.scope {
			t.Errorf("Parse(%q, %v) = %+v, want places %+v remote %v scope %q", c.raw, c.boardRemote, got, c.places, c.remote, c.scope)
		}
		if s := got.String(); s != c.show {
			t.Errorf("Parse(%q).String() = %q, want %q", c.raw, s, c.show)
		}
	}
}

func TestParseIsBounded(t *testing.T) {
	huge := strings.Repeat("Austin, TX; ", 5000)
	got := Parse(huge, false)
	if len(got.Places) != 1 {
		t.Fatalf("repeated place not deduplicated: %d places", len(got.Places))
	}
	var many []string
	for i := range 200 {
		many = append(many, "City"+strings.Repeat("x", i%7)+string(rune('A'+i%26))+", TX")
	}
	if got := Parse(strings.Join(many, "; "), false); len(got.Places) > MaxPlaces {
		t.Fatalf("%d places, more than %d", len(got.Places), MaxPlaces)
	}
	long := strings.Repeat("Ω", 5000) + ", TX"
	for _, p := range Parse(long, false).Places {
		if utf8.RuneCountInString(p.Area) > maxPartRunes+1 || utf8.RuneCountInString(p.Region) > maxPartRunes+1 {
			t.Fatalf("unbounded part: %d runes", utf8.RuneCountInString(p.Area))
		}
	}
}

func TestParseUntrustedText(t *testing.T) {
	for _, raw := range []string{
		"El Paso,\x00 TX", "El Paso\r\nAustin, TX", "\xff\xfe, TX", ",,,;;;|||", "()[]--::", "Remote‮ - US",
		"<script>alert(1)</script>, TX", strings.Repeat(",", 3000),
	} {
		got := Parse(raw, false)
		s := got.String()
		if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 }) {
			t.Errorf("Parse(%q).String() = %q: invalid or control characters", raw, s)
		}
	}
	if got := Parse("El Paso\nAustin, TX", false); len(got.Places) != 2 {
		t.Errorf("a newline separates locations: %+v", got.Places)
	}
}

func TestStateCode(t *testing.T) {
	for in, want := range map[string]string{
		"TX": "TX", "tx": "TX", " Texas ": "TX", "new mexico": "NM", "District of Columbia": "DC", "dc": "DC", "PR": "PR",
	} {
		if got, ok := StateCode(in); !ok || got != want {
			t.Errorf("StateCode(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "XX", "Tex", "Ontario", "none", strings.Repeat("a", 5000)} {
		if got, ok := StateCode(bad); ok {
			t.Errorf("StateCode(%q) = %q, want refusal", bad, got)
		}
	}
	if StateName("NM") != "New Mexico" || StateName("XX") != "" {
		t.Error("StateName")
	}
}

func TestStatesAndNormArea(t *testing.T) {
	l := Parse("El Paso, TX; Las Cruces, NM; Austin, TX", false)
	if got := l.States(); !reflect.DeepEqual(got, []string{"TX", "NM"}) {
		t.Errorf("States() = %v", got)
	}
	if NormArea("  El   Paso ") != NormArea("el paso") || NormArea("El Paso") == NormArea("Paso") {
		t.Error("NormArea")
	}
}

// Invariant: with no listed city of the country, a state name or code is the
// state, for every state ("Smallville, DE" is Delaware).
func TestNoCountrySignalIsTheState(t *testing.T) {
	for code, name := range stateNames {
		for _, raw := range []string{"Smallville, " + code, "Smallville " + code, "Smallville, " + name} {
			if got := Parse(raw, false).Places; len(got) != 1 || got[0].State != code || got[0].Region != "" {
				t.Errorf("Parse(%q) = %+v, want the state %s", raw, got, code)
			}
		}
	}
}

// Invariant: every listed city reads as its country, and a US token in the
// same location makes it the state whatever the city.
func TestCountryCitiesAndTheUSToken(t *testing.T) {
	check := func(code, written string, cities map[string]bool) {
		if len(cities) == 0 {
			t.Errorf("%s: empty city list", code)
		}
		for city := range cities {
			raw := city + ", " + written
			if got := Parse(raw, false).Places; len(got) != 1 || got[0].State != "" || got[0].Region != written {
				t.Errorf("Parse(%q) = %+v, want the country %s", raw, got, written)
			}
			for _, us := range []string{", USA", ", United States", " (US)"} {
				if got := Parse(raw+us, false).Places; len(got) != 1 || got[0].State != code {
					t.Errorf("Parse(%q) = %+v, want the state %s", raw+us, got, code)
				}
			}
		}
	}
	for code, cities := range countryByCode {
		if _, ok := stateNames[code]; !ok {
			t.Errorf("countryByCode %s is not a USPS code", code)
		}
		check(code, code, cities)
	}
	for code, cities := range countryByName {
		check(code, stateNames[code], cities)
	}
}

// Invariant: places split on ";" or "/" are read on their own, so a US place
// beside a country's city changes neither.
func TestSegmentsStayIndependent(t *testing.T) {
	for code, cities := range countryByCode {
		for city := range cities {
			for _, raw := range []string{city + ", " + code + "; Smallville, " + code, "Smallville, " + code + " / " + city + ", " + code} {
				got := Parse(raw, false).Places
				if len(got) != 2 {
					t.Errorf("Parse(%q) = %+v, want two places", raw, got)
					continue
				}
				for _, p := range got {
					if want := NormArea(p.Area) == "smallville"; (p.State == code) != want || (p.Region == code) == want {
						t.Errorf("Parse(%q): %+v read with its neighbour", raw, p)
					}
				}
			}
		}
	}
}
