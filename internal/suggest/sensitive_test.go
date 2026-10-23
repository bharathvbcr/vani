package suggest

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/decide"
)

// failDecider fails the test when asked: a guarded label never reaches a model.
type failDecider struct{ t *testing.T }

func (f failDecider) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	f.t.Errorf("the decision model was asked about a guarded label:\n%s", q.Context)
	return decide.Answer{}, nil
}

func TestGuardedLabelsAreNeverAskedOrDrafted(t *testing.T) {
	labels := []string{
		"Are you a U.S. citizen?", "Are you eligible to work in the United States?",
		"Are you legally entitled to work here?", "Are you a U.S. person?",
		"Do you have employment authorization?", "What is your age?", "Religion",
		"Preferred pronouns", "Racial or ethnic background", "Have you been convicted of a criminal offense?",
		"Marital status", "Are you a U．S． person?", "Are you a citizen?",
	}
	var fields []fieldtriage.Field
	for i, l := range labels {
		fields = append(fields, text(string(rune('a'+i)), l))
	}
	dr := &fakeDrafter{}
	out, err := Engine{D: failDecider{t}, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: MaxDrafts})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range out {
		if s.Status != Manual || s.Draft != "" {
			t.Errorf("%q: status %s draft %q, want manual with no draft", labels[i], s.Status, s.Draft)
		}
	}
	if dr.calls.Load() != 0 {
		t.Errorf("drafter called %d times", dr.calls.Load())
	}
}

// A model that reads a sensitive category, or an essay for a label a pattern
// flags, never produces a draft.
func TestModelReadingOfASensitiveFieldNeverBecomesADraft(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		"Tell us about yourself":          answered(fieldtriage.WorkAuth),
		"Anything else to share?":         answered(fieldtriage.SelfID),
		"Final remarks":                   answered(fieldtriage.Consent),
		"Please state your date of birth": answered(fieldtriage.Essay),
		"Passport number":                 answered(fieldtriage.Essay),
		"Why Acme?":                       answered(fieldtriage.Essay),
	}}
	dr := &fakeDrafter{}
	fields := []fieldtriage.Field{
		text("a", "Tell us about yourself"), text("b", "Anything else to share?"), text("c", "Final remarks"),
		text("d", "Please state your date of birth"), text("e", "Passport number"), text("f", "Why Acme?"),
	}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: MaxDrafts})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range out[:5] {
		if s.Status != Manual || s.Draft != "" {
			t.Errorf("%s: status %s draft %q, want manual with no draft", s.ID, s.Status, s.Draft)
		}
		if strings.Contains(strings.ToLower(s.Reason), "never sent") {
			t.Errorf("%s: reason %q claims the label was never sent, but a model read it", s.ID, s.Reason)
		}
	}
	if out[5].Status != Drafted {
		t.Errorf("a plain essay was not drafted: %s (%s)", out[5].Status, out[5].Reason)
	}
	if dr.calls.Load() != 1 {
		t.Errorf("drafter called %d times, want 1", dr.calls.Load())
	}
}

// A guard-pinned label's label really is not sent to a model, and says so.
func TestGuardPinnedReasonStaysTruthful(t *testing.T) {
	out, err := Engine{D: failDecider{t}, Drafter: &fakeDrafter{}}.Run(context.Background(),
		Request{Job: job, Fields: []fieldtriage.Field{text("a", "Will you require visa sponsorship?")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0].Reason, "not sent to a model") {
		t.Errorf("reason %q", out[0].Reason)
	}
}
