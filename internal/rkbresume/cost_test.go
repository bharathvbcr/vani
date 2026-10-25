package rkbresume

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

// call scripts one provider call: what it answers, what usage it reports, or how
// it fails. block waits for the turn's context to end.
type call struct {
	text  string
	usage llm.Usage
	err   error
	block bool
	// noUsage ends the stream without a usage on its response.
	noUsage bool
}

// scriptedProvider is a priced provider that plays calls in order (the last
// repeats) and counts the calls that reached it.
type scriptedProvider struct {
	calls  []call
	prices budget.Prices
	n      int
}

var testRates = budget.Prices{Rates: budget.Rates{Input: 1000, Output: 5000, CacheWrite: 1250, CacheRead: 100}}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "scripted", Model: model, MaxOutputTokens: 8192}, true
}

func (p *scriptedProvider) PriceOf(string) (budget.Prices, bool) { return p.prices, true }

func (p *scriptedProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	i := min(p.n, len(p.calls)-1)
	p.n++
	c := p.calls[i]
	if c.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.err != nil {
		return nil, c.err
	}
	return &scriptedStream{c: c}, nil
}

type scriptedStream struct {
	c    call
	done bool
}

func (s *scriptedStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.c.text}, nil
	}
	return llm.Chunk{}, io.EOF
}

func (s *scriptedStream) Response() (llm.Response, error) {
	r := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.c.text}}}, StopReason: llm.StopEndTurn}
	if !s.c.noUsage {
		r.Usage = s.c.usage
	}
	return r, nil
}

func (s *scriptedStream) Close() error { return nil }

func nanoOf(in, out int) float64 { return float64(int64(in)*1000+int64(out)*5000) / 1e9 }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func shrinkTurn(t *testing.T) {
	t.Helper()
	old := turnTimeout
	turnTimeout = 80 * time.Millisecond
	t.Cleanup(func() { turnTimeout = old })
}

// Two billed turns are summed, each at its own usage.
func TestCostSumsEveryTurn(t *testing.T) {
	f := newFixture(t)
	sp := &scriptedProvider{prices: testRates, calls: []call{
		{text: "not a choice", usage: llm.Usage{InputTokens: 1000, OutputTokens: 200}},
		{text: validReply(), usage: llm.Usage{InputTokens: 2000, OutputTokens: 300}},
	}}
	f.cfg.Provider = sp
	res := mustBuild(t, f)
	if res.Source != SourceTailored || sp.n != 2 {
		t.Fatalf("scenario: source %s, %d calls", res.Source, sp.n)
	}
	want := nanoOf(1000, 200) + nanoOf(2000, 300)
	if !near(res.Cost.USD, want) || !res.Cost.Complete || res.Cost.Turns != 2 || res.Model != "m" {
		t.Errorf("cost = %+v model %q, want %.9f over 2 complete turns on m", res.Cost, res.Model, want)
	}
}

// A turn that timed out was sent and paid for by an amount nobody knows: the
// total is the first turn's, and says it is a lower bound.
func TestCostSecondTurnTimingOutIsIncompleteNotZero(t *testing.T) {
	shrinkTurn(t)
	f := newFixture(t)
	sp := &scriptedProvider{prices: testRates, calls: []call{
		{text: "not a choice", usage: llm.Usage{InputTokens: 1000, OutputTokens: 200}},
		{block: true},
	}}
	f.cfg.Provider = sp
	res := mustBuild(t, f)
	if res.Source != SourceBase {
		t.Fatalf("source = %s (%s)", res.Source, res.Reason)
	}
	if !near(res.Cost.USD, nanoOf(1000, 200)) || res.Cost.Complete || res.Cost.Turns != 2 {
		t.Errorf("cost = %+v, want the first turn's %.9f, incomplete, 2 turns", res.Cost, nanoOf(1000, 200))
	}
}

// Nothing sent is nothing counted, and complete: no provider, and a budget that
// refuses before sending.
func TestCostCountsNoTurnThatWasNeverSent(t *testing.T) {
	t.Run("no provider", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider, f.cfg.NoModel = nil, "off"
		res := mustBuild(t, f)
		if res.Cost != (Cost{Complete: true}) || res.Model != "" {
			t.Errorf("cost = %+v model %q", res.Cost, res.Model)
		}
	})
	t.Run("budget refused", func(t *testing.T) {
		f := newFixture(t)
		sp := &scriptedProvider{prices: testRates, calls: []call{{err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}}}
		f.cfg.Provider = sp
		res := mustBuild(t, f)
		if res.Source != SourceBase || sp.n != 1 {
			t.Fatalf("source %s, %d calls", res.Source, sp.n)
		}
		if res.Cost != (Cost{Complete: true}) || res.Model != "" {
			t.Errorf("cost = %+v model %q: a turn the budget refused was not sent", res.Cost, res.Model)
		}
	})
}

// Sent without usage, a provider with no price and a provider that failed after
// sending are unknown, never free.
func TestCostUnknownIsNeverZeroAndComplete(t *testing.T) {
	t.Run("no usage on the response", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{text: validReply(), noUsage: true}}}
		res := mustBuild(t, f)
		if res.Cost.Complete || res.Cost.Turns != 1 || res.Cost.USD != 0 {
			t.Errorf("cost = %+v", res.Cost)
		}
	})
	t.Run("no price", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider = f.provider // not a budget.Pricer
		f.provider.replies = []string{validReply()}
		res := mustBuild(t, f)
		if res.Cost.Complete || res.Cost.Turns != 1 {
			t.Errorf("cost = %+v", res.Cost)
		}
	})
	t.Run("the provider failed after the request went out", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{err: errors.New("connection reset")}}}
		res := mustBuild(t, f)
		if res.Cost.Complete || res.Cost.Turns != 1 {
			t.Errorf("cost = %+v", res.Cost)
		}
	})
	t.Run("a usage the price cannot charge", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{text: validReply(), usage: llm.Usage{InputTokens: 0, OutputTokens: 5}}}}
		res := mustBuild(t, f)
		if res.Cost.Complete || res.Cost.USD != 0 {
			t.Errorf("cost = %+v", res.Cost)
		}
	})
}

// A paid build that then fails still reports what it spent: the caller records
// it.
func TestCostSurvivesAFailedAndACancelledBuild(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{text: validReply(), usage: llm.Usage{InputTokens: 1000, OutputTokens: 200}}}}
		f.rkb.fit = func(string) fitSpec { return fitSpec{noReport: true, stderr: "rkb: no Chrome/Chromium found"} }
		res, err := f.build()
		wantCode(t, err, CodeRender)
		if !near(res.Cost.USD, nanoOf(1000, 200)) || !res.Cost.Complete || res.Cost.Turns != 1 || res.Model != "m" {
			t.Errorf("cost = %+v model %q", res.Cost, res.Model)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{text: validReply(), usage: llm.Usage{InputTokens: 1000, OutputTokens: 200}}}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.rkb.onStep = func(step string, c Command) {
			if step == "lint" {
				cancel()
			}
		}
		f.rkb.block = "lint"
		res, err := Build(ctx, f.cfg, Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD})
		wantCode(t, err, CodeCancel)
		if !near(res.Cost.USD, nanoOf(1000, 200)) || res.Cost.Turns != 1 {
			t.Errorf("a cancelled paid build lost its cost: %+v", res.Cost)
		}
	})
}
