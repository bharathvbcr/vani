package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// Every overlay is on by default (owner, 2026-11-13). A default quota is
// soft: an overlay whose inputs do not exist yet draws nothing and the draw
// names it, while the random draw and the overlays that can run go ahead.
// The same quota given on the command line still fails closed.
func TestAuditOverlaysOnByDefaultSkipWhatCannotRun(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 4, 30) // every title is "Engineer ...": all engineering

	o, _ := mustCode(t, 0, "audit", "--list", "--seed", "w1")
	for _, want := range []string{
		"overlay not drawn: disagreement cells: audit --disagree: the teacher readings log is not usable",
		"overlay not drawn: yes-neighbour cell: audit --yes-neighbours: there is no vector store yet",
		fmt.Sprintf("+ %d engineering + 0 teacher-wants + 0 teacher-rejects + 0 yes-neighbours", defaultEngineering),
	} {
		if !strings.Contains(o, want) {
			t.Errorf("default draw lacks %q:\n%s", want, o)
		}
	}

	d := overlayDrawSeed(t, "w1")
	if len(d.Skipped) != 2 {
		t.Errorf("skipped %q, want the cells and the neighbour cell", d.Skipped)
	}
	eng := 0
	for _, p := range d.Picks {
		if eval.EngineeringStratum(p.Stratum) {
			eng++
		}
		if eval.SelectedOnReading(p.Stratum) {
			t.Errorf("a skipped cell drew %+v", p)
		}
	}
	if eng != defaultEngineering {
		t.Errorf("%d engineering picks by default, want %d", eng, defaultEngineering)
	}
	// The random draw is the one the overlays sit over.
	if !maps.Equal(randomPicks(d), randomPicks(overlayDrawSeed(t, "w1", noOverlays...))) {
		t.Error("the default overlays changed the random draw")
	}

	// Given explicitly, the same quotas fail closed.
	if _, e := mustCode(t, 1, "audit", "--list", "--seed", "w1", "--disagree", "5"); !strings.Contains(e, "teacher readings log is not usable") {
		t.Errorf("explicit --disagree: %q", e)
	}
	if _, e := mustCode(t, 1, "audit", "--list", "--seed", "w1", "--yes-neighbours", "3"); !strings.Contains(e, "no vector store yet") {
		t.Errorf("explicit --yes-neighbours: %q", e)
	}
	// --soft-overlays makes given quotas soft too (the Mac app's steppers):
	// skipped and named where they cannot run, drawn as given where they can.
	o, _ = mustCode(t, 0, "audit", "--list", "--seed", "w1", "--soft-overlays", "--disagree", "4", "--yes-neighbours", "6", "--engineering", "5")
	for _, want := range []string{"overlay not drawn: disagreement cells:", "overlay not drawn: yes-neighbour cell:", "+ 5 engineering +"} {
		if !strings.Contains(o, want) {
			t.Errorf("--soft-overlays draw lacks %q:\n%s", want, o)
		}
	}
	// A misspelt --teacher stays an error even with the default quotas.
	seedSonnet(t, state, []string{"gh:m:000"}, func(int) string { return "strong_fit" })
	if _, e := mustCode(t, 1, "audit", "--list", "--seed", "w1", "--teacher", "no-such-model"); !strings.Contains(e, "no no-such-model reading") {
		t.Errorf("misspelt --teacher: %q", e)
	}
}

// With the teacher's readings in place the default draw holds both cells,
// each within its default quota, without a flag.
func TestAuditDefaultDrawsTheCellsWhenReadingsExist(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 4, 30)
	var model, kept []string
	for i := 0; i < 30; i++ {
		model = append(model, fmt.Sprintf("gh:m:%03d", i))
		kept = append(kept, fmt.Sprintf("gh:k:%03d", i))
	}
	seedSonnet(t, state, model, func(int) string { return "strong_fit" })
	seedSonnet(t, state, kept, func(int) string { return "no_fit" })

	d := overlayDrawSeed(t, "w1")
	wants, rejects := 0, 0
	for _, p := range d.Picks {
		switch p.Stratum {
		case eval.CellTeacherWants:
			wants++
		case eval.CellTeacherRejects:
			rejects++
		}
	}
	if wants != defaultDisagree || rejects < 1 || rejects > defaultDisagreeRejects {
		t.Errorf("default cells: %d wants (want %d), %d rejects (want 1-%d; the top decile holds 3)", wants, defaultDisagree, rejects, defaultDisagreeRejects)
	}
	for _, s := range d.Skipped {
		if strings.Contains(s, "disagreement cells") {
			t.Errorf("the cells were skipped with readings in place: %q", s)
		}
	}
}

// The kill rule turns a cell off at 20 judged labels with 0 or 1 yes. A
// default quota obeys it and says so; an explicit one is the owner's override.
func TestAuditDefaultObeysTheKillRule(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 4, 30)
	var model []string
	for i := 0; i < 30; i++ {
		model = append(model, fmt.Sprintf("gh:m:%03d", i))
	}
	seedSonnet(t, state, model, func(int) string { return "strong_fit" })
	for i := 0; i < eval.KillFirstLook; i++ {
		mustCode(t, 0, "audit", "answer", "--key", fmt.Sprintf("gh:m:%03d", i), "--verdict", "no", "--source", eval.CellTeacherWants, "--p-select", "1")
	}

	d := overlayDrawSeed(t, "w1")
	killed := false
	for _, s := range d.Skipped {
		killed = killed || strings.Contains(s, eval.CellTeacherWants+": the kill rule turned this cell off (20 judged, 0 yes)")
	}
	if !killed {
		t.Errorf("skipped %q, want the wants cell turned off by the kill rule", d.Skipped)
	}
	for _, p := range d.Picks {
		if p.Stratum == eval.CellTeacherWants {
			t.Fatalf("a killed cell drew %+v by default", p)
		}
	}
	forced := overlayDrawSeed(t, "w1", "--disagree", "3")
	n := 0
	for _, p := range forced.Picks {
		if p.Stratum == eval.CellTeacherWants {
			n++
		}
	}
	if n == 0 { // what is left of the cell after the random dropped strata
		t.Error("explicit --disagree 3 over a killed cell drew nothing")
	}
}

// A default cell quota is trimmed to the 2x cap with a note; an explicit one
// over the cap is still refused.
func TestAuditDefaultCellsAreTrimmedToTheCap(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 4, 30)
	o, _ := mustCode(t, 0, "audit", "--list", "--seed", "w1", "--scored", "1", "--dropped", "0", "--closed", "0")
	if !strings.Contains(o, fmt.Sprintf("the default --disagree %d was cut to 2", defaultDisagree)) ||
		!strings.Contains(o, fmt.Sprintf("the default --disagree-rejects %d was cut to 0", defaultDisagreeRejects)) {
		t.Errorf("no trim note:\n%s", o)
	}
	mustCode(t, 2, "audit", "--list", "--seed", "w1", "--scored", "1", "--dropped", "0", "--closed", "0", "--disagree", "3")
}

// A vector store that exists but cannot be read is an error even for the
// default neighbour quota: only a store that was never written is skipped.
func TestAuditDefaultNeighboursFailOnAnUnreadableStore(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 4, 30)
	if err := os.MkdirAll(filepath.Join(semanticDir(state), "vectors.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, e := mustCode(t, 1, "audit", "--list", "--seed", "w1"); !strings.Contains(e, "the vector store could not be read") {
		t.Errorf("stderr %q", e)
	}
}

// A cell that drew fewer picks than its quota is named: its pool is running
// out, and one that empties before the kill rule's second look leaves the
// cell to the owner. A full cell, a cell at 0 and the engineering group are
// not named.
func TestShortCellsAreNamed(t *testing.T) {
	picks := []eval.AuditPick{
		{Key: "a", Source: eval.CellTeacherWants}, {Key: "b", Source: eval.CellTeacherWants},
		{Key: "c", Source: eval.CellTeacherRejects}, {Key: "d", Source: eval.CellTeacherRejects},
		{Key: "e", Source: eval.EngineeringKept}, {Key: "f", Source: "decile:3"},
	}
	got := shortCells(picks, eval.AuditQuotas{Disagree: 5, DisagreeRejects: 2, Neighbour: 0, Engineering: 3})
	if len(got) != 1 || !strings.Contains(got[0], eval.CellTeacherWants+": drew 2 of 5") || !strings.Contains(got[0], "owner's call") {
		t.Fatalf("short cells %q, want only the wants cell named", got)
	}
	if got := shortCells(nil, eval.AuditQuotas{Neighbour: 3}); len(got) != 1 || !strings.Contains(got[0], eval.CellYesNeighbour+": drew 0 of 3") {
		t.Fatalf("empty neighbour cell %q", got)
	}
}

func overlayDrawSeed(t *testing.T, seed string, args ...string) auditJSON {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"--json", "audit", "--seed", seed}, args...)...)
	var d auditJSON
	decodeOne(t, o, &d)
	return d
}

func randomPicks(d auditJSON) map[string]bool {
	out := map[string]bool{}
	for _, p := range d.Picks {
		if !eval.SelectedOnReading(p.Stratum) && !eval.EngineeringStratum(p.Stratum) {
			out[string(p.Key)+"@"+p.Stratum] = true
		}
	}
	return out
}
