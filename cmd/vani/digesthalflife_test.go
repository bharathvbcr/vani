package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// The digest ranks with the same half-life `vani jobs --half-life` defaults to,
// stated at the seam rather than left to the zero value's fallback.
func TestDigestOptionsCarryTheJobsHalfLife(t *testing.T) {
	o, _ := digestOptions(config.Config{}, t.TempDir())
	if o.HalfLifeDays != jobinsight.DefaultScoreHalfLifeDays {
		t.Fatalf("digest half-life %v, want %v", o.HalfLifeDays, jobinsight.DefaultScoreHalfLifeDays)
	}
}
