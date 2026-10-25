package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ---- the one owner of "what is left of today's cap"

func TestReadBudgetReadsTheCapAndTodaysSpend(t *testing.T) {
	dir := t.TempDir()
	// No ledger file yet is a day with nothing spent (the first request creates it).
	b := readBudget(context.Background(), filepath.Join(dir, "llm-today.json"), 5)
	if b.DailyCapUSD != 5 || b.SpentUSD == nil || *b.SpentUSD != 0 || b.RemainingUSD == nil || *b.RemainingUSD != 5 {
		t.Fatalf("an unwritten ledger = %+v", b)
	}
	// A ledger that cannot be read leaves the spend unknown, never zero: absent keys.
	bad := filepath.Join(dir, "llm-bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b = readBudget(context.Background(), bad, 5)
	if b.DailyCapUSD != 5 || b.SpentUSD != nil || b.RemainingUSD != nil {
		t.Fatalf("an unreadable ledger gave a spend: %+v", b)
	}
	if b.reached(0.01) {
		t.Errorf("with the spend unknown nothing is reached: the provider is the gate")
	}
	// A folder where the file should be is not a ledger either.
	if b := readBudget(context.Background(), dir, 5); b.SpentUSD != nil {
		t.Errorf("a folder read as a ledger: %+v", b)
	}
}

func TestBudgetReached(t *testing.T) {
	left := llmBudget{DailyCapUSD: 5, SpentUSD: f64(4.9), RemainingUSD: f64(0.1)}
	for need, want := range map[float64]bool{0.05: false, 0.1: false, 0.11: true, 3: true} {
		if got := left.reached(need); got != want {
			t.Errorf("reached(%v) = %v, want %v", need, got, want)
		}
	}
	if (llmBudget{DailyCapUSD: 5}).reached(100) {
		t.Error("an unknown spend is never reached")
	}
	spent := llmBudget{DailyCapUSD: 5, SpentUSD: f64(9), RemainingUSD: f64(0)}
	if !spent.reached(0.0001) {
		t.Error("nothing left is reached by any call")
	}
}
