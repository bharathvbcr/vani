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

	// The teacher wants every model-dropped posting: all in CellTeacherWants.
	var model []string
	for i := 0; i < 30; i++ {
		model = append(model, fmt.Sprintf("gh:m:%03d", i))
	}
	seedSonnet(t, state, model, func(int) string { return "strong_fit" })

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
	for _, p := range cell {
		if p.Stratum != eval.CellTeacherWants || !strings.HasPrefix(string(p.Key), "gh:m:") {
			t.Errorf("cell pick %+v: want a model-dropped posting in %s", p, eval.CellTeacherWants)
		}
	}

	// Answered through the draw lookup, the label records the cell.
	mustCode(t, 0, "audit", "answer", "--key", string(cell[0].Key), "--verdict", "yes", "--seed", "w1", "--disagree", "4")
	if e := readLabels(t, state)[cell[0].Key]; e.Source != eval.CellTeacherWants || e.PSelect != cell[0].PSelect {
		t.Fatalf("label %+v, want source %s p %v", e, eval.CellTeacherWants, cell[0].PSelect)
	}
	// The same lookup without --disagree does not find a cell pick.
	if _, e := mustCode(t, 1, "audit", "answer", "--key", string(cell[1].Key), "--verdict", "no", "--seed", "w1"); !strings.Contains(e, "not in the draw") {
		t.Errorf("stderr %q", e)
	}

	o, _ := mustCode(t, 0, "--json", "relevance")
	var rep relevanceReply
	decodeOne(t, o, &rep)
	if rep.Relevance.Selected != 1 || rep.Relevance.Judged != 0 {
		t.Errorf("relevance selected %d judged %d, want 1 and 0", rep.Relevance.Selected, rep.Relevance.Judged)
	}
	if rep.YesYield == nil || len(rep.YesYield.Rows) != 1 || rep.YesYield.Rows[0].Source != eval.CellTeacherWants || rep.YesYield.Rows[0].Yes != 1 {
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
	// Another model's readings define nothing.
	if err := e.teacherViews(cands, "some-other-model"); err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Teacher != eval.TeacherUnread {
			t.Errorf("%s read under another model: %v", c.Key, c.Teacher)
		}
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
