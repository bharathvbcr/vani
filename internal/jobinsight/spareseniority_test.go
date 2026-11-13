package jobinsight

import "testing"

// SpareSeniorityMismatch only loosens: a mismatch_seniority reading at a
// spared level loses its gate and its fit part, so the posting ranks at least
// as high; at any other level, with no seniority reading, or with the list
// empty, nothing changes.
func TestSpareSeniorityMismatchOnlyLoosensAtListedLevels(t *testing.T) {
	base := DefaultWeights()
	spare := base
	spare.SpareSeniorityMismatch = []string{"mid", " senior "}
	slot := func(v string, answered bool) Slot { return Slot{Value: v, Answered: answered, P: 0.95, HasP: true} }
	in := func(roleFit string, sen Slot) Inputs {
		return Inputs{RoleFit: slot(roleFit, true), Seniority: sen, Coverage: 60, HasCoverage: true, AgeKnown: true, AgeDays: 1}
	}
	cases := []struct {
		name    string
		in      Inputs
		changes bool
	}{
		{"senior mismatch is spared", in("mismatch_seniority", slot("senior", true)), true},
		{"mid mismatch is spared", in("mismatch_seniority", slot("mid", true)), true},
		{"staff_plus is not listed", in("mismatch_seniority", slot("staff_plus", true)), false},
		{"manager is not listed", in("mismatch_seniority", slot("manager", true)), false},
		{"an unanswered seniority spares nothing", in("mismatch_seniority", slot("senior", false)), false},
		{"no seniority reading", in("mismatch_seniority", Slot{}), false},
		{"a domain mismatch is never spared", in("mismatch_domain", slot("senior", true)), false},
		{"a fit is untouched", in("strong_fit", slot("senior", true)), false},
	}
	for _, c := range cases {
		b, s := ScoreWith(c.in, base), ScoreWith(c.in, spare)
		if c.changes {
			if !(s.Gate > b.Gate) || s.Score < b.Score || s.Relevance < b.Relevance {
				t.Errorf("%s: gate %v -> %v, score %v -> %v, relevance %v -> %v", c.name, b.Gate, s.Gate, b.Score, s.Score, b.Relevance, s.Relevance)
			}
			continue
		}
		if s.Score != b.Score || s.Gate != b.Gate || s.Relevance != b.Relevance {
			t.Errorf("%s: changed (score %v -> %v)", c.name, b.Score, s.Score)
		}
	}
	// The default spares nothing.
	if len(DefaultWeights().SpareSeniorityMismatch) != 0 {
		t.Error("the default must spare nothing")
	}
}
