// Package sources turns a watchlist entry into postings. Each ATS has its own
// adapter; all of them reach the network only through an egress.Doer.
package sources

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// TokenProblem is why token cannot name a board, or "" when it can. It is the
// one rule behind config.ValidBoard and srcutil.CheckBoard, so a token the
// config accepts is one every adapter accepts. It holds what no adapter can
// work around; the config is stricter on top (spaces, '?', '#', ".." and a '/'
// outside Workday), because adapters escape those themselves.
func TokenProblem(token string) string {
	switch {
	case strings.TrimSpace(token) == "":
		return "is empty"
	case strings.Contains(token, ":"):
		// ':' separates key parts; Board.KeyPrefix would be ambiguous.
		return "contains ':'"
	}
	return ""
}

// tenantLabel is one lower-case DNS label, 1..63 bytes, with no leading or
// trailing '-'.
var tenantLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// TenantLabelProblem is why token cannot be the subdomain of a per-tenant ATS
// host ({token}.recruitee.com, .pinpointhq.com, .bamboohr.com,
// .jobs.personio.com, .breezy.hr, .teamtailor.com), or "" when it can. The
// token becomes a host name, so it is exactly one lower-case DNS label as
// written: a '.', '/', '@', ':' or '%' would move the request to another host
// or path. It is the one rule behind config.ValidBoard (and so the host
// config.BoardHost allows) and the adapters that build the host from it.
func TenantLabelProblem(token string) string {
	if !tenantLabel.MatchString(token) {
		return "must be one lower-case host label of letters, digits and '-' (at most 63), as in acme.recruitee.com"
	}
	return ""
}

// TenantHostSuffix is what follows the token in a per-tenant board host. It is
// the one table config.BoardHost builds the host from and BoardFromURL reads a
// tenant back out of.
var TenantHostSuffix = map[posting.ATS]string{
	posting.Recruitee:  ".recruitee.com",
	posting.Pinpoint:   ".pinpointhq.com",
	posting.BambooHR:   ".bamboohr.com",
	posting.Personio:   ".jobs.personio.com",
	posting.Breezy:     ".breezy.hr",
	posting.Teamtailor: ".teamtailor.com",
	posting.HireHive:   ".hirehive.com",
	posting.HiBob:      ".careers.hibob.com",
}

// MaxBoardNameLen bounds Board.BoardName in runes; MaxBoardNameBytes bounds it
// in bytes. 64 BMP runes (CJK, 3 bytes each) fit both; 64 supplementary-plane
// runes do not, which is the byte bound doing its own work.
const (
	MaxBoardNameLen   = 64
	MaxBoardNameBytes = 192
)

// BoardNameProblem is why name cannot be a Board.BoardName, or "" when it can:
// 1..MaxBoardNameLen runes (and at most MaxBoardNameBytes bytes) of valid
// UTF-8 that are Unicode letters, digits, '-' or '_', with single spaces
// between them. It is the one rule behind config.ValidBoard and
// srcutil.CheckBoard.
//
// What it keeps out, and why:
//   - '.', '/', '?', '#', ':' and '%' are punctuation, so a name cannot change
//     the request path (".." included); the name is only ever sent through
//     url.PathEscape, which also escapes every byte of a multi-byte rune.
//   - Combining marks, format characters (zero-width joiner and space, the
//     bidirectional overrides, BOM, soft hyphen), other spaces, controls,
//     private-use and unassigned code points are not letters or digits, so a
//     name cannot hide or reorder text. A mark is refused even after a letter:
//     the standard library cannot normalise to NFC, so the one way to keep the
//     accepted set free of "é" written two ways is to accept only the
//     precomposed letter (Zürich, not Zu + U+0308 + rich). The cost is that a
//     script that needs marks (Devanagari vowel signs, Thai tone marks) cannot
//     be a name until golang.org/x/text/unicode/norm is an approved dependency.
//   - Lookalike letters (Cyrillic "а" for "a") are accepted: the name only ever
//     reaches the path of one fixed host, so a lookalike can fail to match a
//     board but cannot reach another host or path.
//
// Names are compared byte for byte by the ATS, so a name that is not NFC for
// other reasons (a compatibility letter such as U+212B) is at worst a board
// that is not found.
func BoardNameProblem(name string) string {
	if name == "" || len(name) > MaxBoardNameBytes || utf8.RuneCountInString(name) > MaxBoardNameLen {
		return fmt.Sprintf("must be 1..%d characters and at most %d bytes", MaxBoardNameLen, MaxBoardNameBytes)
	}
	if !utf8.ValidString(name) {
		return "must be valid UTF-8"
	}
	if strings.HasPrefix(name, " ") || strings.HasSuffix(name, " ") || strings.Contains(name, "  ") {
		return "must not start or end with a space or hold two in a row"
	}
	for _, r := range name {
		switch {
		case r == '-', r == '_', r == ' ', unicode.IsLetter(r), unicode.IsDigit(r):
		default:
			return "may hold only letters, digits, '-', '_' and single spaces (write an accented letter as one character, not letter plus combining mark)"
		}
	}
	return ""
}

// APIName is the name the ATS knows the board by: BoardName when set, else
// Token. The key and KeyPrefix always use Token.
func (b Board) APIName() string {
	if b.BoardName != "" {
		return b.BoardName
	}
	return b.Token
}

// workdaySiteHost is Workday's shared host, "wd{N}.myworkdaysite.com". Unlike
// "{tenant}.wd{N}.myworkdayjobs.com" its name carries no tenant, so a board on
// it names the tenant only in its token, and its public pages live under
// /recruiting/{tenant}/{site}/ rather than /{site}/.
var workdaySiteHost = regexp.MustCompile(`^wd[0-9]{1,3}\.myworkdaysite\.com$`)

// workdayTenantHost is a tenant's own host, "{tenant}.wd{N}.myworkdayjobs.com".
// N runs to three digits on both hosts: Postman's board is on wd108.
var workdayTenantHost = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)\.wd[0-9]{1,3}\.myworkdayjobs\.com$`)

// WorkdayTenantHost returns the tenant a host "{tenant}.wd{N}.myworkdayjobs.com"
// names, read as written (a bare host name, any case), and whether host is one.
// It is the one rule behind config.ValidBoard and the browser ingest's board.
func WorkdayTenantHost(host string) (tenant string, ok bool) {
	m := workdayTenantHost.FindStringSubmatch(strings.ToLower(strings.TrimSpace(host)))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// WorkdaySiteHost reports whether host, as written (a bare host name, any
// case), is a shared Workday host "wd{N}.myworkdaysite.com". A port, path,
// userinfo or extra label is not one.
func WorkdaySiteHost(host string) bool {
	return workdaySiteHost.MatchString(strings.ToLower(strings.TrimSpace(host)))
}

// oracleHCMHost is an Oracle Recruiting Cloud host, "{tenant}.fa.oraclecloud.com"
// or "{tenant}.fa.{region}.oraclecloud.com" ("jpmc.fa.oraclecloud.com" and
// "eeho.fa.us2.oraclecloud.com" answered on 2026-10-27).
var oracleHCMHost = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)\.fa(?:\.[a-z]{2}[0-9]{1,2})?\.oraclecloud\.com$`)

// OracleHCMHostTenant is the tenant, the first label, of an Oracle Recruiting
// Cloud host as written (a bare host name, any case). ok is false for a port,
// path, userinfo, other domain, other label layout or an empty label.
func OracleHCMHostTenant(host string) (tenant string, ok bool) {
	m := oracleHCMHost.FindStringSubmatch(strings.ToLower(strings.TrimSpace(host)))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// vanityHost is a bare lower-case host name of two or more labels: no IP
// literal (the last label starts with a letter), port, path or userinfo.
var vanityHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// OracleHCMHostProblem says why host cannot serve tenant's Oracle Recruiting
// site, or "" when it can. Two layouts are accepted, each tied to the tenant so
// a board cannot send its requests to a host its token does not name:
//   - "{tenant}.fa[.{region}].oraclecloud.com" (OracleHCMHostTenant), and
//   - the company's own host in front of the same API, whose registrable name
//     (the label before the top-level domain) is the tenant: Dell serves it at
//     enterpriseplatform.dell.com (tenant "dell", 592 jobs on 2026-11-17).
//
// Any other oraclecloud.com host is refused, as are IP literals, one-label
// names, and a host where the tenant is only some other label
// ("jpmc.fa.oraclecloud.com.evil.test", "jpmc.wd5.myworkdayjobs.com"). A
// registrable name under a two-part suffix ("dell.co.uk") is refused too:
// fail closed. The host must still be in allowed_hosts for a request to go
// out.
func OracleHCMHostProblem(host, tenant string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	tenant = strings.ToLower(tenant)
	if host == "" {
		return "is empty"
	}
	if label, ok := OracleHCMHostTenant(host); ok {
		if label != tenant {
			return fmt.Sprintf("does not belong to tenant %q", tenant)
		}
		return ""
	}
	if host == "oraclecloud.com" || strings.HasSuffix(host, ".oraclecloud.com") {
		return "is not {tenant}.fa[.{region}].oraclecloud.com"
	}
	if len(host) > 253 || !vanityHost.MatchString(host) {
		return "is not a bare host name"
	}
	labels := strings.Split(host, ".")
	if labels[len(labels)-2] != tenant {
		return fmt.Sprintf("is not on the domain %s.{tld} of tenant %q", tenant, tenant)
	}
	return ""
}

// Board is one watchlist entry: a company's job board on one ATS, or its own
// careers page.
type Board struct {
	ATS posting.ATS `json:"ats"`
	// Token is the board identifier: Greenhouse board token, Lever company
	// slug, Ashby job-board name, Workday "tenant/site", Eightfold company
	// domain, Jibe client code ("amd"), Rippling board slug, Workable
	// account slug, Gem vanity path, the subdomain of a Recruitee, Pinpoint,
	// BambooHR or Personio board, or a short slug you choose for a careers
	// page or an XML feed.
	Token string `json:"token"`
	// BoardName is the board's literal name on the ATS where that cannot be
	// the Token, Ashby only: Hippocratic AI's board is "Hippocratic AI", with
	// a space, which no posting key, --board ref or Mac-app key check takes
	// (JobLinks.checkedKey refuses whitespace), so its Token is a slug such as
	// "hippocratic-ai" and this field names the board in the request. Empty
	// means the Token is the name, as it is for every board that predates it.
	BoardName string `json:"board_name,omitempty"`
	// Company is the display name; adapters fall back to it when the ATS
	// payload carries none.
	Company string `json:"company"`
	// Region selects a regional API host where one exists ("eu" for Lever).
	Region string `json:"region,omitempty"`
	// Domain is the host of the company's own website ("www.anthropic.com"),
	// optional. Vani fetches the company's icon from fixed paths on it
	// (internal/logo), so it must be in allowed_hosts like a board host, and it
	// must be the host that serves the icon: a redirect to another host is
	// refused like any other.
	Domain string `json:"domain,omitempty"`
	// Icon is an https URL of the company's icon, set by the owner when Domain
	// serves none at the fixed paths (the site names it only in its HTML,
	// which Vani never reads). Optional; it needs Domain, under which the icon
	// is cached, and its host must be in allowed_hosts. It is asked for before
	// the fixed paths.
	Icon string `json:"icon,omitempty"`

	// Host is the board's API host where the ATS does not fix one: the
	// Workday tenant host "{tenant}.wd{N}.myworkdayjobs.com" (its tenant label
	// must match Token's tenant), the shared Workday host
	// "wd{N}.myworkdaysite.com" (WorkdaySiteHost; no tenant label, the tenant
	// is Token's), or an Eightfold or Jibe careers host such as
	// "apply.careers.microsoft.com" or "careers.amd.com".
	Host string `json:"host,omitempty"`
	// Search narrows a search-based listing (Workday's searchText, Jibe's
	// keywords, or the query on Amazon, Google, Apple and Eightfold). Searches
	// adds more terms; the board lists the union of every term's results, each
	// posting once.
	// Large boards need narrow terms because every listing is capped, and a
	// term over the cap fails the board rather than being truncated. A posting
	// that stops matching every term is, to Vani, gone from the board: it ages
	// out after two runs like any absent posting.
	Search   string   `json:"search,omitempty"`
	Searches []string `json:"searches,omitempty"`
	// Filters are a search site's own query filters, by parameter name, for
	// the systems that have verified ones (see FilterKeys). Each search term
	// is run with every filter applied.
	Filters map[string][]string `json:"filters,omitempty"`
	// MaxPostings sets the most postings the board may list, Workday, Oracle
	// HCM, Adzuna, Dice and Himalayas only: zero is the adapter's default
	// (workday.MaxPostings, 5,000; oraclehcm.MaxPostings, 5,000;
	// dice.MaxPostings, 1,000; himalayas.MaxPostings, 1,000; adzuna.MaxPostings,
	// 500), at most MaxPostingsCeiling (adzuna.MaxPostingsCeiling, 1,000, for Adzuna).
	// Listing costs a request per page at the per-host pace; detail requests on
	// Workday, Oracle HCM and Dice are bounded per run apart from it. A board
	// over its cap still fails rather than being truncated.
	MaxPostings int `json:"max_postings,omitempty"`

	// URL is https: a page board's careers listing page, an Avature board's
	// listing page on its Host (avature.ParseListURL), or the XML document of
	// a feed board. A feed board has no link_pattern: the document is the
	// listing, and nothing linked from it is fetched.
	URL string `json:"url,omitempty"`
	// LinkPattern is a Go regexp an absolute link on URL must match to count
	// as a posting page. Page boards only.
	LinkPattern string `json:"link_pattern,omitempty"`
	// NextPattern is an optional Go regexp an absolute same-host link on a
	// page board's listing page must match to be followed as a next listing
	// page (up to page.MaxListingPages). Page boards only.
	NextPattern string `json:"next_pattern,omitempty"`
	// ScrollSteps is how many scroll steps a page board runs on its listing
	// page to surface lazy-loaded or virtualized links (0..MaxPageScrollSteps).
	// Page boards only.
	ScrollSteps int `json:"scroll_steps,omitempty"`
	// WaitSelector is an optional CSS selector a page board waits for before
	// reading HTML on listing and posting pages. Page boards only.
	WaitSelector string `json:"wait_selector,omitempty"`
	// Extract declares optional CSS selectors for reading fields from a page
	// board's posting pages when JSON-LD and Microdata are absent. Page boards
	// only.
	Extract *PageExtract `json:"extract,omitempty"`
}

// PageExtract declares CSS selectors for reading posting fields on a page
// board when structured data is absent.
type PageExtract struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Company     string `json:"company,omitempty"`
}

// Bounds on a board's search terms, filters, and page-board options.
const (
	MaxSearches        = 10
	MaxSearchLen       = 100
	MaxFilterValues    = 10
	MaxPageScrollSteps = 10
	MaxSelectorLen     = 256
	// MaxPostingsCeiling bounds Board.MaxPostings. Workday stops counting one
	// query at 2,000 (checked 2026-10-19 on NVIDIA), and the adapter lists a
	// larger board by splitting it on its facets, which count past that; at
	// 15,000 (raised from 10,000 on 2026-11-11) a whole listing is about 1,600
	// requests, some 27 minutes at the per-host pace, and only for a board
	// whose max_postings asks for it.
	MaxPostingsCeiling = 15000
)

// FilterKeys lists, per system, the query filters verified to narrow its
// search (2026-10-19, by comparing result counts with and without each).
// A filter not listed here is refused rather than sent unverified.
var FilterKeys = map[posting.ATS][]string{
	posting.Google: {"target_level", "location"},
	posting.Amazon: {"normalized_country_code[]", "category[]"},
	posting.Apple:  {"location"},
	// "United States": Microsoft 330 -> 179, Qualcomm 359 -> 156.
	posting.Eightfold: {"location"},
	// AMD "machine learning": 163 -> 98 United States, 10 Canada, 28 Austin.
	// The site reads only the first of repeated values, so the source runs
	// one query per value.
	posting.Jibe: {"location"},
	// ServiceNow 732 -> 441 with country=us (2026-10-27). One lower-case ISO
	// code per query: "US" and a second value both answer zero postings.
	posting.SmartRecruiters: {"country"},
	// TikTok: "country" is an English country name from the site's own city
	// list, "location" a city code from it; both become location_code_list
	// (US: 4,318 -> 1,847 rows, 2026-10-28).
	posting.TikTok: {"country", "location"},
	// Himalayas "machine learning" (2026-11-17): 4,451 -> 2,328 with
	// country=US; -> 1,453 adding seniority=Senior; -> 2,072 adding
	// employment_type=Full Time. An unknown value is HTTP 400. A repeated
	// parameter is not a union (country=US&country=CA answered CA's 237, and a
	// comma list is a 400), so config allows one value per filter.
	posting.Himalayas: {"country", "seniority", "employment_type"},
	// ZipRecruiter "machine learning engineer" (2026-11-24): 845 US-wide ->
	// 547 with location "Austin, TX" -> 516 with radius_miles 5; 547 -> 29
	// with posted_within_days 3. Its total is an estimate that drifts (a
	// remote, full-time or senior filter raised it while every row matched
	// the filter), so the list filters were checked on the rows. Values and
	// their rules are ziprecruiter.CheckFilters'; the list is
	// ziprecruiter.FilterKeys (config holds the two equal).
	posting.ZipRecruiter: {"location", "country", "radius_miles", "posted_within_days", "location_type", "employment_type", "seniority"},
	// Adzuna's documented search parameters (OpenAPI spec, 2026-11-24), one
	// value each. NOT verified against live counts: no key was available.
	// Values and their rules are adzuna.CheckFilters'; the list is
	// adzuna.FilterKeys (config holds the two equal).
	// Dice "machine learning engineer" (2026-11-24): 2,330 -> 400 with
	// location "Austin, TX" -> 397 with radius 10 mi; 144 with posted within
	// 3 days; workplace Remote 347, Hybrid 77, both 422 (a union); FULLTIME
	// 2,147, with CONTRACTS 2,323 (a union); Direct Hire 1,805; willing to
	// sponsor 43. A location of "Remote" is ignored, so remote is a workplace
	// type. Values are dice.CheckFilters'; the list is dice.FilterKeys.
	posting.Dice:   {"location", "radius_miles", "posted_within_days", "workplace_type", "employment_type", "employer_type", "sponsorship"},
	posting.Adzuna: {"where", "distance_km", "max_days_old", "category", "salary_min", "full_time", "part_time", "permanent", "contract", "what_exclude", "company"},
}

// SearchTerms is the board's distinct search terms in order: Search, then
// Searches, trimmed, with case-insensitive duplicates and empty terms
// dropped. A board with none has no terms (Workday and Jibe then list
// everything).
func (b Board) SearchTerms() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range append([]string{b.Search}, b.Searches...) {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// KeyPrefix is the key prefix every posting from this board carries. It is
// the one definition shared by the adapters (which build keys) and the
// reconciler (which ages postings absent from a listing), so the two cannot
// disagree about which postings belong to a board.
func (b Board) KeyPrefix() string {
	token := strings.ToLower(strings.TrimSpace(b.Token))
	switch b.ATS {
	case posting.Workday, posting.OracleHCM:
		tenant, site, _ := strings.Cut(token, "/")
		return string(b.ATS) + ":" + tenant + ":" + site + ":"
	default:
		return string(b.ATS) + ":" + token + ":"
	}
}

// Source lists every currently published posting on a board. A board that
// answers with zero postings is a valid, non-error result; a board that
// cannot be read is an error, never an empty list.
type Source interface {
	ATS() posting.ATS
	List(ctx context.Context, b Board) ([]posting.Posting, error)
}
