package main

import (
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

// newPricedProvider meters inner at the operator's rates for model, the model
// llm.model configures, and at the provider's own table for any other model
// (the cheaper llm.triage_model tier). in and out are nano-USD per token.
//
// The configured rates stay conservative on purpose: input and output are the
// operator's figures, and a cache write is charged at the input rate. A cache
// read is charged at the provider's published cache-read rate when it has one
// and it is below the input rate, since a read at the full input price over-
// states what the ledger spends on every cached prompt; otherwise at the input
// rate, as before.
func newPricedProvider(inner llm.Provider, model string, in, out int64) pricedProvider {
	prices := budget.Prices{Rates: budget.Rates{Input: in, Output: out, CacheWrite: in, CacheRead: in}}
	if t, ok := tablePrices(inner, model); ok && t.CacheRead < in {
		prices.CacheRead = t.CacheRead
	}
	return pricedProvider{Provider: inner, prices: prices, model: model}
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
