package main

import (
	"strings"
	"testing"
	"time"
)

// The run record carries mass-hold counts (Mass, MassMarked, Held) beside the
// ordinary ones. A board whose empty listing newly mass-held its 42 postings
// had none die, and the doctor must show it as what it is, a board that listed
// 42, 42 and then nothing (zero_after_nonzero), not as postings that died: a
// mass hold is never put to the model as a mass death.
func TestDoctorReadsAMassHoldAsNoDeaths(t *testing.T) {
	state, _ := doctorSetup(t)
	doctorSeams(t, &doctorFake{}, 0)
	for i, n := range []int{42, 42, 0} {
		listed := n
		r := runRecord{V: 1, PID: 200 + i, StartedAt: time.Date(2026, 11, 20+i, 9, 0, 0, 0, time.UTC), Outcome: "ok"}
		it := boardReport{ATS: "gh", Token: "snap", Company: "Snap", Outcome: boardRead, Listed: &listed,
			Reconcile: &reconcileCounts{Unchanged: n}}
		b := &boardsReport{Configured: 1, Read: 1, Listed: n, Items: []boardReport{it}}
		if n == 0 {
			it.Reconcile = &reconcileCounts{Open: 42, Absent: 42, Mass: true, MassMarked: 42}
			b.Mass, b.MassMarked, b.Items = 1, 42, []boardReport{it}
		}
		r.Boards = b
		if err := writeRunHistory(state, r); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := mustCode(t, 0, "doctor", "--dry-run", "--show")
	i := strings.Index(out, `"board":"gh:snap"`)
	if i < 0 {
		t.Fatalf("gh:snap is not in what the doctor would send:\n%s", out)
	}
	snap := out[i:]
	if j := strings.Index(snap, "}"); j >= 0 {
		snap = snap[:j]
	}
	for _, want := range []string{"zero_after_nonzero", "42,42,0"} {
		if !strings.Contains(snap, want) {
			t.Errorf("gh:snap lacks %q: %s", want, snap)
		}
	}
	low := strings.ToLower(out)
	for _, bad := range []string{"died", "dead", "mass_dead"} {
		if strings.Contains(low, bad) {
			t.Errorf("what the doctor would send says %q after a mass hold", bad)
		}
	}
}
