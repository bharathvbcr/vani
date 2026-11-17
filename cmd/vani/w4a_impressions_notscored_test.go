package main

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// A row the digest listed without a score is not logged as an impression: its
// zero Result would read as a measured priority of 0 to the fitter. The
// scored rows around it keep their positions in the digest.
func TestW4aImpressionsSkipNotScoredRows(t *testing.T) {
	dir := t.TempDir()
	shown := []digest.Shown{
		{Key: "gh:a:0001", Section: digest.SectionRanked, Position: 1, Result: jobinsight.Result{Score: 50, Relevance: 60}},
		{Key: "gh:a:0002", Section: digest.SectionExplore, Position: 2, Source: digest.ExploreSourceDropped, PSelect: 0.5, NotScored: true},
		{Key: "gh:a:0003", Section: digest.SectionRanked, Position: 3, Result: jobinsight.Result{Score: 40, Relevance: 55}},
	}
	if err := recordImpressions(context.Background(), dir, time.Now(), "2026-10-20", shown); err != nil {
		t.Fatal(err)
	}
	got, err := feedback.ReadImpressions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 2 || got.Rows[0].Key != "gh:a:0001" || got.Rows[1].Key != "gh:a:0003" || got.Rows[1].Position != 3 {
		t.Fatalf("logged %+v, want the two scored rows at positions 1 and 3", got.Rows)
	}
}
