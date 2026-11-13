package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// auditedState is a state with 15 audit answers (yes, no, maybe, skip in turn).
func auditedState(t *testing.T) string {
	t.Helper()
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 30, 6, 6)
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(strings.Repeat("1\n3\n2\ns\n", 4))
	mustCode(t, 0, "audit-sample", "--seed", "w1")
	return state
}

func judgedKeys(t *testing.T, state string) []posting.Key {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	done, _, err := s.Labels()
	if err != nil {
		t.Fatal(err)
	}
	var out []posting.Key
	for k, e := range done {
		if v := e.Labels[eval.WouldApply]; v != "" && v != eval.Skip {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		t.Fatal("no judged labels seeded")
	}
	return out
}

func TestRelevanceSetsReadersAndPartsAgainstTheOwner(t *testing.T) {
	state := auditedState(t)
	keys := judgedKeys(t, state)
	store, err := eval.OpenTeacherStore(filepath.Join(state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(eval.TeacherRow{Key: keys[0], TextSHA256: "t", Model: "teacher-model", FixtureSHA: "f", PromptHash: "p",
		Slots: map[string]eval.TeacherSlot{"role_fit": {Reading: "answered", Value: "mismatch_domain"}}, At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	o, _ := mustCode(t, 0, "relevance", "--json")
	var r relevanceReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, o)
	}
	if r.Readers == nil || r.Readers.Judged != r.Relevance.Judged || r.Readers.TeacherStatus != eval.TeacherProxyAvailable {
		t.Fatalf("readers: %+v (relevance judged %d)", r.Readers, r.Relevance.Judged)
	}
	var teacher, engine bool
	for _, row := range r.Readers.Readers {
		teacher = teacher || (row.Kind == eval.ReaderTeacher && row.Reader == "teacher-model" && row.Read == 1)
		engine = engine || row.Kind == eval.ReaderEngine
	}
	if !teacher || !engine {
		t.Errorf("want a teacher and an engine reader: %+v", r.Readers.Readers)
	}
	if r.PartAUCError != "" || len(r.PartAUC) != len(partAUCScopes) {
		t.Fatalf("part AUC: %q %+v", r.PartAUCError, r.PartAUC)
	}
	if all := r.PartAUC[0]; all.Pos+all.Neg != r.Relevance.Judged-r.Relevance.Manual-closedJudged(r.Relevance) {
		t.Errorf("part AUC counts %d+%d against %d judged", all.Pos, all.Neg, r.Relevance.Judged)
	}
	if all := r.PartAUC[0]; all.Status == rankfit.PartAUCOK && len(all.Rows) != rankfit.NumParts+1 {
		t.Errorf("rows: %+v", all.Rows)
	}

	text, _ := mustCode(t, 0, "relevance")
	for _, frag := range []string{"## Readers against the owner", "### teacher teacher-model", "sensitivity", "## Score parts against the owner", "### Score parts against the owner: all judged postings"} {
		if !strings.Contains(text, frag) {
			t.Errorf("report lacks %q:\n%s", frag, text)
		}
	}
}

// closedJudged counts judgments from closed strata, which the fit (and so the
// part AUC) leaves out on purpose.
func closedJudged(r eval.Relevance) int {
	n := 0
	for _, c := range r.Closed {
		n += c.N
	}
	return n
}

func TestRelevanceSaysWhyPartsAndTeachersAreMissing(t *testing.T) {
	state := auditedState(t)
	// No teacher log: the readers list the engine only and say the log is missing.
	o, _ := mustCode(t, 0, "relevance", "--json")
	var r relevanceReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.Readers == nil || r.Readers.TeacherStatus != eval.TeacherProxyMissing {
		t.Errorf("readers without a log: %+v", r.Readers)
	}
	// An impressions log that cannot be read: no part table, and the reason.
	imp := filepath.Join(state, "feedback", feedback.ImpressionsFile)
	if err := os.RemoveAll(imp); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(imp, 0o755); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 0, "relevance", "--json")
	r = relevanceReply{}
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.PartAUCError == "" || len(r.PartAUC) != 0 {
		t.Errorf("unreadable impressions must say so, not give a table: %q %+v", r.PartAUCError, r.PartAUC)
	}
	if text, _ := mustCode(t, 0, "relevance"); !strings.Contains(text, "not computed: read impressions") {
		t.Errorf("text report hides the failure:\n%s", text)
	}
	// A teacher log that is a directory is unreadable, not empty.
	if err := os.MkdirAll(filepath.Join(state, eval.TeacherFile), 0o755); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 0, "relevance", "--json")
	r = relevanceReply{}
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	if r.Readers == nil || r.Readers.TeacherStatus != eval.TeacherProxyUnreadable || r.Readers.TeacherDetail == "" {
		t.Errorf("a directory log: %+v", r.Readers)
	}
}
