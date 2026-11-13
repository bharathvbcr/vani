package rankfit

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// An audit card and a Mac app Jobs row are logged without Score's parts
// (recordAuditCards, `vani impressions log`). Such an impression is not "the
// scores the owner saw": the newest impression that recorded them is, and with
// none the posting is scored now. Before this, the scoreless audit card was the
// newest impression of every audited posting, so ItemFromScores refused it
// ("no recorded gate or fresh") and the weight fit could not run on any audit.
func TestTargetsSkipImpressionsThatRecordedNoScores(t *testing.T) {
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	digest := feedback.Scores{Priority: 40, Relevance: 50, Factors: map[string]float64{"gate": 1, "fresh": 0.8, "semantic": 0.7}}
	labels := map[posting.Key]eval.Entry{
		"gh:a:seen":   {Labels: map[string]string{eval.WouldApply: "yes"}, Source: "decile:9", PSelect: 0.5},
		"gh:a:unseen": {Labels: map[string]string{eval.WouldApply: "no"}, Source: "decile:2", PSelect: 0.5},
	}
	shown := []feedback.Impression{
		{At: at, SlateID: "2026-11-01", Surface: feedback.SurfaceDigest, Key: "gh:a:seen", Position: 3, Scores: digest},
		{At: at.Add(time.Hour), SlateID: "w1", Surface: feedback.SurfaceAudit, Key: "gh:a:seen", Position: 1},
		{At: at.Add(2 * time.Hour), SlateID: "jobs", Surface: feedback.SurfaceJobs, Key: "gh:a:seen", Position: 7},
		{At: at.Add(time.Hour), SlateID: "w1", Surface: feedback.SurfaceAudit, Key: "gh:a:unseen", Position: 2},
	}
	got, _, err := Targets(labels, shown, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := got["gh:a:seen"]
	if seen.Scores == nil || seen.Scores.Factors["gate"] != 1 || seen.Scores.Priority != 40 {
		t.Errorf("seen: want the digest's recorded scores, got %+v", seen.Scores)
	}
	if u := got["gh:a:unseen"]; u.Scores != nil {
		t.Errorf("unseen: only a scoreless audit card; want nil (score now), got %+v", u.Scores)
	}
	if _, err := ItemFromScores("gh:a:seen", *seen.Scores, jobinsight.DefaultWeights(), seen); err != nil {
		t.Errorf("the recorded scores must build an item: %v", err)
	}
	// The scoreless impressions still make a pass: a Jobs row shown and left
	// unmarked is a negative whether or not it carried scores.
	shown = append(shown, feedback.Impression{At: at, SlateID: "jobs", Surface: feedback.SurfaceJobs, Key: "gh:a:passed", Position: 1})
	got, c, err := Targets(labels, shown, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := got["gh:a:passed"]; !ok || p.Grade != GradeNo || p.Scores != nil || c.Passed != 1 {
		t.Errorf("passed: %+v ok=%v passed=%d", p, ok, c.Passed)
	}
}
