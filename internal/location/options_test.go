package location

import (
	"strings"
	"testing"
	"time"
)

// PlaceOptions is exported and jobview builds Places from stored rows, so it
// bounds its own input: an oversized or hostile place is read in bounded time
// and never yields more options than maxOptionParts.
func TestPlaceOptionsBounded(t *testing.T) {
	for _, p := range []Place{
		{Area: strings.Repeat("Austin, ", 5000) + "Houston"},
		{Area: strings.Repeat("-", 200000)},
		{Area: strings.Repeat("not ", 50000) + "US"},
		{Area: strings.Repeat("San Francisco ", 100000)},
		{Region: strings.Repeat("Ontario, Canada, US, ", 20000)},
		{Area: strings.Repeat("\x00�", 100000)},
		{Area: "Austin", State: strings.Repeat("TX", 1000)},
	} {
		start := time.Now()
		opts := PlaceOptions(p)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("place of %d bytes took %v", len(p.Area)+len(p.Region), d)
		}
		if len(opts) > maxOptionParts+1 {
			t.Errorf("place of %d bytes gave %d options; at most %d", len(p.Area)+len(p.Region), len(opts), maxOptionParts+1)
		}
		for _, o := range opts {
			if len(o.State) > 2 {
				t.Errorf("option state %q is not a USPS code", o.State)
			}
		}
	}
}

func TestPlaceOptionsZeroAndEmpty(t *testing.T) {
	if got := PlaceOptions(Place{}); got != nil {
		t.Errorf("zero place: %+v, want none", got)
	}
	if got := PlaceOptions(Place{Area: " , ,, "}); got != nil {
		t.Errorf("only separators: %+v, want none", got)
	}
	// Only office words: a place, but nothing to judge.
	got := PlaceOptions(Place{Area: "HQ"})
	if len(got) != 1 || got[0].Verdict != VerdictAmbiguous {
		t.Errorf("HQ: %+v, want one ambiguous option", got)
	}
	if PlaceInUS(Place{}) || AmbiguousPlace(Place{}) {
		t.Error("the zero place is neither US nor ambiguous")
	}
}

// A state code the place itself carries is trusted only when it is one.
func TestPlaceOptionsForeignShapedState(t *testing.T) {
	if !PlaceInUS(Place{Area: "Austin", State: "TX"}) {
		t.Error("Austin, TX not US")
	}
	if PlaceInUS(Place{Area: "Toronto", State: "ONTARIO"}) {
		t.Error("a state that is not a USPS code read as US")
	}
}

// Unicode names read as names: case-less scripts are places, not notes.
func TestPlaceOptionsUnicode(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Verdict
	}{
		{"東京", VerdictNotUS},
		{"Москва", VerdictNotUS},
		{"São Paulo", VerdictNotUS},
		{"Zürich", VerdictNotUS},
		{"San Francisco", VerdictUS},
	} {
		opts := PlaceOptions(Place{Area: c.in})
		if len(opts) != 1 || opts[0].Verdict != c.want {
			t.Errorf("%q: %+v, want one %v option", c.in, opts, c.want)
		}
	}
}
