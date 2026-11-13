package eval

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The owner's reason uses the pointwise auditor's vocabulary, so the two can be
// compared value for value.
func TestWouldApplyReasonsAreTheAuditorsReasons(t *testing.T) {
	if !slices.Equal(WouldApplyReasons, wouldapply.Reasons[:]) {
		t.Fatalf("owner reasons %v, auditor reasons %v", WouldApplyReasons, wouldapply.Reasons)
	}
}

// A reason says what kept an answer from a yes: it goes with a maybe or a no,
// is one of the options, and never stands alone.
func TestWouldApplyReasonValidation(t *testing.T) {
	sampled := func(labels map[string]string) Entry {
		return Entry{Key: "gh:k:1", Labels: labels, Source: "decile:3", PSelect: 0.5}
	}
	for name, tc := range map[string]struct {
		e  Entry
		ok bool
	}{
		"maybe with a reason":   {sampled(map[string]string{WouldApply: "maybe", WouldApplyReason: "level"}), true},
		"no with a reason":      {sampled(map[string]string{WouldApply: "no", WouldApplyReason: "domain"}), true},
		"maybe without one":     {sampled(map[string]string{WouldApply: "maybe"}), true},
		"yes with a reason":     {sampled(map[string]string{WouldApply: "yes", WouldApplyReason: "level"}), false},
		"skip with a reason":    {sampled(map[string]string{WouldApply: Skip, WouldApplyReason: "level"}), false},
		"reason alone":          {Entry{Key: "gh:k:1", Labels: map[string]string{WouldApplyReason: "level"}}, false},
		"reason with triage":    {Entry{Key: "gh:k:1", Labels: map[string]string{"role_fit": "adjacent", WouldApplyReason: "level"}}, false},
		"unknown reason":        {sampled(map[string]string{WouldApply: "no", WouldApplyReason: "salary"}), false},
		"skip as a reason":      {sampled(map[string]string{WouldApply: "no", WouldApplyReason: Skip}), false},
		"empty reason":          {sampled(map[string]string{WouldApply: "no", WouldApplyReason: ""}), false},
		"case-folded reason":    {sampled(map[string]string{WouldApply: "no", WouldApplyReason: "Level"}), false},
		"employer type reason":  {sampled(map[string]string{WouldApply: "maybe", WouldApplyReason: "employer_type"}), true},
		"location reason":       {sampled(map[string]string{WouldApply: "no", WouldApplyReason: "location"}), true},
		"other reason on maybe": {sampled(map[string]string{WouldApply: "maybe", WouldApplyReason: "other"}), true},
	} {
		if err := tc.e.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: Validate() = %v, want ok %v", name, err, tc.ok)
		}
	}
}

// The reason travels with the would_apply answer: a triage re-label keeps both,
// a new answer replaces both (one without a reason drops the old reason), and
// the triage report and windows never count the reason as a triage slot.
func TestWouldApplyReasonMergesWithTheAnswer(t *testing.T) {
	answered := Entry{Key: "gh:k:1", Labels: map[string]string{WouldApply: "maybe", WouldApplyReason: "level"}, Source: "decile:3", PSelect: 0.5}
	triage := Entry{Key: "gh:k:1", Labels: map[string]string{"role_fit": "adjacent"}}

	m := Merge(answered, triage)
	if m.Labels[WouldApplyReason] != "level" || m.Labels[WouldApply] != "maybe" || m.Labels["role_fit"] != "adjacent" || m.Source != "decile:3" {
		t.Fatalf("a triage re-label lost the answer or its reason: %+v", m)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("merged entry invalid: %v", err)
	}
	re := Merge(m, Entry{Key: "gh:k:1", Labels: map[string]string{WouldApply: "yes"}, Source: "decile:3", PSelect: 0.5})
	if _, ok := re.Labels[WouldApplyReason]; ok || re.Labels["role_fit"] != "adjacent" {
		t.Fatalf("a new yes kept the old maybe's reason: %+v", re)
	}
	if err := re.Validate(); err != nil {
		t.Fatalf("re-answered entry invalid: %v", err)
	}

	onlyAudit := map[posting.Key]Entry{"gh:k:1": answered}
	if n := len(triageLabels(onlyAudit)); n != 0 {
		t.Errorf("an answer with a reason counted as %d triage label(s)", n)
	}
	if hasNonSkip(answered) {
		t.Error("the windows took the reason for a triage slot")
	}
	if got := newMember("w", answered).Labels; len(got) != 0 {
		t.Errorf("a window member kept audit slots: %v", got)
	}
}
