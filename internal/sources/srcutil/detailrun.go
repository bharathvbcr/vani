package srcutil

import (
	"context"
	"errors"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// heldWhy is the reason a listing reports when it leaves out stored postings
// whose unusable text was not re-read (Rows.Hold).
const heldWhy = "stored posting text unreadable and not re-read this run; held for a later run"

// ErrCannotHold is a detail step that must leave out stored postings it could
// not re-read (unusable text past the request limit) with no BadRowSink to
// report them as a shortfall: listing them unread would store an empty text,
// and omitting them quietly would age them as absent.
var ErrCannotHold = errors.New("stored postings with unreadable text could not be re-read within the detail limit, and the caller cannot hold a short listing")

// DetailStep is the loop every bounded-detail adapter runs over a DetailPlan:
// reuse the stored rows, request each row of Plan.Fetch in order, bound the
// requests that came back gone (MaxGone, and a board whose every request was
// gone with nothing reused), bound the ones that missed transiently
// (DetailMisses), and keep the postings in listing order. What differs between
// adapters (the request, what "gone" is, how a stored row is returned, what an
// accepted posting must also satisfy, and how errors read) stays in the
// adapter, behind these fields.
type DetailStep struct {
	// Board is the board being listed: where the step reports the stored
	// postings it holds (Rows.Hold).
	Board sources.Board
	// Rows is how many rows were listed; Plan indexes into them.
	Rows int
	Plan DetailPlan
	// CanDefer is whether a new row may be left out when its request misses
	// (see DetailMisses).
	CanDefer bool

	// Fetch requests row i's detail and builds its posting. A nil posting with
	// a nil error is an answered row the board does not list (Skipped).
	Fetch func(ctx context.Context, i int) (*posting.Posting, error)
	// Reuse returns the stored posting of row i, ready to list (seen time,
	// relocated place). It is called for every Plan.Reuse row and for a stored
	// row whose request missed; the adapter counts what it needs inside it.
	Reuse func(i int) posting.Posting

	// Gone is the error Fetch returns for a row closed since the listing. Nil
	// when no row can be gone. A gone new row is left out; a gone stored row is kept (returned as stored, or held when its text is unusable) and counted Unanswered, since the listing still names it. Both are bounded by MaxGone.
	Gone error
	// GoneErr is the board's error when more rows are gone than MaxGone allows,
	// or all of them are with nothing reused. Required with Gone.
	GoneErr func(gone, fetch int) error
	// FetchErr words the error for a failed request of row i that fails the
	// board. Nil leaves the error as it is. It receives, when the miss bound
	// was what refused, an error wrapping ErrTooManyMisses.
	FetchErr func(i int, err error) error
	// DoneErr words the error of a step whose requests were all missed or
	// gone, with nothing reused (ErrNothingAnswered). Nil leaves it as it is.
	DoneErr func(err error) error
	// LeftOut, when set, is asked before a missed new row is left out; an
	// error fails the board. It is for a caller that knows the row may be one
	// whose posting is stored, which leaving out would age as absent.
	LeftOut func(i int, err error) error
	// Accepted, when set, sees each posting a request returned, after it is
	// counted; an error fails the board.
	Accepted func(n, i int, p posting.Posting) error
}

// DetailResult is what a DetailStep did. It is returned with an error too, so a
// caller's report counts what ran before the failure.
type DetailResult struct {
	// Slots holds each row's posting, nil for a row that is not listed.
	Slots []*posting.Posting
	// Reused rows were returned as stored with no request. Fetched rows were
	// new and answered; Refreshed ones were stored and re-read; Skipped ones
	// answered but are not listed; Gone ones were closed since the listing.
	Reused, Fetched, Refreshed, Skipped, Gone int
	// Unanswered rows missed transiently; LeftOut of them were new rows
	// (not listed), the rest stored rows returned as stored.
	Unanswered, LeftOut int
	// Held unusable stored rows were not listed, being past the request limit
	// (Plan.Held) or missed on their re-read. They were reported to the
	// BadRowSink as a shortfall, so the listing reads as short and their stored
	// postings are kept, not aged.
	Held int
}

// Run does the step. ctx is checked before every request. The error is the
// board's failure: ctx's, GoneErr's, FetchErr's, LeftOut's or Accepted's, or
// the miss bound's (ErrNothingAnswered, from DetailMisses.Done).
func (d DetailStep) Run(ctx context.Context) (DetailResult, error) {
	res := DetailResult{Slots: make([]*posting.Posting, d.Rows)}
	reuse := func(i int) {
		p := d.Reuse(i)
		res.Slots[i] = &p
	}
	for _, i := range d.Plan.Reuse {
		reuse(i)
		res.Reused++
	}
	bad := NewRows(ctx, d.Board, d.Rows)
	if len(d.Plan.Held) > 0 && !bad.CanHold() {
		return res, fmt.Errorf("%w (%d held)", ErrCannotHold, len(d.Plan.Held))
	}
	res.Held = len(d.Plan.Held)
	misses := NewDetailMisses(d.Plan, d.CanDefer)
	misses.canHold = bad.CanHold()
	allowed := MaxGone(len(d.Plan.Fetch))
	for n, i := range d.Plan.Fetch {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		p, err := d.Fetch(ctx, i)
		switch {
		case err != nil && d.Gone != nil && errors.Is(err, d.Gone):
			// Many gone at once is the detail endpoint failing, not postings
			// closing, and would empty a healthy board; stored rows count
			// toward that bound too, and are reported as Gone as well.
			if res.Gone++; res.Gone > allowed || (res.Gone == len(d.Plan.Fetch) && res.Reused == 0) {
				return res, d.GoneErr(res.Gone, len(d.Plan.Fetch))
			}
			if n < d.Plan.New {
				// A new row closed between the listing and its detail; it is
				// not stored, so leaving it out ages nothing.
				continue
			}
			// A stored row the listing still names is not proven closed by one
			// 404 (a CDN blip answers the same): it is kept as a miss, not left
			// out, which would age it as absent. A closed job leaves the listing
			// and ages out then.
			if d.Plan.unusableAt(n) {
				if !misses.canHold {
					return res, d.fetchErr(i, fmt.Errorf("%w (a stored row with unreadable text answered gone)", ErrCannotHold))
				}
				res.Held++
			} else {
				reuse(i)
			}
			res.Unanswered++
			continue
		case err != nil:
			// A partial listing would age healthy postings as absent, so only a
			// transient miss the plan can absorb is left for a later run.
			if !misses.Take(ctx, n, err) {
				return res, d.fetchErr(i, misses.Refusal(ctx, n, err))
			}
			if d.Plan.unusableAt(n) {
				res.Held++
			} else if n >= d.Plan.New {
				reuse(i)
			} else {
				res.LeftOut++
				if d.LeftOut != nil {
					if err := d.LeftOut(i, err); err != nil {
						return res, err
					}
				}
			}
			res.Unanswered++
			continue
		case p == nil:
			res.Skipped++
			continue
		case n >= d.Plan.New:
			res.Refreshed++
		default:
			res.Fetched++
		}
		if d.Accepted != nil {
			if err := d.Accepted(n, i, *p); err != nil {
				return res, err
			}
		}
		res.Slots[i] = p
	}
	err := misses.Done(res.Fetched + res.Refreshed + res.Skipped)
	if err != nil && d.DoneErr != nil {
		err = d.DoneErr(err)
	}
	if err == nil {
		// Reported last, so a step that fails reports nothing (Rows.Done).
		bad.Hold(res.Held, heldWhy)
		err = bad.Done()
	}
	return res, err
}

// fetchErr words err, the failure of row i's request, as the adapter's FetchErr.
func (d DetailStep) fetchErr(i int, err error) error {
	if d.FetchErr != nil {
		return d.FetchErr(i, err)
	}
	return err
}

// Postings is the listed postings of slots, in order.
func Postings(slots []*posting.Posting) []posting.Posting {
	out := make([]posting.Posting, 0, len(slots))
	for _, p := range slots {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// UniquePostings is Postings keeping the first posting of each key, for a
// source whose rows may name one posting twice.
func UniquePostings(slots []*posting.Posting) []posting.Posting {
	out := make([]posting.Posting, 0, len(slots))
	seen := make(map[posting.Key]bool, len(slots))
	for _, p := range slots {
		if p == nil || seen[p.Key] {
			continue
		}
		seen[p.Key] = true
		out = append(out, *p)
	}
	return out
}
