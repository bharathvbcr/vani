package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

func newTestLedger(t *testing.T) *ledger.File {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// The two packages hold the same defaults and the same bounds; this is the only
// place that says so, since config cannot import pipeline.
func TestLivenessDefaultsAgree(t *testing.T) {
	if got := livenessOf(config.Default()); got != pipeline.DefaultLiveness() {
		t.Fatalf("config defaults %+v, pipeline defaults %+v", got, pipeline.DefaultLiveness())
	}
	if config.DefaultMassShare != pipeline.DefaultMassShare || config.DefaultMassMinOpen != pipeline.DefaultMassMinOpen ||
		time.Duration(config.DefaultMassHoldHours)*time.Hour != pipeline.DefaultMassHold {
		t.Fatal("default constants differ")
	}
	if time.Duration(config.MinMassHoldHours)*time.Hour != pipeline.OrdinaryHold || time.Duration(config.MaxMassHoldHours)*time.Hour != pipeline.MaxMassHold {
		t.Fatal("hold bounds differ")
	}
	// Every value config accepts, pipeline accepts: the edges of its ranges.
	share := 1.0
	edge := config.Liveness{MassShare: &share, MassMinOpen: 1, MassHoldHours: config.MinMassHoldHours}
	if err := (pipeline.Liveness{MassShare: edge.Share(), MassMinOpen: edge.MinOpen(), MassHold: edge.Hold()}).Validate(); err != nil {
		t.Fatalf("the low edge: %v", err)
	}
	edge = config.Liveness{MassMinOpen: config.MaxMassMinOpen, MassHoldHours: config.MaxMassHoldHours}
	if err := (pipeline.Liveness{MassShare: edge.Share(), MassMinOpen: edge.MinOpen(), MassHold: edge.Hold()}).Validate(); err != nil {
		t.Fatalf("the high edge: %v", err)
	}
}

// A config's settings reach the Reconciler a run builds, and a reloaded config
// reaches the next run's: readBoards makes its Reconciler from the config the
// run holds.
func TestConfigLivenessReachesTheRunsReconciler(t *testing.T) {
	cfg := config.Default()
	rec, err := newBoardReconciler(cfg, newTestLedger(t))
	if err != nil || rec.Liveness() != pipeline.DefaultLiveness() {
		t.Fatalf("defaults: %+v %v", rec, err)
	}
	share := 0.4
	cfg.Liveness = config.Liveness{MassShare: &share, MassMinOpen: 7, MassHoldHours: 72}
	rec, err = newBoardReconciler(cfg, newTestLedger(t))
	want := pipeline.Liveness{MassShare: 0.4, MassMinOpen: 7, MassHold: 72 * time.Hour}
	if err != nil || rec.Liveness() != want {
		t.Fatalf("set: %+v %v", rec.Liveness(), err)
	}
	cfg.Liveness.MassHoldHours = 3
	if _, err := newBoardReconciler(cfg, newTestLedger(t)); err == nil || !strings.Contains(err.Error(), "mass hold") {
		t.Fatalf("a hold under the ordinary hold was accepted: %v", err)
	}
}

// `config check` refuses a setting the run would refuse.
func TestConfigCheckRefusesBadLiveness(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "config", "check")
	for _, bad := range []config.Liveness{{MassHoldHours: 5}, {MassMinOpen: -2}} {
		c := cfg
		c.Liveness = bad
		// Save refuses an invalid config, so write the bytes a hand edit would.
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		out, errOut := mustCode(t, 1, "config", "check")
		if !strings.Contains(out+errOut, "liveness.") {
			t.Errorf("config check did not name the liveness setting: %q %q", out, errOut)
		}
	}
}
