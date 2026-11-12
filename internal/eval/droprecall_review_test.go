package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A label the digest's Explore section wrote (source "explore:dropped",
// "explore:below_fold") was never drawn from an audit stratum, so it is left
// out, but under its own name: it did not "move". Before, it was counted as
// drawn from another stratum than the posting sits in now.
func TestDropRecallNamesExploreLabelsApartFromMovedOnes(t *testing.T) {
	members := append(pool("gh:k", CellKept, 50), pool("gh:m", "dropped:model:role_fit", 50)...)
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:4", 0, 3, 1, 0, 0.01)
	judge(labels, "gh:m", "dropped:model:role_fit", 0, 3, 0, 0, 0.01)
	labels["gh:k:0010"] = Entry{Labels: map[string]string{WouldApply: "yes"}, Source: "explore:below_fold", PSelect: 0.02}
	labels["gh:m:0010"] = Entry{Labels: map[string]string{WouldApply: "maybe"}, Source: "explore:dropped", PSelect: 0.02}
	labels["gh:m:0011"] = Entry{Labels: map[string]string{WouldApply: "no"}, Source: "decile:3", PSelect: 0.02} // a real move
	d := BuildDropRecall(labels, members)
	if d.NotAudit != 2 || d.Moved != 1 {
		t.Errorf("not from an audit stratum %d, moved %d; want 2 and 1", d.NotAudit, d.Moved)
	}
	if md := d.Markdown(); !strings.Contains(md, "2 not drawn from an audit stratum") {
		t.Errorf("markdown does not name them:\n%s", md)
	}
}

// A label drawn from a drop class before exclusions had a class of their own
// ("dropped:model:role_fit" on a posting an exclusion dropped, which kept the
// model's earlier reading) sits in a cell that was renamed, not moved: the
// posting's drop cause is the one it had when drawn. It still counts in its
// cell. A posting that really moved (drawn kept, dropped now) stays out.
func TestDropRecallCountsLabelsOfARenamedClassInTheirCell(t *testing.T) {
	const now, was = "dropped:exclude:company", "dropped:model:role_fit"
	members := append(pool("gh:k", CellKept, 50), pool("gh:x", now, 20)...)
	for i := range members {
		if members[i].Cell == now {
			members[i].LegacyCell = was
		}
	}
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:4", 0, 3, 1, 0, 0.01)
	judge(labels, "gh:x", was, 0, 3, 0, 1, 0.01) // drawn under the old name
	labels["gh:x:0009"] = Entry{Labels: map[string]string{WouldApply: "no"}, Source: "decile:2", PSelect: 0.01}
	d := BuildDropRecall(labels, members)
	c := cellOf(t, d, now)
	if c.Judged != 3 || c.Wanted.K != 1 {
		t.Errorf("renamed cell %+v; want its 3 labels counted, 1 wanted", c)
	}
	if d.Renamed != 3 || d.Moved != 1 {
		t.Errorf("renamed %d, moved %d; want 3 and 1", d.Renamed, d.Moved)
	}
}

// Postings the owner saved or applied to inside a drop class are known misses.
// The class's bound must count them: 59 "no" labels beside 2 saved drops is not
// a class under the 5% target, and the labels it still needs are a number, not
// "n/a" (one miss needs about 93 labels in all, not infinitely many).
func TestDropRecallBoundCountsKnownMissesFromMarks(t *testing.T) {
	const cls = "dropped:model:seniority"
	members := append(pool("gh:k", CellKept, 200), pool("gh:s", cls, 300)...)
	members[200].Marked, members[200].MarkedWanted = true, true
	members[201].Marked, members[201].MarkedWanted = true, true
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:4", 0, 5, 1, 1, 0.01)
	judge(labels, "gh:s", cls, 2, 59, 0, 0, 0.01)
	d := BuildDropRecall(labels, members)
	c := cellOf(t, d, cls)
	if c.KnownMisses != 2 {
		t.Errorf("known misses %d, want 2", c.KnownMisses)
	}
	if c.UpperBound <= RecallBoundTarget {
		t.Errorf("bound %.4f with 2 known misses reads as under the %.0f%% target", c.UpperBound, 100*RecallBoundTarget)
	}
	if c.LabelsFor5pct <= 0 {
		t.Errorf("labels for a 5%% bound %d; want a positive count", c.LabelsFor5pct)
	}
	if md := d.Markdown(); strings.Contains(md, "| "+cls+" | 59 | 0 | 4.9% | 0 |") {
		t.Errorf("markdown reads as met:\n%s", md)
	}
}

// One wanted label: the bound reaches the target at about 93 labels in all.
func TestLabelsForBoundWithAMiss(t *testing.T) {
	need := labelsForBound(1, 0)
	if need < 85 || need > 100 {
		t.Fatalf("labelsForBound(1, 0) = %d; want about 93", need)
	}
	if upperBound(1, need, recallAlpha) > RecallBoundTarget || upperBound(1, need-1, recallAlpha) <= RecallBoundTarget {
		t.Errorf("%d is not the smallest n with a bound at or under the target", need)
	}
	if got := labelsForBound(1, need+10); got != 0 {
		t.Errorf("past the need: %d", got)
	}
	if got := labelsForBound(2, 30); got <= labelsForBound(1, 30) {
		t.Errorf("two misses need no more than one: %d", got)
	}
}

// A cell whose Jeffreys prior alone puts its 0-of-n mean at or above the 5%
// target (0.5/(n+1) >= 0.05, so n <= 9) is prior-dominated: its median reflects
// the prior more than the labels, and the report says so.
func TestDropRecallMarksPriorDominatedCells(t *testing.T) {
	members := append(pool("gh:k", CellKept, 400), pool("gh:m", "dropped:model:role_fit", 4000)...)
	members = append(members, pool("gh:t", "dropped:title:role_fit", 400)...)
	labels := map[posting.Key]Entry{}
	judge(labels, "gh:k", "decile:4", 0, 3, 0, 1, 0.01)
	judge(labels, "gh:m", "dropped:model:role_fit", 0, 3, 0, 0, 0.01)  // 0 of 3: prior-dominated
	judge(labels, "gh:t", "dropped:title:role_fit", 0, 10, 0, 0, 0.01) // 0 of 10: not
	d := BuildDropRecall(labels, members)
	if !cellOf(t, d, "dropped:model:role_fit").PriorDominated || cellOf(t, d, "dropped:title:role_fit").PriorDominated {
		t.Errorf("prior-dominated flags wrong: %+v", d.Cells)
	}
	md := d.Markdown()
	for _, frag := range []string{"prior-dominated", "stable"} {
		if !strings.Contains(md, frag) {
			t.Errorf("markdown lacks %q:\n%s", frag, md)
		}
	}
}
