package jobcategory

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestOptionsBoundedAndValid(t *testing.T) {
	if len(Options) == 0 {
		t.Fatal("no options defined")
	}
	if len(Options) > decide.MaxOptions {
		t.Fatalf("options count %d exceeds decide.MaxOptions %d", len(Options), decide.MaxOptions)
	}
	seen := map[string]bool{}
	for _, o := range Options {
		if o.Value == "" {
			t.Error("empty value in options")
		}
		if seen[o.Value] {
			t.Errorf("duplicate option value %q", o.Value)
		}
		seen[o.Value] = true
		if o.Label == "" {
			t.Errorf("empty label for option %q", o.Value)
		}
		if strings.TrimSpace(o.Description) == "" {
			t.Errorf("empty description for option %q", o.Value)
		}
		if err := Validate(o.Value); err != nil {
			t.Errorf("Validate(%q) failed: %v", o.Value, err)
		}
	}
}

func TestValidateAdversarial(t *testing.T) {
	for _, bad := range []string{"", "   ", "unknown", "ai_engineer", "AI-ENGINEER", "other;drop", "../tracks"} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) unexpectedly passed", bad)
		}
	}
}

func TestDescriptionsComplete(t *testing.T) {
	d := Descriptions()
	if len(d) != len(Options) {
		t.Fatalf("descriptions count %d, want %d", len(d), len(Options))
	}
	for _, o := range Options {
		desc, ok := d[o.Value]
		if !ok || strings.TrimSpace(desc) == "" {
			t.Errorf("missing or empty description for %q", o.Value)
		}
	}
}

func TestQuestionValid(t *testing.T) {
	p := posting.Posting{
		Key:         "gh:openai:123",
		Company:     "OpenAI",
		Title:       "Research Engineer, Agent Systems",
		Location:    "San Francisco, CA",
		Description: "Build scalable agent evaluations and harnesses.",
	}
	q := Question(p, "Target roles: AI systems")
	if err := decide.ValidateQuestion(q); err != nil {
		t.Fatalf("Question invalid: %v", err)
	}
	if q.Task != Task {
		t.Errorf("task = %q, want %q", q.Task, Task)
	}
	if len(q.Slots) != 1 || q.Slots[0].Name != Slot {
		t.Fatalf("unexpected slots: %+v", q.Slots)
	}
	if q.RecordID != string(p.Key) {
		t.Errorf("RecordID = %q, want %q", q.RecordID, p.Key)
	}
}

func TestUnifiedQuestionSingleFlow(t *testing.T) {
	p := posting.Posting{
		Key:         "gh:anthropic:456",
		Company:     "Anthropic",
		Title:       "Software Engineer, Inference",
		Location:    "San Francisco, CA",
		Description: "Optimize throughput and latency for model serving.",
	}
	q := UnifiedQuestion(p, "ML inference systems")
	if err := decide.ValidateQuestion(q); err != nil {
		t.Fatalf("UnifiedQuestion invalid: %v", err)
	}
	// Triage has 4 slots; unified must have 5 (role_fit, sponsorship, work_mode, seniority, category)
	if len(q.Slots) != 5 {
		t.Fatalf("UnifiedQuestion slots count = %d, want 5", len(q.Slots))
	}
	slotNames := make([]string, len(q.Slots))
	for i, s := range q.Slots {
		slotNames[i] = s.Name
	}
	expected := []string{"role_fit", "sponsorship", "work_mode", "seniority", Slot}
	if strings.Join(slotNames, ",") != strings.Join(expected, ",") {
		t.Errorf("slots = %v, want %v", slotNames, expected)
	}
}

func TestUnifiedDescriptions(t *testing.T) {
	ud := UnifiedDescriptions()
	for _, expectedSlot := range []string{"role_fit", "sponsorship", "work_mode", "seniority", Slot} {
		if _, ok := ud[expectedSlot]; !ok {
			t.Errorf("UnifiedDescriptions missing slot %q", expectedSlot)
		}
	}
	if len(ud[Slot]) != len(Options) {
		t.Errorf("UnifiedDescriptions[category] count = %d, want %d", len(ud[Slot]), len(Options))
	}
}

func TestLabels(t *testing.T) {
	if Label(AIEngineer) != "AI Engineer" {
		t.Errorf("Label(%q) = %q, want %q", AIEngineer, Label(AIEngineer), "AI Engineer")
	}
	if Label("nonexistent") != "nonexistent" {
		t.Errorf("Label(nonexistent) = %q", Label("nonexistent"))
	}
}
