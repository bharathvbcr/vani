package main

import (
	"strings"
	"testing"
)

// No devprism.binary: scoring is off, and the run says so. A silent skip let
// a whole digest of unscored roles pass for a ranked one.
func TestRunnerWarnsWhenScoringIsOff(t *testing.T) {
	cfg := reasonsConfig(t)
	cfg.DevPrism.Binary = ""
	r, warn := runnerFor(t, cfg, t.TempDir())
	if !r.SkipScoring || r.Scorer != nil {
		t.Fatalf("skip %v scorer %T", r.SkipScoring, r.Scorer)
	}
	if !strings.Contains(warn, "scoring disabled") {
		t.Fatalf("warnings: %s", warn)
	}
}

// With a binary the scorer is built (lazily: nothing starts) and there is no
// scoring warning.
func TestRunnerScoresWithDevPrism(t *testing.T) {
	cfg := reasonsConfig(t)
	cfg.DevPrism.Binary = "/nonexistent/devprism"
	r, warn := runnerFor(t, cfg, t.TempDir())
	if r.SkipScoring || r.Scorer == nil {
		t.Fatalf("skip %v scorer %T", r.SkipScoring, r.Scorer)
	}
	if strings.Contains(warn, "scoring disabled") {
		t.Fatalf("warnings: %s", warn)
	}
}
