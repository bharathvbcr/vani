package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/exportlog"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
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

// limitSeen is the limits jobsExport passed to the list builder.
type limitSeen struct {
	limits []int
}

func (s *limitSeen) install(t *testing.T) {
	t.Helper()
	old := jobsExportList
	t.Cleanup(func() { jobsExportList = old })
	jobsExportList = func(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *jobview.Opts, rk jobview.Ranker, info jobview.InsightInfo, kw, limit int) jobview.List {
		s.limits = append(s.limits, limit)
		return jobview.BuildList(snap, m, o, rk, info, kw, limit)
	}
}

func (s *limitSeen) last(t *testing.T) int {
	t.Helper()
	if len(s.limits) == 0 {
		t.Fatal("jobs export built no list")
	}
	return s.limits[len(s.limits)-1]
}

func exportReply(t *testing.T, code int, args ...string) jobsExportJSON {
	t.Helper()
	got, o, stderr := do(t, append([]string{"jobs", "export", "--json"}, args...)...)
	if got != code {
		t.Fatalf("exit %d, want %d\nstderr: %s\nstdout: %s", got, code, stderr, o)
	}
	var r jobsExportJSON
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("reply %q: %v", o, err)
	}
	return r
}

// Ask and skip with a non-empty export record stay inside the Jobs page.
// The default ask path omits --already-exported, which is what the Mac export uses.
// Row equality of that prefix matches the old whole-ledger call, so the recorded
// limit is what fails on math.MaxInt.
func TestJobsExportAskSkipPageBound(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 7)
	var seen limitSeen
	seen.install(t)

	// An empty record keeps --top. The page bound would be wrong here.
	empty := filepath.Join(t.TempDir(), "empty.xlsx")
	if r := exportReply(t, 0, "--dry-run", "--out", empty); r.Rows != 7 || r.AlreadyExported != 0 {
		t.Fatalf("empty ask %+v", r)
	}
	if lim := seen.last(t); lim == math.MaxInt || lim == defaultJobsLimit || lim != defaultExportTop {
		t.Fatalf("empty log limit %d, want --top %d", lim, defaultExportTop)
	}
	absent(t, empty)

	first := filepath.Join(t.TempDir(), "first.xlsx")
	if r := exportReply(t, 0, "--top", "1", "--out", first); r.Rows != 1 || r.AlreadyExported != 0 {
		t.Fatalf("first %+v", r)
	}
	if len(readRecord(t, state).Jobs) == 0 {
		t.Fatal("export record is empty")
	}

	// Default ask, flag omitted: refuse outside a terminal, and do not scan MaxInt.
	ask := filepath.Join(t.TempDir(), "ask.xlsx")
	r := exportReply(t, 1, "--out", ask)
	if r.Code != codeAlreadyExported || r.Rows != 7 {
		t.Fatalf("ask %+v", r)
	}
	if lim := seen.last(t); lim == math.MaxInt || lim != defaultJobsLimit {
		t.Fatalf("ask limit %d, want page bound %d", lim, defaultJobsLimit)
	}
	absent(t, ask)

	order := listKeys(t, "--limit", "7")
	skip := filepath.Join(t.TempDir(), "skip.xlsx")
	r = exportReply(t, 0, "--already-exported", "skip", "--top", "3", "--out", skip)
	if lim := seen.last(t); lim == math.MaxInt || lim != defaultJobsLimit {
		t.Fatalf("skip limit %d, want page bound %d", lim, defaultJobsLimit)
	}
	if r.Rows != 3 || r.Skipped != 1 || !slices.Equal(rowKeys(t, skip), order[1:4]) {
		t.Fatalf("skip %+v keys %v, want %v", r, rowKeys(t, skip), order[1:4])
	}

	// include keeps --top, which is not the page bound.
	inc := filepath.Join(t.TempDir(), "inc.xlsx")
	r = exportReply(t, 0, "--dry-run", "--already-exported", "include", "--top", "4", "--out", inc)
	if lim := seen.last(t); lim == math.MaxInt || lim == defaultJobsLimit || lim != 4 {
		t.Fatalf("include limit %d, want --top 4", lim)
	}
	if r.Rows != 4 {
		t.Fatalf("include %+v", r)
	}
	absent(t, inc)

	// --top above the page raises the bound to --top, not to the whole ledger.
	raised := filepath.Join(t.TempDir(), "raised.xlsx")
	r = exportReply(t, 1, "--top", "1200", "--out", raised)
	if r.Code != codeAlreadyExported {
		t.Fatalf("raised %+v", r)
	}
	if lim := seen.last(t); lim == math.MaxInt || lim != 1200 {
		t.Fatalf("raised limit %d, want 1200", lim)
	}
	absent(t, raised)

	capped := filepath.Join(t.TempDir(), "capped.xlsx")
	r = exportReply(t, 1, "--top", fmt.Sprint(maxJobsLimit), "--out", capped)
	if lim := seen.last(t); lim == math.MaxInt || lim != maxJobsLimit {
		t.Fatalf("top at max limit %d, want %d", lim, maxJobsLimit)
	}
	absent(t, capped)

	// Keys still need every selected job, even when the record is non-empty.
	withKeys(t, "gh:co4:4\ngh:co0:0\n")
	keysOut := filepath.Join(t.TempDir(), "keys.xlsx")
	r = exportReply(t, 0, "--keys-stdin", "--dry-run", "--out", keysOut)
	if lim := seen.last(t); lim != math.MaxInt {
		t.Fatalf("keys limit %d, want %d", lim, math.MaxInt)
	}
	if r.Rows != 2 || r.Total != 2 {
		t.Fatalf("keys %+v", r)
	}
	absent(t, keysOut)

	// Past the hard cap is still usage, and it does not build a list.
	n := len(seen.limits)
	over := filepath.Join(t.TempDir(), "over.xlsx")
	code, _, stderr := do(t, "jobs", "export", "--top", fmt.Sprint(maxJobsLimit+1), "--out", over)
	if code != 2 || !strings.Contains(stderr, fmt.Sprintf("must be 1-%d", maxJobsLimit)) {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(seen.limits) != n {
		t.Fatalf("usage error built a list: %v", seen.limits)
	}
	absent(t, over)
}

// BuildList still selects the whole snapshot, so ns/op may grow with the ledger; the flat metrics are the limit and the rows kept.
func BenchmarkJobsExportAskPageBound(b *testing.B) {
	for _, n := range []int{defaultJobsLimit, 4 * defaultJobsLimit} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			dir := b.TempDir()
			state := filepath.Join(dir, "state")
			b.Setenv("VANI_CONFIG", filepath.Join(dir, "config.json"))
			b.Setenv("VANI_STATE_DIR", state)
			seedExportBench(b, state, n)

			old := jobsExportList
			b.Cleanup(func() { jobsExportList = old })
			var limit int
			jobsExportList = func(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *jobview.Opts, rk jobview.Ranker, info jobview.InsightInfo, kw, got int) jobview.List {
				limit = got
				return jobview.BuildList(snap, m, o, rk, info, kw, got)
			}

			var setupOut, setupErr bytes.Buffer
			code := run([]string{"jobs", "export", "--json", "--top", "1", "--out", filepath.Join(dir, "first.xlsx")}, &setupOut, &setupErr)
			if code != 0 {
				b.Fatalf("setup export exit %d\n%s\n%s", code, setupOut.String(), setupErr.String())
			}

			b.ResetTimer()
			var rows int
			for i := 0; i < b.N; i++ {
				var o, e bytes.Buffer
				code = run([]string{"jobs", "export", "--json", "--dry-run", "--out", filepath.Join(dir, "ask.xlsx")}, &o, &e)
				if code != 0 {
					b.Fatalf("ask exit %d\n%s\n%s", code, o.String(), e.String())
				}
				var r jobsExportJSON
				if err := json.Unmarshal(o.Bytes(), &r); err != nil {
					b.Fatal(err)
				}
				rows = r.Rows
			}
			if limit != defaultJobsLimit || rows != defaultExportTop {
				b.Fatalf("limit %d rows %d, want %d and %d", limit, rows, defaultJobsLimit, defaultExportTop)
			}
			b.ReportMetric(float64(limit), "limit")
			b.ReportMetric(float64(rows), "rows")
		})
	}
}

func seedExportBench(b *testing.B, state string, n int) {
	b.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { l.Close() })
	for i := 0; i < n; i++ {
		p := posting.Posting{
			Key: posting.Key(fmt.Sprintf("gh:co%d:%d", i, i)), ATS: "gh",
			Company: fmt.Sprintf("Co%d", i), Title: fmt.Sprintf("Engineer %d", i),
			URL: fmt.Sprintf("https://boards.test/%d", i), Description: "Build things.",
		}
		ann := map[string]string{
			ledger.AnnExplanation:     "Fits: Inference serving and runtimes (88%).",
			"triage.role_fit":         "strong_fit",
			"triage.role_fit.reading": "answered",
			ledger.AnnRank:            "90",
		}
		ev := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), ev); err != nil {
			b.Fatal(err)
		}
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

// countReader counts Read calls. A prompt the export must not ask fails if this moves.
type countReader struct {
	n int
	r io.Reader
}

func (c *countReader) Read(p []byte) (int, error) {
	c.n++
	if c.r == nil {
		return 0, io.EOF
	}
	return c.r.Read(p)
}

func withCountedPrompt(t *testing.T, tty bool, answer string) *countReader {
	t.Helper()
	oldIn, oldTTY := exportPromptInput, exportPromptTTY
	cr := &countReader{r: strings.NewReader(answer)}
	t.Cleanup(func() { exportPromptInput, exportPromptTTY = oldIn, oldTTY })
	exportPromptInput = cr
	exportPromptTTY = func() bool { return tty }
	return cr
}

// boundProbe is the list the export built: the limit it asked for and the rows it got back.
// Cleanup is registered before the seam is assigned, same as limitSeen.install.
type boundProbe struct {
	limits []int
	got    []int
	keys   [][]string
}

func (p *boundProbe) install(t *testing.T) {
	t.Helper()
	old := jobsExportList
	t.Cleanup(func() { jobsExportList = old })
	jobsExportList = func(snap ledger.Snapshot, m map[posting.Key]marks.Entry, o *jobview.Opts, rk jobview.Ranker, info jobview.InsightInfo, kw, limit int) jobview.List {
		p.limits = append(p.limits, limit)
		l := jobview.BuildList(snap, m, o, rk, info, kw, limit)
		ks := make([]string, len(l.Jobs))
		for i, j := range l.Jobs {
			ks[i] = string(j.Key)
		}
		p.got = append(p.got, len(ks))
		p.keys = append(p.keys, ks)
		return l
	}
}

func (p *boundProbe) last(t *testing.T) (limit, n int, keys []string) {
	t.Helper()
	if len(p.limits) == 0 || len(p.got) != len(p.limits) || len(p.keys) != len(p.limits) {
		t.Fatal("jobs export built no list")
	}
	i := len(p.limits) - 1
	return p.limits[i], p.got[i], p.keys[i]
}

func (p *boundProbe) frozen(t *testing.T) int {
	t.Helper()
	return len(p.limits)
}

func (p *boundProbe) unchanged(t *testing.T, n int) {
	t.Helper()
	if len(p.limits) != n {
		t.Fatalf("list builder ran (%d calls, was %d): limits %v", len(p.limits), n, p.limits[n:])
	}
}

func rejectPersonal(t *testing.T, where, s string) {
	t.Helper()
	low := strings.ToLower(s)
	for _, bad := range []string{"@", "authorization", "555-", "example.com", "synthetic person"} {
		if strings.Contains(low, bad) {
			t.Errorf("%s contains %q", where, bad)
		}
	}
}

// Usage, an empty or duplicate key list, a missing log, and a non-TTY JSON refusal
// stay off math.MaxInt. An empty key list the reader cannot produce must not be
// treated as the keys path: readExportKeys returns a nil slice, and keys != nil
// is what selects MaxInt.
func TestJobsExportPageBoundAdversarial(t *testing.T) {
	_, state := setup(t)
	const n = 6
	seedExportJobs(t, state, n)
	order := listKeys(t, "--limit", fmt.Sprint(n))
	if len(order) != n {
		t.Fatalf("list returned %d keys, seeded %d; the page-bound check did not see the ledger", len(order), n)
	}
	for _, k := range order {
		rejectPersonal(t, "fixture key", k)
	}

	logPath := exportlog.Path(state)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("missing-log check did not run: %v", err)
	}

	var seen boundProbe
	seen.install(t)
	out := filepath.Join(t.TempDir(), "missing-log.xlsx")
	r := exportReply(t, 0, "--dry-run", "--out", out)
	lim, got, keys := seen.last(t)
	if lim == math.MaxInt || lim != defaultExportTop || got != n || r.Rows != n || r.Total != n || r.AlreadyExported != 0 {
		t.Fatalf("missing log limit %d got %d reply %+v, want limit %d and every row", lim, got, r, defaultExportTop)
	}
	if !slices.Equal(keys, order) {
		t.Fatalf("missing log built %v, list is %v", keys, order)
	}
	absent(t, out)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run created an export log: %v", err)
	}

	// Empty log: skip and include stay on --top, not the page, even though a page exists.
	r = exportReply(t, 0, "--dry-run", "--already-exported", "skip", "--top", "1", "--out", out)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim != 1 || got != 1 || r.Rows != 1 || r.Skipped != 0 || !slices.Equal(keys, order[:1]) {
		t.Fatalf("empty-log skip limit %d got %d keys %v reply %+v", lim, got, keys, r)
	}
	r = exportReply(t, 0, "--dry-run", "--already-exported", "include", "--top", "2", "--out", out)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim == defaultJobsLimit || lim != 2 || got != 2 || r.Rows != 2 || !slices.Equal(keys, order[:2]) {
		t.Fatalf("empty-log include limit %d got %d keys %v reply %+v", lim, got, keys, r)
	}

	before := seen.frozen(t)
	for _, already := range []string{"ask", "skip", "include"} {
		for _, top := range []string{"0", "-1", fmt.Sprint(maxJobsLimit + 1), "999999999999", "foo", "9223372036854775808"} {
			code, _, stderr := do(t, "jobs", "export", "--already-exported", already, "--top", top, "--out", out)
			if code != 2 || stderr == "" {
				t.Fatalf("already=%s top=%s: exit %d, stderr %q", already, top, code, stderr)
			}
			if top == "0" || top == "-1" || top == fmt.Sprint(maxJobsLimit+1) || top == "999999999999" {
				if !strings.Contains(stderr, fmt.Sprintf("must be 1-%d", maxJobsLimit)) {
					t.Fatalf("already=%s top=%s: stderr %q", already, top, stderr)
				}
			}
		}
	}
	for _, v := range []string{"", "yes", "SKIP", "skip ", "ask ", "include-all"} {
		code, _, stderr := do(t, "jobs", "export", "--already-exported", v, "--out", out)
		if code != 2 || !strings.Contains(stderr, "ask, skip or include") {
			t.Fatalf("%q: exit %d, stderr %q", v, code, stderr)
		}
	}
	withKeys(t, order[0]+"\n")
	if code, _, stderr := do(t, "jobs", "export", "--top", "1", "--keys-stdin", "--out", out); code != 2 || !strings.Contains(stderr, "give one") {
		t.Fatalf("top and keys: exit %d, stderr %q", code, stderr)
	}
	for _, in := range []struct{ keys, want string }{
		{"\n\n", "read no keys"},
		{" \n\t\n", "read no keys"},
		{"nocolon\n", "key"},
		{strings.Repeat("x", marks.MaxKeyBytes+8) + "\n", "keys-stdin"},
	} {
		withKeys(t, in.keys)
		code, _, stderr := do(t, "jobs", "export", "--keys-stdin", "--out", filepath.Join(t.TempDir(), "bad.xlsx"))
		if code != 2 || !strings.Contains(stderr, in.want) {
			t.Fatalf("keys %.20q: exit %d, stderr %q", in.keys, code, stderr)
		}
	}
	seen.unchanged(t, before)
	absent(t, out)

	// The reader has no non-nil empty list. That is what keeps "" off the MaxInt branch.
	for _, in := range []string{"", "\n", "\n\n", " \n\t\n"} {
		keys, err := readExportKeys(strings.NewReader(in))
		if err == nil || keys != nil {
			t.Fatalf("readExportKeys(%q) = %#v, %v; a non-nil empty list would pass math.MaxInt", in, keys, err)
		}
	}
	dup, err := readExportKeys(strings.NewReader(order[1] + "\n" + order[1] + "\n" + order[2] + "\n"))
	if err != nil || len(dup) != 2 || dup[0] != posting.Key(order[1]) || dup[1] != posting.Key(order[2]) {
		t.Fatalf("duplicate keys %#v, %v", dup, err)
	}

	first := filepath.Join(t.TempDir(), "first.xlsx")
	r = exportReply(t, 0, "--top", "1", "--out", first)
	lim, got, keys = seen.last(t)
	if lim != 1 || got != 1 || r.Rows != 1 || !slices.Equal(keys, order[:1]) || !slices.Equal(rowKeys(t, first), order[:1]) {
		t.Fatalf("first export limit %d got %d keys %v reply %+v", lim, got, keys, r)
	}
	if len(readRecord(t, state).Jobs) != 1 {
		t.Fatal("export log did not become non-empty; later ask/skip checks would not run")
	}

	// top=1 with a non-empty log still builds the page, and the kept row is the old top-1.
	withPrompt(t, true, "i\n")
	kept := filepath.Join(t.TempDir(), "kept.xlsx")
	mustCode(t, 0, "jobs", "export", "--top", "1", "--out", kept)
	if !slices.Equal(rowKeys(t, kept), order[:1]) {
		t.Fatalf("ask top=1 wrote %v, want %v", rowKeys(t, kept), order[:1])
	}
	lim, got, _ = seen.last(t)
	if lim == math.MaxInt || lim != defaultJobsLimit || got != n {
		t.Fatalf("ask top=1 limit %d got %d, want page %d over the whole selection %d", lim, got, defaultJobsLimit, n)
	}
	rejectPersonal(t, "ask top=1 workbook", strings.Join(rowKeys(t, kept), " "))

	ask := filepath.Join(t.TempDir(), "ask.xlsx")
	r = exportReply(t, 1, "--top", "1", "--out", ask)
	lim, got, _ = seen.last(t)
	if r.Code != codeAlreadyExported || r.Rows != 1 || lim == math.MaxInt || lim != defaultJobsLimit || got != n {
		t.Fatalf("json ask top=1 limit %d got %d reply %+v", lim, got, r)
	}
	absent(t, ask)
	r = exportReply(t, 1, "--already-exported", "ask", "--top", "1", "--out", ask)
	lim, _, _ = seen.last(t)
	if r.Code != codeAlreadyExported || lim != defaultJobsLimit {
		t.Fatalf("explicit ask limit %d reply %+v", lim, r)
	}

	skip := filepath.Join(t.TempDir(), "skip.xlsx")
	r = exportReply(t, 0, "--already-exported", "skip", "--top", "1", "--out", skip)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim != defaultJobsLimit || got != n || r.Rows != 1 || r.Skipped != 1 || !slices.Equal(rowKeys(t, skip), order[1:2]) || !slices.Equal(keys, order) {
		t.Fatalf("skip top=1 limit %d got %d keys %v rows %v reply %+v", lim, got, keys, rowKeys(t, skip), r)
	}

	r = exportReply(t, 0, "--dry-run", "--already-exported", "include", "--out", out)
	lim, got, _ = seen.last(t)
	if lim == math.MaxInt || lim == defaultJobsLimit || lim != defaultExportTop || got != n || r.Rows != n {
		t.Fatalf("include default top limit %d got %d reply %+v, want --top %d", lim, got, r, defaultExportTop)
	}

	// Dry-run: a real skip would record the rows it kept and change the next check.
	already := len(readRecord(t, state).Jobs)
	if already != 2 {
		t.Fatalf("log holds %d keys, want the first export and skip top=1; the raised-bound check would not run", already)
	}
	skip1200 := filepath.Join(t.TempDir(), "skip1200.xlsx")
	r = exportReply(t, 0, "--dry-run", "--already-exported", "skip", "--top", "1200", "--out", skip1200)
	lim, got, _ = seen.last(t)
	if lim == math.MaxInt || lim != 1200 || got != n || r.Rows != n-already || r.Skipped != already {
		t.Fatalf("skip raised limit %d got %d reply %+v", lim, got, r)
	}
	absent(t, skip1200)
	skipMax := filepath.Join(t.TempDir(), "skipmax.xlsx")
	r = exportReply(t, 0, "--dry-run", "--already-exported", "skip", "--top", fmt.Sprint(maxJobsLimit), "--out", skipMax)
	lim, got, _ = seen.last(t)
	if lim == math.MaxInt || lim != maxJobsLimit || got != n || r.Rows != n-already || r.Skipped != already {
		t.Fatalf("skip at max limit %d got %d reply %+v", lim, got, r)
	}
	absent(t, skipMax)
	r = exportReply(t, 1, "--top", fmt.Sprint(maxJobsLimit), "--out", filepath.Join(t.TempDir(), "askmax.xlsx"))
	lim, got, _ = seen.last(t)
	if r.Code != codeAlreadyExported || lim == math.MaxInt || lim != maxJobsLimit || got != n || r.Rows != n {
		t.Fatalf("ask at max limit %d got %d reply %+v", lim, got, r)
	}

	withKeys(t, order[1]+"\n"+order[1]+"\n"+order[2]+"\n")
	r = exportReply(t, 0, "--keys-stdin", "--dry-run", "--out", out)
	lim, got, _ = seen.last(t)
	if lim != math.MaxInt || got != n || r.Rows != 2 || r.Total != 2 || len(r.Missing) != 0 {
		t.Fatalf("duplicate keys limit %d got %d reply %+v", lim, got, r)
	}
	withKeys(t, order[0]+"\n"+order[2]+"\n")
	r = exportReply(t, 0, "--keys-stdin", "--already-exported", "skip", "--dry-run", "--out", out)
	lim, got, _ = seen.last(t)
	if lim != math.MaxInt || got != n || r.Rows != 1 || r.Skipped != 1 || r.Total != 2 {
		t.Fatalf("skip keys limit %d got %d reply %+v", lim, got, r)
	}

	// JSON refuses whether or not stdin is a terminal, and does not read the answer.
	// A waiting "i" would include the jobs if the prompt were consulted.
	for _, tty := range []bool{false, true} {
		cr := withCountedPrompt(t, tty, "i\n")
		refused := filepath.Join(t.TempDir(), "json.xlsx")
		r = exportReply(t, 1, "--top", "2", "--out", refused)
		if r.Code != codeAlreadyExported || r.Rows != 2 || cr.n != 0 {
			t.Fatalf("json tty=%v reads %d reply %+v", tty, cr.n, r)
		}
		absent(t, refused)
		lim, _, _ = seen.last(t)
		if lim == math.MaxInt || lim != defaultJobsLimit {
			t.Fatalf("json tty=%v limit %d", tty, lim)
		}
	}
	cr := withCountedPrompt(t, false, "i\n")
	textOut := filepath.Join(t.TempDir(), "text.xlsx")
	code, _, stderr := do(t, "jobs", "export", "--top", "2", "--out", textOut)
	if code != 1 || cr.n != 0 || !strings.Contains(stderr, "nothing written") {
		t.Fatalf("text notty reads %d exit %d stderr %q", cr.n, code, stderr)
	}
	absent(t, textOut)
	cr = withCountedPrompt(t, false, "i\n")
	r = exportReply(t, 0, "--dry-run", "--top", "2", "--out", textOut)
	lim, _, _ = seen.last(t)
	if !r.DryRun || r.Rows != 2 || r.Code != "" || cr.n != 0 || lim == math.MaxInt || lim != defaultJobsLimit {
		t.Fatalf("dry-run notty reads %d limit %d reply %+v", cr.n, lim, r)
	}
	absent(t, textOut)
	rejectPersonal(t, "stderr", stderr)
}

func TestJobsExportUnreadableLog(t *testing.T) {
	_, state := setup(t)
	seedExportJobs(t, state, 2)
	var seen boundProbe
	seen.install(t)
	out := filepath.Join(t.TempDir(), "x.xlsx")

	t.Run("directory", func(t *testing.T) {
		rec := exportlog.Path(state)
		if err := os.MkdirAll(rec, 0o700); err != nil {
			t.Fatal(err)
		}
		before := seen.frozen(t)
		code, stdout, stderr := do(t, "jobs", "export", "--out", out)
		if code == 0 || stderr == "" || strings.Contains(stdout, "exported ") {
			t.Fatalf("unreadable directory looked like a pass: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
		seen.unchanged(t, before)
		absent(t, out)
		if err := os.Remove(rec); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mode 000", func(t *testing.T) {
		rec := exportlog.Path(state)
		if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rec, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(rec, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(rec, 0o600) })
		before := seen.frozen(t)
		code, stdout, stderr := do(t, "jobs", "export", "--already-exported", "skip", "--out", out)
		if code == 0 || stderr == "" {
			t.Fatalf("mode 000 log looked like a pass: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
		seen.unchanged(t, before)
		absent(t, out)
	})
}

func TestJobsExportEmptySnapshotPageBound(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state) // ledger exists and holds nothing
	if _, err := exportlog.Record(context.Background(), exportlog.Path(state), []posting.Key{"gh:co0:0"}, filepath.Join(t.TempDir(), "prior.xlsx"), jobsT0); err != nil {
		t.Fatalf("could not plant a non-empty log: %v", err)
	}
	if len(readRecord(t, state).Jobs) != 1 {
		t.Fatal("empty-snapshot check has no export log")
	}
	var seen boundProbe
	seen.install(t)
	out := filepath.Join(t.TempDir(), "empty.xlsx")
	r := exportReply(t, 0, "--dry-run", "--out", out)
	lim, got, keys := seen.last(t)
	if lim == math.MaxInt || lim != defaultJobsLimit || got != 0 || len(keys) != 0 || r.Rows != 0 || r.Total != 0 || r.AlreadyExported != 0 || r.Code != "" {
		t.Fatalf("empty snapshot limit %d got %d keys %v reply %+v", lim, got, keys, r)
	}
	absent(t, out)
	// No rows were built, so a nil-keys path that passed MaxInt is still visible on the limit.
	r = exportReply(t, 0, "--dry-run", "--already-exported", "skip", "--top", "1", "--out", out)
	lim, got, _ = seen.last(t)
	if lim == math.MaxInt || lim != defaultJobsLimit || got != 0 || r.Rows != 0 || r.Skipped != 0 {
		t.Fatalf("empty snapshot skip limit %d got %d reply %+v", lim, got, r)
	}
	r = exportReply(t, 0, "--dry-run", "--already-exported", "include", "--top", "3", "--out", out)
	lim, got, _ = seen.last(t)
	if lim == math.MaxInt || lim != 3 || got != 0 || r.Rows != 0 {
		t.Fatalf("empty snapshot include limit %d got %d reply %+v", lim, got, r)
	}
}

// A ledger larger than the page: ask keeps the prefix of the bounded list, and
// skip does not walk past that bound to imitate MaxInt. ns/op is not the claim.
func TestJobsExportOversizedPageBound(t *testing.T) {
	raisedTop := 1200
	extra := 50
	if raisedTop <= defaultJobsLimit || raisedTop >= maxJobsLimit {
		t.Fatalf("raised top %d is not between the page %d and the max %d; the walk-past check would not run", raisedTop, defaultJobsLimit, maxJobsLimit)
	}
	n := raisedTop + extra
	if n > maxJobsLimit || extra <= 0 {
		t.Fatalf("seed %d does not leave jobs past the raised bound %d", n, raisedTop)
	}
	shortTop := 50
	onePast := defaultJobsLimit + 1
	if shortTop >= defaultJobsLimit || onePast >= raisedTop || onePast <= defaultJobsLimit {
		t.Fatalf("short %d or one-past %d no longer distinguishes the page from %d", shortTop, onePast, raisedTop)
	}

	_, state := setup(t)
	seedExportJobs(t, state, n)
	full := listJSON(t, "--limit", fmt.Sprint(n))
	if full.Total != n || len(full.Jobs) != n {
		t.Fatalf("list total %d shown %d, seeded %d; oversized check did not see the ledger", full.Total, len(full.Jobs), n)
	}
	allKeys := make([]string, n)
	for i, j := range full.Jobs {
		allKeys[i] = string(j.Key)
		rejectPersonal(t, "fixture", j.Company+" "+j.Title+" "+j.URL+" "+string(j.Key))
	}
	if len(allKeys) <= raisedTop || allKeys[len(allKeys)-1] == "" || allKeys[len(allKeys)-1] == allKeys[0] {
		t.Fatalf("order %d is not usable for a tail key", len(allKeys))
	}

	var seen boundProbe
	seen.install(t)
	out := filepath.Join(t.TempDir(), "empty-log.xlsx")

	r := exportReply(t, 0, "--dry-run", "--out", out)
	lim, got, keys := seen.last(t)
	if lim == math.MaxInt || lim != defaultExportTop || got != defaultExportTop || r.Rows != defaultExportTop || r.Total != n || r.AlreadyExported != 0 || !slices.Equal(keys, allKeys[:defaultExportTop]) {
		t.Fatalf("empty oversized ask limit %d got %d rows %d total %d", lim, got, r.Rows, r.Total)
	}
	r = exportReply(t, 0, "--dry-run", "--already-exported", "skip", "--top", "4", "--out", out)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim != 4 || got != 4 || r.Rows != 4 || r.Skipped != 0 || !slices.Equal(keys, allKeys[:4]) {
		t.Fatalf("empty oversized skip limit %d got %d reply %+v", lim, got, r)
	}
	r = exportReply(t, 0, "--dry-run", "--already-exported", "include", "--top", "4", "--out", out)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim == defaultJobsLimit || lim != 4 || got != 4 || r.Rows != 4 || !slices.Equal(keys, allKeys[:4]) {
		t.Fatalf("empty oversized include limit %d got %d reply %+v", lim, got, r)
	}
	absent(t, out)
	if _, err := os.Stat(exportlog.Path(state)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a log: %v", err)
	}

	page := make([]posting.Key, defaultJobsLimit)
	for i := range page {
		page[i] = posting.Key(allKeys[i])
	}
	if _, err := exportlog.Record(context.Background(), exportlog.Path(state), page, filepath.Join(t.TempDir(), "prior.xlsx"), jobsT0); err != nil {
		t.Fatalf("could not record the page: %v", err)
	}
	if len(readRecord(t, state).Jobs) != defaultJobsLimit {
		t.Fatalf("recorded %d, want the page %d", len(readRecord(t, state).Jobs), defaultJobsLimit)
	}

	// Default ask, flag omitted: the Mac path. Rows are --top of the page, not the ledger.
	r = exportReply(t, 1, "--out", filepath.Join(t.TempDir(), "ask.xlsx"))
	lim, got, keys = seen.last(t)
	if r.Code != codeAlreadyExported || r.Rows != defaultExportTop || r.Total != n || lim == math.MaxInt || lim != defaultJobsLimit || got != defaultJobsLimit || !slices.Equal(keys, allKeys[:defaultJobsLimit]) {
		t.Fatalf("oversized ask limit %d got %d reply %+v", lim, got, r)
	}
	r = exportReply(t, 1, "--already-exported", "ask", "--top", "1200", "--out", filepath.Join(t.TempDir(), "ask1200.xlsx"))
	lim, got, keys = seen.last(t)
	if r.Code != codeAlreadyExported || lim == math.MaxInt || lim != raisedTop || got != raisedTop || r.Rows != raisedTop || !slices.Equal(keys, allKeys[:raisedTop]) {
		t.Fatalf("ask raised limit %d got %d rows %d", lim, got, r.Rows)
	}
	r = exportReply(t, 1, "--top", fmt.Sprint(maxJobsLimit), "--out", filepath.Join(t.TempDir(), "askmax.xlsx"))
	lim, got, _ = seen.last(t)
	if r.Code != codeAlreadyExported || lim == math.MaxInt || lim != maxJobsLimit || got != n || r.Rows != n {
		t.Fatalf("ask at max on a smaller ledger limit %d got %d rows %d", lim, got, r.Rows)
	}

	r = exportReply(t, 0, "--dry-run", "--out", out)
	lim, got, keys = seen.last(t)
	if !r.DryRun || r.Rows != defaultExportTop || r.AlreadyExported != defaultExportTop || lim != defaultJobsLimit || got != defaultJobsLimit || !slices.Equal(keys, allKeys[:defaultJobsLimit]) {
		t.Fatalf("dry-run ask limit %d got %d reply %+v", lim, got, r)
	}

	skipShort := filepath.Join(t.TempDir(), "skip-short.xlsx")
	r = exportReply(t, 0, "--already-exported", "skip", "--top", fmt.Sprint(shortTop), "--out", skipShort)
	lim, got, keys = seen.last(t)
	remainder := n - defaultJobsLimit
	if lim == math.MaxInt || lim != defaultJobsLimit || got != defaultJobsLimit || r.Rows != 0 || r.Skipped != defaultJobsLimit || r.Total != n || !slices.Equal(keys, allKeys[:defaultJobsLimit]) {
		t.Fatalf("skip inside page limit %d got %d reply %+v (remainder past the page is %d)", lim, got, r, remainder)
	}
	if rows := exportRows(t, skipShort); len(rows) != 1 {
		t.Fatalf("skip inside page wrote %d rows", len(rows))
	}

	skipOne := filepath.Join(t.TempDir(), "skip-one.xlsx")
	r = exportReply(t, 0, "--already-exported", "skip", "--top", fmt.Sprint(onePast), "--out", skipOne)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim != onePast || got != onePast || r.Rows != 1 || r.Skipped != defaultJobsLimit || !slices.Equal(keys, allKeys[:onePast]) || !slices.Equal(rowKeys(t, skipOne), allKeys[defaultJobsLimit:onePast]) {
		t.Fatalf("skip one past page limit %d got %d rows %v keys %d reply %+v", lim, got, rowKeys(t, skipOne), len(keys), r)
	}
	// That write recorded the one new key. Put the log back to the page so the
	// raised-bound check is not short one row by this test's own export.
	if err := os.Remove(exportlog.Path(state)); err != nil {
		t.Fatal(err)
	}
	if _, err := exportlog.Record(context.Background(), exportlog.Path(state), page, filepath.Join(t.TempDir(), "prior-again.xlsx"), jobsT0); err != nil {
		t.Fatal(err)
	}
	if len(readRecord(t, state).Jobs) != defaultJobsLimit {
		t.Fatalf("page log was not restored (%d)", len(readRecord(t, state).Jobs))
	}

	skipRaised := filepath.Join(t.TempDir(), "skip-raised.xlsx")
	r = exportReply(t, 0, "--already-exported", "skip", "--top", fmt.Sprint(raisedTop), "--out", skipRaised)
	lim, got, keys = seen.last(t)
	wantRows := raisedTop - defaultJobsLimit
	if remainder <= wantRows {
		t.Fatal("no jobs past the raised bound; the walk-past check did not run")
	}
	wrote := rowKeys(t, skipRaised)
	if lim == math.MaxInt || lim != raisedTop || got != raisedTop || r.Rows != wantRows || r.Rows == remainder || r.Skipped != defaultJobsLimit || !slices.Equal(keys, allKeys[:raisedTop]) || !slices.Equal(wrote, allKeys[defaultJobsLimit:raisedTop]) {
		t.Fatalf("skip raised limit %d got %d rows %d want %d (not remainder %d) wrote %d", lim, got, r.Rows, wantRows, remainder, len(wrote))
	}
	if slices.Contains(wrote, allKeys[raisedTop]) {
		t.Fatalf("skip raised included %s, which is past the bound", allKeys[raisedTop])
	}

	r = exportReply(t, 0, "--dry-run", "--already-exported", "include", "--top", "4", "--out", out)
	lim, got, keys = seen.last(t)
	if lim == math.MaxInt || lim == defaultJobsLimit || lim != 4 || got != 4 || r.Rows != 4 || !slices.Equal(keys, allKeys[:4]) {
		t.Fatalf("include on a larger ledger limit %d got %d reply %+v", lim, got, r)
	}

	// A key past the page is not missing: keys stay on MaxInt and see the whole selection.
	tail := allKeys[n-1]
	if slices.Contains(allKeys[:defaultJobsLimit], tail) {
		t.Fatal("tail key is inside the page; the keys check would not leave the page")
	}
	withKeys(t, tail+"\n"+allKeys[0]+"\n"+tail+"\n")
	r = exportReply(t, 0, "--keys-stdin", "--dry-run", "--out", out)
	lim, got, _ = seen.last(t)
	if lim != math.MaxInt || got != n || r.Rows != 2 || r.Total != 2 || len(r.Missing) != 0 || r.AlreadyExported != 1 {
		t.Fatalf("tail key limit %d got %d reply %+v", lim, got, r)
	}
	for i, lim := range seen.limits[:len(seen.limits)-1] {
		if lim == math.MaxInt {
			t.Fatalf("non-keys call %d passed MaxInt", i)
		}
	}

	before := seen.frozen(t)
	if code, _, stderr := do(t, "jobs", "export", "--top", fmt.Sprint(maxJobsLimit+1), "--already-exported", "skip", "--out", out); code != 2 || !strings.Contains(stderr, fmt.Sprintf("must be 1-%d", maxJobsLimit)) {
		t.Fatalf("over max skip: exit %d, stderr %q", code, stderr)
	}
	seen.unchanged(t, before)

	withPrompt(t, true, "i\n")
	prefix := filepath.Join(t.TempDir(), "prefix.xlsx")
	text, stderr := mustCode(t, 0, "jobs", "export", "--out", prefix)
	lim, got, keys = seen.last(t)
	wrote = rowKeys(t, prefix)
	if lim == math.MaxInt || lim != defaultJobsLimit || got != defaultJobsLimit || !slices.Equal(keys, allKeys[:defaultJobsLimit]) || !slices.Equal(wrote, allKeys[:defaultExportTop]) || !strings.Contains(text, fmt.Sprintf("exported %d of %d jobs", defaultExportTop, n)) {
		t.Fatalf("prefix limit %d got %d wrote %d text %q", lim, got, len(wrote), text)
	}
	var cells strings.Builder
	for _, row := range exportRows(t, prefix) {
		for _, c := range row {
			cells.WriteString(c)
			cells.WriteByte('\n')
		}
	}
	rejectPersonal(t, "prefix workbook", cells.String())
	rejectPersonal(t, "prefix text", text)
	rejectPersonal(t, "prefix stderr", stderr)
}

// The seam's cleanup restores the func that was installed before it, including
// when the builder panics. Cleanups run on the way out, so a failed test does
// not need a second assignment to put the previous func back.
func TestJobsExportSeamRestores(t *testing.T) {
	if jobsExportList != nil {
		t.Fatal("seam already set; restoration cannot be judged from a dirty start")
	}
	var outer limitSeen
	outer.install(t)

	t.Run("panic", func(t *testing.T) {
		var inner limitSeen
		inner.install(t)
		defer func() {
			if recover() == nil {
				t.Error("builder did not panic; cleanup-after-panic did not run")
			}
		}()
		jobsExportList(ledger.Snapshot{}, nil, nil, nil, jobview.InsightInfo{}, listKeywords, 1)
	})
	if jobsExportList == nil {
		t.Fatal("panic cleanup cleared the seam instead of restoring the previous func")
	}
	jobsExportList(ledger.Snapshot{}, map[posting.Key]marks.Entry{}, &jobview.Opts{View: "new"}, nil, jobview.InsightInfo{}, listKeywords, 4)
	if len(outer.limits) != 1 || outer.limits[0] != 4 {
		t.Fatalf("previous func not restored after panic: %v", outer.limits)
	}

	t.Run("inner returns", func(t *testing.T) {
		var inner limitSeen
		inner.install(t)
		jobsExportList(ledger.Snapshot{}, map[posting.Key]marks.Entry{}, &jobview.Opts{View: "new"}, nil, jobview.InsightInfo{}, listKeywords, 3)
		if len(inner.limits) != 1 || inner.limits[0] != 3 {
			t.Fatalf("inner seam %v", inner.limits)
		}
		if len(outer.limits) != 1 {
			t.Fatalf("inner wrote the outer seam: %v", outer.limits)
		}
	})
	jobsExportList(ledger.Snapshot{}, map[posting.Key]marks.Entry{}, &jobview.Opts{View: "new"}, nil, jobview.InsightInfo{}, listKeywords, 5)
	if len(outer.limits) != 2 || outer.limits[1] != 5 {
		t.Fatalf("previous func not restored after inner returned: %v", outer.limits)
	}
}

// Non-parallel export tests finish, cleanups included, before a parallel test
// body runs. This observes the seam after that. It cannot by itself prove two
// overlapping bodies; no export test calls t.Parallel while the seam is set.
func TestJobsExportSeamInvisibleToParallel(t *testing.T) {
	t.Parallel()
	if jobsExportList != nil {
		t.Fatal("parallel test observed jobsExportList set by an export test")
	}
}
