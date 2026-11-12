package location

import (
	"slices"
	"strings"
)

// Countries returns the distinct countries l names, as canonical names
// ("United States", "Germany"), places first and then any country a remote
// scope adds. Each place contributes the countries of its options
// (PlaceOptions), the same verdicts InUS reads, so "United States" is listed
// exactly when some place reads as the US: a US state, a US token or a city
// of the US table; a foreign country, province or region; or a city of the
// foreign lists ("Toronto" is Canada). A name nothing places ("Lagos" is
// listed, "Accra" is not) adds none. A remote scope counts only when it
// names a country ("US", "Canada"), not a region ("Europe", "Anywhere",
// "North America"). Empty means no country was listed: "none" for a
// --country filter.
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

// placeCountries lists the countries of p's options (PlaceOptions), in order:
// the same verdicts PlaceInUS reads, so "United States" is listed exactly when
// some option is in the US. An option names a country when it is a US place,
// names a country or province ("Markham, Ontario"), or is a city of the
// foreign city lists ("Toronto"); an option that names nothing that tells
// ("London", "Lagos") lists none.
func placeCountries(p Place) []string {
	var out []string
	for _, o := range PlaceOptions(p) {
		if o.Country != "" && !slices.Contains(out, o.Country) {
			out = append(out, o.Country)
		}
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
	// Added 2026-11-12 (ISO 3166-1 English short names, the countries boards
	// list that the vocabulary lacked: "Santa Clara, Cuba", "Lima, Peru",
	// "Phoenix, Mauritius" read as US before).
	add("Cuba")
	add("Peru", "perú")
	add("Mauritius")
	add("Serbia")
	add("Uruguay")
	add("Paraguay")
	add("Bolivia")
	add("Ecuador")
	add("Venezuela")
	add("Guatemala")
	add("Honduras")
	add("El Salvador")
	add("Nicaragua")
	add("Dominican Republic")
	add("Jamaica")
	add("Trinidad and Tobago")
	add("Barbados")
	add("Bahamas", "the bahamas")
	add("Belize")
	add("Bangladesh")
	add("Sri Lanka")
	add("Nepal")
	add("Cambodia")
	add("Myanmar")
	add("Laos")
	add("Macau", "macao")
	add("Jordan")
	add("Lebanon")
	add("Kuwait")
	add("Bahrain")
	add("Oman")
	add("Cyprus")
	add("Tunisia")
	add("Algeria")
	add("Ghana")
	add("Ethiopia")
	add("Rwanda")
	add("Uganda")
	add("Tanzania")
	add("Senegal")
	add("Côte d'Ivoire", "cote d ivoire", "ivory coast")
	add("Cameroon")
	add("Belarus")
	add("Kazakhstan")
	add("Uzbekistan")
	add("Armenia")
	add("Bosnia and Herzegovina", "bosnia")
	add("North Macedonia", "macedonia")
	add("Montenegro")
	add("Kosovo")
	add("Liechtenstein")
	add("Monaco")
	add("Andorra")
	// "georgia" as a lone word is the US state to Parse. Drop that alias so a
	// place named only "Georgia" stays the state; the country is the city
	// list, "Republic of Georgia", or an explicit --country Georgia.
	delete(m, "georgia")
	return m
}()

// foreignCities are the foreign cities location.go's lists name (the tech-
// office cities of the countries whose ISO code or name collides with a US
// state), read as that country when written bare: "Toronto" is Canada,
// "Toronto, Austin" is Toronto or Austin. Names as likely a US place
// (Vancouver, Waterloo, Panama City) or of two foreign countries (Córdoba) are
// left out; ambiguousNames holds the first.
var foreignCities = func() map[string]string {
	m := map[string]string{}
	for code, cities := range countryByCode {
		for c := range cities {
			m[c] = collidingCountry[code]
		}
	}
	for code, cities := range countryByName {
		for c := range cities {
			m[c] = collidingCountry[code]
		}
	}
	for _, c := range []string{"vancouver", "waterloo", "panama city", "córdoba", "cordoba"} {
		delete(m, NormArea(c))
	}
	// The foreign tech hubs boards list beside a US city ("San Francisco,
	// Amsterdam"), each far more often that city than any US namesake (a US
	// namesake comes with its state: "Dublin, OH", "Lima, OH", which
	// PlaceOptions reads as the US). Paris is not here: by the owner's
	// decision (2026-11-12) a bare "Paris" is ambiguous (us.go).
	for c, country := range map[string]string{
		"london": "United Kingdom", "manchester": "United Kingdom", "edinburgh": "United Kingdom",
		"amsterdam": "Netherlands", "dublin": "Ireland", "zurich": "Switzerland",
		"zürich": "Switzerland", "geneva": "Switzerland", "stockholm": "Sweden", "copenhagen": "Denmark",
		"oslo": "Norway", "helsinki": "Finland", "madrid": "Spain", "barcelona": "Spain", "lisbon": "Portugal",
		"warsaw": "Poland", "krakow": "Poland", "kraków": "Poland", "prague": "Czechia", "budapest": "Hungary",
		"bucharest": "Romania", "milan": "Italy", "rome": "Italy", "tokyo": "Japan", "osaka": "Japan",
		"seoul": "South Korea", "taipei": "Taiwan", "beijing": "China", "shanghai": "China",
		"shenzhen": "China", "sydney": "Australia", "melbourne": "Australia", "auckland": "New Zealand",
		"são paulo": "Brazil", "sao paulo": "Brazil", "mexico city": "Mexico", "lima": "Peru",
		"santiago": "Chile", "kyiv": "Ukraine", "lviv": "Ukraine", "istanbul": "Turkey", "dubai": "United Arab Emirates",
		"tel aviv": "Israel", "delhi": "India", "nairobi": "Kenya", "lagos": "Nigeria", "cairo": "Egypt",
	} {
		m[NormArea(c)] = country
	}
	return m
}()

// foreignRegions are first-level subdivisions abroad, read as their country
// when they qualify a city ("Bay Area, Ontario", "Houston, Scotland",
// "Austin, Victoria", "Andover, Hampshire", "Henderson, Auckland"). A city's
// own name in first place, or before a US state, stays a city ("Victoria,
// BC", "Ontario, CA"). Source: ISO 3166-2 subdivision names for the countries
// boards list most, 2026-11-12. UK counties that are also common US county
// names after a city (Middlesex, Suffolk, Essex, Norfolk, Somerset, Kent,
// Durham, Cheshire, Berkshire, Devon, Rutland) are left out: after a US city
// they read as unknown, which the exclusions keep.
var foreignRegions = func() map[string]string {
	m := map[string]string{}
	add := func(country string, names ...string) {
		for _, n := range names {
			m[NormArea(n)] = country
		}
	}
	add("Canada", "ontario", "quebec", "québec", "british columbia", "alberta", "manitoba", "saskatchewan",
		"nova scotia", "new brunswick", "newfoundland", "newfoundland and labrador", "prince edward island",
		"yukon", "northwest territories", "nunavut")
	add("Australia", "new south wales", "nsw", "victoria", "queensland", "western australia",
		"south australia", "tasmania", "australian capital territory", "northern territory")
	add("United Kingdom", "scotland", "wales", "northern ireland", "greater london", "greater manchester",
		"west midlands", "tyne and wear", "merseyside", "south yorkshire", "west yorkshire", "north yorkshire",
		"east riding of yorkshire", "yorkshire", "east sussex", "west sussex", "isle of wight", "hampshire",
		"surrey", "oxfordshire", "cambridgeshire", "buckinghamshire", "hertfordshire", "bedfordshire",
		"lincolnshire", "nottinghamshire", "leicestershire", "derbyshire", "staffordshire", "warwickshire",
		"worcestershire", "gloucestershire", "wiltshire", "dorset", "cornwall", "cumbria", "lancashire",
		"northamptonshire", "shropshire", "herefordshire", "northumberland", "county durham")
	add("New Zealand", "auckland", "waikato", "otago", "bay of plenty", "wellington region")
	add("India", "karnataka", "maharashtra", "telangana", "tamil nadu", "haryana", "uttar pradesh", "kerala",
		"west bengal", "gujarat", "andhra pradesh", "rajasthan", "delhi", "delhi ncr")
	add("Germany", "bavaria", "bayern", "baden württemberg", "baden wurttemberg", "north rhine westphalia",
		"nordrhein westfalen", "hesse", "hessen", "saxony", "sachsen", "lower saxony", "niedersachsen")
	add("Ireland", "county dublin", "co dublin", "county cork", "co cork", "leinster", "munster")
	add("Mexico", "jalisco", "nuevo león", "nuevo leon", "cdmx", "ciudad de méxico", "ciudad de mexico")
	add("China", "guangdong", "zhejiang", "jiangsu", "sichuan")
	add("Brazil", "são paulo", "sao paulo", "minas gerais", "rio de janeiro", "santa catarina",
		"rio grande do sul")
	add("Spain", "catalonia", "cataluña", "catalunya", "comunidad de madrid")
	add("Netherlands", "north holland", "noord holland")
	add("Philippines", "metro manila")
	// "Paris, Île-de-France" is France: with a bare Paris ambiguous, its
	// region has to say so.
	add("France", "île de france", "ile de france")
	return m
}()
