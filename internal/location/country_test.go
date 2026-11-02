package location

import (
	"slices"
	"strings"
	"testing"
)

func TestCountries(t *testing.T) {
	cases := []struct {
		raw, want string
		remote    bool
	}{
		{raw: "El Paso, TX", want: "United States"},
		{raw: "Austin, Texas, United States", want: "United States"},
		{raw: "San Francisco", want: "United States"},
		{raw: "Remote - US", want: "United States"},
		{raw: "Remote (Anywhere)", want: ""},
		{raw: "Remote - Europe", want: ""},
		{raw: "Remote (Canada)", want: "Canada"},
		{raw: "London, United Kingdom", want: "United Kingdom"},
		{raw: "Toronto, ON", want: "Canada"},
		{raw: "Toronto, ON, Canada", want: "Canada"},
		{raw: "Toronto, CA", want: "Canada"},
		{raw: "Berlin, DE", want: "Germany"},
		{raw: "Munich, Bavaria, DE", want: "Germany"},
		{raw: "Berlin, Germany", want: "Germany"},
		{raw: "Remote - Germany", want: "Germany"},
		{raw: "Smallville, DE", want: "United States"},
		{raw: "Bangalore, IN", want: "India"},
		{raw: "Indianapolis, IN", want: "United States"},
		{raw: "Bloomington, in", want: ""},
		{raw: "Tbilisi, Georgia", want: "Georgia"},
		{raw: "Atlanta, Georgia", want: "United States"},
		{raw: "Georgia", want: "United States"},
		{raw: "Paris, France", want: "France"},
		{raw: "Paris, FR", want: "France"},
		{raw: "Belgium; Canada", want: "Belgium, Canada"},
		{raw: "Berlin, DE; Austin, TX", want: "Germany, United States"},
		{raw: "Mountain View, CA / Remote (Continental US + Hawaii + Canada Only)", want: "United States, Canada"},
		{raw: "2 Locations", want: ""},
		{raw: "United States", want: "United States"},
		{raw: "", want: ""},
	}
	for _, c := range cases {
		got := Countries(Parse(c.raw, c.remote))
		if !slices.Equal(got, wantCountries(c.want)) {
			t.Errorf("Countries(Parse(%q)) = %q, want %q", c.raw, got, wantCountries(c.want))
		}
	}
}

func TestCanonicalCountry(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"Germany", "Germany", true},
		{"germany", "Germany", true},
		{"DE", "Germany", true},
		{"de", "Germany", true},
		{"usa", "United States", true},
		{"United States", "United States", true},
		{"UK", "United Kingdom", true},
		{"Georgia", "Georgia", true},
		{"CA", "Canada", true},
		{"IN", "India", true},
		{"Texas", "", false},
		{"California", "", false},
		{"Europe", "", false},
		{"Anywhere", "", false},
		{"XX", "", false},
		{"", "", false},
		{"  ", "", false},
	} {
		got, ok := CanonicalCountry(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("CanonicalCountry(%q) = %q, %v, want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func wantCountries(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ", ")
}
