package location

import "strings"

// InUS reports whether l names a place in the United States, or remote work
// whose scope takes in the US. It is what "US postings first" orders by
// (advance_us_first), so it only orders and never drops: a miss leaves a
// posting in the ordinary order, which is why an unreadable or unscoped
// location ("Remote", "2 Locations", empty) reads as false.
//
// A place is in the US when some option of it is (PlaceOptions): a state
// (Parse's, or one written anywhere in the place: "California - San
// Francisco", "US, CA, Santa Clara"), a US token ("USA", "United States",
// "US" in capitals, "U.S."), or a city of usCities written without a state
// ("San Francisco", "NYC", "Greater Boston Area"). A foreign country, province
// or region beside a US city name makes that option foreign ("Santa Clara,
// Cuba", "Houston, Scotland", "Bay Area, Ontario"); a negation makes it not
// the US ("Remote - outside US"); a name as likely foreign as not ("Cambridge",
// "San Jose") is ambiguous, which reads as false here.
//
// A remote scope takes in the US when it names the US, North America, the
// Americas or anywhere.
func InUS(l Location) bool {
	for _, p := range l.Places {
		if PlaceInUS(p) {
			return true
		}
	}
	return ScopeTakesInUS(l.RemoteScope)
}

// ScopeTakesInUS reports whether a remote scope (Location.RemoteScope) lets
// the work be done from the US: it names the US, North America, the Americas
// or anywhere.
func ScopeTakesInUS(scope string) bool {
	for _, s := range strings.Split(scope, ",") {
		switch strings.TrimSpace(s) {
		case "US", "North America", "Americas", "Anywhere":
			return true
		}
	}
	return false
}

// ambiguousNames are names boards write bare that are as likely a US place as
// a foreign one in tech hiring. A name far more often foreign ("London",
// "Paris", "Dublin", "Melbourne") is not here: it reads as the foreign place.
// "San Jose" is here: San José, Costa Rica hires as often as a bare "San Jose"
// means California, and a board writing California adds the state. "NA" is
// North America or N/A.
var ambiguousNames = set(
	"cambridge", "portland", "vancouver", "birmingham", "richmond", "durham",
	"vienna", "columbia", "athens", "lexington", "salem", "kingston",
	"san jose", "na",
)

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

// usCityNames are US cities and regions boards write without a state, none of
// them a well-known place elsewhere: no Cambridge, Birmingham, Vancouver,
// Richmond, Durham, Portland, San Jose, London, Brisbane, Melbourne,
// Alexandria, Brighton, Westminster, Greenwich, Worcester, Norfolk, Lincoln,
// Toledo or St Petersburg (ambiguousNames holds the ones as likely US as
// not). A name matches whole: "San Jose del Cabo" is not San Jose and "SF
// Express" is not SF. A US name followed by a foreign qualifier is foreign
// (PlaceOptions), so a name here may also be a small place abroad ("Houston,
// Scotland").
//
// Sources, 2026-11-12: (1) the live ledger's board location strings, keeping
// an area written with a US state at least 5 times with one state taking 90%
// of those and more often with a state than with any other region, less the
// names above and words that are not only places ("spring", "sandy",
// "cypress", "mesa", "franklin", "hudson"); (2) the largest US cities by
// population and the tech-hiring suburbs boards name; (3) abbreviations boards
// use: "sf", "nyc", "rtp"; (4) US regions boards write as a location ("New
// England", "Pacific Northwest", "Midwest", "East Coast", "DMV", "Tri-State").
var usCityNames = set(
	// Abbreviations and regions.
	"sf", "sf bay area", "bay area", "san francisco bay area", "silicon valley", "nyc", "rtp",
	"research triangle park", "research triangle", "space coast", "dallas fort worth", "dfw",
	"greater seattle", "new england", "pacific northwest", "pnw", "midwest", "east coast", "west coast",
	"dmv", "tri state", "tristate", "southern california", "socal", "northern california", "norcal",
	"twin cities", "puget sound", "front range", "gulf coast", "mid atlantic",
	// Large cities.
	"san francisco", "new york city", "manhattan", "brooklyn", "los angeles", "chicago", "houston", "phoenix",
	"philadelphia", "san antonio", "san diego", "dallas", "austin", "jacksonville",
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
	"charleston", "greenville", "dayton", "columbus", "kansas city", "arlington",
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
	// Regional names boards write for one US area (review of 2e347f4).
	"long island", "orange county", "raleigh durham", "raleigh durham chapel hill", "chapel hill",
)
