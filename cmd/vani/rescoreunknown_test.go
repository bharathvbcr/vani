package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// unknownCoverageState seeds a real on-disk ledger with n digested 0-of-0
// records, one digested record with no score at all, and one digested record
// already marked unknown, and points the config at a DevPrism binary that is
// never started (nothing is eligible to score).
func unknownCoverageState(t *testing.T, n int) {
	t.Helper()
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DevPrism.Binary = filepath.Join(t.TempDir(), "devprism-stub")
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	add := func(id string, ann map[string]string) {
		k, err := posting.GreenhouseKey("acme", id)
		if err != nil {
			t.Fatal(err)
		}
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Eng " + id, Description: "jd", PostedAt: at}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: at, Key: k, Status: posting.Digested, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		add("z"+string(rune('a'+i)), map[string]string{ledger.AnnRank: "0", ledger.AnnCoverage: "0"})
	}
	add("noscore", nil)
	add("known", map[string]string{pipeline.AnnCoverageUnknown: pipeline.CoverageNoMustHaves})
}

// `vani rescore` says how many 0-of-0 coverages it marked unknown, and the
// unknown-coverage postings are their own line, not "digested ... no score".
func TestRescoreCommandReportsRepairedAndUnknown(t *testing.T) {
	unknownCoverageState(t, 3)
	out, _ := mustCode(t, 0, "rescore")
	for _, want := range []string{
		"3 posting(s) with a 0-of-0 coverage marked unknown",
		"4 posting(s) have unknown coverage: DevPrism found no must-haves",
		"1 digested posting(s) have no score; left alone",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// A second run repairs nothing and reports the same disjoint classes.
	out, _ = mustCode(t, 0, "rescore")
	if strings.Contains(out, "marked unknown") || !strings.Contains(out, "4 posting(s) have unknown coverage") ||
		!strings.Contains(out, "1 digested posting(s) have no score") {
		t.Errorf("second run output:\n%s", out)
	}
}

func TestRescoreDryRunReportsUnknownSeparately(t *testing.T) {
	unknownCoverageState(t, 2)
	out, _ := mustCode(t, 0, "rescore", "--dry-run")
	for _, want := range []string{
		"2 posting(s) with a 0-of-0 coverage would be marked unknown",
		"1 digested posting(s) have no score; left alone",
		"1 posting(s) have unknown coverage: DevPrism found no must-haves",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
}
