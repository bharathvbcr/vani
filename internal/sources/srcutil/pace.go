package srcutil

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrPaceBudget is a board whose paced requests would wait longer in all than
// its pacer allows.
var ErrPaceBudget = errors.New("sources: board's request spacing is over its time budget")

// Sleep waits d, or until ctx ends with its error.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pacer spaces one board's requests, which an adapter makes in sequence:
// Wait returns at once the first time and waits Every before each later
// request. The waits of one board together may not pass Budget: the Wait that
// would is ErrPaceBudget at once, without waiting, so a board too large to
// list at its site's pace fails loudly instead of holding the run. A Pacer is
// for one List call and one goroutine.
type Pacer struct {
	// Every is the gap before each request after the first: a site's
	// robots.txt Crawl-delay, or the spacing its rate limit was seen to need.
	Every time.Duration
	// Budget bounds the board's waits in all; zero or less means Every
	// times MaxPacedRequests.
	Budget time.Duration
	// Sleep waits between requests; nil is the real Sleep. A field only so
	// tests need not wait in real time.
	Sleep func(ctx context.Context, d time.Duration) error

	sent   int
	waited time.Duration
}

// MaxPacedRequests is the request count a zero Budget allows for.
const MaxPacedRequests = 300

// Wait spaces the next request; call it before every request of the board.
func (p *Pacer) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.sent == 0 || p.Every <= 0 {
		p.sent++
		return nil
	}
	budget := p.Budget
	if budget <= 0 {
		budget = p.Every * MaxPacedRequests
	}
	if p.waited+p.Every > budget {
		return fmt.Errorf("%w: %d requests spaced %s exceed %s", ErrPaceBudget, p.sent+1, p.Every, budget)
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = Sleep
	}
	if err := sleep(ctx, p.Every); err != nil {
		return err
	}
	p.waited += p.Every
	p.sent++
	return nil
}
