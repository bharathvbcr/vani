package digest

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func unknownAnn() map[string]string {
	return map[string]string{"explanation": "y", pipeline.AnnCoverageUnknown: pipeline.CoverageNoMustHaves}
}

// Empty buckets print no heading: only a ranked role, so neither the Not ranked
// nor the Coverage unknown heading or sentence appears.
func TestAdversarialDigestEmptyBucketsPrintNoHeading(t *testing.T) {
	md, _, _ := RenderShown([]ledger.Record{rec("gh:a:1", posting.Explained, "R", map[string]string{"rank": "70"})}, nil, day, Counts{}, Options{})
	for _, bad := range []string{"Not ranked", "Coverage unknown", "no must-haves"} {
		if strings.Contains(md, bad) {
			t.Errorf("%q printed with no such roles:\n%s", bad, md)
		}
	}
}

// Only coverage-unknown roles: that heading alone, never "Not ranked".
func TestAdversarialDigestOnlyCoverageUnknown(t *testing.T) {
	recs := []ledger.Record{rec("gh:a:1", posting.Explained, "A", unknownAnn()), rec("gh:a:2", posting.Explained, "B", unknownAnn())}
	md, keys, shown := RenderShown(recs, nil, day, Counts{}, Options{})
	if !strings.Contains(md, "## Coverage unknown (2)") || strings.Contains(md, "Not ranked") || strings.Contains(md, "## Ranked") {
		t.Errorf("md:\n%s", md)
	}
	if len(keys) != 2 || shown[0].Section != SectionCoverageUnknown || shown[1].Section != SectionCoverageUnknown {
		t.Errorf("keys %v shown %+v", keys, shown)
	}
}

// A role with a rank AND the marker is ranked (the rank wins); an unsure role
// carrying the marker stays under Unsure.
func TestAdversarialDigestRankBeatsMarkerAndUnsureBeatsMarker(t *testing.T) {
	both := unknownAnn()
	both["rank"] = "60"
	uns := unknownAnn()
	uns["triage.unsure"] = "1"
	recs := []ledger.Record{rec("gh:a:1", posting.Explained, "Both", both), rec("gh:a:2", posting.Explained, "UnsureMarked", uns)}
	md, _, shown := RenderShown(recs, nil, day, Counts{}, Options{})
	if strings.Contains(md, "Coverage unknown") {
		t.Errorf("rank+marker or unsure+marker landed under Coverage unknown:\n%s", md)
	}
	got := map[string]string{}
	for _, s := range shown {
		got[string(s.Key)] = s.Section
	}
	if got["gh:a:1"] != SectionRanked || got["gh:a:2"] != SectionUnsure {
		t.Errorf("sections %v\n%s", got, md)
	}
}

// The bucket is listed in priority order, as its text claims, not in key
// order: key order is the reverse of freshness here, so a key-sorted bucket
// would put the oldest role first.
func TestAdversarialDigestCoverageUnknownListedByPriority(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	var recs []ledger.Record
	for i := 1; i <= 5; i++ {
		d := string(rune('0' + i))
		r := rec("gh:a:"+d, posting.Explained, "Role"+d, unknownAnn())
		r.Posting.PostedAt = now.Add(-time.Duration(60-10*i) * 24 * time.Hour) // gh:a:5 newest
		recs = append(recs, r)
	}
	_, _, shown := RenderShown(recs, nil, now, Counts{}, Options{})
	var order []string
	for _, s := range shown {
		order = append(order, string(s.Key))
	}
	for i := 1; i < len(shown); i++ {
		if shown[i-1].Result.Score < shown[i].Result.Score {
			t.Errorf("not priority-descending at %d; order %v", i, order)
		}
	}
	if shown[0].Result.Score == shown[len(shown)-1].Result.Score {
		t.Skipf("freshness did not move the priority score (all %v); order %v cannot distinguish key from priority order", shown[0].Result.Score, order)
	}
}
