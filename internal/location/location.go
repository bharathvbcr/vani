// Package location reads a job board's free-text location into the places it
// names (an area and a US state, or an area and the region after it as the
// board wrote it) and whether it lists the role as remote, and where remote.
//
// It is display-only: `vani jobs`, the Mac app and the digest read it, and no
// engine rule does. The input is untrusted board text; it is bounded before it
// is read and only ever produces strings, never anything that is followed or
// executed. There is no city or metro table: an area is the place as the board
// wrote it, and only US states (which the board abbreviates and spells
// inconsistently) and a short remote-scope vocabulary are normalised. The one
// exception is a short list of foreign cities, used only to tell a country from
// a state written the same way ("Tbilisi, Georgia", "Berlin, DE").
package location

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxInputRunes bounds how much of a location string is read.
	MaxInputRunes = 1000
	// MaxPlaces bounds how many places one location yields.
	MaxPlaces = 20
	// maxSegments bounds how many places (pieces between separators) are read.
	maxSegments = 40
	// maxPartRunes bounds one area or region string.
	maxPartRunes = 80
)

// Place is one place a posting names. State is a USPS code for a US place;
// Region is what followed the area otherwise (a country, province or both, as
// written). Both are empty when the board gave a bare area.
type Place struct {
	Area   string `json:"area,omitempty"`
	State  string `json:"state,omitempty"`
	Region string `json:"region,omitempty"`
}

// Location is what a board's location says.
type Location struct {
	Places []Place
	// Remote is true when the board flags the role remote or its location text
	// says remote (not hybrid, not negated). It is what the board listed, not
	// triage's work_mode reading.
	Remote bool
	// RemoteScope is where remote work is allowed when the text says so ("US",
	// "Canada", "Europe", "Anywhere", ...); empty when unstated or not remote.
	RemoteScope string
}

// String renders the location for display: "El Paso, TX; Las Cruces, NM ·
// Remote (US)". It is empty when nothing was recognised.
func (l Location) String() string {
	parts := make([]string, 0, len(l.Places))
	for _, p := range l.Places {
		if s := p.String(); s != "" {
			parts = append(parts, s)
		}
	}
	s := strings.Join(parts, "; ")
	if l.Remote {
		r := "Remote"
		if l.RemoteScope != "" {
			r += " (" + l.RemoteScope + ")"
		}
		if s == "" {
			return r
		}
		s += " · " + r
	}
	return s
}

// String renders one place: "El Paso, TX", "London, United Kingdom", "TX".
func (p Place) String() string {
	tail := p.State
	if tail == "" {
		tail = p.Region
	}
	switch {
	case p.Area != "" && tail != "":
		return p.Area + ", " + tail
	case p.Area != "":
		return p.Area
	}
	return tail
}

// States returns the distinct USPS codes the location names, in order.
func (l Location) States() []string {
	var out []string
	for _, p := range l.Places {
		if p.State != "" && !slices.Contains(out, p.State) {
			out = append(out, p.State)
		}
	}
	return out
}

// Parse reads raw, a board's location text, and boardRemote, the board's own
// remote flag.
func Parse(raw string, boardRemote bool) Location {
	loc := Location{Remote: boardRemote}
	switch strings.Join(words(raw), " ") {
	case "n a", "na", "none", "not applicable", "not specified", "unspecified", "tbd", "tba":
		return loc // a placeholder; split on its "/" it would read as places "N" and "A"
	}
	var scopes []string
	addScope := func(s string) {
		if s != "" && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	n := 0
	for _, group := range segments(clean(raw)) {
		var placed []Place
		for _, seg := range group {
			if n == maxSegments {
				break
			}
			n++
			remote := remoteInText(words(seg))
			loc.Remote = loc.Remote || remote
			p, ok, scope := parseSegment(seg, remote)
			if remote || !ok || p.Area == "" && p.State == "" {
				// A scope counts for remote work when the segment says remote, or
				// when it names nothing but a country ("United States" on a role
				// the board flags remote).
				addScope(scope)
			}
			if ok {
				placed = append(placed, p)
			}
		}
		for _, p := range shareTail(placed) {
			if len(loc.Places) < MaxPlaces && !hasPlace(loc.Places, p) {
				loc.Places = append(loc.Places, p)
			}
		}
	}
	if loc.Remote && len(scopes) > 0 {
		loc.RemoteScope = strings.Join(scopes, ", ")
	}
	return loc
}

// StateCode reads a USPS code or a state name in any case ("tx", "Texas",
// "district of columbia"). It is for user input such as a --state flag.
func StateCode(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if c := strings.ToUpper(s); len(c) == 2 {
		if _, ok := stateNames[c]; ok {
			return c, true
		}
	}
	c, ok := stateByName[strings.Join(words(s), " ")]
	return c, ok
}

// StateName is the full name for a USPS code, or "" if it is not one.
func StateName(code string) string { return stateNames[code] }

// NormArea folds an area for comparison: case, accents kept, punctuation and
// runs of space folded. "el  paso" and "El Paso" compare equal.
func NormArea(s string) string { return strings.Join(words(s), " ") }

func hasPlace(ps []Place, p Place) bool {
	for _, q := range ps {
		if NormArea(q.Area) == NormArea(p.Area) && q.State == p.State && NormArea(q.Region) == NormArea(p.Region) {
			return true
		}
	}
	return false
}

// clean bounds the input and turns control characters into spaces.
func clean(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if n == MaxInputRunes {
			break
		}
		if r == utf8.RuneError || unicode.IsControl(r) {
			if r == '\n' {
				b.WriteByte(';')
			} else {
				b.WriteByte(' ')
			}
		} else {
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}

// segmentSep turns the separators boards put between locations into ';'.
var segmentSep = strings.NewReplacer("|", ";", " • ", ";", " or ", ";")

// segments splits a location into groups of places: groups at ';', "|", "•"
// and " or ", and the places within a group at "/". Places joined by a slash
// share what follows the last one ("Dallas/Fort Worth, TX"; shareTail).
func segments(s string) [][]string {
	var out [][]string
	for _, grp := range strings.Split(segmentSep.Replace(s), ";") {
		var pieces []string
		for _, seg := range strings.Split(grp, "/") {
			if seg = strings.TrimSpace(seg); seg != "" {
				pieces = append(pieces, seg)
			}
		}
		if len(pieces) > 0 {
			out = append(out, pieces)
		}
	}
	return out
}

// shareTail gives the bare areas of one slash group the state or region of
// the group's last place, when it has one: "Dallas/Fort Worth, TX" is two
// Texas places. A group that ends on a bare area ("Austin, TX/Berlin") or
// with no place lends nothing.
func shareTail(ps []Place) []Place {
	if len(ps) < 2 {
		return ps
	}
	last := ps[len(ps)-1]
	if last.State == "" && last.Region == "" {
		return ps
	}
	for i := range ps[:len(ps)-1] {
		if ps[i].Area != "" && ps[i].State == "" && ps[i].Region == "" {
			ps[i].State, ps[i].Region = last.State, last.Region
		}
	}
	return ps
}

// partSep splits one location into its comma-like parts. A spaced dash or a
// colon separates ("Remote - US", "Hybrid: Austin, TX"); a bare hyphen does
// not ("Winston-Salem").
var partSep = strings.NewReplacer("(", ",", ")", ",", "[", ",", "]", ",", " - ", ",", " – ", ",", " — ", ",", ": ", ",", "+", ",", "&", ",")

// workModeWords are dropped from a part before it is read as a place.
var workModeWords = map[string]bool{
	"remote": true, "hybrid": true, "onsite": true, "office": true, "person": true, "based": true, "only": true, "first": true, "flexible": true, "fully": true, "100": true, "work": true,
	"from": true, "home": true, "wfh": true, "telecommute": true, "telework": true, "friendly": true,
	"optional": true, "or": true, "within": true, "position": true, "role": true, "non": true, "not": true,
	"continental": true, "contiguous": true,
	"some": true, "occasional": true, "travel": true, "required": true, "eligible": true,
}

// parseSegment reads one location. ok is false when no place is named; scope
// is a remote scope or country the segment names ("US", "Canada", ...). A
// remote segment that names only a scope ("Remote - US") is a scope, not a
// place; a non-remote one ("Canada") is a place with that region.
func parseSegment(seg string, remote bool) (Place, bool, string) {
	var parts []string
	for _, part := range strings.Split(partSep.Replace(seg), ",") {
		if part = stripWorkMode(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 || multipleLocations(parts) {
		return Place{}, false, ""
	}
	// A remote segment that lists only scopes and US states is a scope list:
	// "Remote (Continental US + Hawaii + Canada Only)" -> "US, Canada".
	if remote {
		if s, ok := scopeList(parts); ok {
			return Place{}, false, s
		}
	}

	scope := ""
	// Trailing scopes and countries: "Austin, Texas, United States",
	// "Remote, US", "Toronto, Canada".
	var region []string
	usOnly := true
	for len(parts) > 0 {
		s, isScope := scopeOf(parts[len(parts)-1])
		if !isScope {
			break
		}
		if scope == "" {
			scope = s
		}
		usOnly = usOnly && s == "US"
		region = append([]string{bounded(parts[len(parts)-1])}, region...)
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		if remote {
			return Place{}, false, scope
		}
		return Place{Region: bounded(strings.Join(region, ", "))}, true, scope
	}

	// A US state: the last word of a lone part written as a code ("El Paso
	// TX", "Washington DC"), or the last part ("El Paso, TX", "Austin, Texas").
	if usOnly {
		if len(parts) == 1 {
			if i := strings.LastIndexByte(parts[0], ' '); i > 0 && (len(parts[0])-i-1 == 2 || strings.HasPrefix(parts[0][i+1:], "D.C")) {
				if code, ok := stateOf(parts[0][i+1:]); ok {
					if scope != "US" && isCountryCity(code, parts[0][i+1:], parts[0][:i]) {
						return Place{Area: bounded(parts[0][:i]), Region: bounded(parts[0][i+1:])}, true, scope
					}
					return Place{Area: bounded(parts[0][:i]), State: code}, true, scopeOr(scope, "US")
				}
			}
		}
		if code, ok := stateOf(parts[len(parts)-1]); ok {
			area := strings.Join(parts[:len(parts)-1], ", ")
			// A country written like the state ("Tbilisi, Georgia", "Berlin, DE",
			// "Munich, Bavaria, DE"), unless the location also says US
			// ("Berlin, DE, USA"). The city is the first part.
			if scope != "US" && len(parts) > 1 && isCountryCity(code, parts[len(parts)-1], parts[0]) {
				rest := append(slices.Clone(parts[1:]), region...)
				return Place{Area: bounded(parts[0]), Region: bounded(strings.Join(rest, ", "))}, true, scope
			}
			// A board's bare "New York" is the city, not the state.
			if len(parts) == 1 && code == "NY" && NormArea(parts[0]) == "new york" {
				area = "New York"
			}
			return Place{Area: bounded(area), State: code}, true, scopeOr(scope, "US")
		}
	}

	// Elsewhere: the first part is the area and the rest is the region.
	rest := append(slices.Clone(parts[1:]), region...)
	return Place{Area: bounded(parts[0]), Region: bounded(strings.Join(rest, ", "))}, true, scope
}

// scopeList joins parts that are all scopes or US states, at least one of
// them a scope, into one scope. States alone ("Remote - Texas") stay places.
func scopeList(parts []string) (string, bool) {
	var out []string
	anyScope := false
	for _, p := range parts {
		s, ok := scopeOf(p)
		if !ok {
			if _, isState := stateOf(p); !isState {
				return "", false
			}
			s = "US"
		}
		anyScope = anyScope || ok
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", "), anyScope
}

func scopeOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// weakWords are dropped only beside another kept word, so "Hybrid in Boston"
// reads as "Boston" while a lone "in" or "ON" (Ontario) is kept.
var weakWords = map[string]bool{"in": true, "on": true, "site": true, "at": true}

// stripWorkMode drops work-mode words from a part, keeping the rest as the
// board wrote it ("Hybrid in Boston" -> "Boston", "US-Remote" -> "US",
// "On-site" -> "").
func stripWorkMode(part string) string {
	// Each field keeps its kept hyphen pieces together ("Winston-Salem").
	type piece struct {
		text   string
		weak   bool
		joined bool // joined to the previous piece by a hyphen
	}
	var keep []piece
	strong, dropped := 0, false
	fields := strings.Fields(part)
	for fi, field := range fields {
		first := true
		for _, s := range strings.Split(field, "-") {
			w := words(s)
			switch {
			case len(w) == 0:
			case fi == len(fields)-1 && isStateCode(s):
				// "Portland, OR", "Evansville IN": a trailing code in capitals is
				// the state, not the word it spells.
				keep = append(keep, piece{s, false, !first})
				first = false
				strong++
			case allIn(w, workModeWords):
				dropped = true
			case allIn(w, weakWords):
				keep = append(keep, piece{s, true, !first})
				first = false
			default:
				keep = append(keep, piece{s, false, !first})
				first = false
				strong++
			}
		}
	}
	// A weak word stays only when it is all there is ("in", "ON"); next to a
	// place or a dropped mode word ("Hybrid in Boston", "On-site") it goes.
	lone := strong == 0 && len(keep) == 1 && !dropped
	var b strings.Builder
	for _, k := range keep {
		if k.weak && !lone {
			continue
		}
		if b.Len() > 0 {
			if k.joined {
				b.WriteByte('-')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(k.text)
	}
	return strings.Trim(b.String(), " .;:-–—/")
}

func allIn(ws []string, set map[string]bool) bool {
	for _, w := range ws {
		if !set[w] {
			return false
		}
	}
	return true
}

// multipleLocations reports placeholders such as Workday's "2 Locations".
func multipleLocations(parts []string) bool {
	if len(parts) != 1 {
		return false
	}
	w := words(parts[0])
	if len(w) == 0 {
		return true
	}
	last := w[len(w)-1]
	if last != "locations" && last != "location" && last != "places" && last != "sites" {
		return w[0] == "various" || w[0] == "multiple" || w[0] == "tbd"
	}
	return true
}

func bounded(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxPartRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxPartRunes]) + "…"
}

// isStateCode reports whether s is a USPS code written in capitals ("OR", not "or").
func isStateCode(s string) bool {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	_, ok := stateNames[s]
	return len(s) == 2 && ok
}

// stateOf reads a part as a US state: a two-letter USPS code written in
// capitals ("TX", so "in", "or" and "me" are not states), or a state name in
// any case ("Texas", "new mexico", "Washington D.C.").
func stateOf(part string) (string, bool) {
	part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), "."))
	if len(part) == 2 && part == strings.ToUpper(part) {
		_, ok := stateNames[part]
		return part, ok
	}
	if len(part) == 4 && (part == "D.C." || part == "D.C") {
		return "DC", true
	}
	c, ok := stateByName[strings.Join(words(part), " ")]
	return c, ok
}

// A state written by name or code can be a country's: "Tbilisi, Georgia",
// "Berlin, DE" (ISO 3166 Germany, USPS Delaware). The invariant: with no listed
// city of that country it is the state ("Smallville, Georgia", "Smallville,
// DE"), because these are US-centred boards and a US city needs no table; and a
// US token in the same location makes it the state whatever the city. Each
// list holds the country's main tech-office cities, none of them a well-known
// US city of the colliding state.

// countryByName: state names a country shares. Georgia is the only one
// ("Jersey" is not "New Jersey").
var countryByName = map[string]map[string]bool{
	"GA": set("tbilisi", "batumi", "kutaisi", "rustavi", "zugdidi", "gori", "poti", "telavi"),
}

// countryByCode: USPS codes that are also ISO 3166-1 alpha-2 codes, for the
// countries with tech offices. Gabon (GA), Laos (LA), Niger (NE), Sudan (SD),
// Seychelles (SC), Tunisia (TN), Montenegro (ME), Cayman (KY), Montserrat
// (MS), Macao (MO), New Caledonia (NC) and the Holy See (VA) are left out, so
// they read as the state; PR is Puerto Rico either way.
var countryByCode = map[string]map[string]bool{
	"DE": set("berlin", "munich", "münchen", "hamburg", "frankfurt", "frankfurt am main", "cologne", "köln",
		"stuttgart", "düsseldorf", "dusseldorf", "leipzig", "karlsruhe"),
	"IN": set("bangalore", "bengaluru", "hyderabad", "pune", "chennai", "mumbai", "new delhi", "gurgaon",
		"gurugram", "noida", "kolkata", "ahmedabad"),
	"CA": set("toronto", "vancouver", "montreal", "montréal", "ottawa", "calgary", "edmonton", "waterloo",
		"kitchener", "mississauga"),
	"IL": set("tel aviv", "tel aviv yafo", "haifa", "jerusalem", "herzliya", "ramat gan", "petah tikva", "beer sheva"),
	"AR": set("buenos aires", "córdoba", "cordoba"),
	"CO": set("bogotá", "bogota", "medellín", "medellin"),
	"MA": set("casablanca", "rabat"),
	"MD": set("chișinău", "chisinau"),
	"MT": set("valletta", "sliema", "st julian s"),
	"PA": set("panama city", "ciudad de panamá"),
	"ID": set("jakarta"),
	"MN": set("ulaanbaatar"),
	"AL": set("tirana"),
	"AZ": set("baku"),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[NormArea(n)] = true
	}
	return m
}

// isCountryCity reports whether city, with the state written as written (a
// name or a two-letter code), is a listed city of the country written that way.
func isCountryCity(code, written, city string) bool {
	if len(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(written), "."))) == 2 {
		return countryByCode[code][NormArea(city)]
	}
	return countryByName[code][NormArea(city)]
}

// scopeOf reads a part as a remote scope or country from the short vocabulary.
func scopeOf(part string) (string, bool) {
	s, ok := scopes[strings.Join(words(part), " ")]
	return s, ok
}

var scopes = map[string]string{
	"us": "US", "u s": "US", "usa": "US", "u s a": "US", "united states": "US", "united states of america": "US",
	"america": "US", "lower 48": "US", "lower 48 states": "US", "the us": "US", "the usa": "US", "the u s": "US", "the u s a": "US", "the united states": "US", "the uk": "UK",
	"the united kingdom": "UK", "canada": "Canada", "mexico": "Mexico", "méxico": "Mexico",
	"uk": "UK", "u k": "UK", "united kingdom": "UK", "england": "UK", "great britain": "UK", "britain": "UK",
	"europe": "Europe", "eu": "Europe", "emea": "EMEA", "apac": "APAC", "asia pacific": "APAC",
	"latam": "LATAM", "latin america": "LATAM", "north america": "North America", "americas": "Americas",
	"anywhere": "Anywhere", "worldwide": "Anywhere", "global": "Anywhere", "world": "Anywhere",
}

// words lowercases s and splits it into runs of letters and digits, so
// matching is on whole words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// remoteInText reports whether a location names remote work: "remote" (or a
// synonym) as a whole word, not negated ("non-remote", "no remote", "not
// remote"), in a text that does not say hybrid.
func remoteInText(ws []string) bool {
	if slices.Contains(ws, "hybrid") {
		return false
	}
	for i, w := range ws {
		switch w {
		case "remote", "telecommute", "telework", "wfh":
		case "home":
			if i < 2 || ws[i-1] != "from" || ws[i-2] != "work" {
				continue
			}
		default:
			continue
		}
		if i > 0 && (ws[i-1] == "non" || ws[i-1] == "no" || ws[i-1] == "not") {
			continue
		}
		return true
	}
	return false
}

var stateNames = map[string]string{
	"AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas", "CA": "California", "CO": "Colorado",
	"CT": "Connecticut", "DE": "Delaware", "DC": "District of Columbia", "FL": "Florida", "GA": "Georgia",
	"HI": "Hawaii", "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "IA": "Iowa", "KS": "Kansas",
	"KY": "Kentucky", "LA": "Louisiana", "ME": "Maine", "MD": "Maryland", "MA": "Massachusetts",
	"MI": "Michigan", "MN": "Minnesota", "MS": "Mississippi", "MO": "Missouri", "MT": "Montana",
	"NE": "Nebraska", "NV": "Nevada", "NH": "New Hampshire", "NJ": "New Jersey", "NM": "New Mexico",
	"NY": "New York", "NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio", "OK": "Oklahoma",
	"OR": "Oregon", "PA": "Pennsylvania", "PR": "Puerto Rico", "RI": "Rhode Island", "SC": "South Carolina",
	"SD": "South Dakota", "TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VT": "Vermont", "VA": "Virginia",
	"WA": "Washington", "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming",
}

// stateByName maps a folded state name to its code. "washington dc" and its
// spellings are DC; bare "washington" is the state.
var stateByName = func() map[string]string {
	m := map[string]string{"washington dc": "DC", "washington d c": "DC", "d c": "DC", "dc": "DC"}
	for code, name := range stateNames {
		m[strings.Join(words(name), " ")] = code
	}
	return m
}()
