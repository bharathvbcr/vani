package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

func TestItemsUnderRemapsGradesPerMode(t *testing.T) {
	items := []rankfit.Item{
		{Key: "y", Grade: rankfit.GradeYes, Judged: true},
		{Key: "m", Grade: rankfit.GradeMaybe, Judged: true},
		{Key: "n", Grade: rankfit.GradeNo, Judged: true},
		{Key: "mark", Grade: rankfit.GradeYes}, // a mark: never a judgment
	}
	eng := func(k posting.Key) bool { return k == "y" }
	grades := func(m eval.LabelMode) map[string]int {
		out := map[string]int{}
		for _, it := range itemsUnder(items, m, eng) {
			out[it.Key] = it.Grade
		}
		return out
	}
	for m, want := range map[eval.LabelMode]map[string]int{
		eval.ModeYesOrMaybe:     {"y": rankfit.GradeYes, "m": rankfit.GradeMaybe, "n": rankfit.GradeNo},
		eval.ModeYesOnly:        {"y": rankfit.GradeYes, "m": rankfit.GradeNo, "n": rankfit.GradeNo},
		eval.ModeMaybesExcluded: {"y": rankfit.GradeYes, "n": rankfit.GradeNo},
		eval.ModeEngineering:    {"y": rankfit.GradeYes},
	} {
		g := grades(m)
		if len(g) != len(want) {
			t.Errorf("%s: %v, want %v", m, g, want)
		}
		for k, v := range want {
			if g[k] != v {
				t.Errorf("%s %s: %d, want %d", m, k, g[k], v)
			}
		}
	}
	if items[1].Grade != rankfit.GradeMaybe {
		t.Error("itemsUnder modified its input")
	}
}

func TestRelevanceReportsSensitivityAcrossModes(t *testing.T) {
	auditedState(t)
	o, _ := mustCode(t, 0, "relevance", "--json")
	var r relevanceReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.Sensitivity == nil || len(r.Sensitivity.Modes) != len(eval.LabelModes) {
		t.Fatalf("sensitivity: %+v", r.Sensitivity)
	}
	byMode := map[eval.LabelMode]sensitivityMode{}
	for _, m := range r.Sensitivity.Modes {
		byMode[m.Mode] = m
	}
	base, yesOnly, noMaybe := byMode[eval.ModeYesOrMaybe], byMode[eval.ModeYesOnly], byMode[eval.ModeMaybesExcluded]
	// yes_or_maybe repeats the headline readers.
	if base.Readers.Judged != r.Readers.Judged {
		t.Errorf("yes_or_maybe judged %d, headline %d", base.Readers.Judged, r.Readers.Judged)
	}
	// yes_only keeps every judged posting and moves the maybes to no.
	if yesOnly.Readers.Judged != base.Readers.Judged || yesOnly.Readers.Maybe != 0 || yesOnly.Readers.No != base.Readers.No+base.Readers.Maybe {
		t.Errorf("yes_only: %+v against %+v", yesOnly.Readers, base.Readers)
	}
	// maybes_excluded drops them.
	if noMaybe.Readers.Judged != base.Readers.Judged-base.Readers.Maybe || noMaybe.Readers.Maybe != 0 {
		t.Errorf("maybes_excluded: %+v against %+v", noMaybe.Readers, base.Readers)
	}
	for _, m := range r.Sensitivity.Modes {
		if r.Sensitivity.PartAUCError == "" && m.PartAUC == nil {
			t.Errorf("%s: no part AUC and no reason", m.Mode)
		}
	}
	text, _ := mustCode(t, 0, "relevance")
	for _, frag := range []string{"## Sensitivity", "| engineering_only |", "Readers, role_fit sensitivity", "Score parts, AUC where the part was known"} {
		if !strings.Contains(text, frag) {
			t.Errorf("report lacks %q", frag)
		}
	}
}
