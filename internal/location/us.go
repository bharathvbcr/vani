package location

import (
	"slices"
	"strings"
)

// InUS reports whether l names a place in the United States, or remote work
// whose scope takes in the US. It is what "US postings first" orders by
// (advance_us_first), so it only orders and never drops: a miss leaves a
// posting in the ordinary order, which is why an unreadable or unscoped
// location ("Remote", "2 Locations", empty) reads as false.
//
// It reads Parse's output rather than changing Parse, because near-duplicate
// detection and the exclusions read Parse as engine rules, and Parse reads a
// state or country only at the end of a place. InUS reads every part of a
// place's area and region, so these are US places too:
//   - a leading or embedded country: "US, CA, Santa Clara", "United States,
//     Washington, Redmond", "Sunnyvale, California USA", "USA-Remote";
//   - a state that is not the last part: "California - San Francisco",
//     "Mountain View, California (HQ)", "Burlingame, CA (HQ)";
//   - a US city boards write without a state (usCities): "San Francisco",
//     "San Francisco HQ", "Union Square, New York City".
//
// A part naming the US decides it. Otherwise a part naming another country, or
// a Canadian province code, makes the place foreign whatever else it says
// ("Mississauga, ON, CA", "Remote Canada"). A state is read from the last part
// back, and a state code that is also a country's ("IN", "DE", "CA") is that
// country when another part is one of its cities (isCountryCity): "Pune, MH,
// IN" and "IN - Bengaluru, India" are India.
//
// A remote scope takes in the US when it names the US, North America, the
// Americas or anywhere.
func InUS(l Location) bool {
	for _, p := range l.Places {
		if placeInUS(p) {
			return true
		}
	}
	for _, s := range strings.Split(l.RemoteScope, ",") {
		switch strings.TrimSpace(s) {
		case "US", "North America", "Americas", "Anywhere":
			return true
		}
	}
	return false
}

func placeInUS(p Place) bool {
	if p.State != "" {
		return true
	}
	var parts []string
	for _, s := range []string{p.Area, p.Region} {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
	}
	foreign := false
	for _, part := range parts {
		switch partCountry(part) {
		case "":
		case "US":
			return true
		default:
			foreign = true
		}
	}
	if foreign {
		return false
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if code, ok := stateOf(parts[i]); ok {
			for j, city := range parts {
				if j != i && isCountryCity(code, parts[i], city) {
					return false
				}
			}
			return true
		}
	}
	if usCity(parts[0], false) {
		return true
	}
	for _, part := range parts[1:] {
		if usCity(part, true) {
			return true
		}
	}
	return false
}

// partCountry is the country or scope one part of a place names: "US" for the
// United States, another scopes value, "Canada" for a province code written in
// capitals, or "" for none. A state ("New Mexico", "Georgia") names none. A
// part names the US when it is a US scope or says "USA" or "United States"
// anywhere in it ("California USA", "United States and Canada"), or "US" as its
// first or last word ("US-Remote").
func partCountry(part string) string {
	if s, ok := scopeOf(part); ok {
		return s
	}
	if canadaProvinces[strings.TrimSuffix(strings.TrimSpace(part), ".")] {
		return "Canada"
	}
	if _, ok := stateOf(part); ok {
		return ""
	}
	w := words(part)
	if len(w) == 0 {
		return ""
	}
	for i, x := range w {
		if x == "usa" || x == "united" && i+1 < len(w) && w[i+1] == "states" {
			return "US"
		}
	}
	for _, x := range []string{w[0], w[len(w)-1]} {
		if s, ok := scopes[x]; ok {
			return s
		}
	}
	return ""
}

// canadaProvinces are the Canadian province and territory codes, written in
// capitals as boards write them ("Toronto, ON"). None is a USPS code.
var canadaProvinces = map[string]bool{
	"ON": true, "QC": true, "BC": true, "AB": true, "MB": true, "SK": true, "NS": true, "NB": true,
	"NL": true, "PE": true, "YT": true, "NT": true, "NU": true,
}

// usCity reports whether part is a city of usCities, as whole words. The area
// (a place's first part) may go on after the city ("San Francisco HQ"); a later
// part must be the city exactly ("Union Square, New York City"), so a site name
// such as "Hyderabad - Phoenix Aquila, India" is not Phoenix.
func usCity(part string, exact bool) bool {
	w := words(part)
	for _, c := range usCities {
		if len(w) == len(c) || !exact && len(w) > len(c) {
			if slices.Equal(w[:len(c)], c) {
				return true
			}
		}
	}
	return false
}

// usCities are US cities boards write without a state, none of them a
// well-known city elsewhere: no Cambridge, Birmingham, Vancouver, Richmond,
// Durham, Portland or London. Each is its words (as words gives them).
var usCities = func() [][]string {
	var out [][]string
	for _, c := range []string{
		"san francisco", "sf bay area", "bay area", "silicon valley",
		"new york city", "nyc", "manhattan", "brooklyn",
		"seattle", "greater seattle", "bellevue", "redmond", "kirkland",
		"mountain view", "palo alto", "menlo park", "sunnyvale", "santa clara", "san jose", "cupertino",
		"redwood city", "san mateo", "foster city", "burlingame", "oakland", "berkeley", "emeryville",
		"los angeles", "santa monica", "culver city", "irvine", "san diego",
		"austin", "dallas", "houston", "chicago", "boston", "somerville", "denver", "boulder", "atlanta",
		"pittsburgh", "philadelphia", "reston", "mclean", "herndon", "raleigh", "salt lake city", "lehi",
		"phoenix", "scottsdale", "tempe", "miami", "nashville", "minneapolis", "detroit", "ann arbor",
	} {
		out = append(out, words(c))
	}
	return out
}()
