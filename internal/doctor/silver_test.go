package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var silverT0 = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func silverRun() (Report, []DropCase) {
	rep := Report{At: silverT0, Model: "claude-test", DropFinds: []DropFinding{
		{Key: "gh:a:1", Verdict: DropWrong, Cause: "title_rule_too_broad", Why: "SECRET-WHY-TEXT"},
		{Key: "gh:a:2", Verdict: DropCorrect, Cause: "none", Why: "SECRET-WHY-TEXT"},
		{Key: "gh:a:3", Verdict: DropUnclear, Cause: "other", Why: "SECRET-WHY-TEXT"},
		{Key: "gh:never:sent", Verdict: DropWrong, Cause: "other", Why: "x"},
	}}
	cases := []DropCase{
		{Key: "gh:a:1", Stratum: "dropped:title:role_fit", PSelect: 0.1, Company: "Acme", Title: "Staff Engineer", Description: "build"},
		{Key: "gh:a:2", Stratum: "dropped:model:role_fit", PSelect: 0.2, Company: "Acme", Title: "Welder", Description: "weld"},
		{Key: "gh:a:3", Stratum: "dropped:model:work_mode", PSelect: 0.3, Company: "Acme", Title: "Onsite", Description: "x"},
	}
	return rep, cases
}

var keepNoWorkMode = func(s string) bool { return !strings.HasSuffix(s, ":work_mode") }

// Rows carry no free text, only the findings on postings that were sent, and
// none from a stratum keep refuses; appending twice under one run adds nothing.
func TestSilverRowsAndIdempotentAppend(t *testing.T) {
	rep, cases := silverRun()
	prompt := NewSilverRun("system", "summary", []string{"rule"})
	rows, withheld := SilverRows(rep, cases, "20261113T090000Z", prompt, keepNoWorkMode)
	if len(rows) != 2 || withheld != 1 {
		t.Fatalf("rows %d withheld %d, want 2 and 1", len(rows), withheld)
	}
	if rows[0].TextSHA256 != TextSHA256(cases[0]) || rows[0].TextSHA256 == TextSHA256(cases[1]) || rows[0].Stratum != cases[0].Stratum || rows[0].PSelect != 0.1 {
		t.Fatalf("row %+v", rows[0])
	}
	path := filepath.Join(t.TempDir(), SilverFile)
	s, err := OpenSilverStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.Append(rows); err != nil || n != 2 {
		t.Fatalf("append %d %v", n, err)
	}
	if n, err := s.Append(rows); err != nil || n != 0 {
		t.Fatalf("a retried part appended %d (%v), want 0", n, err)
	}
	s2, err := OpenSilverStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s2.Append(rows); err != nil || n != 0 || len(s2.Rows()) != 2 {
		t.Fatalf("after reopening: appended %d (%v), rows %d", n, err, len(s2.Rows()))
	}
	// Another run of the same posting is a new verdict.
	again, _ := SilverRows(rep, cases, "20261120T090000Z", prompt, keepNoWorkMode)
	if n, _ := s2.Append(again); n != 2 {
		t.Fatalf("another run appended %d, want 2", n)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "SECRET-WHY-TEXT") || strings.Contains(string(raw), "Staff Engineer") {
		t.Fatalf("the log holds free text:\n%s", raw)
	}
}

// One bad row writes nothing; a torn tail is repaired, and a garbage line is
// skipped on read, not fatal.
func TestSilverAppendIsAllOrNothingAndSurvivesTornTails(t *testing.T) {
	rep, cases := silverRun()
	rows, _ := SilverRows(rep, cases, "r1", NewSilverRun("s", "t", nil), keepNoWorkMode)
	path := filepath.Join(t.TempDir(), SilverFile)
	s, _ := OpenSilverStore(path)
	bad := append(append([]SilverRow{}, rows...), SilverRow{RunID: "r1", Key: "gh:b:1", Stratum: "dropped:x", Model: "m", PSelect: 0.5, Verdict: "maybe", Cause: "none", PromptSHA256: rows[0].PromptSHA256, TextSHA256: rows[0].TextSHA256})
	if n, err := s.Append(bad); err == nil || n != 0 {
		t.Fatalf("a bad verdict appended %d (%v)", n, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused batch wrote the log: %v", err)
	}
	for _, r := range []SilverRow{{PSelect: 0}, {PSelect: 1.5}} {
		x := rows[0]
		x.PSelect = r.PSelect
		if _, err := s.Append([]SilverRow{x}); err == nil {
			t.Errorf("p_select %v accepted", r.PSelect)
		}
	}
	if err := os.WriteFile(path, []byte("not json\n{\"v\":1,\"run_id\":\"torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSilverStore(path)
	if err != nil || s.Skipped != 2 {
		t.Fatalf("skipped %d err %v, want 2 and nil", s.Skipped, err)
	}
	if n, err := s.Append(rows); err != nil || n != 2 {
		t.Fatalf("append after a torn tail: %d %v", n, err)
	}
	s, _ = OpenSilverStore(path)
	if len(s.Rows()) != 2 || s.Skipped != 2 {
		t.Fatalf("rows %d skipped %d after repair", len(s.Rows()), s.Skipped)
	}
}

func silverRow(key string, verdict, prompt string, at time.Time) SilverRow {
	return SilverRow{V: SilverSchema, RunID: at.Format(time.RFC3339), At: at, Key: posting.Key(key), Stratum: "dropped:title:role_fit", PSelect: 0.5,
		Verdict: verdict, Cause: "none", Model: "m", PromptSHA256: PromptSHA256(prompt), SummarySHA256: PromptSHA256("sum"), RulesSHA256: PromptSHA256("rules"), TextSHA256: PromptSHA256(key)}
}

// The comparison shows no figure below the auditor's floors, keeps prompts
// apart, takes the newest verdict per posting, and leaves maybe answers and
// unclear verdicts out.
func TestSilverAgreementFloorsAndPrompts(t *testing.T) {
	labels := map[posting.Key]eval.Entry{}
	var rows []SilverRow
	for i := range SilverMinYes {
		k := fmt.Sprintf("y%02d", i)
		labels[posting.Key(k)] = eval.Entry{Labels: map[string]string{eval.WouldApply: "yes"}}
		v := DropCorrect
		if i < 7 {
			v = DropWrong
		}
		rows = append(rows, silverRow(k, DropCorrect, "p1", silverT0), silverRow(k, v, "p1", silverT0.Add(time.Hour))) // newest wins
	}
	for i := range SilverMinNo - 1 {
		k := fmt.Sprintf("n%02d", i)
		labels[posting.Key(k)] = eval.Entry{Labels: map[string]string{eval.WouldApply: "no"}}
		rows = append(rows, silverRow(k, DropCorrect, "p1", silverT0))
	}
	labels["m0"] = eval.Entry{Labels: map[string]string{eval.WouldApply: "maybe"}}
	rows = append(rows, silverRow("m0", DropWrong, "p1", silverT0), silverRow("u0", DropUnclear, "p1", silverT0))
	labels["u0"] = eval.Entry{Labels: map[string]string{eval.WouldApply: "no"}}
	rows = append(rows, silverRow("y00", DropWrong, "p2", silverT0)) // another prompt

	as := BuildSilverAgreement(rows, labels)
	if len(as) != 2 {
		t.Fatalf("%d prompts, want 2", len(as))
	}
	var p1 SilverAgreement
	for _, a := range as {
		if a.PromptSHA256 == PromptSHA256("p1") {
			p1 = a
		} else if a.Status != "insufficient" || a.OwnerYes != 1 {
			t.Errorf("prompt p2 %+v", a)
		}
	}
	if p1.Status != "insufficient" || p1.OwnerYes != SilverMinYes || p1.OwnerNo != SilverMinNo-1 || p1.Unclear != 1 || p1.WrongRate != 0 {
		t.Fatalf("29 no labels: %+v, want insufficient with no figure", p1)
	}
	if md := SilverMarkdown(as, 0); !strings.Contains(md, "insufficient") || strings.Contains(md, "called a wrong drop") {
		t.Errorf("markdown:\n%s", md)
	}
	k := fmt.Sprintf("n%02d", SilverMinNo-1)
	labels[posting.Key(k)] = eval.Entry{Labels: map[string]string{eval.WouldApply: "no"}}
	rows = append(rows, silverRow(k, DropWrong, "p1", silverT0))
	for _, a := range BuildSilverAgreement(rows, labels) {
		if a.PromptSHA256 != PromptSHA256("p1") {
			continue
		}
		if a.Status != "ok" || a.WrongOnYes != 7 || a.CorrectOnNo != SilverMinNo-1 || a.WrongRate != 0.7 || !(a.WrongLo < 0.7 && a.WrongHi > 0.7) {
			t.Fatalf("at the floors: %+v", a)
		}
		if md := SilverMarkdown([]SilverAgreement{a}, 3); !strings.Contains(md, "owner yes called a wrong drop: 7 of 10") || !strings.Contains(md, "3 lines of the log") {
			t.Errorf("markdown:\n%s", md)
		}
	}
	if md := SilverMarkdown(nil, 0); !strings.Contains(md, "no data") {
		t.Errorf("empty markdown:\n%s", md)
	}
}

// Labels chosen because a model reading disagreed (eval.SelectedOnReading) are
// not independent of the verdicts they would score: they are left out of every
// count, and the comparison says how many it left out.
func TestSilverAgreementLeavesOutReadingSelectedLabels(t *testing.T) {
	labels := map[posting.Key]eval.Entry{}
	var rows []SilverRow
	for i := range 4 {
		k := fmt.Sprintf("s%d", i)
		labels[posting.Key(k)] = eval.Entry{Source: eval.CellTeacherWants, Labels: map[string]string{eval.WouldApply: "yes"}}
		rows = append(rows, silverRow(k, DropWrong, "p1", silverT0))
	}
	for i := range 2 {
		k := fmt.Sprintf("r%d", i)
		labels[posting.Key(k)] = eval.Entry{Source: "decile:3", Labels: map[string]string{eval.WouldApply: "yes"}}
		rows = append(rows, silverRow(k, DropWrong, "p1", silverT0))
	}
	if !eval.SelectedOnReading(eval.CellTeacherWants) || eval.SelectedOnReading("decile:3") {
		t.Fatal("fixture sources do not match eval.SelectedOnReading")
	}
	as := BuildSilverAgreement(rows, labels)
	if len(as) != 1 || as[0].OwnerYes != 2 || as[0].OwnerNo != 0 {
		t.Fatalf("reading-selected labels were scored: %+v", as)
	}
	if as[0].LabelsLeftOut != 4 {
		t.Errorf("left out %d, want 4", as[0].LabelsLeftOut)
	}
	md := SilverMarkdown(as, 0)
	if !strings.Contains(md, "on 2 owner labels") || !strings.Contains(md, "4 owner labels left out") {
		t.Errorf("markdown lacks n and the left-out count:\n%s", md)
	}
	// Only reading-selected labels: nothing is scored, and it says why.
	for _, k := range []string{"r0", "r1"} {
		delete(labels, posting.Key(k))
	}
	as = BuildSilverAgreement(rows, labels)
	if as[0].OwnerYes != 0 || as[0].Status != "insufficient" || as[0].LabelsLeftOut != 4 {
		t.Errorf("all-selected labels: %+v", as[0])
	}
}

// labelledRows gives each of SilverMinYes yes and SilverMinNo no postings one
// verdict built by mk, with the owner's label for it.
func labelledRows(mk func(key, verdict string) SilverRow) ([]SilverRow, map[posting.Key]eval.Entry) {
	labels := map[posting.Key]eval.Entry{}
	var rows []SilverRow
	for i := range SilverMinYes {
		k := fmt.Sprintf("y%02d", i)
		labels[posting.Key(k)] = eval.Entry{Labels: map[string]string{eval.WouldApply: "yes"}}
		rows = append(rows, mk(k, DropWrong))
	}
	for i := range SilverMinNo {
		k := fmt.Sprintf("n%02d", i)
		labels[posting.Key(k)] = eval.Entry{Labels: map[string]string{eval.WouldApply: "no"}}
		rows = append(rows, mk(k, DropCorrect))
	}
	return rows, labels
}

// Runs that differ in the summary, the rules or the model are separate rows,
// never pooled; one configuration still pools its own runs.
func TestSilverAgreementGroupsByPromptSummaryRulesAndModel(t *testing.T) {
	base := func(mut func(*SilverRow)) func(string, string) SilverRow {
		return func(k, v string) SilverRow {
			r := silverRow(k, v, "p", silverT0)
			if mut != nil {
				mut(&r)
			}
			return r
		}
	}
	variants := map[string]func(*SilverRow){
		"summary": func(r *SilverRow) { r.SummarySHA256 = PromptSHA256("other summary") },
		"rules":   func(r *SilverRow) { r.RulesSHA256 = PromptSHA256("other rules") },
		"model":   func(r *SilverRow) { r.Model = "other-model" },
		"prompt":  func(r *SilverRow) { r.PromptSHA256 = PromptSHA256("other prompt") },
	}
	for name, mut := range variants {
		rowsA, labels := labelledRows(base(nil))
		rowsB, _ := labelledRows(base(mut))
		as := BuildSilverAgreement(append(rowsA, rowsB...), labels)
		if len(as) != 2 {
			t.Fatalf("%s: %d rows, want 2 (runs under different configurations were pooled): %+v", name, len(as), as)
		}
		for _, a := range as {
			if a.Status != "ok" || a.OwnerYes != SilverMinYes || a.OwnerNo != SilverMinNo {
				t.Errorf("%s: %+v, want each configuration scored alone", name, a)
			}
		}
	}
	// The same configuration at two times is one row.
	rowsA, labels := labelledRows(base(nil))
	later, _ := labelledRows(func(k, v string) SilverRow { return silverRow(k, v, "p", silverT0.Add(time.Hour)) })
	if as := BuildSilverAgreement(append(rowsA, later...), labels); len(as) != 1 || as[0].Verdicts != SilverMinYes+SilverMinNo {
		t.Errorf("one configuration split: %+v", as)
	}
}

// Schema 1 rows (no summary or rules hash) are read and kept in a group of
// their own, said to have no recorded summary or rules; they are never
// written again.
func TestSilverLegacyRowsAreGroupedApartAndNeverWritten(t *testing.T) {
	legacy := func(k, v string) SilverRow {
		r := silverRow(k, v, "p", silverT0)
		r.V, r.SummarySHA256, r.RulesSHA256 = 1, "", ""
		return r
	}
	current := func(k, v string) SilverRow { return silverRow(k, v, "p", silverT0.Add(time.Hour)) }
	oldRows, labels := labelledRows(legacy)
	newRows, _ := labelledRows(current)

	path := filepath.Join(t.TempDir(), SilverFile)
	var raw []byte
	for _, r := range oldRows {
		line, _ := json.Marshal(r)
		raw = append(append(raw, line...), '\n')
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSilverStore(path)
	if err != nil || s.Skipped != 0 || len(s.Rows()) != len(oldRows) {
		t.Fatalf("legacy rows: read %d skipped %d err %v", len(s.Rows()), s.Skipped, err)
	}
	if _, err := s.Append(oldRows[:1]); err == nil {
		t.Error("a schema 1 row was appended")
	}
	if n, err := s.Append(newRows); err != nil || n != len(newRows) {
		t.Fatalf("append current rows: %d %v", n, err)
	}
	as := BuildSilverAgreement(s.Rows(), labels)
	if len(as) != 2 {
		t.Fatalf("%d rows, want the legacy and the current group apart: %+v", len(as), as)
	}
	md := SilverMarkdown(as, 0)
	if !strings.Contains(md, "summary and rules not recorded") || !strings.Contains(md, "model m") {
		t.Errorf("markdown:\n%s", md)
	}
	bad := silverRow("k", DropWrong, "p", silverT0)
	bad.V = 1 // schema 1 with a hash it cannot have
	if bad.validate() == nil {
		t.Error("a schema 1 row with a summary hash validated")
	}
	bad = silverRow("k", DropWrong, "p", silverT0)
	bad.SummarySHA256 = "short"
	if bad.validate() == nil {
		t.Error("a schema 2 row with a malformed summary hash validated")
	}
}

// The recorded hashes follow the summary and the rules, not their order.
func TestNewSilverRunHashesSummaryAndRulesAsSet(t *testing.T) {
	a := NewSilverRun("sys", "sum", []string{"r1", "r2"})
	if b := NewSilverRun("sys", "sum", []string{"r2", "r1"}); a != b {
		t.Error("rule order split the run")
	}
	for name, b := range map[string]SilverRun{
		"summary": NewSilverRun("sys", "sum2", []string{"r1", "r2"}),
		"rules":   NewSilverRun("sys", "sum", []string{"r1"}),
		"joined":  NewSilverRun("sys", "sum", []string{"r1r2"}),
		"system":  NewSilverRun("sys2", "sum", []string{"r1", "r2"}),
		"none":    NewSilverRun("sys", "sum", nil),
	} {
		if a == b {
			t.Errorf("%s did not change the run", name)
		}
	}
	if len(a.SummarySHA256) != 64 || len(a.RulesSHA256) != 64 {
		t.Errorf("hashes %+v", a)
	}
}
