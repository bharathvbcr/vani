package srcutil

import (
	"errors"
	"fmt"
)

// ErrTotalBelowRows is a listing whose reported total is below the rows its
// own first page served. A page cannot list more jobs than the search counts,
// so the total cannot be believed, and a walk that stopped at it would end
// early and leave every stored posting not yet listed to age as absent. The
// board fails instead. Adapters wrap it with their own shape error.
var ErrTotalBelowRows = errors.New("the listing's total is below the rows it served")

// TotalCoversRows is the one rule every paged adapter applies to the total a
// first page reports: it must be a count (not negative) and at least the
// distinct rows that page served. A failure is ErrTotalBelowRows. A total
// above the rows served is not judged here: it is the walk's to reach, ending
// on a short or empty page, or failing at the page cap.
func TotalCoversRows(total, served int) error {
	if total < 0 || total < served {
		return fmt.Errorf("%w: it counts %d postings and its first page lists %d", ErrTotalBelowRows, total, served)
	}
	return nil
}
