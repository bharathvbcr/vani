package oraclehcm

import (
	"context"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// CheckSize asks the board's host, with one first-page request per search term
// of b, whether List can list it, and fails with srcutil.ErrTooManyPostings
// when a term alone counts more than the board's limit (capFor): List would
// fail that board on every run. It lists nothing and fetches no detail, so a
// caller can refuse to save a board List could never list. The terms' union is
// not counted (that needs the listings), so a board of several large terms can
// still fail in List. A reply it cannot read is an error, never a small count.
func CheckSize(ctx context.Context, d egress.Doer, b sources.Board) error {
	t, err := resolve(b)
	if err != nil {
		return err
	}
	w := newWalker(&source{d: d}, t)
	for _, term := range t.terms {
		first, err := w.firstPage(ctx, term)
		if err != nil {
			return fmt.Errorf("oraclehcm %q: search %q: %w", b.Token, term, err)
		}
		if first.total > t.limit {
			return fmt.Errorf("oraclehcm %q: search %q: %w: it lists %d postings, over the %d limit (max_postings)",
				b.Token, term, srcutil.ErrTooManyPostings, first.total, t.limit)
		}
	}
	return nil
}
