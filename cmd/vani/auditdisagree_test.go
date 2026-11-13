package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// --disagree draws the disagreement cells from the teacher's stored role_fit
// readings, on top of a random draw that is the same with or without them; a
// label drawn there records the cell, and `vani relevance` counts it apart and
// in the yes yield.
func TestAuditDisagreeDrawsTheCellsOverTheSameRandomDraw(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 4, 30)

	// Without a teacher log the cells cannot be drawn: an error, not an empty cell.
	if _, e := mustCode(t, 1, "audit", "--list", "--seed", "w1", "--disagree", "3"); !strings.Contains(e, "teacher readings log is not usable") {
		t.Errorf("stderr %q", e)
	}
	// With no --disagree the log is never needed, and never created.
	mustCode(t, 0, "audit", "--list", "--seed", "w1")
	if _, err := os.Stat(filepath.Join(state, eval.TeacherFile)); err == nil {
		t.Fatal("a draw without --disagree created the teacher log")
	}

	// The teacher wants every model-dropped posting (CellTeacherWants) and
	// rejects every kept one, so the top decile is CellTeacherRejects.
	var model, kept []string
	for i := 0; i < 30; i++ {
		model = append(model, fmt.Sprintf("gh:m:%03d", i))
		kept = append(kept, fmt.Sprintf("gh:k:%03d", i))
	}
	seedSonnet(t, state, model, func(int) string { return "strong_fit" })
	seedSonnet(t, state, kept, func(int) string { return "no_fit" })

	draw := func(args ...string) auditJSON {
		o, _ := mustCode(t, 0, append([]string{"--json", "audit", "--seed", "w1"}, args...)...)
		var d auditJSON
		decodeOne(t, o, &d)
		return d
	}
	base := draw()
	with := draw("--disagree", "4")
	var random, cell []auditPickJSON
	for _, p := range with.Picks {
		if eval.SelectedOnReading(p.Stratum) {
			cell = append(cell, p)
		} else {
			random = append(random, p)
		}
	}
	sortPicks := func(ps []auditPickJSON) []auditPickJSON {
		ps = slices.Clone(ps)
		slices.SortFunc(ps, func(a, b auditPickJSON) int { return strings.Compare(string(a.Key), string(b.Key)) })
		return ps
	}
	if !slices.Equal(sortPicks(random), sortPicks(base.Picks)) {
		t.Fatalf("the random picks changed with --disagree:\n got %v\nwant %v", random, base.Picks)
	}
	if len(cell) != 4 {
		t.Fatalf("%d cell picks, want 4: %v", len(cell), with.Picks)
	}
	var wantsPick, rejectsPick *auditPickJSON
	for i, p := range cell {
		switch {
		case p.Stratum == eval.CellTeacherWants && strings.HasPrefix(string(p.Key), "gh:m:"):
			wantsPick = &cell[i]
		case p.Stratum == eval.CellTeacherRejects && strings.HasPrefix(string(p.Key), "gh:k:"):
			rejectsPick = &cell[i]
		default:
			t.Errorf("cell pick %+v: want a model drop the teacher wants or a kept posting it rejects", p)
		}
	}
	if wantsPick == nil || rejectsPick == nil {
		t.Fatalf("both cells should be drawn: %v", cell)
	}

	// Answered through the draw lookup, the label records the cell.
	mustCode(t, 0, "audit", "answer", "--key", string(wantsPick.Key), "--verdict", "yes", "--seed", "w1", "--disagree", "4")
	if e := readLabels(t, state)[wantsPick.Key]; e.Source != eval.CellTeacherWants || e.PSelect != wantsPick.PSelect {
		t.Fatalf("label %+v, want source %s p %v", e, eval.CellTeacherWants, wantsPick.PSelect)
	}
	// The same lookup without --disagree does not find a cell pick.
	if _, e := mustCode(t, 1, "audit", "answer", "--key", string(rejectsPick.Key), "--verdict", "no", "--seed", "w1"); !strings.Contains(e, "not in the draw") {
		t.Errorf("stderr %q", e)
	}
	mustCode(t, 0, "audit", "answer", "--key", string(rejectsPick.Key), "--verdict", "no", "--seed", "w1", "--disagree", "4")
	// One random kept pick, answered yes.
	var randomKept *auditPickJSON
	for i, p := range random {
		if strings.HasPrefix(p.Stratum, "decile:") {
			randomKept = &random[i]
			break
		}
	}
	if randomKept == nil {
		t.Fatalf("no random kept pick: %v", random)
	}
	mustCode(t, 0, "audit", "answer", "--key", string(randomKept.Key), "--verdict", "yes", "--seed", "w1")

	o, _ := mustCode(t, 0, "--json", "relevance")
	var rep relevanceReply
	decodeOne(t, o, &rep)
	if rep.Relevance.Selected != 2 || rep.Relevance.Judged != 1 {
		t.Errorf("relevance selected %d judged %d, want 2 and 1", rep.Relevance.Selected, rep.Relevance.Judged)
	}
	if rep.Readers == nil || rep.Readers.Selected != 2 || rep.Readers.Judged != 1 {
		t.Errorf("readers %+v, want 2 selected and 1 judged", rep.Readers)
	}
	if rep.DropRecall == nil || rep.DropRecall.Selected != 2 {
		t.Errorf("drop recall %+v, want 2 selected", rep.DropRecall)
	}
	// The seniority shadow reads the sampled labels only: the cell "no" on a
	// kept posting is not one of its negatives.
	if rep.SeniorityShadow == nil {
		t.Fatal("no seniority shadow")
	}
	for _, v := range rep.SeniorityShadow.Variants {
		for _, a := range v.AUC {
			if a.Neg != 0 {
				t.Errorf("shadow %s %s counts %d negatives; the only no is a cell label", v.Name, a.Mode, a.Neg)
			}
		}
	}
	rows := map[string]eval.YieldRow{}
	for _, r := range rep.YesYield.Rows {
		rows[r.Source] = r
	}
	if rows[eval.CellTeacherWants].Yes != 1 || rows[eval.CellTeacherRejects].N != 1 || rep.YesYield.Random.N != 1 {
		t.Errorf("yes yield %+v", rep.YesYield)
	}
}

// A posting in a stratum the auditor leaves out (work authorization, work
// mode, an owner exclusion) or a closed one is never read for the cells, so it
// is never drawn because of a reading nor shown as an exemplar from one; an
// unread posting stays unread when the teacher has no row for it.
func TestTeacherViewsSkipLeftOutStrata(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	keys := []string{"gh:a:spon", "gh:a:excl", "gh:a:wm", "gh:a:dead", "gh:a:role", "gh:a:kept", "gh:a:rej"}
	seedSonnet(t, state, keys, func(i int) string {
		if keys[i] == "gh:a:rej" {
			return "no_fit"
		}
		return "strong_fit"
	})
	cands := []eval.AuditCandidate{
		{Key: "gh:a:spon", Dropped: true, Class: "title:sponsorship"},
		{Key: "gh:a:excl", Dropped: true, Class: "exclude:title"},
		{Key: "gh:a:wm", Dropped: true, Class: "model:work_mode"},
		{Key: "gh:a:dead", Closed: true, Class: "dead"},
		{Key: "gh:a:role", Dropped: true, Class: "title:role_fit"},
		{Key: "gh:a:kept", Score: 1},
		{Key: "gh:a:rej", Score: 2},
		{Key: "gh:a:none", Score: 3, Teacher: eval.TeacherWants}, // stale view, no row: reset
	}
	e := &env{stateDir: state}
	if err := e.teacherViews(cands, sonnetReader); err != nil {
		t.Fatal(err)
	}
	want := []eval.TeacherView{eval.TeacherUnread, eval.TeacherUnread, eval.TeacherUnread, eval.TeacherUnread,
		eval.TeacherWants, eval.TeacherWants, eval.TeacherRejects, eval.TeacherUnread}
	for i, c := range cands {
		if c.Teacher != want[i] {
			t.Errorf("%s: view %v, want %v", c.Key, c.Teacher, want[i])
		}
	}
	// A model with no row for any candidate (a misspelt --teacher) is an
	// error, not cells with no disagreement, and leaves every view unread.
	if err := e.teacherViews(cands, "some-other-model"); err == nil || !strings.Contains(err.Error(), "no some-other-model reading") {
		t.Fatalf("err %v", err)
	}
	for _, c := range cands {
		if c.Teacher != eval.TeacherUnread {
			t.Errorf("%s read under another model: %v", c.Key, c.Teacher)
		}
	}
}

// The auditor is compared with the owner on the judged set; a cell label was
// drawn because a teacher's reading disagreed with the engine, so it is left
// out and counted, and a cell skip is neither judged nor selected.
func TestAuditorJudgedSetLeavesCellLabelsOut(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	answer := func(key, verdict, source string) {
		mustCode(t, 0, "audit", "answer", "--key", key, "--verdict", verdict, "--source", source, "--p-select", "0.5")
	}
	answer("gh:k:001", "yes", "decile:3")
	answer("gh:k:002", "yes", eval.CellTeacherWants)
	answer("gh:k:003", "no", eval.CellTeacherRejects)
	answer("gh:k:004", eval.Skip, eval.CellTeacherWants)
	j, err := (&env{stateDir: state}).auditorJudgedSet()
	if err != nil {
		t.Fatal(err)
	}
	if j.labelled != 1 || j.selected != 2 || len(j.labels) != 1 {
		t.Fatalf("labelled %d selected %d labels %v; want the decile answer only, 2 selected", j.labelled, j.selected, j.labels)
	}
	if _, ok := j.labels["gh:k:001"]; !ok || slices.Contains(j.keys, "gh:k:002") {
		t.Errorf("keys %v", j.keys)
	}
}

func TestAuditDisagreeFlagBounds(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	for _, args := range [][]string{
		{"--disagree", "-1"},
		{"--disagree", fmt.Sprint(maxAuditQuota + 1)},
		{"--scored", "1", "--dropped", "0", "--closed", "0", "--disagree", "3"}, // under a third random
	} {
		mustCode(t, 2, append([]string{"audit", "--list"}, args...)...)
		mustCode(t, 2, append([]string{"audit", "answer", "--key", "gh:k:001", "--verdict", "no"}, args...)...)
	}
}
