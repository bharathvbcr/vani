package rankfit

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A judgment drawn from a disagreement cell was chosen because a model's
// reading disagreed with the score; weighted into the fit it would pull the
// weights toward that model. It stays out, is counted apart, and a digest row
// of the same posting never brings it back as a passed negative.
func TestTargetsLeaveDisagreementCellsOut(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	wa := func(v, src string, p float64) eval.Entry {
		return eval.Entry{Labels: map[string]string{eval.WouldApply: v}, Source: src, PSelect: p}
	}
	labels := map[posting.Key]eval.Entry{
		"gh:a:rand":  wa("yes", "decile:2", 0.05),
		"gh:a:want":  wa("yes", eval.CellTeacherWants, 0.4),
		"gh:a:rej":   wa("no", eval.CellTeacherRejects, 0.4),
		"gh:a:wskip": wa(eval.Skip, eval.CellTeacherWants, 0.4),
	}
	shown := []feedback.Impression{
		{At: at, SlateID: "2026-11-13", Surface: feedback.SurfaceDigest, Key: "gh:a:rej", Position: 1},
		{At: at, SlateID: "2026-11-13", Surface: feedback.SurfaceDigest, Key: "gh:a:wskip", Position: 2},
	}
	got, c, err := Targets(labels, shown, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []posting.Key{"gh:a:want", "gh:a:rej", "gh:a:wskip"} {
		if tg, ok := got[k]; ok {
			t.Errorf("%s from a disagreement cell became a target %+v", k, tg)
		}
	}
	if len(got) != 1 || c.Yes != 1 || c.Selected != 2 || c.Skipped != 1 || c.Passed != 0 {
		t.Errorf("targets %d, counts %+v; want the decile judgment only, 2 selected, 1 skipped, 0 passed", len(got), c)
	}
}

// An engineering-group judgment and a yes-neighbour one stay out of the fit
// too (until the weights are post-stratified): counted apart, never a passed
// negative through a digest row; a skip is a skip.
func TestTargetsLeaveEngineeringAndNeighboursOut(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	wa := func(v, src string, p float64) eval.Entry {
		return eval.Entry{Labels: map[string]string{eval.WouldApply: v}, Source: src, PSelect: p}
	}
	labels := map[posting.Key]eval.Entry{
		"gh:a:rand":  wa("no", "decile:2", 0.05),
		"gh:a:eng":   wa("yes", eval.EngineeringKept, 0.1),
		"gh:a:engd":  wa("no", eval.EngineeringDropped, 0.1),
		"gh:a:eskip": wa(eval.Skip, eval.EngineeringKept, 0.1),
		"gh:a:near":  wa("yes", eval.CellYesNeighbour, 0.3),
	}
	shown := []feedback.Impression{
		{At: at, SlateID: "2026-11-13", Surface: feedback.SurfaceDigest, Key: "gh:a:engd", Position: 1},
		{At: at, SlateID: "2026-11-13", Surface: feedback.SurfaceDigest, Key: "gh:a:eskip", Position: 2},
	}
	got, c, err := Targets(labels, shown, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || c.No != 1 || c.Engineering != 2 || c.Selected != 1 || c.Skipped != 1 || c.Passed != 0 {
		t.Errorf("targets %v, counts %+v; want the decile judgment only, 2 engineering, 1 selected, 1 skipped, 0 passed", got, c)
	}
	// A bad p_select on an engineering label is still refused, not hidden.
	labels["gh:a:bad"] = wa("yes", eval.EngineeringKept, 0)
	if _, _, err := Targets(labels, nil, nil); err == nil {
		t.Error("an engineering label with p_select 0 was accepted")
	}
}
