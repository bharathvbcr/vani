package location

import (
	"slices"
	"strings"
)

// Countries returns the distinct countries l names, as canonical names
// ("United States", "Germany"), places first and then any country a remote
// scope adds. A US state is the United States. A remote scope counts only
// when it names a country ("US", "Canada"), not a region ("Europe",
// "Anywhere", "North America"). Empty means no country was listed: "none"
// for a --country filter.
//
// It reads Parse's output and does not change it. Near-duplicate detection
// and the exclusions read Parse as an engine rule.
func Countries(l Location) []string {
	var out []string
	add := func(c string) {
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, p := range l.Places {
		for _, c := range placeCountries(p) {
			add(c)
		}
	}
	for _, s := range strings.Split(l.RemoteScope, ",") {
		if c, ok := namedCountry(strings.TrimSpace(s)); ok {
			add(c)
		}
	}
	return out
}

// CanonicalCountry reads a country a person typed ("Germany", "de", "usa")
// as the name Countries returns. A US state name is not a country; a
// two-letter code that is also a USPS code is the country ("DE" is Germany),
// which is what a country filter asks for.
func CanonicalCountry(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if c, ok := namedCountry(s); ok {
		return c, true
	}
	tok := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if len(tok) == 2 {
		if c, ok := collidingCountry[tok]; ok {
			return c, true
		}
		if c, ok := isoCountry[tok]; ok {
			return c, true
		}
	}
	// "Georgia" is a US state to namedCountry. On a country flag it is the
	// country; a US state is --state GA.
	if strings.Join(words(s), " ") == "georgia" {
		return "Georgia", true
	}
	return "", false
}

func placeCountries(p Place) []string {
	if p.State != "" {
		return []string{"United States"}
	}
	parts := placeParts(p)
	var out []string
	add := func(c string) {
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, part := range parts {
		if c, ok := namedCountry(part); ok {
			add(c)
		}
	}
	for i, part := range parts {
		code, ok := collidingCode(part)
		if !ok {
			continue
		}
		for j, other := range parts {
			if i != j && isCountryCity(code, part, other) {
				add(collidingCountry[code])
			}
		}
	}
	for _, part := range parts {
		if canadaProvinces[strings.TrimSuffix(strings.TrimSpace(part), ".")] {
			add("Canada")
		}
	}
	if len(out) == 0 && placeInUS(p) {
		return []string{"United States"}
	}
	return out
}

func placeParts(p Place) []string {
	var parts []string
	for _, s := range []string{p.Area, p.Region} {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
	}
	return parts
}

// collidingCode is a USPS code that Parse left as a region because the city
// is one of that country's, or a two-letter token Parse did not take as a
// state. A capitalised state with no such city never reaches here: Parse
// sets Place.State.
func collidingCode(part string) (string, bool) {
	if code, ok := stateOf(part); ok {
		if collidingCountry[code] != "" {
			return code, true
		}
		return "", false
	}
	tok := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(part), "."))
	if len(tok) == 2 && collidingCountry[tok] != "" {
		return tok, true
	}
	return "", false
}

// namedCountry is the country one part names, or false when it names a US
// state, a region ("Europe") or nothing we read.
func namedCountry(part string) (string, bool) {
	if s, ok := scopeOf(part); ok {
		c, ok := scopeCountry[s]
		return c, ok
	}
	if _, ok := stateOf(part); ok {
		return "", false
	}
	if c, ok := countryAliases[strings.Join(words(part), " ")]; ok {
		return c, true
	}
	tok := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(part), "."))
	if len(tok) == 2 {
		if _, isState := stateNames[tok]; !isState {
			if c, ok := isoCountry[tok]; ok {
				return c, true
			}
		}
	}
	return "", false
}

// scopeCountry is the scopes value that is a country. Europe, EMEA, APAC,
// LATAM, North America, Americas and Anywhere are regions, not countries.
var scopeCountry = map[string]string{
	"US": "United States", "UK": "United Kingdom", "Canada": "Canada", "Mexico": "Mexico",
}

// collidingCountry is the country of a USPS code Parse can also read as ISO
// 3166 (location.go's countryByCode and countryByName). "DE" on a country
// flag is Germany; Delaware is --state DE.
var collidingCountry = map[string]string{
	"DE": "Germany", "IN": "India", "CA": "Canada", "IL": "Israel",
	"AR": "Argentina", "CO": "Colombia", "MA": "Morocco", "MD": "Moldova",
	"MT": "Malta", "PA": "Panama", "ID": "Indonesia", "MN": "Mongolia",
	"AL": "Albania", "AZ": "Azerbaijan", "GA": "Georgia",
}

// isoCountry is an alpha-2 code that is not a USPS code, so a board's "FR"
// or "JP" is that country. Codes in collidingCountry are not repeated here.
var isoCountry = map[string]string{
	"FR": "France", "GB": "United Kingdom", "IE": "Ireland", "NL": "Netherlands",
	"BE": "Belgium", "ES": "Spain", "IT": "Italy", "PT": "Portugal",
	"CH": "Switzerland", "AT": "Austria", "SE": "Sweden", "NO": "Norway",
	"DK": "Denmark", "FI": "Finland", "PL": "Poland", "CZ": "Czechia",
	"RO": "Romania", "HU": "Hungary", "GR": "Greece", "UA": "Ukraine",
	"TR": "Turkey", "AE": "United Arab Emirates", "SG": "Singapore", "JP": "Japan",
	"KR": "South Korea", "CN": "China", "TW": "Taiwan", "HK": "Hong Kong",
	"AU": "Australia", "NZ": "New Zealand", "BR": "Brazil", "CL": "Chile",
	"PH": "Philippines", "VN": "Vietnam", "TH": "Thailand", "MY": "Malaysia",
	"ZA": "South Africa", "NG": "Nigeria", "KE": "Kenya", "EG": "Egypt",
	"CR": "Costa Rica", "MX": "Mexico", "LU": "Luxembourg", "IS": "Iceland",
	"SK": "Slovakia", "SI": "Slovenia", "HR": "Croatia", "BG": "Bulgaria",
	"LT": "Lithuania", "LV": "Latvia", "EE": "Estonia", "QA": "Qatar",
	"SA": "Saudi Arabia", "RU": "Russia", "PK": "Pakistan",
}

var countryAliases = func() map[string]string {
	m := map[string]string{}
	add := func(name string, extra ...string) {
		m[strings.Join(words(name), " ")] = name
		for _, e := range extra {
			m[strings.Join(words(e), " ")] = name
		}
	}
	add("Germany", "deutschland", "federal republic of germany")
	add("France")
	add("India")
	add("Ireland", "republic of ireland", "eire")
	add("Netherlands", "the netherlands", "holland", "nederland")
	add("Belgium", "belgique")
	add("Spain", "espana", "españa")
	add("Italy", "italia")
	add("Portugal")
	add("Switzerland", "schweiz", "suisse", "svizzera")
	add("Austria", "osterreich", "österreich")
	add("Sweden", "sverige")
	add("Norway", "norge")
	add("Denmark", "danmark")
	add("Finland", "suomi")
	add("Poland", "polska")
	add("Czechia", "czech republic")
	add("Romania")
	add("Hungary")
	add("Greece")
	add("Ukraine")
	add("Israel")
	add("Turkey", "turkiye", "türkiye")
	add("United Arab Emirates", "uae")
	add("Singapore")
	add("Japan")
	add("South Korea", "republic of korea", "korea")
	add("China", "prc", "people's republic of china")
	add("Taiwan")
	add("Hong Kong", "hong kong sar")
	add("Australia")
	add("New Zealand")
	add("Brazil", "brasil")
	add("Argentina")
	add("Chile")
	add("Colombia")
	add("Costa Rica")
	add("Panama")
	add("Indonesia")
	add("Malaysia")
	add("Thailand")
	add("Vietnam", "viet nam")
	add("Philippines")
	add("South Africa")
	add("Nigeria")
	add("Kenya")
	add("Egypt")
	add("Morocco")
	add("Moldova")
	add("Malta")
	add("Mongolia")
	add("Albania")
	add("Azerbaijan")
	add("Georgia", "republic of georgia") // the word "georgia" is also the US state; see CanonicalCountry
	add("Luxembourg")
	add("Iceland")
	add("Slovakia")
	add("Slovenia")
	add("Croatia")
	add("Bulgaria")
	add("Lithuania")
	add("Latvia")
	add("Estonia")
	add("Qatar")
	add("Saudi Arabia")
	add("Russia", "russian federation")
	add("Pakistan")
	// "georgia" as a lone word is the US state to Parse. Drop that alias so a
	// place named only "Georgia" stays the state; the country is the city
	// list, "Republic of Georgia", or an explicit --country Georgia.
	delete(m, "georgia")
	return m
}()
