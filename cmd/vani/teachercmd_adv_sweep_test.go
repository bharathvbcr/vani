package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

// Sweep --budget-usd through the real Manvi meter (fake Anthropic server,
// truthful usage at a stated prompt-tokens-per-body-byte density, full-budget
// replies) with 16 in flight: the ratio of SETTLED spend (ledger
// spent_nano_usd, priced from provider-reported usage) to --budget-usd. Retained
// reservations (attempts cancelled at the stop, or unmeasured) are logged, not
// asserted: they are Manvi's upper bound, not a measurement.
func TestAdvRealMeterWorstDensityBudgetSweep(t *testing.T) {
	for _, density := range []float64{0.5, 1.0} {
		worst := 0.0
		for _, budgetUSD := range []float64{0.002, 0.004, 0.006, 0.01, 0.02, 0.04} {
			state := advSetup(t, nil, advN(40, advDense))
			srv := advServe(t, density, llmbackend.DefaultMaxTokens+llmbackend.ReasoningHeadroom, 0, false, 16)
			l := advReal(t, state, srv.URL, 5)
			_, _, _ = do(t, "teacher", "--of", "d1-ultra", "--budget-usd", fmt.Sprint(budgetUSD), "--concurrency", "16")
			snap, err := l.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			spent := snap.SpentNanoUSD
			t.Logf("  settled %d retained %d (cancelled or unmeasured attempts) in-flight %d", snap.SpentNanoUSD, snap.RetainedNanoUSD, snap.InFlightNanoUSD())
			ratio := float64(spent) / (budgetUSD * 1e9)
			worst = max(worst, ratio)
			t.Logf("density %.1f budget $%.3f: %d calls (peak %d), spent %s = %.2fx", density, budgetUSD, srv.calls.Load(), srv.peak.Load(), usd(spent), ratio)
			if spent > int64(budgetUSD*1e9) {
				t.Errorf("density %.1f budget $%.3f: spent %d passed it by %d nano-USD", density, budgetUSD, spent, spent-int64(budgetUSD*1e9))
			}
		}
		t.Logf("density %.1f: largest spend/budget ratio %.2f", density, worst)
	}
}
