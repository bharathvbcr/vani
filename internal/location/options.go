package location

import (
	"strings"
	"unicode"
)

// Verdict is what one option of a place says about the United States.
type Verdict uint8

const (
	// VerdictNotUS: the option is a place outside the US, or one nothing
	// places in the US ("London", "Lagos"). The US needs evidence: a state, a
	// US token, or a city of usCities.
	VerdictNotUS Verdict = iota
	// VerdictUS: the option is in the United States.
	VerdictUS
	// VerdictAmbiguous: as likely in the US as not ("Cambridge", "San Jose",
	// "North America", "Santa Clara, Villa Clara", an unresolved negation). A
	// rule that would drop on "not US" keeps it unjudged.
	VerdictAmbiguous
)

func (v Verdict) String() string {
	switch v {
	case VerdictUS:
		return "US"
	case VerdictAmbiguous:
		return "ambiguous"
	}
	return "not US"
}

// Option is one place a Place names. A board's comma list names several
// ("Toronto, Austin" is Toronto or Austin); a city with its qualifiers names
// one ("Austin, Texas, United States", "US, CA, Santa Clara", "Madrid, MD,
// Spain"). Every reader of "is this in the US" composes these verdicts with
// its own quantifier: InUS and exclude.only_locations ask whether SOME option
// is US, exclude.locations whether EVERY option matches, and Countries lists
// each option's country.
type Option struct {
	// Text is the option's parts as the board wrote them, joined with ", ",
	// without its negations ("excluding US").
	Text string
	// State is the USPS code of a US option's state, when it names one.
	State string
	// Country is the canonical country ("United States", "Canada"), or ""
	// when the option names a region ("Europe") or nothing that tells.
	Country string
	Verdict Verdict
	// Unrecognised is set on a VerdictNotUS option that is not known to be
	// elsewhere: its name is in none of the tables (a US town missing from
	// usCityNames reads this way, as does any small place abroad). A rule that
	// drops on "not US" drops it all the same, but says it did not recognise
	// the place, which a place abroad the tables do know ("London", "Lagos")
	// does not.
	Unrecognised bool
}

// maxOptionParts bounds the comma parts one place is read for. Parse bounds a
// part to maxPartRunes and the input to MaxInputRunes, so this is a second
// wall, not the first.
const maxOptionParts = 40

// PlaceOptions reads one place into the options it names, each with its
// verdict. It is the one owner of "is this place in the US": PlaceInUS,
// AmbiguousPlace and Countries read it, and so do the exclusions.
//
// A place's parts (its area, region and Parse's state, split at commas) are
// read in order. A city or site name starts an option; a state, country,
// province or region after it qualifies it; a country, state or region
// before any city (Workday's "US, CA, Santa Clara") waits for the first city.
// A qualifier that contradicts the option it would join starts its own
// option instead, so "London, Houston, New York, Singapore" is London;
// Houston, New York; and Singapore.
func PlaceOptions(p Place) []Option {
	// A Place may come from anywhere (jobview rebuilds them from stored
	// rows), so its own size is bounded here, not only by Parse.
	if len(p.State) > 2 {
		p.State = ""
	}
	raw := placeParts(Place{Area: boundedRunes(p.Area, MaxInputRunes), Region: boundedRunes(p.Region, MaxInputRunes)})
	if len(raw) > maxOptionParts {
		raw = raw[:maxOptionParts]
	}
	for i := range raw {
		raw[i] = bounded(raw[i])
	}
	raw = splitZipState(raw)
	ps := make([]part, 0, len(raw)+1)
	for i, r := range raw {
		// The part before Parse's state is the town's name ("Lebanon, NH");
		// earlier parts of the area are read as written ("Canada, Mexico, New
		// York" is Canada, or Mexico, NY).
		ps = append(ps, classifyPart(r, i == 0, p.State != "" && i == len(raw)-1))
	}
	if p.State != "" && !slicesContainsState(ps, p.State) {
		// Parse's state qualifies the last city like a code would: "Ottawa,
		// IL" and "Berlin, NH" are US places, and "Toronto, Montreal, New
		// York" (Parse: Toronto, Montreal; NY) is Toronto or Montreal, NY.
		ps = append(ps, part{text: p.State, kind: kindState, state: p.State})
	}
	resolveCollisions(ps)
	washingtonDC(ps)

	var opts []*optBuild
	var cur *optBuild
	start := func() *optBuild {
		cur = &optBuild{}
		opts = append(opts, cur)
		return cur
	}
	for i := range ps {
		q := &ps[i]
		switch q.kind {
		case kindNoise, kindNegOther:
			if cur != nil {
				cur.add(q, false)
			}
		case kindHead:
			name := NormArea(q.text)
			switch {
			case cur != nil && cur.headName == name:
				// "Vienna, VIENNA, Austria", "San Jose, San Jose, Costa
				// Rica": the city written twice is one city.
				cur.add(q, false)
			case cur != nil && !cur.hasHead() && !cur.refusesHead(q):
				cur.setHead(q, name)
			case cur != nil && cur.head == headLeadUS && cur.unsettled():
				// "Phoenix Aquila, Hyderabad", "SF Express, Shenzhen": the
				// site belongs to the place written after it.
				cur.setHead(q, name)
			case cur != nil && cur.headLike && cur.head == headNone && len(cur.states) == 1 && !cur.hasCountry && q.head == headOther:
				// The state first, then its city: "Massachusetts -
				// Burlington", "California - Palo Alto".
				cur.setHead(q, name)
			case cur != nil && cur.head == headUS && cur.unsettled() && q.head == headOther && len(raw) == 2:
				// "Santa Clara, Villa Clara": a qualifier nobody can place
				// after a US city name. Not judged. In a longer list ("NYC,
				// Sea, SF, Tor") the unknown name is an item of its own.
				cur.unqual = true
				cur.add(q, true)
			default:
				start()
				cur.setHead(q, name)
			}
		case kindState:
			if cur != nil && len(cur.states) == 0 && !cur.hasCountry && !cur.negUS && !(q.strong && cur.head == headForeign) {
				cur.states = append(cur.states, q.state)
				cur.strongState = cur.strongState || q.strong && cur.hasHead()
				cur.add(q, true)
				continue
			}
			start()
			cur.states = append(cur.states, q.state)
			cur.headLike = q.strong // "New York, London": the state stands for its city
			// Inside a list ("Montreal, New York, Singapore") a state written
			// in full is an item: a country after it is the next item. First
			// in the place ("Washington, England") it is the town's name.
			cur.strongState = q.strong && len(opts) > 1
			cur.add(q, true)
		case kindUS:
			if cur == nil || cur.us || cur.hasCountry || cur.negUS {
				start()
			}
			cur.us = true
			cur.add(q, true)
		case kindCountry:
			if cur == nil || !cur.takesCountry(q) {
				start()
			}
			cur.hasCountry = true
			if cur.country == "" {
				cur.country = q.country
			}
			cur.add(q, true)
		case kindWide:
			if cur == nil {
				start()
			}
			cur.wide = true
			cur.add(q, true)
		case kindNegUS, kindNegUnknown:
			if cur == nil {
				start()
			}
			if q.kind == kindNegUS {
				cur.negUS = true
			} else {
				cur.negUnknown = true
			}
			cur.add(q, false)
		case kindCode:
			if cur == nil {
				start()
			}
			cur.code = true
			cur.add(q, true)
		}
	}
	if len(opts) == 0 {
		if len(ps) == 0 {
			return nil
		}
		// Only office words ("HQ"): a place nothing can be read from.
		return []Option{{Text: strings.Join(raw, ", "), Verdict: VerdictAmbiguous}}
	}
	opts = mergeCityRegion(mergeSites(opts))
	out := make([]Option, len(opts))
	for i, o := range opts {
		out[i] = o.option()
	}
	// "Georgia" beside a foreign place is as likely the country: an option
	// whose only US evidence is GA, in a place that names somewhere outside
	// the US, is not judged ("London, Berlin, Georgia").
	for i, o := range opts {
		if out[i].Verdict != VerdictUS || o.us || o.head == headUS || len(o.states) != 1 || o.states[0] != "GA" {
			continue
		}
		for j := range out {
			if j != i && out[j].Verdict == VerdictNotUS {
				out[i].Verdict, out[i].Country, out[i].State = VerdictAmbiguous, "", ""
				break
			}
		}
	}
	return out
}

// mergeSites folds an unknown name into the US option beside it when nothing
// in the place is foreign and that option's state or country is written: a
// suburb or site before its city ("Bala Cynwyd, Philadelphia Area, PA") or
// after it ("Salt Lake City, Utah, Holladay"). With any foreign evidence in
// the place ("Zug, Boston" in a list of foreign cities), or beside a city
// with no qualifier ("Accra, Houston", "NYC, Sea, SF, Tor"), every name stays
// an option of its own: exclude.locations keeps such a list rather than drop
// a foreign option it could not place.
func mergeSites(opts []*optBuild) []*optBuild {
	if len(opts) < 2 {
		return opts
	}
	for _, o := range opts {
		if o.hasCountry || o.head == headForeign || o.negUS {
			return opts
		}
	}
	unknown := func(o *optBuild) bool {
		return o.head == headOther && len(o.states) == 0 && !o.us && !o.code && !o.wide && !o.negUnknown
	}
	us := func(o *optBuild) bool {
		return !o.unqual && (len(o.states) > 0 || o.us || o.head == headUS)
	}
	settled := func(o *optBuild) bool { return us(o) && (len(o.states) > 0 || o.us) }
	out := opts[:0:0]
	for i := 0; i < len(opts); i++ {
		o := opts[i]
		switch {
		case unknown(o) && i+1 < len(opts) && settled(opts[i+1]):
			opts[i+1].texts = append(o.texts, opts[i+1].texts...)
		case unknown(o) && len(out) > 0 && settled(out[len(out)-1]):
			out[len(out)-1].texts = append(out[len(out)-1].texts, o.texts...)
		default:
			out = append(out, o)
		}
	}
	return out
}

// mergeCityRegion folds a city written alone into the option after it when
// that option is a region and its country with no city of its own: "Hyderabad,
// Sindh, Pakistan" is one place in Pakistan, not Hyderabad (India) and Sindh.
// A US city never folds into a foreign country this way.
func mergeCityRegion(opts []*optBuild) []*optBuild {
	out := opts[:0:0]
	for i := 0; i < len(opts); i++ {
		o := opts[i]
		if i+1 < len(opts) && o.unsettled() && !o.headLike && (o.head == headForeign || o.head == headOther) &&
			opts[i+1].hasCountry && (opts[i+1].head == headOther || opts[i+1].head == headNone) && !opts[i+1].headLike {
			n := opts[i+1]
			n.texts = append(o.texts, n.texts...)
			if n.head == headNone {
				n.head, n.headName, n.headCountry = o.head, o.headName, o.headCountry
			}
			continue
		}
		out = append(out, o)
	}
	return out
}

// PlaceInUS reports whether some option of p is in the United States.
func PlaceInUS(p Place) bool {
	for _, o := range PlaceOptions(p) {
		if o.Verdict == VerdictUS {
			return true
		}
	}
	return false
}

// AmbiguousPlace reports whether p cannot be said to be in or out of the US:
// no option of it is in the US and one is as likely US as not (a bare
// "Cambridge", "San Jose", a region such as "North America" that takes in the
// US among others). A rule that would drop on "not US" treats it as unknown
// and keeps the posting.
func AmbiguousPlace(p Place) bool {
	amb := false
	for _, o := range PlaceOptions(p) {
		switch o.Verdict {
		case VerdictUS:
			return false
		case VerdictAmbiguous:
			amb = true
		}
	}
	return amb
}

type partKind uint8

const (
	kindHead       partKind = iota // a city or site name
	kindNoise                      // only office or area words: "HQ", "Campus"
	kindUS                         // names the United States
	kindCountry                    // another country or a region outside the US
	kindWide                       // a region that takes in the US: North America, Americas, Anywhere
	kindState                      // a US state, by name or USPS code
	kindCode                       // a capitalised code that is not a state ("MH", "KA"): a foreign region's
	kindNegUS                      // "excluding US", "outside the US", "Not US"
	kindNegOther                   // a negation of something else ("no Canada"): ignored
	kindNegUnknown                 // a negation nothing resolves: not judged
)

type headKind uint8

const (
	headNone      headKind = iota
	headOther              // a name nothing places
	headUS                 // a city of usCities
	headAmbiguous          // a name of ambiguousNames
	headForeign            // a foreign city of the country lists (country set)
	headLeadUS             // a longer name a US city or state leads ("Boston Children's Hospital")
)

type part struct {
	text    string
	kind    partKind
	head    headKind
	country string // kindCountry, or a headForeign city's country
	state   string // kindState: the USPS code
	strong  bool   // kindState written by name, or read by Parse as the place's state
	note    bool   // kindCountry read from a longer phrase: "Canada OK", "UK hours", "Mexico team"
}

// classifyPart reads one comma part of a place. In a place's first part a
// foreign region's name is a city ("Victoria, BC"), not the region. Just
// before a state Parse read (stated), a country's or region's name is a US
// town named for it ("Lebanon, NH", "Peru, IN", "Victoria, TX", "Ontario,
// CA"), not the country.
func classifyPart(raw string, first, stated bool) part {
	t := strings.TrimSpace(raw)
	q := part{text: t}
	if t == "" {
		q.kind = kindNoise
		return q
	}
	if t == "NoVA" || t == "NOVA" {
		// Northern Virginia, as written. Not the word "nova": "Nova Scotia"
		// is Canada.
		q.kind, q.head = kindHead, headUS
		return q
	}
	dotless := strings.TrimSuffix(t, ".")
	if canadaProvinces[dotless] {
		q.kind, q.country = kindCountry, "Canada"
		return q
	}
	if s, ok := scopeOf(t); ok && (!stated || s == "US") {
		classifyScope(&q, s)
		return q
	}
	if s, ok := salesRegions[NormArea(t)]; ok && !stated {
		classifyScope(&q, s)
		return q
	}
	if c, ok := isoPrefixCountry(t); ok && !stated {
		// Before the negation: "NO-Norway" is Norway, not "no Norway".
		q.kind, q.country = kindCountry, c
		return q
	}
	ws := words(t)
	if k, _, ok := negation(t, ws); ok {
		q.kind = k
		return q
	}
	if code, ok := stateOf(t); ok {
		q.kind, q.state, q.strong = kindState, code, len(dotless) > 2
		return q
	}
	if c, ok := namedCountry(t); ok && (!stated || c == "United States") {
		if c == "United States" {
			q.kind = kindUS
		} else {
			q.kind, q.country = kindCountry, c
		}
		return q
	}
	if !stated {
		if c, ok := iso3Country[t]; ok {
			q.kind, q.country = kindCountry, c
			return q
		}
	}
	if !first && !stated {
		if c, ok := foreignRegions[NormArea(t)]; ok {
			q.kind, q.country = kindCountry, c
			return q
		}
	}
	if namesUS(t, ws) {
		q.kind = kindUS
		return q
	}
	if code, ok := hyphenState(t); ok {
		q.kind, q.state = kindState, code
		return q
	}
	if f := strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == '-' }); len(f) >= 2 && isStateCode(f[0]) {
		// A state code in capitals leading the part: "MA 02215", "VA Metro",
		// "WA New York" (a list run together), "CA-San Francisco". A code that
		// is also a country's leads that country when a piece after it says
		// so: "DE-Berlin-Trion Building", "IN-Bangalore-MSO", "CA-Ontario-
		// Toronto", "IL-Israel-Remote", "CO-Colombia-Remote".
		code := f[0]
		if c := collidingCountry[code]; c != "" && ledCountry(code, c, t) {
			q.kind, q.country = kindCountry, c
			return q
		}
		q.kind, q.state = kindState, code
		return q
	}
	// A site code or street number ("SF9", "Building 5", "4380 Ziegler
	// Road") names no place: its words go, and the rest is read.
	var named []string
	for _, w := range ws {
		if !strings.ContainsFunc(w, unicode.IsDigit) {
			named = append(named, w)
		}
	}
	if len(named) == 0 {
		q.kind = kindNoise
		return q
	}
	if c, ok := firstWordCountry(named); ok && !stated {
		q.kind, q.country = kindCountry, c // "Malaysia-Penang", "Singapore-Depot Road"
		return q
	}
	for end := len(named) - 1; end > 0; end-- {
		if c, ok := foreignCities[strings.Join(named[:end], " ")]; ok {
			q.kind, q.head, q.country = kindHead, headForeign, c // "Ottawa March Road"
			return q
		}
	}
	if allIn(named, noteWords) || streetWords[named[len(named)-1]] {
		// A note ("preferred", "possible for exceptional candidates") or a
		// street ("Fremont St"): no place.
		q.kind = kindNoise
		return q
	}
	// A name of the tables, read with its office and area words dropped one
	// at a time ("Greater Boston Area", "Tri-State Area", "Washington State").
	// It comes before the region words so "New England" is not England.
	lead := named
	for len(lead) > 0 && leadAffix[lead[0]] {
		lead = lead[1:]
	}
	if len(lead) == 0 {
		q.kind = kindNoise
		return q
	}
	for end := len(lead); end > 0; end-- {
		name := strings.Join(lead[:end], " ")
		switch {
		case usCityNames[name]:
			q.kind, q.head = kindHead, headUS
			return q
		case ambiguousNames[name]:
			q.kind, q.head = kindHead, headAmbiguous
			return q
		}
		if code, ok := stateByName[name]; ok && end < len(lead) {
			q.kind, q.state, q.strong = kindState, code, true
			return q
		}
		if c, ok := foreignCities[name]; ok {
			q.kind, q.head, q.country = kindHead, headForeign, c
			return q
		}
		if !tailAffix[lead[end-1]] {
			break
		}
		if end == 1 {
			q.kind = kindNoise // only office words
			return q
		}
	}
	if tailAffix[lead[len(lead)-1]] {
		// A named site ("Sunset Corporate Campus", "Lovelace HQ"): no place
		// of its own.
		q.kind = kindNoise
		return q
	}
	if code, ok := phraseState(named); ok && !stated {
		// "Upstate New York", "Northern Virginia", "Must be located in
		// Georgia": a state named at the end of a regional phrase.
		q.kind, q.state, q.strong = kindState, code, true
		return q
	}
	if usWide(named) {
		// "Nationwide", "EST", "Pacific Time": takes in the US, says no
		// more. Read like North America: US beside a US place, not judged
		// alone.
		q.kind = kindWide
		return q
	}
	if s := regionPhrase(named); s != "" && !stated {
		classifyScope(&q, s)
		q.note = true
		return q
	}
	if len(dotless) >= 2 && len(dotless) <= 4 && dotless == strings.ToUpper(dotless) && isLetters(dotless) {
		q.kind = kindCode
		return q
	}
	if !first && len(dotless) == 2 && unicode.IsUpper(rune(dotless[0])) && unicode.IsLower(rune(dotless[1])) && stateNames[strings.ToUpper(dotless)] != "" {
		// "Sunnyvale, Ca", "Blacksburg, Va": a state code in title case
		// after a place.
		q.kind, q.state = kindState, strings.ToUpper(dotless)
		return q
	}
	if strings.ContainsFunc(t, unicode.IsLower) && !strings.ContainsFunc(t, unicode.IsUpper) {
		// Boards capitalise place names; a lowercase tag ("dbt", "hq
		// pending") is a note. A script without case ("東京") is read as a
		// name.
		q.kind = kindNoise
		return q
	}
	q.kind, q.head = kindHead, headOther
	// A US city or state leading a longer name ("Boston Children's
	// Hospital", "Massachusetts General Hospital", "NYC-Privy") is that
	// place's site, unless what follows places it ("Phoenix Aquila,
	// Hyderabad"): PlaceOptions decides.
	for end := len(lead) - 1; end > 0; end-- {
		name := strings.Join(lead[:end], " ")
		if usCityNames[name] || len(name) > 2 && stateByName[name] != "" && name != "d c" {
			q.head = headLeadUS
			// Two US names run together ("San Francisco Or New York", with
			// the "Or" a work-mode word Parse drops) are both US.
			if rest := strings.Join(lead[end:], " "); usCityNames[rest] || stateByName[rest] != "" {
				q.head = headUS
			}
			break
		}
	}
	return q
}

// salesRegions are region codes boards write as a place ("Remote, AMER",
// "Remote-NAMER", "CONUS"), read here as the region they stand for. They are
// not Parse scopes: as a scope, AMER beside a state ("Remote, California,
// United States, AMER") would make the whole segment a scope list and lose
// the state, and Parse's places are near-duplicate keys.
var salesRegions = map[string]string{
	"amer": "Americas", "namer": "North America", "noram": "North America", "conus": "US",
}

// phraseWords may come before a state's name in a phrase that names the
// state or a part of it: a direction or extent ("Upstate New York", "Western
// North Carolina", "Greater New York") or a residency note ("Must be located
// in Georgia", "Anywhere in Texas"). A word outside the list leaves the
// phrase unread: "Baja California" is Mexico, "Republic of Georgia" a country.
var phraseWords = set("north", "south", "east", "west", "northern", "southern", "eastern", "western",
	"central", "upstate", "downstate", "northeast", "northwest", "southeast", "southwest", "northeastern",
	"northwestern", "southeastern", "southwestern", "greater", "coastal", "rural", "state", "of", "in",
	"the", "must", "be", "located", "reside", "residing", "live", "living", "anywhere", "within",
	"throughout", "across", "all")

// phraseState reads a state named at the end of a phrase of phraseWords,
// taking the longest name ("Western North Carolina" is NC, not Carolina).
// The phrase is short: a part is bounded to maxPartRunes before it is read.
func phraseState(ws []string) (string, bool) {
	for start := 1; start < len(ws); start++ {
		if !phraseWords[ws[start-1]] {
			return "", false
		}
		name := strings.Join(ws[start:], " ")
		if code, ok := stateByName[name]; ok && code != "DC" {
			return code, true
		}
	}
	return "", false
}

// usTimeZones are the US time zones boards write as a location ("Remote
// EST", "Pacific Time"). The two-letter forms are left out: "CT" and "MT"
// are states, "PT" is Portugal.
var usTimeZones = set("est", "edt", "cst", "cdt", "mst", "mdt", "pst", "pdt", "akst", "akdt", "hst",
	"eastern time", "central time", "mountain time", "pacific time", "eastern standard time",
	"central standard time", "mountain standard time", "pacific standard time", "eastern daylight time",
	"central daylight time", "mountain daylight time", "pacific daylight time")

// usWide reports a part that takes in the US without saying more:
// "Nationwide", or a US time zone with "zone", "hours" or "time" after it or
// not ("EST", "Pacific Time Zone", "CST hours"). "Pacific" or "Central" alone
// is not a time zone.
func usWide(ws []string) bool {
	if len(ws) == 1 && ws[0] == "nationwide" {
		return true
	}
	for len(ws) > 1 && (ws[len(ws)-1] == "zone" || ws[len(ws)-1] == "timezone" || ws[len(ws)-1] == "tz" || ws[len(ws)-1] == "hours") {
		ws = ws[:len(ws)-1]
	}
	if usTimeZones[strings.Join(ws, " ")] {
		return true
	}
	// "EST time": the abbreviation with "time" after it.
	return len(ws) == 2 && ws[1] == "time" && usTimeZones[ws[0]]
}

// ledCountry reports whether a part led by a code that is also country c's
// ("DE", "IN", "CA") goes on to name that country: one of its hyphen pieces
// (or, without a hyphen, the words after the code) is a city of c's list,
// names c, or is one of c's regions.
func ledCountry(code, c, t string) bool {
	pieces := strings.Split(t, "-")
	if len(pieces) < 2 {
		if i := strings.IndexByte(t, ' '); i > 0 {
			pieces = []string{t[:i], t[i+1:]}
		}
	}
	for _, p := range pieces[1:] {
		p = strings.TrimSpace(p)
		if isCountryCity(code, code, p) || foreignCities[NormArea(p)] == c || foreignRegions[NormArea(p)] == c {
			return true
		}
		if n, ok := namedCountry(p); ok && n == c {
			return true
		}
	}
	return false
}

// firstWordCountry reads a country named as the first word of a longer part
// ("Malaysia-Penang B", "India Bangalore"): one word of countryAliases.
func firstWordCountry(ws []string) (string, bool) {
	if len(ws) < 2 {
		return "", false
	}
	c, ok := countryAliases[ws[0]]
	return c, ok
}

// noteWords are what boards write beside a place that is not a place:
// "San Francisco (preferred)", "New York City (Remote possible for
// exceptional candidates)", and neighbourhoods a city's office sits in
// ("NYC (SoHo)").
var noteWords = set("preferred", "prefered", "preference", "possible", "considered", "consider", "open", "to",
	"relocation", "relocate", "negotiable", "candidates", "candidate", "exceptional", "for", "priority",
	"plus", "also", "ok", "okay", "welcome", "strongly", "encouraged", "and", "the", "a", "an", "with",
	"available", "if", "needed", "as", "per", "team", "teams", "tbd", "tba", "post", "posting",
	"any", "flex", "virtual", "address",
	"soho", "midtown", "tribeca", "fidi", "financial", "district", "flatiron", "dumbo", "uptown",
	"soma", "hudson", "yards", "back", "bay", "kendall", "square", "seaport", "mission", "union")

// streetWords end a street or address: a part ending in one names no place
// ("Fremont St", "Second St", "W Adams Street"). Not "fl": "Tampa Fl" is
// Florida.
var streetWords = set("st", "street", "ave", "avenue", "rd", "road", "blvd", "boulevard", "dr", "drive",
	"ln", "lane", "pkwy", "parkway", "hwy", "highway", "suite", "ste", "floor", "plaza", "way")

// hyphenState reads Workday's hyphenated form, a US city and its state code
// in capitals ("Chicago-IL-W Adams Street", "Scottsdale-AZ-N Scottsdale Rd",
// "CA-San Francisco"): the city must be whole and the code a USPS code.
func hyphenState(t string) (string, bool) {
	ps := strings.Split(t, "-")
	if len(ps) < 2 {
		return "", false
	}
	for i := 0; i+1 < len(ps); i++ {
		a, b := strings.TrimSpace(ps[i]), strings.TrimSpace(ps[i+1])
		switch {
		case isStateCode(b) && usCityNames[NormArea(a)]:
			return b, true
		case isStateCode(a) && usCityNames[NormArea(b)]:
			return a, true
		}
	}
	return "", false
}

// boundedRunes cuts s to at most n runes.
func boundedRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func slicesContainsState(ps []part, code string) bool {
	for _, q := range ps {
		if q.kind == kindState && q.state == code {
			return true
		}
	}
	return false
}

func classifyScope(q *part, s string) {
	switch s {
	case "US":
		q.kind = kindUS
	case "North America", "Americas", "Anywhere":
		q.kind = kindWide
	default:
		q.kind, q.country = kindCountry, scopeCountry[s] // "" for Europe, EMEA, APAC, LATAM
	}
}

// resolveCollisions turns a state written like a country ("IN", "DE",
// "Georgia") into that country when another part is one of its cities:
// "Pune, MH, IN", "Berlin, DE", "Tbilisi, Georgia".
func resolveCollisions(ps []part) {
	for i := range ps {
		if ps[i].kind != kindState {
			continue
		}
		c := collidingCountry[ps[i].state]
		if c == "" {
			continue
		}
		for j := range ps {
			// "Georgia (Country)": the board says which.
			if j != i && (isCountryCity(ps[i].state, ps[i].text, ps[j].text) || ps[i].strong && NormArea(ps[j].text) == "country") {
				ps[i].kind, ps[i].country, ps[i].state = kindCountry, c, ""
				break
			}
		}
	}
}

// optBuild is an option while its parts are read.
type optBuild struct {
	texts       []string
	head        headKind
	headName    string
	headCountry string
	headLike    bool // a state written by name that started the option stands for its city
	states      []string
	strongState bool // a state written by name after the option's city
	us          bool
	hasCountry  bool
	country     string
	wide        bool
	code        bool
	negUS       bool
	negUnknown  bool
	unqual      bool
}

func (o *optBuild) add(q *part, text bool) {
	if text && q.text != "" {
		o.texts = append(o.texts, q.text)
	}
}

func (o *optBuild) hasHead() bool { return o.head != headNone || o.headLike }

func (o *optBuild) setHead(q *part, name string) {
	o.head, o.headName, o.headCountry = q.head, name, q.country
	o.add(q, true)
}

// unsettled: nothing but the city has been read, no state, country or US
// token that would already decide the option.
func (o *optBuild) unsettled() bool {
	return len(o.states) == 0 && !o.us && !o.hasCountry && !o.negUS
}

// refusesHead: a city that would contradict the qualifiers already read
// starts its own option. A US city after a foreign country alone ("Canada,
// Houston") and a foreign city after "US" alone are lists, not one place.
// An ambiguous name after a region alone ("Remote - EMEA, Remote - NA") is a
// second option too: a region is not a country that would settle it.
func (o *optBuild) refusesHead(q *part) bool {
	return q.head == headUS && o.hasCountry || q.head == headForeign && o.us ||
		q.head == headAmbiguous && o.hasCountry && o.country == ""
}

// takesCountry: a country or region qualifies the option unless the option
// already says the US, names another country, or holds a state written in
// full after its city ("Houston, New York, Singapore" is a list). An option
// a state code has placed in the US keeps it, and what follows is an option
// of its own, as after "New York, NY (Europe)": always for a city of the US
// table ("Seattle, WA, Canada", "San Francisco, CA (Canada OK)"), and for
// any town but a city of the foreign lists when what follows is a region or
// a note naming a country ("Bentonville, AR (EMEA hours)", "Smallville, KS
// (Canada OK)"). A country written as a whole part after a town the US table
// does not hold and a code is that country's address, the code a subdivision
// code a USPS code shares ("Madrid, MD, Spain" is Comunidad de Madrid,
// "Westport, MO, Ireland" Mayo, "Hoofddorp, NH, Netherlands" Noord-Holland;
// every such string in the live ledger on 2026-11-12 was foreign). A city of
// the foreign lists takes the qualifier, which names which one ("London,
// Ontario" is in Canada).
func (o *optBuild) takesCountry(q *part) bool {
	switch {
	case o.us, o.negUS:
		return false
	case o.hasCountry && o.country != q.country && q.country != "" && o.country != "":
		return false
	case o.strongState:
		return false
	case len(o.states) > 0 && o.head == headUS:
		return false
	case len(o.states) > 0 && o.head != headForeign && (q.note || q.country == ""):
		return false
	case len(o.states) > 0 && o.head == headAmbiguous && (q.country == "" || collidingCountry[o.states[0]] != q.country):
		// A name as likely US as not keeps its state unless the code is
		// also the country's ISO code ("Richmond, CA, Canada" is Canada).
		// A city of the US table keeps it either way: "San Francisco, CA
		// (Canada OK)".
		return false
	}
	return true
}

// washingtonDC reads "Washington" just before DC as the city, so
// "Washington, D.C." and "Washington, DC Metro Area" are one option in DC,
// not Washington state and DC. Other state names before a state are left as
// written: after Parse, "New York, New Jersey" (a list) and "Indiana, PA" (a
// town) are the same parts, and reading them as one would lose a state.
func washingtonDC(ps []part) {
	for i := 0; i+1 < len(ps); i++ {
		if ps[i].kind == kindState && ps[i].state == "WA" && NormArea(ps[i].text) == "washington" &&
			ps[i+1].kind == kindState && ps[i+1].state == "DC" {
			ps[i].kind, ps[i].head, ps[i].state, ps[i].strong = kindHead, headUS, "", false
		}
	}
}

// splitZipState splits a part that ends in a state code and a ZIP code
// ("Plano TX 75024", "Austin TX 78701-1234") into the town and the code: a
// ZIP after a USPS code says the US. Without the ZIP a code after a name
// stays in the part ("Pune MH"), as Parse already reads "Plano TX".
func splitZipState(raw []string) []string {
	var out []string
	for _, r := range raw {
		f := strings.Fields(r)
		if n := len(f); n >= 3 && isZip(f[n-1]) && isStateCode(f[n-2]) {
			out = append(out, strings.Join(f[:n-2], " "), f[n-2])
			continue
		}
		out = append(out, r)
	}
	if len(out) > maxOptionParts {
		out = out[:maxOptionParts]
	}
	return out
}

// isZip reports a US ZIP code: five digits, or ZIP+4.
func isZip(s string) bool {
	if len(s) == 10 && s[5] == '-' {
		return isDigits(s[:5]) && isDigits(s[6:])
	}
	return len(s) == 5 && isDigits(s)
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func (o *optBuild) option() Option {
	opt := Option{Text: strings.Join(o.texts, ", ")}
	switch {
	case o.negUS:
		opt.Verdict = VerdictNotUS
	case o.hasCountry:
		opt.Verdict, opt.Country = VerdictNotUS, o.country
		if opt.Country == "" && o.head == headForeign {
			opt.Country = o.headCountry // "Munich, Europe"
		}
	case o.us || len(o.states) > 0:
		opt.Verdict, opt.Country = VerdictUS, "United States"
		if len(o.states) > 0 {
			opt.State = o.states[0]
		}
	case o.head == headUS && o.unqual:
		opt.Verdict = VerdictAmbiguous
	case o.head == headUS:
		opt.Verdict, opt.Country = VerdictUS, "United States"
	case o.head == headAmbiguous, o.head == headLeadUS:
		opt.Verdict = VerdictAmbiguous
	case o.head == headForeign:
		opt.Verdict, opt.Country = VerdictNotUS, o.headCountry
	case o.head == headOther:
		opt.Verdict, opt.Unrecognised = VerdictNotUS, true
	case o.wide, o.negUnknown:
		opt.Verdict = VerdictAmbiguous
	default:
		opt.Verdict = VerdictNotUS
	}
	return opt
}

// negWords negate the place after them ("excluding US", "outside the US",
// "no Canada", "Not US"). "Non-remote" never reaches here: Parse drops a
// negated work mode with the mode.
var negWords = map[string]bool{
	"excluding": true, "exclude": true, "excludes": true, "except": true, "outside": true,
	"no": true, "not": true, "non": true, "without": true,
}

// negFiller are words between a negation and the place it negates: "not in
// the US", "not available in CA", "not open to Texas".
var negFiller = set("the", "in", "of", "available", "located", "open", "to", "for", "hiring")

// negation reads a part that negates a place: kindNegUS when the place is the
// US, kindNegOther when it is another place a vocabulary knows, kindNegUnknown
// otherwise. state is the USPS code when the negated place is a US state
// ("non-California", "not available in CA"; a code only in capitals). ok is
// false when the part has no negation word with something after it.
func negation(t string, ws []string) (kind partKind, state string, ok bool) {
	i := -1
	for j, w := range ws {
		if negWords[w] && j < len(ws)-1 {
			i = j
			break
		}
	}
	if i < 0 {
		return 0, "", false
	}
	rest := ws[i+1:]
	for len(rest) > 1 && negFiller[rest[0]] {
		rest = rest[1:]
	}
	r := strings.Join(rest, " ")
	switch r {
	case "us", "u s", "usa", "u s a", "united states", "united states of america", "america", "the us":
		return kindNegUS, "", true
	}
	if s, ok := scopes[r]; ok {
		if s == "US" {
			return kindNegUS, "", true // "outside the lower 48", "excluding the USA"
		}
		return kindNegOther, "", true
	}
	if s, ok := salesRegions[r]; ok {
		if s == "US" {
			return kindNegUS, "", true // "outside CONUS"
		}
		return kindNegOther, "", true
	}
	if code, ok := stateByName[r]; ok {
		return kindNegOther, code, true
	}
	if f := strings.Fields(t); len(rest) == 1 && len(f) > 0 && isStateCode(strings.Trim(f[len(f)-1], ".,;:()")) &&
		strings.EqualFold(strings.Trim(f[len(f)-1], ".,;:()"), r) {
		return kindNegOther, strings.ToUpper(r), true
	}
	if _, ok := countryAliases[r]; ok {
		return kindNegOther, "", true
	}
	if _, ok := foreignRegions[r]; ok {
		return kindNegOther, "", true
	}
	return kindNegUnknown, "", true
}

// negatedUSState is the US state a part excludes ("non-California", "not
// available in CA", "excluding Hawaii"), or "". Georgia is left out: "no
// Georgia" beside a foreign region may be the country.
func negatedUSState(part string) string {
	k, code, ok := negation(part, words(part))
	if !ok || k != kindNegOther || code == "" || code == "GA" {
		return ""
	}
	return code
}

// namesUS reports whether a part names the US among other words: "USA" or
// "United States" anywhere in it ("California USA", "United States and
// Canada", "USA-Colorado-Fort Collins"), or the token US written in capitals
// or as U.S. ("US-Remote", "Remote US"). A lowercase "us" in prose ("Contact
// us") is the pronoun.
func namesUS(t string, ws []string) bool {
	for i, w := range ws {
		if w == "usa" || w == "united" && i+1 < len(ws) && ws[i+1] == "states" {
			return true
		}
	}
	return hasUSToken(t)
}

// hasUSToken reports "US" in capitals as a whole token, or "U.S." / "U.S".
func hasUSToken(t string) bool {
	if strings.Contains(t, "U.S") {
		return true
	}
	for _, tok := range strings.FieldsFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if tok == "US" {
			return true
		}
	}
	return false
}

// regionPhrase is a region a longer part names: "South America" and "Central
// America" anywhere in it are LATAM (so "america" alone never makes them the
// US), a scope as its first or last word ("London UK", "Remote Canada").
// "us" and "america" alone are not read here: the US needs namesUS.
func regionPhrase(ws []string) string {
	for i := 0; i+1 < len(ws); i++ {
		if s, ok := scopes[ws[i]+" "+ws[i+1]]; ok && s != "US" {
			return s
		}
	}
	if len(ws) < 2 {
		return ""
	}
	for _, w := range []string{ws[0], ws[len(ws)-1]} {
		if w == "us" || w == "america" {
			continue
		}
		if s, ok := scopes[w]; ok && s != "US" {
			return s
		}
	}
	return ""
}

func isLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// isoPrefixCountry reads an ISO code that leads a hyphenated part
// ("GB-London", "JP-Tokyo", "IND-Pune"), when the code is not a USPS code.
func isoPrefixCountry(t string) (string, bool) {
	i := strings.IndexByte(t, '-')
	if i != 2 && i != 3 {
		return "", false
	}
	head := t[:i]
	if head != strings.ToUpper(head) {
		return "", false
	}
	if _, isState := stateNames[head]; isState {
		return "", false
	}
	if c, ok := isoCountry[head]; ok {
		return c, true
	}
	if c, ok := iso3Country[head]; ok {
		return c, true
	}
	return "", false
}

// leadAffix and tailAffix are the office and area words a board puts around a
// place name: "Greater Boston Area" is boston, "San Francisco HQ" san
// francisco, "Portland Metro" portland, "Washington State" washington. The
// city match and the ambiguity check read the same name, so "Cambridge HQ"
// stays as ambiguous as "Cambridge".
var leadAffix = set("greater", "metro", "downtown", "main")

var tailAffix = set("hq", "headquarters", "office", "offices", "campus", "hub", "area", "metro",
	"metropolitan", "metroplex", "region", "site", "state", "main", "corporate", "branch", "location",
	"downtown", "center", "centre", "building", "facility", "lab", "labs", "studio", "studios",
	"virtual", "address")
