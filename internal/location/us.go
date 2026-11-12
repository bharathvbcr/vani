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

// PlaceInUS reports whether one place is in the United States, by the rules
// InUS documents. It is the one answer to "is this place in the US": InUS, the
// country list (Countries) and the exclusions' "United States" entry all read
// it, so a place cannot be US to one and foreign to another.
func PlaceInUS(p Place) bool { return placeInUS(p) }

// AmbiguousPlace reports whether p cannot be said to be in or out of the US:
// a bare name that is a well-known place both in and outside the US
// ("Cambridge", "Portland", "Vancouver") with no state or region to tell
// which, or only a region that takes in the US among others ("North America",
// "Americas"). PlaceInUS reads it as not US; a rule that would drop on "not
// US" should treat it as unknown and keep the posting.
func AmbiguousPlace(p Place) bool {
	if p.State != "" {
		return false
	}
	if p.Region == "" {
		return ambiguousNames[NormArea(p.Area)]
	}
	if p.Area != "" {
		return false
	}
	s, ok := scopeOf(p.Region)
	return ok && (s == "North America" || s == "Americas")
}

// ambiguousNames are names boards write bare that are as likely a US place as
// a foreign one in tech hiring. A name far more often foreign ("London",
// "Paris", "Dublin", "Melbourne") is not here: it reads as the foreign place.
var ambiguousNames = set(
	"cambridge", "portland", "vancouver", "birmingham", "richmond", "durham",
	"vienna", "columbia", "athens", "lexington", "salem", "kingston",
)

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
	if len(parts) == 0 {
		return false
	}
	foreign := false
	for _, part := range parts {
		switch partCountry(part) {
		case "":
			// A country the scope vocabulary does not hold ("Spain", "Costa
			// Rica", "GB-London") is foreign too: "Madrid, MD, Spain" is not
			// Maryland and "San Jose, Costa Rica" is not California.
			if partForeign(part) {
				foreign = true
			}
		case "US":
			return true
		default:
			foreign = true
		}
	}
	if foreign {
		// A board's comma list of cities in one place ("London, Houston, New
		// York, Singapore") names at least three and one of them is a US place
		// written in full: one of its options is the US. "San Jose, Costa
		// Rica" (two parts) and "Madrid, MD, Spain" (the US-looking part a
		// code) stay foreign.
		if len(parts) >= 3 {
			for _, part := range parts {
				if usLead(part) {
					return true
				}
			}
		}
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

// usLead reports whether part is a US place written in full: a state by name
// ("New York", not "NY"; not "Georgia", which is also a country) or a city of
// usCities exactly.
func usLead(part string) bool {
	if code, ok := stateOf(part); ok && len(strings.TrimSpace(part)) > 2 && code != "GA" {
		return true
	}
	return usCity(part, true)
}

// partForeign reports whether one part names a country other than the US that
// partCountry's scope vocabulary does not: a country name ("Spain", "Costa
// Rica", "Brasil"), or an ISO code that is not a USPS code written in capitals
// alone ("FR") or leading a hyphenated part ("GB-London", "JP-Tokyo"). A state
// ("Georgia", "IN") names no country here.
func partForeign(part string) bool {
	if c, ok := namedCountry(part); ok {
		return c != "United States"
	}
	tok := strings.TrimSpace(part)
	if _, ok := iso3Country[tok]; ok {
		return true
	}
	if i := strings.IndexByte(tok, '-'); i == 2 || i == 3 {
		head := tok[:i]
		if head == strings.ToUpper(head) {
			if _, isState := stateNames[head]; !isState {
				if _, ok := isoCountry[head]; ok {
					return true
				}
				if _, ok := iso3Country[head]; ok {
					return true
				}
			}
		}
	}
	return false
}

// iso3Country holds the ISO alpha-3 codes boards put before a city ("IND-Pune",
// "VNM-Da Nang", "GBR"), for the countries that hire most often. USA is not
// here: it is the US.
var iso3Country = map[string]string{
	"IND": "India", "GBR": "United Kingdom", "DEU": "Germany", "FRA": "France", "CAN": "Canada",
	"MEX": "Mexico", "BRA": "Brazil", "CHN": "China", "JPN": "Japan", "KOR": "South Korea",
	"SGP": "Singapore", "AUS": "Australia", "IRL": "Ireland", "NLD": "Netherlands", "ESP": "Spain",
	"ITA": "Italy", "POL": "Poland", "ISR": "Israel", "VNM": "Vietnam", "PHL": "Philippines",
	"TWN": "Taiwan", "CHE": "Switzerland", "SWE": "Sweden", "ROU": "Romania", "ARG": "Argentina",
	"COL": "Colombia", "CRI": "Costa Rica", "MYS": "Malaysia", "THA": "Thailand", "IDN": "Indonesia",
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
// well-known place elsewhere: no Cambridge, Birmingham, Vancouver, Richmond,
// Durham, Portland, London, Brisbane, Melbourne, Alexandria, Brighton,
// Westminster, Greenwich, Worcester, Norfolk, Lincoln, Toledo or St
// Petersburg (ambiguousNames holds the ones as likely US as not). Each is its
// words (as words gives them).
//
// Sources, 2026-11-12: (1) the live ledger's board location strings, keeping
// an area written with a US state at least 5 times with one state taking 90%
// of those and more often with a state than with any other region, less the
// names above and words that are not only places ("spring", "sandy",
// "cypress", "mesa", "franklin", "hudson"); (2) the largest US cities by
// population and the tech-hiring suburbs boards name; (3) abbreviations boards
// use: "sf", "nyc", "rtp".
var usCities = func() [][]string {
	var out [][]string
	for _, c := range []string{
		// Abbreviations and regions.
		"sf", "sf bay area", "bay area", "san francisco bay area", "silicon valley", "nyc", "rtp",
		"research triangle park", "space coast", "dallas fort worth", "dfw", "greater seattle",
		// Large cities.
		"san francisco", "new york city", "manhattan", "brooklyn", "los angeles", "chicago", "houston", "phoenix",
		"philadelphia", "san antonio", "san diego", "dallas", "san jose", "austin", "jacksonville",
		"fort worth", "charlotte", "indianapolis", "seattle", "denver", "nashville", "oklahoma city",
		"el paso", "boston", "las vegas", "detroit", "memphis", "louisville", "baltimore", "milwaukee",
		"albuquerque", "tucson", "fresno", "sacramento", "atlanta", "omaha", "colorado springs", "raleigh",
		"long beach", "virginia beach", "miami", "oakland", "minneapolis", "tulsa", "tampa", "new orleans",
		"wichita", "cleveland", "bakersfield", "anaheim", "honolulu", "santa ana", "corpus christi",
		"henderson", "st louis", "saint louis", "st paul", "saint paul", "pittsburgh", "cincinnati",
		"anchorage", "greensboro", "plano", "orlando", "jersey city", "chandler", "lubbock", "scottsdale",
		"reno", "buffalo", "gilbert", "north las vegas", "winston salem", "chesapeake", "irving", "boise",
		"spokane", "baton rouge", "tacoma", "des moines", "grand rapids", "huntsville", "salt lake city",
		"tallahassee", "overland park", "knoxville", "providence", "chattanooga", "provo", "ann arbor",
		"pasadena", "fort lauderdale", "west palm beach", "boca raton", "san bernardino", "modesto",
		"fort collins", "ft collins", "college station", "waco", "amarillo", "abilene", "pensacola",
		"jacksonville", "charleston", "greenville", "dayton", "columbus", "kansas city", "arlington",
		"wilmington", "newark",
		// Tech-hiring cities and suburbs.
		"mountain view", "palo alto", "east palo alto", "menlo park", "sunnyvale", "santa clara",
		"cupertino", "redwood city", "san mateo", "foster city", "burlingame", "south san francisco",
		"san carlos", "san bruno", "millbrae", "emeryville", "berkeley", "alameda", "hayward", "fremont",
		"milpitas", "los gatos", "pleasanton", "livermore", "santa monica", "culver city", "el segundo",
		"playa vista", "hawthorne", "irvine", "costa mesa", "tustin", "mission viejo", "foothill ranch",
		"beverly hills", "folsom", "vandenberg", "bellevue", "redmond", "kirkland", "woodinville",
		"bothell", "hillsboro", "beaverton", "boulder", "broomfield", "longmont", "arvada", "lehi",
		"tempe", "casa grande", "frisco", "richardson", "round rock", "the woodlands", "westlake",
		"starbase", "bastrop", "brownsville", "mcgregor", "hutto", "cary", "morrisville", "herndon",
		"reston", "mclean", "tysons", "chantilly", "ashburn", "fairfax", "bethesda", "rockville",
		"gaithersburg", "silver spring", "jessup", "somerville", "waltham", "boxborough", "andover",
		"maynard", "quincy", "norwood", "westborough", "westford", "north reading", "princeton",
		"hoboken", "secaucus", "florham park", "mahwah", "branchburg", "stamford", "wilton", "armonk",
		"tarrytown", "sleepy hollow", "white plains", "yorktown heights", "rensselaer", "fishkill",
		"poughkeepsie", "glen cove", "plainview", "getzville", "canandaigua", "north chicago",
		"schaumburg", "mettawa", "riverwoods", "highland park", "alpharetta", "peachtree corners",
		"roswell", "cape canaveral", "lake mary", "palm beach gardens", "juno beach", "southaven",
		"southfield", "chippewa falls", "sioux falls", "conshohocken", "king of prussia",
		"quonset", "o fallon", "grandview", "warrenton", "westerville", "sandusky",
	} {
		w := words(c)
		if !slices.ContainsFunc(out, func(x []string) bool { return slices.Equal(x, w) }) {
			out = append(out, w)
		}
	}
	return out
}()
