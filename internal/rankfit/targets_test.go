package rankfit

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestTargets(t *testing.T) {
	t.Parallel()
	at := func(h int) time.Time { return time.Date(2026, 10, 20, h, 0, 0, 0, time.UTC) }
	sc := func(p float64) feedback.Scores {
		return feedback.Scores{Priority: p, Factors: map[string]float64{"gate": 1, "fresh": 1}}
	}
	wa := func(v string) eval.Entry { return eval.Entry{Labels: map[string]string{eval.WouldApply: v}} }
	labels := map[posting.Key]eval.Entry{
		"gh:a:yes":       wa("yes"),
		"gh:a:maybe":     wa("maybe"),
		"gh:a:overrules": wa("no"),
		"gh:a:skip":      wa(eval.Skip),
		"gh:a:skipsaved": wa(eval.Skip),
		// A triage-slot label is not a judgment: the posting falls through to
		// its mark.
		"gh:a:slotonly": {Labels: map[string]string{"role_fit": "strong_fit"}},
	}
	shown := []feedback.Impression{
		{At: at(1), Key: "gh:a:passed", Scores: sc(50)},
		{At: at(3), Key: "gh:a:passed", Scores: sc(51)}, // newest impression's scores
		{At: at(2), Key: "gh:a:passed", Scores: sc(49)},
		{At: at(1), Key: "gh:a:saved", Scores: sc(60)},
		{At: at(1), Key: "gh:a:skip", Scores: sc(70)},  // a skip labels nothing; shown, it is passed
		{At: at(1), Key: "gh:a:maybe", Scores: sc(30)}, // a judgment takes the scores the owner was shown
	}
	m := map[posting.Key]marks.Mark{
		"gh:a:saved": marks.Saved, "gh:a:applied": marks.Applied, "gh:a:hidden": marks.Hidden,
		"gh:a:overrules": marks.Saved, "gh:a:skipsaved": marks.Saved, "gh:a:unshown": marks.None,
		"gh:a:slotonly": marks.Hidden,
	}
	got, c, err := Targets(labels, shown, m)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		grade    int
		judged   bool
		priority float64 // -1: no scores
	}
	wants := map[posting.Key]want{
		"gh:a:yes":       {GradeYes, true, -1},
		"gh:a:maybe":     {GradeMaybe, true, 30},
		"gh:a:overrules": {GradeNo, true, -1},
		"gh:a:saved":     {GradeYes, false, 60},
		"gh:a:applied":   {GradeYes, false, -1},
		"gh:a:hidden":    {GradeNo, false, -1},
		"gh:a:skipsaved": {GradeYes, false, -1},
		"gh:a:slotonly":  {GradeNo, false, -1},
		"gh:a:passed":    {GradeNo, false, 51},
		"gh:a:skip":      {GradeNo, false, 70},
	}
	if len(got) != len(wants) {
		t.Fatalf("got %d targets, want %d: %+v", len(got), len(wants), got)
	}
	for k, w := range wants {
		g, ok := got[k]
		p := -1.0
		if g.Scores != nil {
			p = g.Scores.Priority
		}
		if !ok || g.Grade != w.grade || g.Judged != w.judged || p != w.priority {
			t.Errorf("%s: %+v (priority %v), want %+v", k, g, p, w)
		}
	}
	if _, ok := got["gh:a:unshown"]; ok {
		t.Error("an unmarked posting nothing showed became a label")
	}
	if c != (TargetCounts{Yes: 1, Maybe: 1, No: 1, Saved: 3, Hidden: 2, Passed: 2, Skipped: 2}) || c.Judged() != 3 {
		t.Errorf("counts %+v", c)
	}
	if got, c, err := Targets(nil, nil, nil); err != nil || len(got) != 0 || c != (TargetCounts{}) {
		t.Error("nothing in gave targets")
	}
}
