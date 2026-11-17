package srcutil

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/sources"
)

// Bad rows. A listing is a list of rows an adapter turns into postings, and
// one row that cannot become a posting (an empty title, a URL with a bad
// scheme, a date that does not parse) used to fail the whole board, so a board
// with one such row went unread, and its stored postings unreconciled, until
// the ATS fixed the row.
//
// A caller that can fold a short listing (the run: a listing that is short of
// what its board holds reconciles as a short one, whose stored postings it
// leaves out are held, not aged) installs a BadRowSink on the context. An
// adapter then skips a bad row, counts it, and the sink receives the count to
// report as the listing's shortfall. Without a sink an adapter fails on the
// first bad row as it always did: a caller that cannot hold what the listing
// leaves out must not be handed a listing that quietly leaves it out.
//
// More than a tenth of the rows bad (MaxGone, at least one) is the payload
// changing shape, not a few bad rows, and fails the board as before.

type badRowsKey struct{}

// BadRowSink receives, once per successful listing of board b, how many rows
// the adapter skipped and the first skipped row's error. The error text may
// hold the row's own content, which is untrusted: a sink that logs it bounds
// it and keeps it to one line.
type BadRowSink func(b sources.Board, skipped int, first string)

// WithBadRowSink returns ctx carrying sink. A nil sink is no sink.
func WithBadRowSink(ctx context.Context, sink BadRowSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, badRowsKey{}, sink)
}

// Rows tracks the rows one listing skipped. It belongs to one List call and is
// not safe for concurrent use.
type Rows struct {
	sink    BadRowSink
	count   *listingCounter // the listing's counter (CountBadRows), or nil
	b       sources.Board
	total   int
	prior   int // rows an earlier stage of this listing skipped; counted against the bound, not reported
	skipped int
	held    int
	first   string
}

// NewRows starts counting for a listing of total rows on b. A paged listing
// that does not know its total yet passes 0 and calls Add as each page lands.
func NewRows(ctx context.Context, b sources.Board, total int) *Rows {
	sink, _ := ctx.Value(badRowsKey{}).(BadRowSink)
	count, _ := ctx.Value(countKey{}).(*listingCounter)
	if count != nil {
		count.used.Store(1)
		count.total.Add(int64(max(total, 0)))
	}
	return &Rows{sink: sink, count: count, b: b, total: total}
}

// NewRowsAfter starts counting for a listing of total rows on b, of which an
// earlier stage already skipped already. The bound is on both together, so two
// stages that each stay inside it cannot together leave out more than it
// allows; Done reports only the rows this Rows skipped, so the sum a sink
// receives across the stages stays exact.
func NewRowsAfter(ctx context.Context, b sources.Board, total, already int) *Rows {
	r := NewRows(ctx, b, total)
	r.prior = max(already, 0)
	return r
}

// ListingCount is what every Rows an adapter started during one List call saw:
// the rows it counted (NewRows and Add) and the rows it skipped. Used says
// whether the adapter used Rows at all; 11 adapters do not, and their counts
// are zero.
type ListingCount struct {
	Total, Skipped int
	Held           int // stored postings the listing left out on purpose (Rows.Hold); not bad rows, never counted toward the bound
	Used           bool
}

type listingCounter struct{ total, skipped, held, used atomic.Int64 }

type countKey struct{}

// CountBadRows returns ctx carrying a per-listing counter, and the function
// that reads it. Every Rows created under the returned ctx adds its rows and
// skips to the counter as it counts, whether or not a BadRowSink is installed
// or ever called, so the caller can bound what a paged adapter (a Rows per
// page) left out across all of its pages. The sink is untouched: each Rows
// still reports only its own skips, so the sums a sink receives stay exact.
// The counter belongs to this ctx: another List call has its own.
func CountBadRows(ctx context.Context) (context.Context, func() ListingCount) {
	c := &listingCounter{}
	return context.WithValue(ctx, countKey{}, c), func() ListingCount {
		return ListingCount{Total: int(c.total.Load()), Skipped: int(c.skipped.Load()), Held: int(c.held.Load()), Used: c.used.Load() > 0}
	}
}

// Add counts n more rows of the listing.
func (r *Rows) Add(n int) {
	r.total += n
	if r.count != nil {
		r.count.total.Add(int64(n))
	}
}

// Skipped is how many rows Bad has skipped so far. An adapter whose board
// counts its own rows adds it to the postings it kept, so a skipped row still
// counts as a row the board sent.
func (r *Rows) Skipped() int { return r.skipped }

// Bad is called with the error that made one row unusable. A nil return means
// the row is skipped and counted: the caller moves on to the next row. A
// non-nil return is the error to fail the board with, because no sink is
// installed or the skipped rows are already over the bound; it is err itself
// when there is no sink, so the failure reads as it always did.
func (r *Rows) Bad(err error) error {
	if r.sink == nil {
		return err
	}
	r.skipped++
	if r.count != nil {
		r.count.skipped.Add(1)
	}
	if r.first == "" || r.skipped == 1 {
		r.first = err.Error() // a bad row's error outranks a hold's constant reason
	}
	if r.skipped+r.prior > MaxGone(r.total) {
		return r.over(err)
	}
	return nil
}

// CanHold is whether a caller that can hold what a listing leaves out installed
// a sink, so Hold and Bad can report a shortfall rather than fail the board.
func (r *Rows) CanHold() bool { return r.sink != nil }

// Hold counts n stored postings the listing leaves out on purpose and must not
// age (their re-read was not made or did not answer), so the listing reads as
// short: the caller holds them. Unlike Bad it has no bound of its own: how many
// may be held is the caller's (the detail step's miss bound and request cap), and
// held rows do not count toward the bad-row bound. why is the one-line reason the
// sink receives when no row was skipped by Bad; it is constant text, never row
// content. Without a sink it holds nothing and returns false, and the caller must
// fail the board.
func (r *Rows) Hold(n int, why string) bool {
	if r.sink == nil {
		return false
	}
	if n > 0 {
		r.held += n
		if r.count != nil {
			r.count.held.Add(int64(n))
		}
		if r.first == "" {
			r.first = why
		}
	}
	return true
}

func (r *Rows) over(err error) error {
	return fmt.Errorf("%w (%d bad rows of %d, over the %d the board may leave out)", err, r.skipped+r.prior, r.total, MaxGone(r.total))
}

// Done ends the listing. It returns an error when the rows skipped turn out to
// be over the bound (a paged listing learns its total only at the end), and
// otherwise reports the shortfall to the sink. Call it just before returning
// the postings, so a board that then fails reports nothing.
func (r *Rows) Done() error {
	if r.sink == nil {
		return nil
	}
	if r.skipped+r.prior > MaxGone(r.total) {
		return r.over(fmt.Errorf("rows could not be read: %s", r.first))
	}
	if r.skipped+r.held == 0 {
		return nil
	}
	r.sink(r.b, r.skipped+r.held, r.first)
	return nil
}
