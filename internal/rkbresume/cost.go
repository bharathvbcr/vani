package rkbresume

import (
	"context"
	"errors"
	"sync"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

// nanoPerUSD converts budget's nano-USD to dollars.
const nanoPerUSD = 1e9

// costMeter sums what a build's model turns cost. Every turn that reached the
// provider counts in turns; one whose usage never arrived (a timeout, a
// cancel, a dropped stream) or that the provider cannot price counts as
// unknown, and makes the total a lower bound (Cost.Complete false). A turn the
// budget refused before anything was sent is not counted at all.
type costMeter struct {
	mu      sync.Mutex
	nano    int64
	turns   int
	unknown int
	model   string
}

func (c *costMeter) sent(model string) {
	c.mu.Lock()
	c.turns++
	c.model = model
	c.mu.Unlock()
}

func (c *costMeter) billed(nano int64) {
	c.mu.Lock()
	c.nano += nano
	c.mu.Unlock()
}

func (c *costMeter) unbilled() {
	c.mu.Lock()
	c.unknown++
	c.mu.Unlock()
}

// snapshot is the total so far and the model the turns were sent to ("" when
// none was sent).
func (c *costMeter) snapshot() (string, Cost) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model, Cost{USD: float64(c.nano) / nanoPerUSD, Complete: c.unknown == 0, Turns: c.turns}
}

// stamp puts the meter's totals on a Result.
func (c *costMeter) stamp(r *Result) {
	r.Model, r.Cost = c.snapshot()
}

// meter wraps p so each turn it carries is accounted in c. Prices come from p
// itself when it publishes them (budget.Pricer, as the metered provider the
// caller builds does); without a price the turn is unknown, not free.
func (c *costMeter) meter(p llm.Provider) llm.Provider {
	pr, _ := p.(budget.Pricer)
	return &meteredProvider{Provider: p, c: c, pricer: pr}
}

type meteredProvider struct {
	llm.Provider
	c      *costMeter
	pricer budget.Pricer
}

func (m *meteredProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	st, err := m.Provider.Stream(ctx, req)
	if err != nil {
		if errors.Is(err, budget.ErrExhausted) {
			return nil, err // refused before anything was sent
		}
		m.c.sent(req.Model)
		m.c.unbilled()
		return nil, err
	}
	m.c.sent(req.Model)
	return &meteredStream{Stream: st, m: m, model: req.Model}, nil
}

// meteredStream accounts its turn exactly once: when the response arrives with
// usage, or, failing that, when the stream is closed.
type meteredStream struct {
	llm.Stream
	m     *meteredProvider
	model string
	once  sync.Once
}

func (s *meteredStream) settle(u llm.Usage, ok bool) {
	s.once.Do(func() {
		if ok && s.m.pricer != nil {
			if prices, found := s.m.pricer.PriceOf(s.model); found {
				if nano, err := prices.Charge(u); err == nil {
					s.m.c.billed(nano)
					return
				}
			}
		}
		s.m.c.unbilled()
	})
}

func (s *meteredStream) Response() (llm.Response, error) {
	resp, err := s.Stream.Response()
	s.settle(resp.Usage, err == nil)
	return resp, err
}

func (s *meteredStream) Close() error {
	s.settle(llm.Usage{}, false)
	return s.Stream.Close()
}
