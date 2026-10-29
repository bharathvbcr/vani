package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/exportlog"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// withPrompt makes the export think it runs in a terminal (tty) whose owner
// answers answer.
func withPrompt(t *testing.T, tty bool, answer string) {
	t.Helper()
	oldIn, oldTTY := exportPromptInput, exportPromptTTY
	t.Cleanup(func() { exportPromptInput, exportPromptTTY = oldIn, oldTTY })
	exportPromptInput = strings.NewReader(answer)
	exportPromptTTY = func() bool { return tty }
}

func rowKeys(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	for _, r := range exportRows(t, path)[1:] {
		out = append(out, r[col("Key")])
	}
	return out
}

func listKeys(t *testing.T, args ...string) []string {
	t.Helper()
	var out []string
	for _, j := range listJSON(t, args...).Jobs {
		out = append(out, string(j.Key))
	}
	return out
}

func readRecord(t *testing.T, state string) exportlog.Log {
	t.Helper()
	l, err := exportlog.Read(exportlog.Path(state))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s was written: %v", path, err)
	}
}

// A second export of the same jobs is refused when it cannot ask: nothing is
// written, and the reply names how many, when, where, and the code to branch on.
func TestJobsExportRefusesJobsExportedBefore(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 5)
	first := filepath.Join(t.TempDir(), "first.xlsx")
	if r := exportJSON(t, "--top", "3", "--out", first); r.AlreadyExported != 0 || r.Skipped != 0 || r.LastExportedAt != nil {
		t.Fatalf("first export %+v", r)
	}
	if l := readRecord(t, state); len(l.Jobs) != 3 || len(l.Recent) != 1 || l.Recent[0].File != first || l.Recent[0].Rows != 3 {
		t.Fatalf("record %+v", l)
	}

	again := filepath.Join(t.TempDir(), "again.xlsx")
	code, o, _ := do(t, "jobs", "export", "--json", "--top", "4", "--out", again)
	var r jobsExportJSON
	if err := json.Unmarshal([]byte(o), &r); err != nil || code != 1 {
		t.Fatalf("exit %d, reply %q: %v", code, o, err)
	}
	if r.Code != codeAlreadyExported || r.AlreadyExported != 3 || r.Rows != 4 || r.LastFile != first || r.LastExportedAt == nil ||
		!strings.Contains(r.Error, "--already-exported skip") {
		t.Fatalf("refusal %+v", r)
	}
	absent(t, again)

	// Text mode, not at a terminal: the same refusal, on stderr.
	code, _, stderr := do(t, "jobs", "export", "--top", "4", "--out", again)
	if code != 1 || !strings.Contains(stderr, "3 of these 4 jobs were exported before") || !strings.Contains(stderr, first) ||
		!strings.Contains(stderr, "nothing written") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	absent(t, again)
	if l := readRecord(t, state); len(l.Recent) != 1 {
		t.Fatalf("a refused export was recorded: %+v", l.Recent)
	}
}

// Skip leaves exported jobs out and fills --top from the rest of the list, in
// its order; include writes them again and counts the second export.
func TestJobsExportSkipAndInclude(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 7)
	order := listKeys(t, "--limit", "7")
	exportJSON(t, "--top", "3", "--out", filepath.Join(t.TempDir(), "a.xlsx"))

	next := filepath.Join(t.TempDir(), "next.xlsx")
	r := exportJSON(t, "--top", "3", "--already-exported", "skip", "--out", next)
	if r.Rows != 3 || r.Total != 7 || r.Skipped != 3 || r.AlreadyExported != 3 {
		t.Fatalf("skip %+v", r)
	}
	if got := rowKeys(t, next); !slices.Equal(got, order[3:6]) {
		t.Fatalf("skip wrote %v, want the next three %v", got, order[3:6])
	}

	// One job left that was never exported: skip writes it alone.
	last := filepath.Join(t.TempDir(), "last.xlsx")
	if r := exportJSON(t, "--top", "5", "--already-exported", "skip", "--out", last); r.Rows != 1 || r.Skipped != 6 {
		t.Fatalf("skip to the end %+v", r)
	}
	if got := rowKeys(t, last); !slices.Equal(got, order[6:]) {
		t.Fatalf("wrote %v", got)
	}

	all := filepath.Join(t.TempDir(), "all.xlsx")
	if r := exportJSON(t, "--top", "7", "--already-exported", "include", "--out", all); r.Rows != 7 || r.AlreadyExported != 7 || r.Skipped != 0 {
		t.Fatalf("include %+v", r)
	}
	if got := rowKeys(t, all); !slices.Equal(got, order) {
		t.Fatalf("include wrote %v", got)
	}
	l := readRecord(t, state)
	if l.Jobs[posting.Key(order[0])].Count != 2 || l.Jobs[posting.Key(order[6])].Count != 2 || len(l.Recent) != 4 {
		t.Fatalf("record %+v", l)
	}

	// Everything exported: skip writes the header alone, and records nothing new.
	none := filepath.Join(t.TempDir(), "none.xlsx")
	if r := exportJSON(t, "--already-exported", "skip", "--out", none); r.Rows != 0 || r.Skipped != 7 {
		t.Fatalf("skip all %+v", r)
	}
	if rows := exportRows(t, none); len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
	if l := readRecord(t, state); len(l.Recent) != 4 {
		t.Fatalf("an empty export was recorded: %+v", l.Recent)
	}
}

// Selected keys: stdin holds the keys, so the export never asks, even at a
// terminal; skip leaves exported keys out without calling them missing.
func TestJobsExportSelectedKeysExportedBefore(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 5)
	withKeys(t, "gh:co1:1\ngh:co2:2\n")
	exportJSON(t, "--keys-stdin", "--out", filepath.Join(t.TempDir(), "a.xlsx"))

	withPrompt(t, true, "i\n")
	withKeys(t, "gh:co2:2\ngh:co3:3\ngh:gone:9\n")
	out := filepath.Join(t.TempDir(), "b.xlsx")
	code, _, stderr := do(t, "jobs", "export", "--keys-stdin", "--out", out)
	if code != 1 || !strings.Contains(stderr, "1 of these 2 jobs were exported before") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	absent(t, out)

	withKeys(t, "gh:co2:2\ngh:co3:3\ngh:gone:9\n")
	code, o, _ := do(t, "jobs", "export", "--json", "--keys-stdin", "--already-exported", "skip", "--out", out)
	var r jobsExportJSON
	if err := json.Unmarshal([]byte(o), &r); err != nil || code != 0 {
		t.Fatalf("exit %d, reply %q: %v", code, o, err)
	}
	if r.Rows != 1 || r.Total != 3 || r.Skipped != 1 || !slices.Equal(r.Missing, []posting.Key{"gh:gone:9"}) {
		t.Fatalf("reply %+v", r)
	}
	if got := rowKeys(t, out); !slices.Equal(got, []string{"gh:co3:3"}) {
		t.Fatalf("wrote %v", got)
	}
}

// At a terminal the export asks: s skips, i includes, anything else cancels.
func TestJobsExportAsksAtATerminal(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 4)
	order := listKeys(t, "--limit", "4")
	exportJSON(t, "--top", "2", "--out", filepath.Join(t.TempDir(), "a.xlsx"))

	for _, answer := range []string{"\n", "", "n\n", "yes\n", strings.Repeat("s", 1000)} {
		withPrompt(t, true, answer)
		out := filepath.Join(t.TempDir(), "c.xlsx")
		code, _, stderr := do(t, "jobs", "export", "--top", "2", "--out", out)
		if code != 1 || !strings.Contains(stderr, "[s/i/C]") || !strings.Contains(stderr, "cancelled; nothing written") {
			t.Fatalf("answer %.10q: exit %d, stderr %q", answer, code, stderr)
		}
		absent(t, out)
	}

	withPrompt(t, true, "S\n")
	skip := filepath.Join(t.TempDir(), "s.xlsx")
	text, _ := mustCode(t, 0, "jobs", "export", "--top", "2", "--out", skip)
	if got := rowKeys(t, skip); !slices.Equal(got, order[2:4]) || !strings.Contains(text, "left out 2 jobs exported before") {
		t.Fatalf("skip wrote %v, text %q", got, text)
	}

	withPrompt(t, true, " include \n")
	inc := filepath.Join(t.TempDir(), "i.xlsx")
	mustCode(t, 0, "jobs", "export", "--top", "2", "--out", inc)
	if got := rowKeys(t, inc); !slices.Equal(got, order[:2]) {
		t.Fatalf("include wrote %v", got)
	}

	// --json never asks, whatever stdin is.
	withPrompt(t, true, "i\n")
	if code, _, _ := do(t, "jobs", "export", "--json", "--top", "2", "--out", filepath.Join(t.TempDir(), "j.xlsx")); code != 1 {
		t.Fatalf("--json asked: exit %d", code)
	}
}

// --dry-run writes neither the workbook nor the record, and reports the count.
func TestJobsExportDryRun(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 4)
	out := filepath.Join(t.TempDir(), "d.xlsx")
	if r := exportJSON(t, "--dry-run", "--out", out); !r.DryRun || r.Rows != 4 || r.AlreadyExported != 0 || r.Path != out {
		t.Fatalf("dry run %+v", r)
	}
	absent(t, out)
	absent(t, exportlog.Path(state))

	exportJSON(t, "--top", "1", "--out", filepath.Join(t.TempDir(), "a.xlsx"))
	r := exportJSON(t, "--dry-run", "--out", out)
	if r.Rows != 4 || r.AlreadyExported != 1 || r.LastExportedAt == nil || r.Code != "" {
		t.Fatalf("dry run after an export %+v", r)
	}
	if r := exportJSON(t, "--dry-run", "--already-exported", "skip", "--top", "2", "--out", out); r.Rows != 2 || r.Skipped != 1 {
		t.Fatalf("dry run skip %+v", r)
	}
	absent(t, out)
	if l := readRecord(t, state); len(l.Recent) != 1 {
		t.Fatalf("a dry run was recorded: %+v", l.Recent)
	}
	text, stderr := mustCode(t, 0, "jobs", "export", "--dry-run", "--out", out)
	if !strings.HasPrefix(text, "would export 4 of 4 jobs") || !strings.Contains(stderr, "1 of these 4 jobs were exported before") {
		t.Fatalf("text %q, stderr %q", text, stderr)
	}
}

// The record fails closed: one that does not read stops the export before the
// workbook; one that cannot be written after it says the workbook exists.
func TestJobsExportRecordFailures(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 2)
	rec := exportlog.Path(state)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x.xlsx")
	if code, _, stderr := do(t, "jobs", "export", "--already-exported", "include", "--out", out); code != 1 || !strings.Contains(stderr, "corrupt") {
		t.Fatalf("corrupt record: exit %d, stderr %q", code, stderr)
	}
	absent(t, out)

	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rec+".lock", 0o700); err != nil { // the lock cannot be opened
		t.Fatal(err)
	}
	code, o, _ := do(t, "jobs", "export", "--json", "--out", out)
	var r jobsExportJSON
	if err := json.Unmarshal([]byte(o), &r); err != nil || code != 1 {
		t.Fatalf("exit %d, reply %q: %v", code, o, err)
	}
	if r.Code != codeRuntime || r.Path != out || r.Rows != 2 || !strings.Contains(r.Error, "wrote "+out+", but could not record") {
		t.Fatalf("reply %+v", r)
	}
	if rows := exportRows(t, out); len(rows) != 3 {
		t.Fatalf("rows %v", rows)
	}
}

func TestJobsExportAlreadyExportedFlagIsChecked(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 1)
	out := filepath.Join(t.TempDir(), "x.xlsx")
	for _, v := range []string{"", "yes", "SKIP", "skip "} {
		if code, _, stderr := do(t, "jobs", "export", "--already-exported", v, "--out", out); code != 2 || !strings.Contains(stderr, "ask, skip or include") {
			t.Errorf("%q: exit %d, stderr %q", v, code, stderr)
		}
	}
	absent(t, out)
}
