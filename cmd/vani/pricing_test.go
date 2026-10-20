package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/llm/gemini"
)

func noKey() (credentials.Secret, error) { return credentials.Secret{}, errors.New("no key in tests") }

// opus is the live config's model and rates.
const (
	opusIn  = 4000
	opusOut = 20000
)

func opusPriced(t *testing.T) pricedProvider {
	t.Helper()
	return newPricedProvider(anthropic.New("", noKey), "claude-opus-5-5", opusIn, opusOut)
}

func positive(t *testing.T, label string, r budget.Rates) {
	t.Helper()
	if r.Input <= 0 || r.Output <= 0 || r.CacheWrite <= 0 || r.CacheRead <= 0 {
		t.Errorf("%s: a zero rate would make the spend cap meaningless: %+v", label, r)
	}
}

// The configured model keeps the operator's conservative input and output
// rates (and writes at the input rate, as before), but cache reads are metered
// at Manvi's cache-read rate instead of the full input price.
func TestConfiguredModelKeepsConfiguredRatesAndTableCacheRead(t *testing.T) {
	got, ok := opusPriced(t).PriceOf("claude-opus-5-5")
	table, _ := anthropic.PriceOf("claude-opus-5-5")
	if !ok || got.Input != opusIn || got.Output != opusOut || got.CacheWrite != opusIn {
		t.Fatalf("configured rates changed: %+v ok=%v", got, ok)
	}
	if got.CacheRead != table.CacheRead || got.CacheRead >= opusIn {
		t.Fatalf("cache read %d, want Manvi's rate %d (input is %d)", got.CacheRead, table.CacheRead, opusIn)
	}
	positive(t, "opus", got.Rates)
}

// A different model is metered by Manvi's own table, not the configured
// model's rates: that is the point of a cheap escalation tier.
func TestOtherModelUsesManvisTable(t *testing.T) {
	got, ok := opusPriced(t).PriceOf("claude-haiku-5-5")
	want, _ := anthropic.PriceOf("claude-haiku-5-5")
	if !ok || got != want {
		t.Fatalf("PriceOf(haiku) = %+v ok=%v, want Manvi's %+v", got, ok, want)
	}
	if got.Input >= opusIn || got.CacheRead != want.CacheRead || got.LongPromptAbove != want.LongPromptAbove {
		t.Fatalf("haiku is not priced as haiku: %+v", got)
	}
}

// A model Manvi does not price falls back to the configured rates. It is
// never free: a zero price would let any amount through the daily cap.
func TestUnknownModelFallsBackToConfiguredRatesNeverZero(t *testing.T) {
	p := opusPriced(t)
	for _, model := range []string{"claude-unreleased-9", "", "gemini-3.8-flash", "../etc/passwd"} {
		got, ok := p.PriceOf(model)
		if !ok {
			t.Errorf("PriceOf(%q) not priced", model)
			continue
		}
		if got.Input != opusIn || got.Output != opusOut {
			t.Errorf("PriceOf(%q) = %+v, want the configured rates", model, got)
		}
		positive(t, model, got.Rates)
		if err := got.Validate(); err != nil {
			t.Errorf("PriceOf(%q) is not a table budget accepts: %v", model, err)
		}
	}
}

// A table entry that cannot bound a charge (zero or negative rates) is not
// trusted: the configured rates apply instead.
type zeroPricer struct{ llm.Provider }

func (zeroPricer) PriceOf(string) (budget.Prices, bool) { return budget.Prices{}, true }

func TestZeroTableEntryIsNeverUsed(t *testing.T) {
	p := newPricedProvider(zeroPricer{anthropic.New("", noKey)}, "claude-opus-5-5", opusIn, opusOut)
	for _, model := range []string{"claude-opus-5-5", "claude-haiku-5-5"} {
		got, _ := p.PriceOf(model)
		positive(t, model, got.Rates)
		if got.Input != opusIn || got.Output != opusOut || got.CacheRead != opusIn {
			t.Errorf("PriceOf(%q) = %+v, want the configured rates throughout", model, got)
		}
	}
}

// Without a table (Gemini publishes none) every model is metered at the
// configured rates, cache reads included, exactly as before.
func TestProviderWithoutTableMetersAtConfiguredRates(t *testing.T) {
	p := newPricedProvider(gemini.New("", noKey), "gemini-3.8-flash", 1500, 7500)
	for _, model := range []string{"gemini-3.8-flash", "gemini-other", "claude-haiku-5-5"} {
		got, ok := p.PriceOf(model)
		want := budget.Prices{Rates: budget.Rates{Input: 1500, Output: 7500, CacheWrite: 1500, CacheRead: 1500}}
		if !ok || got != want {
			t.Errorf("PriceOf(%q) = %+v, want %+v", model, got, want)
		}
	}
}

// A cache-read rate must not exceed the configured input rate: the operator's
// figure is the ceiling, even for a tight configured price.
func TestCacheReadIsCappedAtConfiguredInput(t *testing.T) {
	table, _ := anthropic.PriceOf("claude-opus-5-5")
	in := table.CacheRead - 50
	got, _ := newPricedProvider(anthropic.New("", noKey), "claude-opus-5-5", in, opusOut).PriceOf("claude-opus-5-5")
	if got.CacheRead != in {
		t.Fatalf("cache read %d, want the input rate %d", got.CacheRead, in)
	}
	// A configured model Manvi does not know reads its cache at the input rate.
	got, _ = newPricedProvider(anthropic.New("", noKey), "claude-custom", opusIn, opusOut).PriceOf("claude-custom")
	if got.CacheRead != opusIn {
		t.Fatalf("unknown configured model: cache read %d, want %d", got.CacheRead, opusIn)
	}
}

// A pricedProvider built without a model (the form existing tests use) meters
// every model at its prices, as it always did.
func TestPricedProviderWithoutModelIsUnchanged(t *testing.T) {
	prices := budget.Prices{Rates: budget.Rates{Input: 4000, Output: 20000, CacheWrite: 4000, CacheRead: 4000}}
	p := pricedProvider{Provider: anthropic.New("", noKey), prices: prices}
	for _, model := range []string{"claude-opus-5-5", "claude-haiku-5-5", ""} {
		if got, ok := p.PriceOf(model); !ok || got != prices {
			t.Errorf("PriceOf(%q) = %+v ok=%v", model, got, ok)
		}
	}
}

// The wired chain (budget.Provider over pricedProvider) reports the same
// per-model prices, so the ledger charges a Haiku escalation at Haiku's rate.
func TestMeteredChainPricesPerModel(t *testing.T) {
	ctx := context.Background()
	l, err := budget.Open(ctx, filepath.Join(t.TempDir(), "llm.json"), int64(10*1e9))
	if err != nil {
		t.Fatal(err)
	}
	bp := &budget.Provider{Inner: opusPriced(t), Ledger: l}
	haiku, _ := anthropic.PriceOf("claude-haiku-5-5")
	if got, ok := bp.PriceOf("claude-haiku-5-5"); !ok || got != haiku {
		t.Fatalf("chain prices haiku at %+v ok=%v, want %+v", got, ok, haiku)
	}
	if got, ok := bp.PriceOf("claude-opus-5-5"); !ok || got.Input != opusIn {
		t.Fatalf("chain prices opus at %+v ok=%v", got, ok)
	}
}
