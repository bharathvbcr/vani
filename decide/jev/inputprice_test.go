package jev

import (
	"context"
	"math"
	"testing"

	"github.com/bharathvbcr/decide"
)

// subqResp is SubQ's documented /v1/systemone response shape
// (docs.subq.ai/endpoints/decisions-api, read 2026-11-17): a choice answer
// carries choice, probabilities and confidence, and usage reports input
// tokens with no cost.
const subqResp = `{"model":"subq-snap-1.2","answers":{
"mode":{"type":"choice","choice":"remote","probabilities":{"remote":1,"onsite":0},"confidence":1},
"level":{"type":"choice","choice":"senior","probabilities":{"junior":0.13,"senior":0.87},"confidence":0.79}},
"usage":{"input_tokens":2500000,"output_tokens":0}}`

func subqConfig(sp decide.Spend, price float64) Config {
	return Config{Endpoint: "https://api.subq.ai/v1/systemone", Model: "subq-snap-1.2", Key: testKey,
		Name: "subq-snap-1-2", Spend: sp, SpendKey: "jev", InputUSDPerMTok: price}
}

func TestSubQResponseReadsAndPricesInputTokens(t *testing.T) {
	sp := &fakeSpend{}
	b, d, _ := newBackend(t, subqConfig(sp, 0.04), ok(subqResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Slots["mode"]; got.Reading != decide.Answered || got.Value != "remote" || got.Jev.Probability != 1 {
		t.Errorf("mode = %+v", got)
	}
	if got := a.Slots["level"]; got.Reading != decide.Answered || got.Value != "senior" || got.Jev.Probability != 0.79 {
		t.Errorf("level = %+v (confidence wins over the option's probability)", got)
	}
	// 2.5M input tokens at $0.04 per million.
	if want := 0.1; math.Abs(a.CostUSD-want) > 1e-12 || len(sp.charged) != 1 || math.Abs(sp.charged[0]-want) > 1e-12 {
		t.Errorf("cost = %v charged = %v, want %v", a.CostUSD, sp.charged, want)
	}
	if got := d.reqs[0].Header.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
	if a.Backend != "subq-snap-1-2" {
		t.Errorf("backend = %q", a.Backend)
	}
}

func TestReportedCostWinsOverInputPrice(t *testing.T) {
	sp := &fakeSpend{}
	b, _, _ := newBackend(t, subqConfig(sp, 0.04),
		ok(`{"answers":{"mode":{"choice":"remote","confidence":0.9},"level":{"choice":"senior","confidence":0.9}},"usage":{"cost":0.002,"input_tokens":2500000}}`))
	if a, _ := b.Decide(context.Background(), question()); a.CostUSD != 0.002 || len(sp.charged) != 1 || sp.charged[0] != 0.002 {
		t.Errorf("cost = %v charged = %v", a.CostUSD, sp.charged)
	}
}

func TestInputTokensWithoutPriceChargeNothing(t *testing.T) {
	sp := &fakeSpend{}
	b, _, _ := newBackend(t, subqConfig(sp, 0), ok(subqResp))
	if a, _ := b.Decide(context.Background(), question()); a.CostUSD != 0 || len(sp.charged) != 0 {
		t.Errorf("unpriced: cost = %v charged = %v", a.CostUSD, sp.charged)
	}
}

func TestHostileUsageNeverOverchargesOrGoesNegative(t *testing.T) {
	for name, usage := range map[string]string{
		"negative tokens": `{"input_tokens":-5}`,
		"negative cost":   `{"cost":-1,"input_tokens":-5}`,
		"zero tokens":     `{"input_tokens":0}`,
	} {
		sp := &fakeSpend{}
		b, _, _ := newBackend(t, subqConfig(sp, 0.04),
			ok(`{"answers":{"mode":{"choice":"remote","confidence":0.9},"level":{"choice":"senior","confidence":0.9}},"usage":`+usage+`}`))
		if a, _ := b.Decide(context.Background(), question()); a.CostUSD != 0 || len(sp.charged) != 0 {
			t.Errorf("%s: cost = %v charged = %v", name, a.CostUSD, sp.charged)
		}
	}
	// A huge claimed count is clamped, so the cost stays finite.
	sp := &fakeSpend{}
	b, _, _ := newBackend(t, subqConfig(sp, MaxInputUSDPerMTok),
		ok(`{"answers":{"mode":{"choice":"remote","confidence":0.9},"level":{"choice":"senior","confidence":0.9}},"usage":{"input_tokens":9223372036854775807}}`))
	a, _ := b.Decide(context.Background(), question())
	if math.IsInf(a.CostUSD, 0) || math.IsNaN(a.CostUSD) || a.CostUSD != float64(maxBilledInputTokens)*MaxInputUSDPerMTok/1e6 {
		t.Errorf("clamped cost = %v", a.CostUSD)
	}
}

func TestInputPriceBounds(t *testing.T) {
	for _, p := range []float64{-0.01, MaxInputUSDPerMTok + 1, math.NaN(), math.Inf(1)} {
		c := subqConfig(nil, p)
		if err := c.Validate(); err == nil {
			t.Errorf("price %v accepted", p)
		}
	}
	for _, p := range []float64{0, 0.04, MaxInputUSDPerMTok} {
		c := subqConfig(nil, p)
		if err := c.Validate(); err != nil {
			t.Errorf("price %v refused: %v", p, err)
		}
	}
}
