package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// w4dadvCfg writes body as a rank weights file and returns a config pointing at it.
func w4dadvCfg(t *testing.T, body string) config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "w.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RankWeightsFile = p
	return cfg
}

const w4dadvShares = `{"fit":0.1,"coverage":0.1,"semantic":0.05,"keyword":0.6,"title":0.05,"employer":0.05,"kb_match":0.05}`

func w4dadvFile(weights string) string {
	return `{"v":1,"fitted_at":"2026-10-18T00:00:00Z","shares":` + w4dadvShares + `,"report":{"labels":9,"targets":{},"weights":` + weights + `}}`
}

// The old read check refused a result whose fitted accuracy is below the hand
// accuracy, or either outside [0,1]. NoGainReason reads neither, so a damaged or
// hand-edited result with a positive gain_lo passes. Each case must not rank.
func TestW4dAdvDamagedResultsDoNotRank(t *testing.T) {
	good := `"held_out_pairs":400,"gain_lo":0.01,"gain_hi":0.1,"resamples":1000`
	for name, weights := range map[string]string{
		"accuracy above 1":          `{"fit_accuracy":1.5,"hand_accuracy":0.6,` + good + `}`,
		"negative accuracy":         `{"fit_accuracy":-0.2,"hand_accuracy":-0.5,` + good + `}`,
		"fit below hand":            `{"fit_accuracy":0.3,"hand_accuracy":0.6,` + good + `}`,
		"accuracies absent (zeros)": `{` + good + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			w, used, warn := rankWeights(w4dadvCfg(t, w4dadvFile(weights)))
			if w != nil || used != rankWeightsHand {
				t.Errorf("shares ranked from a result no fit can write: used %q warn %q", used, warn)
			}
		})
	}
}

// A newer vani that renames a gate field in the held-out result: the older
// binary reads past it into a zero value. A zero "stratified" skips the
// scored-stratum floor, so the gate fails open. The ignored field must refuse.
func TestW4dAdvRenamedGateFieldFailsClosed(t *testing.T) {
	weights := `{"fit_accuracy":0.66,"hand_accuracy":0.6,"held_out_pairs":400,"gain_lo":0.01,"gain_hi":0.1,"resamples":1000,` +
		`"is_stratified":true,"scored_pair_count":3}`
	w, used, warn := rankWeights(w4dadvCfg(t, w4dadvFile(weights)))
	if w != nil || used != rankWeightsHand {
		t.Errorf("a held-out result with unreadable gate fields passed: used %q warn %q", used, warn)
	}
}
