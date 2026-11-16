package srcutil

import (
	"context"
	"errors"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// DetailStep is the loop every bounded-detail adapter runs over a DetailPlan:
// reuse the stored rows, request each row of Plan.Fetch in order, bound the
// requests that came back gone (MaxGone, and a board whose every request was
// gone with nothing reused), bound the ones that missed transiently
// (DetailMisses), and keep the postings in listing order. What differs between
// adapters (the request, what "gone" is, how a stored row is returned, what an
// accepted posting must also satisfy, and how errors read) stays in the
// adapter, behind these fields.
type DetailStep struct {
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
	// when no row can be gone. A gone row is left out, within the bound.
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
	misses := NewDetailMisses(d.Plan, d.CanDefer)
	allowed := MaxGone(len(d.Plan.Fetch))
	for n, i := range d.Plan.Fetch {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		p, err := d.Fetch(ctx, i)
		switch {
		case err != nil && d.Gone != nil && errors.Is(err, d.Gone):
			// Closed between the listing and its detail; leaving it out is the
			// truth. Many at once is the detail endpoint failing, not postings
			// closing, and would empty a healthy board.
			if res.Gone++; res.Gone > allowed || (res.Gone == len(d.Plan.Fetch) && res.Reused == 0) {
				return res, d.GoneErr(res.Gone, len(d.Plan.Fetch))
			}
			continue
		case err != nil:
			// A partial listing would age healthy postings as absent, so only a
			// transient miss the plan can absorb is left for a later run.
			if !misses.Take(ctx, n, err) {
				err = misses.Refusal(ctx, n, err)
				if d.FetchErr != nil {
					err = d.FetchErr(i, err)
				}
				return res, err
			}
			if n >= d.Plan.New {
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
	return res, err
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
