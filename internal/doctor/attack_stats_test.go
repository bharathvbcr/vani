package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestAttackNewSilverRunSetSemantics(t *testing.T) {
	h := func(rules []string) string { return NewSilverRun("p", "s", rules).RulesSHA256 }
	if h([]string{"a", "b"}) != h([]string{"b", "a"}) {
		t.Error("order splits one rule set")
	}
	// Length prefix: no joining character can merge two lists.
	seen := map[string][]string{}
	lists := [][]string{nil, {}, {""}, {"", ""}, {"a"}, {"a", "b"}, {"ab"}, {"a:b"}, {"1:a"}, {"1:a", "b"}, {"a\x00b"}, {"a", "\x00b"}, {"a\nb"}, {"a", "b", "c"}, {"abc"}, {"a b"}, {"A"}}
	for _, l := range lists {
		k := h(l)
		if p, dup := seen[k]; dup && !(len(p) == 0 && len(l) == 0) {
			t.Errorf("%q and %q collide", p, l)
		}
		seen[k] = l
	}
	// Empty rules and no rules are one configuration (both are "no rules").
	if h(nil) != h([]string{}) {
		t.Error("nil and empty rules differ")
	}
	// The input slice is not reordered.
	in := []string{"z", "a"}
	NewSilverRun("p", "s", in)
	if in[0] != "z" {
		t.Error("NewSilverRun sorted the caller's slice")
	}
	// Summary and prompt hash separately: swapping them differs.
	if NewSilverRun("x", "y", nil) == NewSilverRun("y", "x", nil) {
		t.Error("prompt and summary swapped is the same run")
	}
	// Every field is 64 hex, so a written row validates.
	r := NewSilverRun("", "", nil)
	if len(r.PromptSHA256) != 64 || len(r.SummarySHA256) != 64 || len(r.RulesSHA256) != 64 {
		t.Errorf("empty inputs give hashes %+v", r)
	}
}

func TestAttackLegacyAndNewRowsNeverPoolAndEmptySummaryIsNotLegacy(t *testing.T) {
	mk := func(legacy bool) func(string, string) SilverRow {
		return func(k, v string) SilverRow {
			r := silverRow(k, v, "p", silverT0)
			if legacy {
				r.V, r.SummarySHA256, r.RulesSHA256 = 1, "", ""
			}
			return r
		}
	}
	a, labels := labelledRows(mk(true))
	b, _ := labelledRows(mk(false))
	as := BuildSilverAgreement(append(a, b...), labels)
	if len(as) != 2 {
		t.Fatalf("legacy and schema 2 rows pooled: %d groups", len(as))
	}
	for _, g := range as {
		if g.Verdicts != SilverMinYes+SilverMinNo || g.Status != "ok" {
			t.Errorf("group %+v", g)
		}
	}
	// A configuration that really had an empty summary and no rules hashes to
	// the hash of "", which is not "": it stays apart from legacy.
	e := NewSilverRun("p", "", nil)
	if e.SummarySHA256 == "" || e.RulesSHA256 == "" {
		t.Error("an empty configuration hashes to the empty string")
	}
	md := SilverMarkdown(as, 0)
	if !strings.Contains(md, "not recorded") {
		t.Errorf("legacy group not labelled:\n%s", md)
	}
}

func TestAttackModelCaseAndWhitespaceSplitGroups(t *testing.T) {
	// Documented behaviour: the model name is compared exactly.
	rowsA, labels := labelledRows(func(k, v string) SilverRow { return silverRow(k, v, "p", silverT0) })
	rowsB, _ := labelledRows(func(k, v string) SilverRow {
		r := silverRow(k, v, "p", silverT0)
		r.Model = "M"
		return r
	})
	if n := len(BuildSilverAgreement(append(rowsA, rowsB...), labels)); n != 2 {
		t.Errorf("groups %d", n)
	}
}

func TestAttackAllLabelsReadingSelected(t *testing.T) {
	labels := map[posting.Key]eval.Entry{}
	var rows []SilverRow
	for i := range 5 {
		k := fmt.Sprintf("s%d", i)
		labels[posting.Key(k)] = eval.Entry{Source: eval.CellTeacherWants, Labels: map[string]string{eval.WouldApply: "yes"}}
		rows = append(rows, silverRow(k, DropWrong, "p", silverT0))
	}
	as := BuildSilverAgreement(rows, labels)
	if len(as) != 1 || as[0].OwnerYes != 0 || as[0].OwnerNo != 0 || as[0].LabelsLeftOut != 5 || as[0].Status != "insufficient" {
		t.Fatalf("%+v", as)
	}
	md := SilverMarkdown(as, 0)
	if strings.Contains(md, "NaN") || strings.Contains(md, "%") {
		t.Errorf("n=0 group prints a rate:\n%s", md)
	}
	if !strings.Contains(md, "insufficient") {
		t.Errorf("no insufficiency stated:\n%s", md)
	}
	if b, err := json.Marshal(as); err != nil || !json.Valid(b) {
		t.Errorf("json %v", err)
	}
}

func TestAttackUnknownLabelSourcesAndDuplicates(t *testing.T) {
	labels := map[posting.Key]eval.Entry{}
	var rows []SilverRow
	sources := []string{"", "unknown:xyz", "DISAGREE", " ", "decile:", eval.CellTeacherWants, eval.DisagreePrefix + "x", eval.NeighbourPrefix + "y"}
	for i, src := range sources {
		k := fmt.Sprintf("k%d", i)
		labels[posting.Key(k)] = eval.Entry{Source: src, Labels: map[string]string{eval.WouldApply: "yes"}}
		// duplicate verdicts for the same key: newest wins, counted once
		rows = append(rows, silverRow(k, DropWrong, "p", silverT0), silverRow(k, DropWrong, "p", silverT0))
	}
	as := BuildSilverAgreement(rows, labels)
	if len(as) != 1 || as[0].Verdicts != len(sources) {
		t.Fatalf("%+v", as)
	}
	if as[0].OwnerYes+as[0].LabelsLeftOut != len(sources) {
		t.Errorf("a label was neither scored nor counted left out: %+v", as[0])
	}
	// Unknown sources fail towards scoring (not selected on a reading); the
	// ones SelectedOnReading names are left out.
	want := 0
	for _, s := range sources {
		if eval.SelectedOnReading(s) {
			want++
		}
	}
	if as[0].LabelsLeftOut != want {
		t.Errorf("left out %d, want %d", as[0].LabelsLeftOut, want)
	}
}

// Rows of a newer schema, or a half-hashed row, are skipped and counted, never
// silently pooled.
func TestAttackSilverReaderRejectsMalformedSchema2(t *testing.T) {
	good := silverRow("a", DropWrong, "p", silverT0)
	lines := []string{}
	enc := func(r SilverRow) string { b, _ := json.Marshal(r); return string(b) }
	lines = append(lines, enc(good))
	noRules := good
	noRules.Key, noRules.RulesSHA256 = "b", ""
	lines = append(lines, enc(noRules))
	v3 := good
	v3.Key, v3.V = "c", 3
	lines = append(lines, enc(v3))
	legacyWithHash := good
	legacyWithHash.Key, legacyWithHash.V = "d", 1
	lines = append(lines, enc(legacyWithHash))
	shortHash := good
	shortHash.Key, shortHash.SummarySHA256 = "e", "abc"
	lines = append(lines, enc(shortHash))
	p := filepath.Join(t.TempDir(), SilverFile)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSilverStore(p)
	if err != nil || len(s.Rows()) != 1 || s.Skipped != 4 {
		t.Fatalf("rows %d skipped %d err %v", len(s.Rows()), s.Skipped, err)
	}
	// Appending to a file holding legacy rows leaves them untouched.
	leg := good
	leg.Key, leg.V, leg.SummarySHA256, leg.RulesSHA256 = "old", 1, "", ""
	if err := os.WriteFile(p, []byte(enc(leg)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ = OpenSilverStore(p)
	if len(s.Rows()) != 1 {
		t.Fatal("legacy row not read")
	}
	nw := good
	nw.Key = "new"
	if n, err := s.Append([]SilverRow{nw}); err != nil || n != 1 {
		t.Fatalf("append: %d %v", n, err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), enc(leg)+"\n") {
		t.Error("the legacy row was rewritten")
	}
	s, _ = OpenSilverStore(p)
	if len(s.Rows()) != 2 || s.Skipped != 0 {
		t.Errorf("mixed file: rows %d skipped %d", len(s.Rows()), s.Skipped)
	}
}
