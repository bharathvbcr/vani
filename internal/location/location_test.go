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
