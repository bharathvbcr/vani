package rekey

import (
	"errors"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/marks"
)

// A posting at a stage, and one set back after an interview, keep the stage
// history, the furthest stage and the date first applied under their new keys;
// the old keys are left with nothing.
func TestStagesMoveWithTheirPosting(t *testing.T) {
	for name, final := range map[string]marks.Stage{"ended": marks.StageRejected, "set back": marks.StageNone, "furthest": marks.StageInterview} {
		state := t.TempDir()
		seed(t, state)
		dir := marks.Dir(state)
		ok := oldKey(t, cpp)
		if _, err := marks.SetApplied(dir, ok, 0, t0); err != nil {
			t.Fatal(err)
		}
		for i, s := range []marks.Stage{marks.StageScreening, marks.StageInterview, final} {
			if _, err := marks.SetStage(dir, ok, s, t0.Add(time.Duration(i+1)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		want, _ := marks.Read(dir)
		if _, err := Run(state, false, run); err != nil {
			t.Fatal(err)
		}
		got, err := marks.Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		nk := newKey(t, cpp)
		w, g := want[ok], got[nk]
		if g.Mark != marks.Applied || g.Stage != w.Stage || g.Furthest != w.Furthest || !g.MarkedAt.Equal(t0) || !g.StageAt.Equal(w.StageAt) {
			t.Errorf("%s: moved to %+v, from %+v", name, g, w)
		}
		if _, left := got[ok]; left {
			t.Errorf("%s: the old key still has %+v", name, got[ok])
		}
		if w.Furthest != marks.StageInterview {
			t.Fatalf("%s: setup: furthest %q", name, w.Furthest)
		}
	}
}

// A crash after the first posting is fully moved is finished by the next run,
// and neither run touches a stage twice.
func TestCrashAfterAStagedPostingIsFinished(t *testing.T) {
	state := t.TempDir()
	seed(t, state)
	dir := marks.Dir(state)
	ok := oldKey(t, cpp)
	marks.SetApplied(dir, ok, 0, t0)
	marks.SetStage(dir, ok, marks.StageOffer, t0.Add(time.Hour))
	marks.SetStage(dir, ok, marks.StageWithdrawn, t0.Add(2*time.Hour))
	boom := errors.New("crash")
	crash = func(s string) error {
		if s == "marks" {
			return boom
		}
		return nil
	}
	_, err := Run(state, false, run)
	crash = func(string) error { return nil }
	if !errors.Is(err, boom) {
		t.Fatalf("first run: %v", err)
	}
	if _, err := Run(state, false, run); err != nil {
		t.Fatal(err)
	}
	got, _ := marks.Read(dir)
	g := got[newKey(t, cpp)]
	if g.Stage != marks.StageWithdrawn || g.Furthest != marks.StageOffer || !g.MarkedAt.Equal(t0) {
		t.Fatalf("%+v", g)
	}
	if _, left := got[ok]; left {
		t.Fatalf("old key kept %+v", got[ok])
	}
}
