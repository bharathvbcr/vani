package doctor

import (
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
	prompt := PromptSHA256("system")
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
	rows, _ := SilverRows(rep, cases, "r1", PromptSHA256("s"), keepNoWorkMode)
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
		Verdict: verdict, Cause: "none", Model: "m", PromptSHA256: PromptSHA256(prompt), TextSHA256: PromptSHA256(key)}
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
