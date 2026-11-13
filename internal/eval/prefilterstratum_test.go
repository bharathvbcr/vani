package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A row dropped at listing time is a dropped candidate of the class
// "prefilter:<rule>", drawn as the stratum "dropped:prefilter:<rule>" with its
// own inclusion probability.
func TestAuditDrawsAPrefilterClassAsItsOwnStratum(t *testing.T) {
	var cands []AuditCandidate
	for i, k := range []posting.Key{"x:aa01", "x:aa02", "x:aa03", "x:aa04"} {
		class := "prefilter:exclude_title"
		if i == 3 {
			class = "prefilter:exclude_location_unrecognised"
		}
		cands = append(cands, AuditCandidate{Key: k, Dropped: true, Class: class})
	}
	plan := AuditDraw(cands, nil, "2026-W46", AuditQuotas{Dropped: 2})
	got := map[string]int{}
	for _, p := range plan.Picks {
		if !strings.HasPrefix(p.Source, PrefilterStratumPrefix) {
			t.Errorf("stratum %q", p.Source)
		}
		got[p.Source]++
	}
	if len(plan.Picks) != 2 || got["dropped:prefilter:exclude_title"] != 1 || got["dropped:prefilter:exclude_location_unrecognised"] != 1 {
		t.Errorf("picks %+v", plan.Picks)
	}
	if AuditCell(cands[3]) != "dropped:prefilter:exclude_location_unrecognised" {
		t.Errorf("cell %q", AuditCell(cands[3]))
	}
}

// Labels on pre-ledger rows are counted apart: their pool is a capped sample
// of an uncounted population, so they must not move any cell or be called
// "no longer in the pool".
func TestDropRecallCountsPrefilterLabelsApart(t *testing.T) {
	labels := map[posting.Key]Entry{
		"x:aa01": {Key: "x:aa01", Labels: map[string]string{WouldApply: "yes"}, Source: "dropped:prefilter:exclude_title", PSelect: 0.5},
		"x:aa02": {Key: "x:aa02", Labels: map[string]string{WouldApply: "no"}, Source: "dropped:prefilter:exclude_title", PSelect: 0.5},
	}
	members := []RecallMember{{Key: "gh:k:1", Cell: CellKept}, {Key: "gh:d:1", Cell: "dropped:title:other"}}
	out := BuildDropRecall(labels, members)
	if out.Prefilter != 2 || out.OutOfPool != 0 {
		t.Errorf("prefilter %d out of pool %d", out.Prefilter, out.OutOfPool)
	}
	for _, c := range out.Cells {
		if c.Judged != 0 {
			t.Errorf("cell %s judged %d from pre-ledger labels", c.Cell, c.Judged)
		}
	}
	if !strings.Contains(out.Markdown(), "dropped at listing time") {
		t.Errorf("the report does not say they are left out:\n%s", out.Markdown())
	}
}
