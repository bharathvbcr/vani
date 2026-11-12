package jobinsight

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// gateOf is the gate factor Score gives in under the default weights.
func gateOf(t *testing.T, in Inputs) float64 {
	t.Helper()
	return factorOf(t, ScoreWith(in, DefaultWeights()), "gate").Value
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A seniority penalty and a mismatch_seniority reading say one thing: the
// role's level is wrong for the seeker. It is penalised once, by the stronger
// of the two, never by both. The expected numbers are worked by hand from the
// default weights (PenaltyStrength 0.8, Mismatch 0.6), not by the formula.
func TestSeniorityPenalisedOnceWithMismatchSeniority(t *testing.T) {
	senior := Penalty{Name: "seniority", Slot: sl("senior", 0.9)} // 1 - 0.8*0.9 = 0.28
	for _, tc := range []struct {
		name    string
		fit     Slot
		penalty []Penalty
		want    float64
	}{
		// TPU Performance Engineer's shape: a confident senior penalty, a weak
		// mismatch_seniority (1 - 0.6*0.3 = 0.82). Before: 0.28*0.82 = 0.2296.
		{"weak mismatch, strong penalty: the penalty", sl("mismatch_seniority", 0.3), []Penalty{senior}, 0.28},
		// A strong mismatch over a weak penalty (1 - 0.8*0.2 = 0.84; 1 - 0.6*0.95 = 0.43).
		{"strong mismatch, weak penalty: the mismatch", sl("mismatch_seniority", 0.95), []Penalty{{Name: "seniority", Slot: sl("senior", 0.2)}}, 0.43},
		// Two different facts still compound: off-domain and off-level (0.28 * (1 - 0.6*0.9) = 0.1288).
		{"mismatch_domain keeps the seniority penalty", sl("mismatch_domain", 0.9), []Penalty{senior}, 0.1288},
		// No mismatch: the penalty alone.
		{"adjacent: the penalty alone", sl("adjacent", 0.9), []Penalty{{Name: "seniority", Slot: sl("staff_plus", 0.9)}}, 0.28},
		// Another slot's penalty is not the seniority fact: both apply (0.28 * 0.82 * (1 - 0.8*0.5 = 0.6)).
		{"other slot still compounds", sl("mismatch_seniority", 0.3), []Penalty{senior, {Name: "work_mode", Slot: sl("onsite", 0.5)}}, 0.28 * 0.6},
		// No seniority penalty: the mismatch gate alone, as before.
		{"mismatch alone", sl("mismatch_seniority", 0.3), nil, 0.82},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if g := gateOf(t, Inputs{RoleFit: tc.fit, Penalties: tc.penalty}); !near(g, tc.want) {
				t.Fatalf("gate %.6f, want %.6f", g, tc.want)
			}
		})
	}
}

// A staff_plus reading that the posting's own year figures contradict is
// penalised at the level those years imply; with no figure, or a staff-level
// one, it stands. Only the ranking's penalty is read this way: the stored
// reading is untouched.
func TestInputsForStaffReadingFollowsStatedYears(t *testing.T) {
	ann := map[string]string{
		"triage.seniority":         "staff_plus",
		"triage.seniority.reading": "answered",
		"triage.seniority.p":       "0.9",
	}
	staff := []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}}}
	senior := []DropRule{{Slot: "seniority", Values: []string{"senior"}}}
	both := []DropRule{{Slot: "seniority", Values: []string{"senior", "staff_plus"}}}
	for _, tc := range []struct {
		name  string
		desc  string
		rules []DropRule
		want  string // the penalised value, "" for none
	}{
		{"staff years keep staff", "10+ years building and scaling distributed infrastructure", staff, "staff_plus"},
		{"no figure keeps staff", "Staff engineer on our platform team.", staff, "staff_plus"},
		{"evicted body keeps staff", "", staff, "staff_plus"},
		{"5+ years is not staff", "5+ years of experience as a software engineer", staff, ""},
		{"5+ years reads senior", "5+ years of experience as a software engineer", senior, "senior"},
		{"4+ years reads mid, which no rule names", "Member of Data Staff. 4+ years of experience", both, ""},
		{"a date is no figure", "Founded 10 years ago. Staff engineer.", staff, "staff_plus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rec("gh:a:1", "Acme", time.Hour, ann)
			r.Posting.Description = tc.desc
			got := InputsFor(r, RankContext{Now: inT0, DropRules: tc.rules}).Penalties
			var want []Penalty
			if tc.want != "" {
				want = []Penalty{{Name: "seniority", Slot: Slot{Value: tc.want, Answered: true, P: 0.9, HasP: true}}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("penalties %+v, want %+v", got, want)
			}
		})
	}
}
