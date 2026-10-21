package digest

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A role DevPrism never scored and a role it scored but found no must-haves in
// are different facts, and the digest says so (A2-6): "Not ranked" for the
// first, "Coverage unknown" for the second. Neither claims "no particular
// order", because the bucket is sorted by priority.
func TestDigestSeparatesNeverScoredFromCoverageUnknown(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", posting.Explained, "NeverScored", map[string]string{"explanation": "x"}),
		rec("gh:a:2", posting.Explained, "NoMustHaves", map[string]string{
			"explanation": "y", pipeline.AnnCoverageUnknown: pipeline.CoverageNoMustHaves,
		}),
		rec("gh:a:3", posting.Explained, "Ranked", map[string]string{"rank": "70"}),
	}
	md, keys, shown := RenderShown(recs, nil, day, Counts{}, Options{})

	if strings.Contains(md, "no particular order") {
		t.Errorf("a priority-sorted bucket claims no order:\n%s", md)
	}
	wantBlocks := map[string]string{
		"neverScored": "## Not ranked (1)\n\nDevPrism did not score these, so they have no must-have coverage; they are listed by priority alone.\n\n### NeverScored at Acme\n",
		"unknown":     "## Coverage unknown (1)\n\nDevPrism scored these but found no must-haves in the posting, so there is no coverage to rank by; they are listed by priority alone.\n\n### NoMustHaves at Acme\n",
	}
	for name, want := range wantBlocks {
		if !strings.Contains(md, want) {
			t.Errorf("%s: missing block %q in:\n%s", name, want, md)
		}
	}
	ranked, notRanked, unknown := strings.Index(md, "## Ranked roles (1)"), strings.Index(md, "## Not ranked (1)"), strings.Index(md, "## Coverage unknown (1)")
	if ranked < 0 || notRanked < ranked || unknown < notRanked {
		t.Errorf("want Ranked, Not ranked, Coverage unknown in that order:\n%s", md)
	}
	// Per-role coverage line: only the never-scored role says "not scored".
	unknownRole := md[strings.Index(md, "### NoMustHaves"):]
	if strings.Contains(unknownRole, "not scored") || !strings.Contains(unknownRole, "- Must-have coverage: unknown (DevPrism found no must-haves)") {
		t.Errorf("coverage-unknown role line:\n%s", unknownRole)
	}
	neverRole := md[strings.Index(md, "### NeverScored"):strings.Index(md, "## Coverage unknown")]
	if !strings.Contains(neverRole, "- Must-have coverage: not scored") {
		t.Errorf("never-scored role line:\n%s", neverRole)
	}
	if len(keys) != 3 || len(shown) != 3 || shown[2].Section != SectionCoverageUnknown || shown[1].Section != SectionUnranked {
		t.Errorf("keys %v, shown %+v", keys, shown)
	}
}
