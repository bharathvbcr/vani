// Package sources turns a watchlist entry into postings. Each ATS has its own
// adapter; all of them reach the network only through an egress.Doer.
package sources

import (
	"context"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Board is one watchlist entry: a company's job board on one ATS, or its own
// careers page.
type Board struct {
	ATS posting.ATS `json:"ats"`
	// Token is the board identifier: Greenhouse board token, Lever company
	// slug, Ashby job-board name, Workday "tenant/site", Eightfold company
	// domain, Jibe client code ("amd"), or a short slug you choose for a
	// careers page.
	Token string `json:"token"`
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
	// must match Token's tenant), or an Eightfold or Jibe careers host such as
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
	// MaxPostings sets the most postings the board may list, Workday only:
	// zero is the adapter's default (workday.MaxPostings, 5,000), at most
	// MaxPostingsCeiling. Listing costs a request per 20 postings at the
	// per-host pace (about a second each); detail requests are bounded per
	// run apart from it (workday.MaxDetails). A board over its cap still
	// fails rather than being truncated.
	MaxPostings int `json:"max_postings,omitempty"`

	// URL is a careers listing page (https). Page boards only.
	URL string `json:"url,omitempty"`
	// LinkPattern is a Go regexp an absolute link on URL must match to count
	// as a posting page. Page boards only.
	LinkPattern string `json:"link_pattern,omitempty"`
}

// Bounds on a board's search terms and filters.
const (
	MaxSearches     = 10
	MaxSearchLen    = 100
	MaxFilterValues = 10
	// MaxPostingsCeiling bounds Board.MaxPostings. Workday stops counting one
	// query at 2,000 (checked 2026-10-19 on NVIDIA), and the adapter lists a
	// larger board by splitting it on its facets, which count past that; at
	// 10,000 a whole listing is at most about 600 requests, ten minutes at the
	// per-host pace.
	MaxPostingsCeiling = 10000
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
	case posting.Workday:
		tenant, site, _ := strings.Cut(token, "/")
		return string(posting.Workday) + ":" + tenant + ":" + site + ":"
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
