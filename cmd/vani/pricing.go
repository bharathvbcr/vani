package main

import (
	"math"
	"math/bits"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

// newPricedProvider meters inner at the operator's rates for model, the model
// llm.model configures, and at the provider's own table for any other model
// (the cheaper llm.triage_model tier). in and out are nano-USD per token.
//
// The configured rates stay conservative on purpose: input and output are the
// operator's figures. A cache write is charged at the provider's own
// cache-write multiple of input (the table's CacheWrite over its Input) applied
// to the operator's input rate, never below it: Manvi's adapter always sends
// cache_control, so a write is billed above plain input. A cache read is
// charged at the provider's published cache-read rate when it has one and it is
// below the input rate, since a read at the full input price overstates what
// the ledger spends on every cached prompt. A model the provider does not
// price keeps both at the input rate.
func newPricedProvider(inner llm.Provider, model string, in, out int64) pricedProvider {
	t, ok := tablePrices(inner, model)
	return pricedProvider{Provider: inner, prices: configuredPrices(in, out, t, ok), model: model}
}

// configuredPrices is the operator's input and output rates with the cache
// rates taken from table, the provider's published entry for the same model,
// when ok. It is the one place the fallback's cache rates are decided, so the
// metered path and the teacher/auditor planners cannot disagree.
func configuredPrices(in, out int64, table budget.Prices, ok bool) budget.Prices {
	p := budget.Prices{Rates: budget.Rates{Input: in, Output: out, CacheWrite: in, CacheRead: in}}
	if !ok {
		return p
	}
	p.CacheWrite = max(in, scaleRate(in, table.CacheWrite, table.Input))
	if table.CacheRead < in {
		p.CacheRead = table.CacheRead
	}
	return p
}

// scaleRate is rate*num/den rounded up, saturating at MaxInt64 instead of
// overflowing. den is positive for any table entry tablePrices accepts; a
// non-positive den or num returns rate unchanged.
func scaleRate(rate, num, den int64) int64 {
	if rate <= 0 || num <= 0 || den <= 0 {
		return rate
	}
	hi, lo := bits.Mul64(uint64(rate), uint64(num))
	if hi >= uint64(den) {
		return math.MaxInt64
	}
	q, r := bits.Div64(hi, lo, uint64(den))
	if r != 0 {
		q++
	}
	if q > math.MaxInt64 || q == 0 {
		return math.MaxInt64
	}
	return int64(q)
}

// PriceOf answers budget.Pricer. The configured model, and any model when none
// was configured (a pricedProvider built from prices alone), is metered at the
// configured prices. Another model is metered at the provider's table; a model
// the table does not know, or whose entry could not bound a charge, falls back
// to the configured prices. It never reports a zero rate: a free model would
// make the daily cap meaningless.
func (p pricedProvider) PriceOf(model string) (budget.Prices, bool) {
	if p.model == "" || model == p.model {
		return p.prices, true
	}
	if t, ok := tablePrices(p.Provider, model); ok {
		return t, true
	}
	return p.prices, true
}

// tablePrices is the provider's published price for model, only when the
// provider publishes one and it is usable: every rate positive and bounded
// (budget.Prices.Validate), so a zero or garbled entry reads as unknown.
func tablePrices(p llm.Provider, model string) (budget.Prices, bool) {
	pr, ok := p.(budget.Pricer)
	if !ok {
		return budget.Prices{}, false
	}
	t, ok := pr.PriceOf(model)
	if !ok || t.Validate() != nil {
		return budget.Prices{}, false
	}
	return t, true
}
