package digest

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The digest orders by jobinsight.Score, the ranker the Jobs list sorts by: a
// fresh strong fit leads a stale, high-coverage domain mismatch, which the old
// rank-only order put first.
func TestDigestOrdersByScore(t *testing.T) {
	old := rec("gh:a:0", posting.Digested, "First crawl", nil)
	old.FirstSeen = day.Add(-60 * 24 * time.Hour)

	stale := rec("gh:a:1", posting.Explained, "Account Executive", map[string]string{
		ledger.AnnRank: "95", ledger.AnnCoverage: "95",
		"triage.role_fit": "mismatch_domain", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9",
	})
	stale.FirstSeen = day.Add(-50 * 24 * time.Hour)
	stale.Posting.PostedAt = day.Add(-50 * 24 * time.Hour)

	fresh := rec("gh:a:2", posting.Explained, "Inference Engineer", map[string]string{
		ledger.AnnRank: "60", ledger.AnnCoverage: "60",
		"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9",
	})
	fresh.FirstSeen = day.Add(-24 * time.Hour)
	fresh.Posting.PostedAt = day.Add(-24 * time.Hour)

	unscored := rec("gh:a:3", posting.Explained, "Unscored", nil)
	unscored.FirstSeen = day.Add(-time.Hour)

	recs := []ledger.Record{old, stale, fresh, unscored}
	md, keys := Render(recs, nil, day, Counts{})
	want := []posting.Key{"gh:a:2", "gh:a:1", "gh:a:3"}
	if len(keys) != len(want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys %v, want %v", keys, want)
		}
	}
	// A posting DevPrism did not score is still "Not ranked", whatever its score.
	if i, j := strings.Index(md, "## Ranked roles (2)"), strings.Index(md, "## Not ranked (1)"); i < 0 || j < i {
		t.Errorf("headings:\n%s", md)
	}
	if sel := Select(recs, nil); len(sel) != 3 || sel[0].Key != "gh:a:2" {
		t.Errorf("Select order %v", sel)
	}
}
