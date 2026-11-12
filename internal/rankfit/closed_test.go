package rankfit

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The audit's closed strata (closed:dead, closed:duplicate) measure liveness
// and the duplicate fold, not the ranking: a dead or folded posting is not in
// anything the ranking orders now, and a duplicate is a copy of a posting that
// may be judged in its own right. Their judgments stay out of the weight fit
// and are counted apart, so adding the strata leaves the fit's population as
// it was.
func TestTargetsLeaveClosedStrataOut(t *testing.T) {
	t.Parallel()
	wa := func(v, src string, p float64) eval.Entry {
		return eval.Entry{Labels: map[string]string{eval.WouldApply: v}, Source: src, PSelect: p}
	}
	labels := map[posting.Key]eval.Entry{
		"gh:a:1": wa("yes", "decile:5", 0.01),
		"gh:a:2": wa("no", "dropped:model:role_fit", 0.002),
		"gh:a:3": wa("yes", eval.ClosedPrefix+"dead", 0.0004),
		"gh:a:4": wa("maybe", eval.ClosedPrefix+"duplicate", 0.05),
		"gh:a:5": wa("skip", eval.ClosedPrefix+"dead", 0.0004),
	}
	got, c, err := Targets(labels, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["gh:a:3"]; ok {
		t.Error("a closed:dead judgment entered the fit")
	}
	if _, ok := got["gh:a:4"]; ok {
		t.Error("a closed:duplicate judgment entered the fit")
	}
	if len(got) != 2 || c.Yes != 1 || c.No != 1 || c.Maybe != 0 || c.Closed != 3 {
		t.Errorf("targets %d, counts %+v; want the decile and dropped judgments only, 3 closed", len(got), c)
	}
}
