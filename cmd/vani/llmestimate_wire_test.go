package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The resume and re-triage dry runs both say what a model call would cost against
// today's cap. The app reads each reply's keys, so the one estimate type they now
// share must write exactly the keys each always did: resume's `budget` object
// beside its `estimate`, re-triage's cap and spend inline in its `estimate`. These
// tests build their values by assignment, so they held before the types were
// folded and still hold after.

func wireOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func f64(v float64) *float64 { return &v }

func TestResumeEstimateAndBudgetWriteTheirOwnKeys(t *testing.T) {
	var b llmBudget
	b.DailyCapUSD, b.SpentUSD, b.RemainingUSD = 5, f64(1.5), f64(3.5)
	want := map[string]any{"daily_cap_usd": 5.0, "spent_usd": 1.5, "remaining_usd": 3.5}
	if got := wireOf(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("budget = %v, want %v", got, want)
	}
	// An unknown spend is absent, never zero.
	var unknown llmBudget
	unknown.DailyCapUSD = 5
	if got := wireOf(t, unknown); !reflect.DeepEqual(got, map[string]any{"daily_cap_usd": 5.0}) {
		t.Errorf("budget with no spend = %v", got)
	}
	var est resumeEstimate
	est.Turns, est.InputTokens, est.MaxOutputTokens, est.MaxUSD, est.PriceSource, est.CapReached = 2, 100, 300, 0.5, "rates", true
	want = map[string]any{"turns": 2.0, "input_tokens": 100.0, "max_output_tokens": 300.0, "max_usd": 0.5, "price_source": "rates", "cap_reached": true}
	if got := wireOf(t, est); !reflect.DeepEqual(got, want) {
		t.Errorf("estimate = %v, want %v", got, want)
	}
}

func TestRetriageEstimateWritesItsOwnKeysWithTheBudgetInline(t *testing.T) {
	fit := 3
	var est retriageEstimate
	est.InputTokens, est.TypicalOutputTokens, est.MaxOutputTokens, est.TypicalUSD, est.MaxUSD = 100, 40, 300, 0.01, 0.5
	est.PriceSource, est.DailyCapUSD, est.SpentUSD, est.RemainingUSD = "rates", 5, f64(1.5), f64(3.5)
	est.CapReached, est.Postings, est.FitAtMax = true, 4, &fit
	want := map[string]any{"input_tokens": 100.0, "typical_output_tokens": 40.0, "max_output_tokens": 300.0, "typical_usd": 0.01, "max_usd": 0.5,
		"price_source": "rates", "daily_cap_usd": 5.0, "spent_usd": 1.5, "remaining_usd": 3.5, "cap_reached": true, "postings": 4.0, "fit_at_max": 3.0}
	if got := wireOf(t, est); !reflect.DeepEqual(got, want) {
		t.Errorf("estimate = %v, want %v", got, want)
	}
	// One posting, spend unknown: the optional keys are absent.
	var one retriageEstimate
	one.InputTokens, one.TypicalOutputTokens, one.MaxOutputTokens, one.PriceSource, one.DailyCapUSD = 1, 2, 3, "rates", 5
	want = map[string]any{"input_tokens": 1.0, "typical_output_tokens": 2.0, "max_output_tokens": 3.0, "typical_usd": 0.0, "max_usd": 0.0, "price_source": "rates", "daily_cap_usd": 5.0, "cap_reached": false}
	if got := wireOf(t, one); !reflect.DeepEqual(got, want) {
		t.Errorf("single estimate = %v, want %v", got, want)
	}
}
