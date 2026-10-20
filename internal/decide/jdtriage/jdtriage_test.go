package jdtriage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestQuestionIsValidAndFixed(t *testing.T) {
	q := Question(posting.Posting{Title: "ML Engineer", Location: "Remote", Description: "Build things."})
	if err := decide.ValidateQuestion(q); err != nil {
		t.Fatal(err)
	}
	if q.Task != "career.jd_triage" || q.Prompt != Prompt {
		t.Errorf("task=%q prompt=%q", q.Task, q.Prompt)
	}
	want := map[string][]string{
		"role_fit":    {"strong_fit", "partial_fit", "adjacent", "mismatch_domain", "mismatch_seniority"},
		"sponsorship": {"sponsors_visa", "no_sponsorship", "citizenship_or_clearance_required", "not_stated"},
		"work_mode":   {"remote", "hybrid", "onsite", "not_stated"},
		"seniority":   {"intern_or_new_grad", "mid", "senior", "staff_plus", "manager"},
	}
	if len(q.Slots) != len(want) {
		t.Fatalf("slots = %d", len(q.Slots))
	}
	for _, s := range q.Slots {
		if strings.Join(s.Options, ",") != strings.Join(want[s.Name], ",") {
			t.Errorf("%s options = %v", s.Name, s.Options)
		}
	}
	text := string(q.Context)
	for _, part := range []string{"ML Engineer", "Remote", "Build things."} {
		if !strings.Contains(text, part) {
			t.Errorf("context lacks %q: %q", part, text)
		}
	}
	if q.Facts[FactContextTruncated] != "false" {
		t.Errorf("facts = %v", q.Facts)
	}
}

func TestDescriptionsCoverEveryOption(t *testing.T) {
	d := Descriptions()
	for _, s := range Slots {
		for _, o := range s.Options {
			if strings.TrimSpace(d[s.Name][o.Value]) == "" {
				t.Errorf("%s/%s has no description", s.Name, o.Value)
			}
		}
	}
	if len(d) != len(Slots) {
		t.Errorf("descriptions for %d slots, table has %d", len(d), len(Slots))
	}
}

func TestContextCapAndFacts(t *testing.T) {
	long := posting.Posting{Title: "T", Description: strings.Repeat("é", MaxContextBytes)} // 2 bytes each
	q := Question(long)
	if len(q.Context) > MaxContextBytes {
		t.Fatalf("context = %d bytes", len(q.Context))
	}
	if q.Facts[FactContextTruncated] != "true" {
		t.Errorf("facts = %v", q.Facts)
	}
	if !strings.HasSuffix(string(q.Context), "é") || strings.ContainsRune(string(q.Context), '�') {
		t.Error("cut split a rune")
	}
	if err := decide.ValidateQuestion(q); err != nil {
		t.Errorf("capped question invalid: %v", err)
	}

	// Exactly at the cap is not truncated.
	p := posting.Posting{}
	overhead := len("Title: \nLocation: \n\n")
	p.Description = strings.Repeat("a", MaxContextBytes-overhead)
	q = Question(p)
	if len(q.Context) != MaxContextBytes || q.Facts[FactContextTruncated] != "false" {
		t.Errorf("len=%d facts=%v", len(q.Context), q.Facts)
	}
	p.Description += "a"
	if q = Question(p); q.Facts[FactContextTruncated] != "true" || len(q.Context) != MaxContextBytes {
		t.Errorf("one over: len=%d facts=%v", len(q.Context), q.Facts)
	}
}

func TestEmptyPostingStillValid(t *testing.T) {
	if err := decide.ValidateQuestion(Question(posting.Posting{})); err != nil {
		t.Fatal(err)
	}
}

type capture struct {
	q   decide.Question
	err error
}

func (c *capture) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	c.q = q
	return decide.Answer{Backend: "fake"}, c.err
}

// The target summary goes into the context; the question text never varies,
// because Lappi answers only the exact trained question.
func TestTriagerRendersTargetSummaryIntoContext(t *testing.T) {
	c := &capture{}
	tr := Triager{D: c, TargetSummary: "  ML systems /\n inference   roles "}
	a, err := tr.Triage(context.Background(), posting.Posting{Title: "x", Location: "y", Description: "z"})
	if err != nil || a.Backend != "fake" {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	if c.q.Prompt != Prompt {
		t.Errorf("prompt changed with a summary: %q", c.q.Prompt)
	}
	if want := "Target roles: ML systems / inference roles\n\nTitle: x\nLocation: y\n\nz"; string(c.q.Context) != want {
		t.Errorf("context = %q", c.q.Context)
	}
	if err := decide.ValidateQuestion(c.q); err != nil {
		t.Error(err)
	}

	c = &capture{}
	if _, err := (Triager{D: c}).Triage(context.Background(), posting.Posting{Title: "x"}); err != nil ||
		c.q.Prompt != Prompt || string(c.q.Context) != "Title: x\nLocation: \n\n" {
		t.Errorf("no summary: prompt=%q context=%q err=%v", c.q.Prompt, c.q.Context, err)
	}
}

// fixtureSHA256 pins fixture.json. Lappi-decision's generator pins the same
// hash; a change here is a contract change that needs a retrain.
const fixtureSHA256 = "0cf04ad791d67fb04bf4e1c5be9b3206f61cf356dddf0a3054c9a8b5a13be47d"

func TestFixturePinned(t *testing.T) {
	sum := sha256.Sum256(FixtureJSON)
	if got := hex.EncodeToString(sum[:]); got != fixtureSHA256 {
		t.Fatalf("fixture.json sha256 = %s, pinned %s: a contract change (update Lappi-decision's copy too)", got, fixtureSHA256)
	}
	if Prompt == "" || len(Slots) != 4 {
		t.Fatalf("fixture not loaded: prompt %q, %d slots", Prompt, len(Slots))
	}
}

// Each Lappi family carries exactly one slot of the combined question, with
// the same options in the same order, so a per-slot Lappi answer and a
// multi-slot Liquid D answer are comparable.
func TestFamiliesMirrorSlots(t *testing.T) {
	if len(Families) != len(Slots) {
		t.Fatalf("%d families, %d slots", len(Families), len(Slots))
	}
	want := map[string]string{"role_fit": "career.jd_role_fit", "sponsorship": "career.jd_sponsorship",
		"work_mode": "career.jd_work_mode", "seniority": "career.jd_seniority"}
	q := Question(posting.Posting{Title: "x"})
	for i, f := range Families {
		if want[f.Slot.Name] != f.Task {
			t.Errorf("slot %s is family %q", f.Slot.Name, f.Task)
		}
		if q.Slots[i].Name != f.Slot.Name || len(q.Slots[i].Options) != len(f.Slot.Options) {
			t.Fatalf("question slot %d = %+v, family %s", i, q.Slots[i], f.Task)
		}
		for j, o := range f.Slot.Options {
			if q.Slots[i].Options[j] != o.Value {
				t.Errorf("%s option %d: %q vs %q", f.Task, j, q.Slots[i].Options[j], o.Value)
			}
		}
		if strings.Contains(f.Question, "content markers") {
			t.Errorf("%s question mentions content markers, which Lappi's prompt does not have", f.Task)
		}
	}
}

func TestParseFixtureRejectsBrokenFiles(t *testing.T) {
	good := string(FixtureJSON)
	for name, bad := range map[string]string{
		"not json":       "{",
		"unknown field":  strings.Replace(good, `"fixture_version": 2,`, `"fixture_version": 2, "extra": 1,`, 1),
		"wrong task":     strings.Replace(good, `"career.jd_triage"`, `"career.other"`, 1),
		"wrong version":  strings.Replace(good, `"fixture_version": 2`, `"fixture_version": 1`, 1),
		"cap mismatch":   strings.Replace(good, `"max_bytes": 24576`, `"max_bytes": 1000`, 1),
		"dup option":     strings.Replace(good, `"value": "partial_fit"`, `"value": "strong_fit"`, 1),
		"no placeholder": strings.Replace(good, `"{target}Title:`, `"Title:`, 1),
		"empty desc":     strings.Replace(good, `"The role is fully remote."`, `" "`, 1),
		"dup family":     strings.Replace(good, `"career.jd_work_mode"`, `"career.jd_seniority"`, 1),
		"family is task": strings.Replace(good, `"career.jd_work_mode"`, `"career.jd_triage"`, 1),
		"family prefix":  strings.Replace(good, `"career.jd_work_mode"`, `"jd_work_mode"`, 1),
		"empty family q": strings.Replace(good, `"Where does this job posting say the work is done?"`, `" "`, 1),
	} {
		if bad == good {
			t.Fatalf("%s: replacement did not apply", name)
		}
		if _, err := parseFixture([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := parseFixture(FixtureJSON); err != nil {
		t.Fatal(err)
	}
}

func TestTriagerRefusesBadConfigAndPropagatesErrors(t *testing.T) {
	if _, err := (Triager{}).Triage(context.Background(), posting.Posting{}); err == nil {
		t.Error("nil Decider accepted")
	}
	c := &capture{}
	tr := Triager{D: c, TargetSummary: strings.Repeat("x", MaxTargetSummaryBytes+1)}
	if _, err := tr.Triage(context.Background(), posting.Posting{}); err == nil || c.q.Task != "" {
		t.Errorf("oversized summary: err=%v asked=%q", err, c.q.Task)
	}
	boom := errors.New("down")
	if _, err := (Triager{D: &capture{err: boom}}).Triage(context.Background(), posting.Posting{}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestRecordIDIsPostingKey(t *testing.T) {
	if q := Question(posting.Posting{Key: "gh:acme:1"}); q.RecordID != "gh:acme:1" {
		t.Errorf("RecordID = %q", q.RecordID)
	}
}

func TestTriagerExtraSlotsSingleFlow(t *testing.T) {
	c := &capture{}
	extra := decide.Slot{Name: "category", Options: []string{"ai-engineer", "other"}}
	tr := Triager{D: c, ExtraSlots: []decide.Slot{extra}}
	p := posting.Posting{Key: "gh:acme:2", Title: "Engineer"}
	if _, err := tr.Triage(context.Background(), p); err != nil {
		t.Fatalf("Triage failed: %v", err)
	}
	if len(c.q.Slots) != 5 {
		t.Fatalf("slots = %d, want 5", len(c.q.Slots))
	}
	last := c.q.Slots[len(c.q.Slots)-1]
	if last.Name != "category" || len(last.Options) != 2 {
		t.Errorf("last slot = %+v", last)
	}
}
