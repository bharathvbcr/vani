package workday

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// CheckSize asks Workday, with one first-page request per search term of b,
// whether List can list the board, and fails with srcutil.ErrTooManyPostings
// when a term alone counts more than the board's limit (capFor), or counts past Workday's
// 2000-row window with no facet that could split it or with one facet alone
// vouching for the count: List would fail that board on every run. It lists
// nothing and fetches no detail, so a caller can refuse to save a board List
// could never list. The terms' union is not counted (that needs the
// listings), so a board of several large terms can still fail in List.
//
// A reply it cannot read is an error, never a small count. The count is the
// board's own (expectedTotal): its total under the window, else its facets,
// which count past the window.
func CheckSize(ctx context.Context, d egress.Doer, b sources.Board) error {
	if err := srcutil.CheckBoard(b, posting.Workday); err != nil {
		return err
	}
	tenant, site, err := parseToken(b.Token)
	if err != nil {
		return fmt.Errorf("workday %q: %w", b.Token, err)
	}
	host, err := checkHost(b.Host, tenant)
	if err != nil {
		return fmt.Errorf("workday %q: %w", b.Token, err)
	}
	limit, err := capFor(b)
	if err != nil {
		return fmt.Errorf("workday %q: %w", b.Token, err)
	}
	w := newWalker(&source{d: d, now: time.Now}, "https://"+host+"/wday/cxs/"+url.PathEscape(tenant)+"/"+url.PathEscape(site)+"/jobs", limit)
	terms := b.SearchTerms()
	if len(terms) == 0 {
		terms = []string{""}
	}
	none := map[string][]string{}
	for _, term := range terms {
		w.term = term
		first, err := w.page(ctx, none, 0)
		if err != nil {
			return fmt.Errorf("workday %q: search %q: %w", b.Token, term, err)
		}
		count, corroborated := expectedTotal(first.total, first.facets, none)
		_, splits := chooseSplit(first.facets, none)
		switch {
		case count > limit:
			return fmt.Errorf("workday %q: search %q: %w: it lists %d postings, over the %d limit (max_postings)",
				b.Token, term, srcutil.ErrTooManyPostings, count, limit)
		case count >= window && (!corroborated || !splits):
			return fmt.Errorf("workday %q: search %q: %w: it lists %d or more postings, past Workday's %d-row window, and its facets cannot split it",
				b.Token, term, srcutil.ErrTooManyPostings, count, window)
		}
	}
	return nil
}
