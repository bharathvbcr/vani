package location

import (
	"strings"
	"testing"
)

func TestInUS(t *testing.T) {
	cases := []struct {
		raw         string
		boardRemote bool
		want        bool
	}{
		// A state Parse reads.
		{raw: "El Paso, TX", want: true},
		{raw: "Seattle, Washington, United States", want: true},
		{raw: "Washington, D.C.", want: true},
		{raw: "New York", want: true},
		// A country before the place, which Parse leaves in the area.
		{raw: "US, CA, Santa Clara", want: true},
		{raw: "United States, Washington, Redmond", want: true},
		{raw: "USA - Sunnyvale, CA; Canada - Toronto", want: true},
		{raw: "Sunnyvale, California USA", want: true},
		{raw: "United States and Canada", want: true},
		{raw: "United States", want: true},
		// A state that is not the last part.
		{raw: "California - San Francisco", want: true},
		{raw: "Mountain View, California (HQ)", want: true},
		{raw: "Burlingame, CA (HQ)", want: true},
		{raw: "Boise, ID - Main Site", want: true},
		// A US city written without a state.
		{raw: "San Francisco", want: true},
		{raw: "San Francisco HQ", want: true},
		{raw: "Palo Alto Office", want: true},
		{raw: "Union Square, New York City", want: true},
		{raw: "NYC", want: true},
		// Remote scopes.
		{raw: "Remote - US", want: true},
		{raw: "Remote US", want: true},
		{raw: "United States (Remote)", boardRemote: true, want: true},
		{raw: "Remote - North America", want: true},
		{raw: "Remote, Global", want: true},
		{raw: "Remote - CA; Remote - NC", want: true},
		// Any place in the US is enough.
		{raw: "London, United Kingdom; New York, NY, United States", want: true},
		{raw: "Toronto, Ontario, Canada; Austin, Texas, United States", want: true},

		// Foreign places, including state codes that are a country's.
		{raw: "London", want: false},
		{raw: "London, UK", want: false},
		{raw: "Toronto", want: false},
		{raw: "Toronto, ON", want: false},
		{raw: "Mississauga, ON, CA", want: false},
		{raw: "Vancouver, BC", want: false},
		{raw: "MARKHAM, Canada", want: false},
		{raw: "Pune, MH, IN", want: false},
		{raw: "Chennai, TN, IN", want: false},
		{raw: "IN - Bengaluru, India", want: false},
		{raw: "Bengaluru, KA, IN", want: false},
		{raw: "Berlin, DE", want: false},
		{raw: "Tbilisi, Georgia", want: false},
		{raw: "Georgia, Tbilisi", want: false},
		{raw: "Mexico City, Mexico", want: false},
		{raw: "Mexico - Mexico City", want: false},
		{raw: "Remote Canada", want: false},
		{raw: "Remote - Europe", want: false},
		// A site name that starts like a US city is not one past the area.
		{raw: "Hyderabad - Phoenix Aquila, India", want: false},
		// A city of the list does not match a longer word.
		{raw: "Austinville", want: false},
		// Nothing to read.
		{raw: "", want: false},
		{raw: "Remote", want: false},
		{raw: "Remote", boardRemote: true, want: false},
		{raw: "Hybrid", want: false},
		{raw: "2 Locations", want: false},
		{raw: "N/A", want: false},
		{raw: ",,, ;; //", want: false},
	}
	for _, c := range cases {
		if got := InUS(Parse(c.raw, c.boardRemote)); got != c.want {
			t.Errorf("InUS(%q, remote=%v) = %v, want %v (parsed %+v)", c.raw, c.boardRemote, got, c.want, Parse(c.raw, c.boardRemote))
		}
	}
}

// InUS reads only what Parse bounded: an oversized or hostile location is
// answered without reading past MaxInputRunes, and a US place past the bound
// is not seen.
func TestInUSOversized(t *testing.T) {
	long := strings.Repeat("Atlantis, Nowhere; ", 200) + "Austin, TX"
	if InUS(Parse(long, false)) {
		t.Fatal("a US place past MaxInputRunes was read")
	}
	if !InUS(Parse("Austin, TX; "+long, false)) {
		t.Fatal("a US place before the bound was missed")
	}
	if InUS(Parse(strings.Repeat("\x00�", 5000), true)) {
		t.Fatal("control characters read as a US place")
	}
}

func TestInUSZero(t *testing.T) {
	if InUS(Location{}) {
		t.Fatal("the zero Location is not in the US")
	}
	if !InUS(Location{RemoteScope: "Canada, US"}) {
		t.Fatal("a scope list naming the US takes it in")
	}
	if InUS(Location{Remote: true, RemoteScope: "UK, Europe"}) {
		t.Fatal("a scope list without the US does not")
	}
}
