package main

import (
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

// Every configured rate the config accepts must still give prices the budget
// package accepts: before the change CacheWrite equalled input, so Validate
// passed for any input up to the config cap (1_000_000 nano-USD/token).
func TestW1badvConfiguredPricesStayValidUpToTheConfigCap(t *testing.T) {
	table, ok := anthropic.PriceOf("claude-opus-5-5")
	if !ok {
		t.Fatal("no opus entry")
	}
	for _, in := range []int64{1, 1_000, 800_000, 800_001, 999_999, 1_000_000} {
		old := budget.Prices{Rates: budget.Rates{Input: in, Output: 1, CacheWrite: in, CacheRead: in}}
		if err := old.Validate(); err != nil {
			t.Fatalf("precondition: old prices at %d: %v", in, err)
		}
		got := configuredPrices(in, 1, table, true)
		if err := got.Validate(); err != nil {
			t.Errorf("input %d: configured prices were valid before and are refused now: %v (cache write %d)", in, err, got.CacheWrite)
		}
	}
}
