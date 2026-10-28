package oraclehcm

import (
	"context"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// listPage is one listing answer as read: the search's own count of its jobs
// and this page's rows.
type listPage struct {
	total int
	rows  []row
}

// walker lists a board's rows into rows, in listing order, each requisition
// once across every search term.
type walker struct {
	s    *source
	t    target
	seen map[string]bool
	rows []row
	// deleted counts the deleted-requisition markers listed (tombstoneRE).
	deleted int
	// requests counts the listing requests of every term; maxRequests bounds
	// them: every term's pages up to the board's limit, plus the first page.
	requests    int
	maxRequests int
}

func newWalker(s *source, t target) *walker {
	return &walker{s: s, t: t, seen: map[string]bool{}, maxRequests: len(t.terms) * (t.limit/pageSize + 3)}
}

// page reads one listing page of term at offset and checks its shape: one
// search item, a count, the rows array (a search with no match answers an
// empty one; a reply without it is an error page or a new shape, never an
// empty board), at most pageSize rows, each with a requisition number.
func (w *walker) page(ctx context.Context, term string, offset int) (listPage, error) {
	if err := ctx.Err(); err != nil {
		return listPage{}, err
	}
	if w.requests >= w.maxRequests {
		return listPage{}, fmt.Errorf("%w: the listing needs more than %d requests; set the board's search to narrow it",
			srcutil.ErrTooManyPostings, w.maxRequests)
	}
	w.requests++
	var resp listResponse
	if err := srcutil.GetJSONLimit(ctx, w.s.d, listURL(w.t, term, offset), maxListBody, &resp); err != nil {
		return listPage{}, err
	}
	if len(resp.Items) != 1 {
		return listPage{}, fmt.Errorf("%w: listing at offset %d has %d search items, want 1", errShape, offset, len(resp.Items))
	}
	it := resp.Items[0]
	switch {
	case it.TotalJobsCount == nil || *it.TotalJobsCount < 0:
		return listPage{}, fmt.Errorf("%w: listing at offset %d has no usable TotalJobsCount", errShape, offset)
	case it.RequisitionList == nil:
		return listPage{}, fmt.Errorf("%w: listing at offset %d has no requisitionList array", errShape, offset)
	case len(it.RequisitionList) > pageSize:
		return listPage{}, fmt.Errorf("%w: listing at offset %d has %d rows, over the page size %d", errShape, offset, len(it.RequisitionList), pageSize)
	}
	for _, r := range it.RequisitionList {
		// A row that cannot be fetched or keyed cannot be accounted for, and
		// leaving it out would age its posting as absent. The id also reaches
		// the detail finder, so only digits pass.
		if id := r.ID.String(); !idRE.MatchString(id) && !tombstoneRE.MatchString(id) {
			return listPage{}, fmt.Errorf("%w: listing at offset %d has a requisition id %q that is neither a number nor a deleted marker", errShape, offset, id)
		}
	}
	return listPage{total: *it.TotalJobsCount, rows: it.RequisitionList}, nil
}

// add records rows new to this term into here and new to the board into
// w.rows, and returns how many were new to this term. A deleted requisition's
// marker is counted in here (the search's total counts it) and nowhere else.
func (w *walker) add(rows []row, here map[string]bool) (int, error) {
	n := 0
	for _, r := range rows {
		id := r.ID.String()
		if here[id] {
			continue
		}
		here[id] = true
		n++
		if tombstoneRE.MatchString(id) {
			if !w.seen[id] {
				w.seen[id] = true
				w.deleted++
			}
			continue
		}
		if w.seen[id] {
			continue
		}
		if len(w.rows) >= w.t.limit {
			return n, fmt.Errorf("%w: board lists more than %d postings; set the board's search to narrow it or raise max_postings",
				srcutil.ErrTooManyPostings, w.t.limit)
		}
		w.seen[id] = true
		w.rows = append(w.rows, r)
	}
	return n, nil
}

// firstPage reads the first page of term and holds its total to its rows: a
// page cannot list more jobs than the search counts (workday's pagedTotal
// rule), so a count under the rows cannot be believed, and it is the count the
// paging stops at. Without this check "TotalJobsCount":0 beside 200 rows would
// read as a board that is one page long.
func (w *walker) firstPage(ctx context.Context, term string) (listPage, error) {
	first, err := w.page(ctx, term, 0)
	if err != nil {
		return listPage{}, err
	}
	distinct := map[string]bool{}
	for _, r := range first.rows {
		distinct[r.ID.String()] = true
	}
	if first.total < len(distinct) {
		return listPage{}, fmt.Errorf("%w: the search counts %d postings but its first page lists %d", errShape, first.total, len(distinct))
	}
	return first, nil
}

// walk lists every row of term's search ("" is the whole board). The total is
// read from the first page only: it drifts by one while a board changes, and a
// stop that waited on a moving count could not be told from a stuck cursor.
// Paging goes by offset to that total, an empty page also ending it: a short
// page does not (a counted requisition can be missing from a page far from the
// end). A page with nothing new is a stuck cursor. The rows found are then
// held to the total less shortfall, so a listing that lost rows fails the board
// instead of aging its postings.
func (w *walker) walk(ctx context.Context, term string) error {
	first, err := w.firstPage(ctx, term)
	if err != nil {
		return err
	}
	if first.total > w.t.limit {
		return fmt.Errorf("%w: the search lists %d postings, over the %d limit; set the board's search to narrow it or raise max_postings",
			srcutil.ErrTooManyPostings, first.total, w.t.limit)
	}
	if first.total > 0 && len(first.rows) == 0 {
		// A page cannot list jobs a board counts none of the other way round.
		return fmt.Errorf("%w: the search counts %d postings but its first page lists none", errIncomplete, first.total)
	}
	here := map[string]bool{}
	if _, err := w.add(first.rows, here); err != nil {
		return err
	}
	for offset := pageSize; offset < first.total; offset += pageSize {
		p, err := w.page(ctx, term, offset)
		if err != nil {
			return err
		}
		if len(p.rows) == 0 {
			break
		}
		n, err := w.add(p.rows, here)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: the board repeated a page at offset %d, after %d jobs", errIncomplete, offset, len(here))
		}
	}
	if len(here) < first.total-shortfall(first.total) {
		return fmt.Errorf("%w: the board listed %d of the %d jobs it counts", errIncomplete, len(here), first.total)
	}
	return nil
}
