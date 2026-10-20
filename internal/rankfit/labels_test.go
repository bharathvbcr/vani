package rankfit

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestTargets(t *testing.T) {
	m := map[posting.Key]marks.Mark{
		"gh:a:saved": marks.Saved, "gh:a:applied": marks.Applied, "gh:a:hidden": marks.Hidden,
		"gh:a:none": marks.None, "gh:a:overruled": marks.Saved,
	}
	labels := map[posting.Key]eval.Entry{
		"gh:a:overruled": {Key: "gh:a:overruled", Labels: map[string]string{WouldApplyKey: WouldApplyNo}},
		"gh:a:judged":    {Key: "gh:a:judged", Labels: map[string]string{WouldApplyKey: WouldApplyYes, "role_fit": "strong_fit"}},
		"gh:a:skipped":   {Key: "gh:a:skipped", Labels: map[string]string{WouldApplyKey: eval.Skip}},
		"gh:a:slotonly":  {Key: "gh:a:slotonly", Labels: map[string]string{"role_fit": "adjacent"}},
		"gh:a:garbage":   {Key: "gh:a:garbage", Labels: map[string]string{WouldApplyKey: "maybe"}},
	}
	got := Targets(labels, m)
	want := map[posting.Key]Target{
		"gh:a:saved": {Positive: true}, "gh:a:applied": {Positive: true}, "gh:a:hidden": {},
		"gh:a:overruled": {Judged: true}, "gh:a:judged": {Positive: true, Judged: true},
	}
	if len(got) != len(want) {
		t.Fatalf("targets %+v, want %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %+v, want %+v", k, got[k], v)
		}
	}
	if len(Targets(nil, nil)) != 0 {
		t.Error("no labels and no marks gave targets")
	}
}

func rec(key string, ann map[string]string) ledger.Record {
	return ledger.Record{Key: posting.Key(key), Annotations: ann}
}

func TestPlattSamples(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", map[string]string{
			"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "0.8000",
			"triage.work_mode": "remote", "triage.work_mode.reading": "answered", "triage.work_mode.backend": "claude",
		}),
		rec("gh:a:2", map[string]string{
			"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "NaN",
			"triage.work_mode": "remote", "triage.work_mode.reading": "unsure", "triage.work_mode.backend": "claude",
		}),
		rec("gh:a:3", map[string]string{"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered"}), // no backend
	}
	labels := map[posting.Key]eval.Entry{
		"gh:a:1":    {Labels: map[string]string{"role_fit": "strong_fit", "work_mode": "onsite", WouldApplyKey: WouldApplyYes}},
		"gh:a:2":    {Labels: map[string]string{"role_fit": "partial_fit", "work_mode": "remote"}},
		"gh:a:3":    {Labels: map[string]string{"role_fit": "strong_fit"}},
		"gh:a:gone": {Labels: map[string]string{"role_fit": "strong_fit"}},
		"gh:a:skip": {Labels: map[string]string{"role_fit": eval.Skip}},
	}
	got, missing := PlattSamples(recs, labels)
	if missing != 2 {
		t.Errorf("missing %d, want 2 (gone, skip)", missing)
	}
	d1 := got["d1-ultra"]
	if len(d1) != 2 || !d1[0].Correct || !d1[0].HasP || d1[0].P != 0.8 || d1[1].Correct {
		t.Errorf("d1-ultra samples %+v", d1)
	}
	// "NaN" parses as a float; FitPlatt treats it as no valid p.
	if cl := got["claude"]; len(cl) != 1 || cl[0].Correct || cl[0].HasP {
		t.Errorf("claude samples %+v (the unsure reading is not one)", cl)
	}
	if len(got) != 2 {
		t.Errorf("backends %v", got)
	}
}
