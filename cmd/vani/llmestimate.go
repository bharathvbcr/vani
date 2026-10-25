package main

import "context"

// What a model call may cost, said before it is made, against today's cap. The
// resume build (`jobs resume build --dry-run`) and the re-triage (`jobs retriage
// --dry-run`, and its batch) each estimate one, and both read the one cap
// (decide.daily_usd.llm) shared with every other model call Vani makes. These are
// the types and the reading they share; each command adds only what is its own
// (a build's turns, a re-triage's typical output and batch fit). The JSON keys are
// the app's contract, and the types are embedded so each reply keeps exactly the
// keys it had: resume's `budget` object beside its `estimate`, re-triage's cap and
// spend inline in its `estimate` (llmestimate_wire_test.go pins both).

// llmBudget is today's cap, and what of it is spent and left. SpentUSD and
// RemainingUSD are absent when today's spend could not be read: unknown is never
// shown as zero.
type llmBudget struct {
	DailyCapUSD  float64  `json:"daily_cap_usd"`
	SpentUSD     *float64 `json:"spent_usd,omitempty"`
	RemainingUSD *float64 `json:"remaining_usd,omitempty"`
}

// readBudget is the cap and, when the budget ledger at path can be read, today's
// committed spend (settled, retained and in flight, as readLLMSpend counts it) and
// what is left. A ledger that cannot be read leaves both absent.
func readBudget(ctx context.Context, path string, dailyCapUSD float64) llmBudget {
	b := llmBudget{DailyCapUSD: dailyCapUSD}
	if spent, err := readLLMSpend(ctx, path); err == nil {
		left := max(dailyCapUSD-spent, 0)
		b.SpentUSD, b.RemainingUSD = &spent, &left
	}
	return b
}

// reached says what is left of the cap is less than a call may need. Advice only:
// the metered provider is the gate. False when today's spend is unknown.
func (b llmBudget) reached(needUSD float64) bool {
	return b.RemainingUSD != nil && *b.RemainingUSD < needUSD
}

// llmEstimate is what model calls would cost at most. InputTokens is an upper
// bound at the byte-per-token ratio kbmatch uses; MaxOutputTokens one call's
// ceiling as the provider shapes it; CapReached says (reached) that what is left
// of the cap is less than the dearest single call needs.
type llmEstimate struct {
	InputTokens     int64   `json:"input_tokens"`
	MaxOutputTokens int64   `json:"max_output_tokens"`
	MaxUSD          float64 `json:"max_usd"`
	PriceSource     string  `json:"price_source"`
	CapReached      bool    `json:"cap_reached"`
}
