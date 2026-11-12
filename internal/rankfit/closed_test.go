package rankfit

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
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
	if len(got) != 2 || c.Yes != 1 || c.No != 1 || c.Maybe != 0 || c.Closed != 2 || c.Skipped != 1 {
		t.Errorf("targets %d, counts %+v; want the decile and dropped judgments only, 2 closed, 1 skipped", len(got), c)
	}
}

// Every audit card is also logged as an impression (recordAuditCards, the Mac
// app's Audit tab), and rankfit passes every impression to Targets. A closed
// judgment is left out of the fit on purpose, so its card's impression must not
// bring it back as a "shown and left unmarked" negative: an owner "yes" on a
// closed:dead card would otherwise train as a no.
func TestTargetsClosedJudgmentIsNotAPassedNegative(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	wa := func(v, src string, p float64) eval.Entry {
		return eval.Entry{Labels: map[string]string{eval.WouldApply: v}, Source: src, PSelect: p}
	}
	labels := map[posting.Key]eval.Entry{
		"gh:a:dead":      wa("yes", eval.ClosedPrefix+"dead", 0.0004),
		"gh:a:dup":       wa("no", eval.ClosedPrefix+"duplicate", 0.05),
		"gh:a:deadskip":  wa(eval.Skip, eval.ClosedPrefix+"dead", 0.0004),
		"gh:a:auditskip": wa(eval.Skip, "decile:3", 0.001),
	}
	shown := []feedback.Impression{
		// The audit cards themselves.
		{At: at, SlateID: "seed", Surface: feedback.SurfaceAudit, Key: "gh:a:dead", Position: 1},
		{At: at, SlateID: "seed", Surface: feedback.SurfaceAudit, Key: "gh:a:dup", Position: 2},
		{At: at, SlateID: "seed", Surface: feedback.SurfaceAudit, Key: "gh:a:deadskip", Position: 3},
		{At: at, SlateID: "seed", Surface: feedback.SurfaceAudit, Key: "gh:a:auditskip", Position: 4},
		// The closed posting was also in a digest before it died.
		{At: at.Add(-48 * time.Hour), SlateID: "2026-11-10", Surface: feedback.SurfaceDigest, Key: "gh:a:dead", Position: 7},
		// An audit card the owner has not answered yet is not a pass either.
		{At: at, SlateID: "seed", Surface: feedback.SurfaceAudit, Key: "gh:a:unanswered", Position: 5},
		// A posting a digest showed and the owner left unmarked is still a pass.
		{At: at, SlateID: "2026-11-12", Surface: feedback.SurfaceDigest, Key: "gh:a:passed", Position: 3},
	}
	got, c, err := Targets(labels, shown, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []posting.Key{"gh:a:dead", "gh:a:dup", "gh:a:deadskip", "gh:a:auditskip", "gh:a:unanswered"} {
		if tg, ok := got[k]; ok {
			t.Errorf("%s became a target %+v; a judged or audit-only posting is never a passed negative", k, tg)
		}
	}
	if tg, ok := got["gh:a:passed"]; !ok || tg.Grade != GradeNo || tg.Judged {
		t.Errorf("a digest row left unmarked: %+v, %v; want a passed no", tg, ok)
	}
	if c.Passed != 1 || c.Closed != 2 || c.Skipped != 2 {
		t.Errorf("counts %+v; want 1 passed, 2 closed, 2 skipped", c)
	}
}
