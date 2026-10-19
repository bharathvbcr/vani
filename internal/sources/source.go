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
	// slug, Ashby job-board name, Workday "tenant/site", or a short slug you
	// choose for a careers page.
	Token string `json:"token"`
	// Company is the display name; adapters fall back to it when the ATS
	// payload carries none.
	Company string `json:"company"`
	// Region selects a regional API host where one exists ("eu" for Lever).
	Region string `json:"region,omitempty"`

	// Host is the Workday tenant host, "{tenant}.wd{N}.myworkdayjobs.com".
	// Workday only; the tenant label must match Token's tenant.
	Host string `json:"host,omitempty"`
	// Search narrows a Workday listing (the board's searchText). Large
	// Workday boards need it, because the listing is capped.
	Search string `json:"search,omitempty"`

	// URL is a careers listing page (https). Page boards only.
	URL string `json:"url,omitempty"`
	// LinkPattern is a Go regexp an absolute link on URL must match to count
	// as a posting page. Page boards only.
	LinkPattern string `json:"link_pattern,omitempty"`
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
