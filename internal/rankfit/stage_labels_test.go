package rankfit

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A posting at any post-apply stage labels exactly as one that is only
// applied: a yes. Rejected and withdrawn included: the owner wanted the role,
// and what the company did about it is not a reason to learn the opposite.
// The labels are built from a real marks store, the way the command builds
// them, so a stage that ever leaked into the mark map would show here.
func TestEveryStageLabelsAsApplied(t *testing.T) {
	dir := marks.Dir(t.TempDir())
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	stages := append([]marks.Stage{marks.StageNone}, marks.Stages...)
	keys := map[marks.Stage]posting.Key{}
	for i, s := range stages {
		k, err := posting.GreenhouseKey("acme", string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		keys[s] = k
		if _, err := marks.SetApplied(dir, k, 0, at); err != nil {
			t.Fatal(err)
		}
		if s != marks.StageNone {
			if _, err := marks.SetStage(dir, k, s, at.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	m, err := marks.ReadMarks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, counts, err := Targets(nil, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	want := got[keys[marks.StageNone]]
	if want.Grade != GradeYes {
		t.Fatalf("an applied posting labels %+v, want a yes", want)
	}
	for _, s := range stages {
		if g, ok := got[keys[s]]; !ok || g != want {
			t.Errorf("stage %q labels %+v (labelled %v), want exactly %+v", s, g, ok, want)
		}
	}
	if counts.Saved != len(stages) || counts.Hidden != 0 || counts.Passed != 0 {
		t.Errorf("counts %+v, want every one of the %d counted as saved or applied", counts, len(stages))
	}
	// The entries Read returns, as `vani jobs` builds the mark map from them.
	entries, err := marks.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	fromEntries := map[posting.Key]marks.Mark{}
	for k, e := range entries {
		fromEntries[k] = e.Mark
	}
	if again, c2, err := Targets(nil, nil, fromEntries); err != nil || len(again) != len(got) || c2 != counts {
		t.Errorf("Read's marks label differently from ReadMarks': %v %+v %v", len(again), c2, err)
	}
}
