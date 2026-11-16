package main

import (
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// Apple's client differs from the run's only in its timeout: the same
// allowlist and body bound, so it can reach nothing the run's client cannot,
// and every other host keeps egress's default.
func TestAppleClientDiffersFromTheRunsOnlyInItsTimeout(t *testing.T) {
	allowed := []string{"jobs.apple.com", "boards.greenhouse.io"}
	run, ap := runEgressConfig(allowed), appleEgressConfig(allowed)
	if !slices.Equal(ap.AllowedHosts, run.AllowedHosts) || ap.MaxBodyBytes != run.MaxBodyBytes || ap.UserAgent != run.UserAgent {
		t.Fatalf("apple config %+v strays from the run's %+v", ap, run)
	}
	if run.Timeout != 0 {
		t.Fatalf("the run's client sets timeout %s; want egress's default", run.Timeout)
	}
	if ap.Timeout != appleTimeout || appleTimeout <= 30*time.Second {
		t.Fatalf("apple timeout %s, want appleTimeout %s, over the 30s default", ap.Timeout, appleTimeout)
	}
	// egress accepts it: the timeout is within its bound.
	if _, err := egress.New(ap); err != nil {
		t.Fatalf("egress refuses the apple config: %v", err)
	}
}
