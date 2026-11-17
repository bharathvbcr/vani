package main

import (
	"math"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
)

// Anthropic's prompt-caching page, read 2026-11-16 at
// https://platform.claude.com/docs/en/build-with-claude/prompt-caching
// (docs.claude.com redirects there): "Cache write tokens with a 5-minute TTL
// are 1.25 times the base input tokens price", the 1-hour TTL is 2 times, and
// "by default, the cache has a 5-minute lifetime". Manvi's adapter sends
// cache_control {"type":"ephemeral"} with no ttl (llm/anthropic/adapter.go,
// wireCache), so the 5-minute multiple, 1.25x, is the one a write is billed at.
func TestW1bCacheWriteIsChargedAtTheProviderMultiple(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-5", "claude-haiku-4-5"} {
		table, ok := anthropic.PriceOf(model)
		if !ok {
			t.Fatalf("%s is not in Manvi's table", model)
		}
		if table.CacheWrite != table.Input*5/4 {
			t.Fatalf("%s: Manvi's cache write %d is not 1.25x input %d: the doc multiple changed", model, table.CacheWrite, table.Input)
		}
		got, _ := newPricedProvider(anthropic.New("", noKey), model, table.Input, table.Output).PriceOf(model)
		if got.CacheWrite != table.Input*5/4 {
			t.Errorf("%s: cache write %d, want 1.25x input = %d", model, got.CacheWrite, table.Input*5/4)
		}
		// One million cache-write tokens cost 1.25x one million input tokens.
		// InputTokens is the whole prompt, cache writes included, and Charge
		// refuses a usage with no output.
		c, err := got.Charge(llm.Usage{InputTokens: 1_000_000, OutputTokens: 1, CacheWriteTokens: 1_000_000})
		want := got.Output + 1_000_000*table.Input*5/4
		if err != nil || c != want {
			t.Errorf("%s: 1M cache-write tokens charged %d (err %v), want %d", model, c, err, want)
		}
	}
	// An operator rate above the table's is scaled by the same multiple.
	got, _ := newPricedProvider(anthropic.New("", noKey), "claude-opus-5-5", 8000, opusOut).PriceOf("claude-opus-5-5")
	if got.CacheWrite != 10000 {
		t.Errorf("configured input 8000: cache write %d, want 10000", got.CacheWrite)
	}
}

// A call with no cache writes costs what it cost when a write was priced at
// the input rate: the change touches only the cache-write rate.
func TestW1bCallWithoutCacheWritesCostsWhatItDid(t *testing.T) {
	table, _ := anthropic.PriceOf("claude-opus-5-5")
	got, _ := opusPriced(t).PriceOf("claude-opus-5-5")
	before := budget.Prices{Rates: budget.Rates{Input: opusIn, Output: opusOut, CacheWrite: opusIn, CacheRead: table.CacheRead}}
	for _, u := range []llm.Usage{
		{},
		{InputTokens: 12_345, OutputTokens: 678},
		{InputTokens: 50_001, OutputTokens: 1, CacheReadTokens: 50_000},
		{InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 1_000_000, ReasoningTokens: 5},
	} {
		a, aerr := got.Charge(u)
		b, berr := before.Charge(u)
		if a != b || (aerr == nil) != (berr == nil) {
			t.Errorf("%+v: charged %d (%v), was %d (%v)", u, a, aerr, b, berr)
		}
	}
	w, werr := got.Charge(llm.Usage{InputTokens: 100, OutputTokens: 1, CacheWriteTokens: 100})
	o, oerr := before.Charge(llm.Usage{InputTokens: 100, OutputTokens: 1, CacheWriteTokens: 100})
	if werr != nil || oerr != nil || w <= o {
		t.Errorf("a cache write is not dearer than before: %d vs %d", w, o)
	}
}

func TestW1bScaleRateNeverOverflowsOrLowers(t *testing.T) {
	for _, c := range []struct{ rate, num, den, want int64 }{
		{4000, 5000, 4000, 5000},
		{3, 5, 4, 4},
		{0, 5, 4, 0},
		{-7, 5, 4, -7},
		{4000, 0, 4000, 4000},
		{4000, -1, 4000, 4000},
		{4000, 5000, 0, 4000},
		{math.MaxInt64, math.MaxInt64, 1, math.MaxInt64},
		{math.MaxInt64, 5, 4, math.MaxInt64},
		{1, math.MaxInt64, 1, math.MaxInt64},
	} {
		if got := scaleRate(c.rate, c.num, c.den); got != c.want {
			t.Errorf("scaleRate(%d,%d,%d) = %d, want %d", c.rate, c.num, c.den, got, c.want)
		}
	}
	if got := configuredPrices(5000, 9000, budget.Prices{Rates: budget.Rates{Input: 4000, Output: 1, CacheWrite: 1, CacheRead: 1}}, true); got.CacheWrite != 5000 {
		t.Errorf("a table multiple below 1 lowered the write rate to %d", got.CacheWrite)
	}
}

// teacherPrices and the metered fallback read the same cache rates.
func TestW1bTeacherPricesAgreeWithTheMeteredFallback(t *testing.T) {
	var cfg config.Config
	cfg.LLM.Provider, cfg.LLM.Model = "anthropic", "claude-opus-5-5"
	cfg.LLM.InputNanoUSDPerToken, cfg.LLM.OutputNanoUSDPerToken = opusIn, opusOut
	got, _, err := teacherPrices(cfg, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := opusPriced(t).PriceOf("claude-opus-5-5")
	if got != want {
		t.Fatalf("teacher %+v, metered %+v", got, want)
	}
}
