package eval

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func currentRow(key, model, fixture, prompt string, slots map[string]TeacherSlot) TeacherRow {
	return TeacherRow{
		Key: posting.Key(key), TextSHA256: "t", Model: model, FixtureSHA: fixture, PromptHash: prompt,
		Slots: slots, At: time.Unix(1, 0).UTC(),
	}
}

func TestCurrentReadingsKeepsOnlyTheCurrentIdentity(t *testing.T) {
	const summary = "ml systems roles"
	path := filepath.Join(t.TempDir(), TeacherFile)
	s, err := OpenTeacherStore(path)
	if err != nil {
		t.Fatal(err)
	}
	fx, ph := FixtureSHA(), PromptHash(summary)
	ans := func(v string) map[string]TeacherSlot {
		return map[string]TeacherSlot{"role_fit": {Reading: "answered", Value: v}}
	}
	for _, r := range []TeacherRow{
		currentRow("cur", "haiku", fx, ph, ans("old-answer")),
		currentRow("cur", "haiku", fx, ph, ans("new-answer")), // newest wins
		currentRow("stale-prompt", "haiku", fx, PromptHash("another summary"), ans("x")),
		currentRow("stale-fixture", "haiku", "deadbeef", ph, ans("x")),
		currentRow("other-model", "opus", fx, ph, ans("x")),
		currentRow("abstained", "haiku", fx, ph, map[string]TeacherSlot{
			"role_fit": {Reading: "abstained"}, "work_mode": {Reading: "failed", Value: "remote"},
		}),
	} {
		if err := s.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	// A reopened store (the label command's path) sees the same.
	for name, st := range map[string]*TeacherStore{"live": s, "reopened": mustOpenTeacher(t, path)} {
		c := st.Current("haiku", summary)
		got, ok := c.Reading("cur")
		if !ok || got["role_fit"] != "new-answer" || len(got) != 1 {
			t.Fatalf("%s: cur = %v, %v", name, got, ok)
		}
		for _, k := range []string{"stale-prompt", "stale-fixture", "other-model", "abstained", "absent"} {
			if got, ok := c.Reading(k); ok || len(got) != 0 {
				t.Fatalf("%s: %s must have no current reading, got %v", name, k, got)
			}
		}
	}
}

func TestCurrentReadingsZeroValueIsEmpty(t *testing.T) {
	if got, ok := (CurrentReadings{}).Reading("k"); ok || got != nil {
		t.Fatalf("zero value: %v, %v", got, ok)
	}
}

func mustOpenTeacher(t *testing.T, path string) *TeacherStore {
	t.Helper()
	s, err := OpenTeacherStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
