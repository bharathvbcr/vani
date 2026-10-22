package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// failedBoards is a boards report in the shape of the owner's 2026-10-22 run:
// two boards failed, one read.
func failedBoards() *boardsReport {
	return &boardsReport{Configured: 3, Read: 1, Failed: 2, Listed: 35, Items: []boardReport{
		{ATS: "gh", Token: "nvidia", Company: "NVIDIA", Outcome: boardRead},
		{ATS: "eightfold", Token: "microsoft", Company: "Microsoft", Outcome: boardFailed, Error: "list: http 429 (too many requests) from apply.careers.microsoft.com"},
		{ATS: "wd", Token: "salesforce/External_Career_Site", Company: "Salesforce", Outcome: boardFailed, Error: "list: page 41: workday pagination returned the same page twice"},
	}}
}

func runsJSON[T any](t *testing.T, args ...string) T {
	t.Helper()
	o, _ := mustCode(t, 0, args...)
	var v T
	if err := json.Unmarshal([]byte(o), &v); err != nil {
		t.Fatalf("%v is not one JSON object: %v\n%s", args, err, o)
	}
	return v
}

func writeLast(t *testing.T, stateDir string, r runRecord) {
	t.Helper()
	if err := writeRunRecord(stateDir, r); err != nil {
		t.Fatal(err)
	}
}

func TestRunsListWithNoRuns(t *testing.T) {
	setup(t)
	o, _ := mustCode(t, 0, "runs", "list")
	if !strings.Contains(o, "no run has been recorded yet") {
		t.Errorf("text: %q", o)
	}
	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	if l.Version != runsVersion || l.Runs == nil || len(l.Runs) != 0 || l.Problems == nil || len(l.Problems) != 0 {
		t.Errorf("empty listing must carry empty arrays, not null: %+v", l)
	}
}

func TestRunsListNewestFirstMarksTheLatest(t *testing.T) {
	_, stateDir := setup(t)
	for _, i := range []int{3, 1, 2} {
		putHistoryRaw(t, stateDir, histRec(i, outcomeOK))
	}
	bad := histRec(2, outcomeFailed)
	bad.PID = 9
	bad.Boards = failedBoards()
	putHistoryRaw(t, stateDir, bad)
	writeLast(t, stateDir, histRec(3, outcomeOK))

	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	var ids []string
	for _, r := range l.Runs {
		ids = append(ids, r.ID)
	}
	want := []string{runID(histRec(3, "")), runID(histRec(2, "")), runID(bad), runID(histRec(1, ""))}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	for _, r := range l.Runs {
		if r.Latest != (r.ID == want[0]) {
			t.Errorf("%s latest=%v", r.ID, r.Latest)
		}
	}
	byID := map[string]runRow{}
	for _, r := range l.Runs {
		byID[r.ID] = r
	}
	if r := byID[runID(bad)]; !r.HasFailures || r.FailedBoards != 2 || r.State != outcomeFailed {
		t.Errorf("failed row: %+v", r)
	}
	if r := byID[runID(histRec(1, ""))]; r.HasFailures || r.DurationSec == nil || *r.DurationSec != 180 {
		t.Errorf("ok row: %+v", r)
	}
	text, _ := mustCode(t, 0, "runs", "list")
	if !strings.HasPrefix(text, "* "+want[0]) || !strings.Contains(text, "2 board(s) failed") {
		t.Errorf("text listing:\n%s", text)
	}
}

// A run in progress, or one whose history write failed, is only in last.json:
// the listing still shows it, once.
func TestRunsListIncludesTheRunOnlyLastJSONHolds(t *testing.T) {
	_, stateDir := setup(t)
	putHistoryRaw(t, stateDir, histRec(1, outcomeOK))
	running := histRec(2, outcomeRunning)
	running.FinishedAt = time.Time{}
	running.PID = os.Getpid()
	writeLast(t, stateDir, running)
	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	if len(l.Runs) != 2 || l.Runs[0].ID != runID(running) || l.Runs[0].Source != "last" || !l.Runs[0].Latest || l.Runs[0].State != outcomeRunning {
		t.Fatalf("runs: %+v", l.Runs)
	}
	if l.Runs[1].Source != "history" || l.Runs[1].Latest {
		t.Errorf("history row: %+v", l.Runs[1])
	}
}

// A history file that cannot be read is reported with its id and reason, the
// others are still listed: neither "no such run" nor a failed listing.
func TestRunsListReportsCorruptEntriesAndKeepsListingTheRest(t *testing.T) {
	_, stateDir := setup(t)
	good := putHistoryRaw(t, stateDir, histRec(1, outcomeOK))
	corrupt := runID(histRec(2, ""))
	writeStateFile(t, stateDir, "runs/history/"+corrupt+".json", `{"v":1,"pid":`)
	newer := runID(histRec(3, ""))
	writeStateFile(t, stateDir, "runs/history/"+newer+".json", `{"v":99,"pid":1,"started_at":"2026-10-22T09:00:00Z","outcome":"ok"}`)
	huge := runID(histRec(4, ""))
	writeStateFile(t, stateDir, "runs/history/"+huge+".json", strings.Repeat(" ", maxRunRecord+10))
	link := runID(histRec(5, ""))
	if err := os.Symlink(filepath.Join(stateDir, "runs", "history", good+".json"), filepath.Join(stateDir, "runs", "history", link+".json")); err != nil {
		t.Fatal(err)
	}

	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	if len(l.Runs) != 1 || l.Runs[0].ID != good {
		t.Fatalf("runs: %+v", l.Runs)
	}
	reasons := map[string]string{}
	for _, p := range l.Problems {
		reasons[p.ID] = p.Error
	}
	for id, want := range map[string]string{corrupt: "corrupt", newer: "version 99", huge: "exceeds", link: "not a regular file"} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("problem for %s = %q, want it to say %q", id, reasons[id], want)
		}
	}
	text, _ := mustCode(t, 0, "runs", "list")
	if strings.Count(text, "unreadable") != 4 {
		t.Errorf("text listing must show each unreadable entry:\n%s", text)
	}
	// Asking for a corrupt run names why, and does not call it unknown.
	_, stderr := mustCode(t, exitRuntime, "runs", "show", corrupt)
	if !strings.Contains(stderr, "corrupt") || strings.Contains(stderr, "no run ") {
		t.Errorf("show of a corrupt entry: %q", stderr)
	}
}

func TestRunsListBoundsWhatItReads(t *testing.T) {
	_, stateDir := setup(t)
	for i := range maxRunHistoryScan + 7 {
		putHistoryRaw(t, stateDir, histRec(i, outcomeOK))
	}
	l := runsJSON[runsListJSON](t, "runs", "list", "--json")
	if len(l.Runs) != maxRunHistoryScan || l.Unlisted != 7 {
		t.Fatalf("%d runs, %d unlisted; want %d and 7", len(l.Runs), l.Unlisted, maxRunHistoryScan)
	}
	if l.Runs[0].ID != runID(histRec(maxRunHistoryScan+6, "")) {
		t.Errorf("newest first: %s", l.Runs[0].ID)
	}
	if text, _ := mustCode(t, 0, "runs", "list"); !strings.Contains(text, "7 older history file(s) not listed") {
		t.Errorf("text must say what it left out:\n%s", text[max(len(text)-200, 0):])
	}
}

func TestRunsShowByIDLatestAndUnknown(t *testing.T) {
	_, stateDir := setup(t)
	old := histRec(1, outcomeFailed)
	old.Boards = failedBoards()
	oldID := putHistoryRaw(t, stateDir, old)
	writeLast(t, stateDir, histRec(2, outcomeOK))

	s := runsJSON[runShowJSON](t, "runs", "show", "--json", oldID)
	if s.ID != oldID || s.Source != "history" || s.State != outcomeFailed || s.Record.Boards == nil || s.Record.Boards.Failed != 2 {
		t.Errorf("show by id: %+v", s)
	}
	// The flag may follow the id.
	if s2 := runsJSON[runShowJSON](t, "runs", "show", oldID, "--json"); s2.ID != oldID {
		t.Errorf("--json after the id: %+v", s2)
	}
	latest := runsJSON[runShowJSON](t, "runs", "show", "--json")
	if latest.ID != runID(histRec(2, "")) || latest.Source != "last" {
		t.Errorf("default is the latest: %+v", latest)
	}
	if again := runsJSON[runShowJSON](t, "runs", "show", "--json", "latest"); again.ID != latest.ID {
		t.Errorf("explicit latest: %+v", again)
	}
	// An id that last.json holds but the history does not is found there.
	only := histRec(5, outcomeFailed)
	writeLast(t, stateDir, only)
	if s := runsJSON[runShowJSON](t, "runs", "show", "--json", runID(only)); s.Source != "last" {
		t.Errorf("id of the last record: %+v", s)
	}

	o, _ := mustCode(t, 0, "runs", "show", oldID)
	for _, want := range []string{"Run: " + oldID, "Boards: 1 of 3 read, 2 failed", "Failed boards (2 of 3):"} {
		if !strings.Contains(o, want) {
			t.Errorf("show text lacks %q:\n%s", want, o)
		}
	}

	// Unknown: exit 1 with the reason; in --json, {"error"} on stdout.
	unknown := runID(histRec(77, ""))
	_, stderr := mustCode(t, exitRuntime, "runs", "show", unknown)
	if !strings.Contains(stderr, "no run "+unknown) {
		t.Errorf("stderr: %q", stderr)
	}
	o, _ = mustCode(t, exitRuntime, "runs", "failures", "--json", unknown)
	var e struct{ Error string }
	if err := json.Unmarshal([]byte(o), &e); err != nil || !strings.Contains(e.Error, "no run "+unknown) {
		t.Errorf("json error reply: %q (%v)", o, err)
	}
}

func TestRunsLatestWithNoRunIsAnErrorNotAnEmptyReport(t *testing.T) {
	setup(t)
	_, stderr := mustCode(t, exitRuntime, "runs", "failures")
	if !strings.Contains(stderr, "no run has been recorded yet") {
		t.Errorf("stderr: %q", stderr)
	}
	_, stateDir := setup(t)
	writeStateFile(t, stateDir, "runs/last.json", "{")
	_, stderr = mustCode(t, exitRuntime, "runs", "failures")
	if !strings.Contains(stderr, "corrupt") {
		t.Errorf("an unreadable last.json is not 'no run': %q", stderr)
	}
}

// An id from the command line names a history file by pattern only, so no
// argument reaches outside the directory.
func TestRunsRefuseMalformedIDs(t *testing.T) {
	_, stateDir := setup(t)
	writeStateFile(t, stateDir, "secret.json", "{}")
	for _, id := range []string{"../../secret", "..", "last", "20261022", runID(histRec(1, ""))[1:], "20261022T090000.123Z-1/../../x", strings.Repeat("9", 5000), "-x"} {
		for _, sub := range []string{"show", "failures"} {
			code, _, stderr := do(t, "runs", sub, id)
			if code != exitUsage {
				t.Errorf("runs %s %.30q: exit %d (%q), want usage error %d", sub, id, code, stderr, exitUsage)
			}
			// The usage text follows the error line; the line itself must not echo the argument whole.
			if first, _, _ := strings.Cut(stderr, "\n"); len(first) > 300 {
				t.Errorf("runs %s: the error line echoes the whole argument (%d bytes)", sub, len(first))
			}
		}
	}
	for _, args := range [][]string{{"runs"}, {"runs", "wat"}, {"runs", "list", "x"}, {"runs", "show", "a", "b"}, {"runs", "failures", "--nope"}} {
		if code, _, _ := do(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func failuresOf(t *testing.T, args ...string) runFailuresJSON {
	t.Helper()
	return runsJSON[runFailuresJSON](t, append([]string{"runs", "failures", "--json"}, args...)...)
}

func TestRunsFailuresOfASuccessfulRunSaysSo(t *testing.T) {
	_, stateDir := setup(t)
	ok := histRec(1, outcomeOK)
	ok.Boards = &boardsReport{Configured: 1, Read: 1, Listed: 3, Items: []boardReport{{ATS: "gh", Token: "a", Outcome: boardRead}}}
	writeLast(t, stateDir, ok)
	rep := failuresOf(t)
	if rep.HasFailures || rep.Version != runsVersion || rep.ID != runID(ok) {
		t.Fatalf("reply: %+v", rep)
	}
	if !strings.Contains(rep.Text, "No failures in this run.") || strings.Contains(rep.Text, "Failed boards") {
		t.Errorf("text:\n%s", rep.Text)
	}
	// The text form prints exactly the JSON's text.
	if o, _ := mustCode(t, 0, "runs", "failures"); o != rep.Text {
		t.Errorf("text form differs from the JSON text:\n%s\n---\n%s", o, rep.Text)
	}
}

func TestRunsFailuresOneFailedBoard(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeOK)
	r.Boards = failedBoards()
	r.Boards.Failed = 1
	r.Boards.Items = r.Boards.Items[:2]
	writeLast(t, stateDir, r)
	writeStateFile(t, stateDir, "runs/manual.log", "vani: board eightfold:microsoft: http 429\n")
	rep := failuresOf(t)
	if !rep.HasFailures {
		t.Fatal("a run with a failed board has failures")
	}
	for _, want := range []string{
		"Vani run report", "Run: " + runID(r), "Trigger: schedule", "Started: 2026-10-22T09:01:00Z", "Finished: 2026-10-22T09:04:00Z (took 3m0s)", "Outcome: ok",
		"Failed boards (1 of 3):", "eightfold:microsoft (Microsoft): list: http 429 (too many requests) from apply.careers.microsoft.com",
		"Run record: " + filepath.Join(stateDir, "runs", "last.json"),
		"Log: " + filepath.Join(stateDir, "runs", "manual.log") + " (",
	} {
		if !strings.Contains(rep.Text, want) {
			t.Errorf("report lacks %q:\n%s", want, rep.Text)
		}
	}
	if strings.Contains(rep.Text, "nvidia") || strings.Contains(rep.Text, "Salesforce") || strings.Contains(rep.Text, "Backend errors") || strings.Contains(rep.Text, "No failures") {
		t.Errorf("report names something that did not fail:\n%s", rep.Text)
	}
}

// A rate-limited board counts in Failed (cmd/vani/boards.go); the report names
// it like a failed one, never as a board the record dropped.
func TestRunsFailuresNamesARateLimitedBoard(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeOK)
	r.Boards = &boardsReport{Configured: 2, Read: 1, Failed: 1, RateLimited: 1, Items: []boardReport{
		{ATS: "gh", Token: "a", Company: "A", Outcome: boardRead},
		{ATS: "ef", Token: "microsoft.com", Company: "Microsoft", Outcome: boardRateLimited,
			Error: "rate limited by apply.careers.microsoft.com (HTTP 429), retry next run"},
	}}
	writeLast(t, stateDir, r)
	rep := failuresOf(t)
	if !rep.HasFailures {
		t.Fatal("a run with a rate-limited board has failures")
	}
	if !strings.Contains(rep.Text, "ef:microsoft.com (Microsoft): rate limited by apply.careers.microsoft.com (HTTP 429), retry next run") {
		t.Errorf("report lacks the rate-limited board:\n%s", rep.Text)
	}
	if strings.Contains(rep.Text, "not in the record") {
		t.Errorf("report says a recorded board was dropped:\n%s", rep.Text)
	}
}

func TestRunsFailuresManyFailures(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeFailed)
	r.Trigger = triggerManual
	r.Options = &runOptions{Boards: []string{"a:b", "c:d"}, Advance: 50, NoDigest: true}
	r.Stage, r.Cause = stageDigest, causeDigestHeldBack
	r.Error = "digest: the Jobs marks could not be read"
	r.Boards = failedBoards()
	r.Boards.Failed, r.Boards.Unrecorded = 5, 3 // 3 failed boards the record does not name
	r.BackendErrors = &backendErrorReport{Causes: []backendErrorCause{
		{Backend: "openrouter/some-model", Count: 5, Example: "http 402: insufficient credits (request gen-abc)"},
		{Backend: "anthropic", Count: 2, Example: "http 429: rate_limit_error"},
	}, Unrecorded: 4}
	r.CountsBefore, r.CountsAfter = map[string]int{"live": 5}, map[string]int{"live": 7}
	writeLast(t, stateDir, r)
	writeStateFile(t, stateDir, "logs/vani.err.log", "x")

	rep := failuresOf(t)
	for _, want := range []string{
		"Trigger: manual (2 boards; advance cap 50; no digest)", "Outcome: failed", "Failed at: digest", "Error: digest: the Jobs marks could not be read", "Cause: digest_held_back",
		"Failed boards (5 of 3):", "eightfold:microsoft (Microsoft): list: http 429", "wd:salesforce/External_Career_Site (Salesforce): list: page 41",
		"(3 more failed board(s) are not in the record",
		"Backend errors (11 call(s), 2 cause(s) recorded):", "openrouter/some-model (5 call(s)): http 402", "anthropic (2 call(s)): http 429: rate_limit_error",
		"(4 more call(s) failed with causes not recorded)",
		"Log: " + filepath.Join(stateDir, "logs", "vani.err.log"),
	} {
		if !strings.Contains(rep.Text, want) {
			t.Errorf("report lacks %q:\n%s", want, rep.Text)
		}
	}
	if strings.Contains(rep.Text, "Ledger:") {
		t.Errorf("the copy report is not the full show text:\n%s", rep.Text)
	}
	if full, _ := mustCode(t, 0, "runs", "show", runID(r)); !strings.Contains(full, "Ledger: 5 -> 7 postings") {
		t.Errorf("show adds the ledger line:\n%s", full)
	}
}

// A run still going is not a failure; one whose process is gone is.
func TestRunsFailuresOfAnInterruptedRun(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeRunning)
	r.FinishedAt = time.Time{}
	r.PID = os.Getpid()
	writeLast(t, stateDir, r)
	if rep := failuresOf(t); rep.HasFailures || !strings.Contains(rep.Text, "Outcome: running") || !strings.Contains(rep.Text, "Finished: not recorded") {
		t.Errorf("a live run: %+v", rep)
	}
	old := pidAlive
	pidAlive = func(int) bool { return false }
	t.Cleanup(func() { pidAlive = old })
	if rep := failuresOf(t); !rep.HasFailures || !strings.Contains(rep.Text, "Outcome: interrupted") {
		t.Errorf("a dead run: %+v", rep)
	}
}

// One over-long error line is cut at maxReportLine and the report says so;
// the record's own bounds would have kept it shorter, so this is a record from
// elsewhere.
func TestRunsFailuresCutsAnOverLongLine(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeFailed)
	r.Error = strings.Repeat("é", 5000)
	r.Boards = &boardsReport{Configured: 1, Failed: 1, Items: []boardReport{{ATS: "wd", Token: "t", Outcome: boardFailed, Error: strings.Repeat("x", 9000)}}}
	writeLast(t, stateDir, r)
	rep := failuresOf(t)
	if rep.ShortenedLines != 2 || rep.CutLines != 0 {
		t.Errorf("shortened %d, cut %d; want 2 and 0", rep.ShortenedLines, rep.CutLines)
	}
	for _, l := range strings.Split(rep.Text, "\n") {
		if n := utf8.RuneCountInString(l); n > maxReportLine+len("Error: ") && !strings.HasPrefix(l, "Run record:") {
			t.Errorf("a line of %d runes: %.60s…", n, l)
		}
	}
	if !strings.Contains(rep.Text, "[2 line(s) shortened to 600 characters; vani runs show --json "+runID(r)+" has the whole record]") {
		t.Errorf("the report does not say what it cut:\n%s", rep.Text[max(len(rep.Text)-300, 0):])
	}
}

// Past the byte bound the failed boards at the end are cut and counted; the
// header, the files and the footer survive, and the report stays bounded.
func TestRunsFailuresIsBoundedAndSaysWhatItCut(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeFailed)
	b := &boardsReport{Configured: 400, Failed: 400}
	for i := range 400 {
		b.Items = append(b.Items, boardReport{ATS: "wd", Token: fmt.Sprintf("tok%03d", i), Company: "Co", Outcome: boardFailed, Error: strings.Repeat("e", 400)})
	}
	r.Boards = b
	writeLast(t, stateDir, r)
	rep := failuresOf(t)
	if len(rep.Text) > maxReportBytes {
		t.Errorf("report is %d bytes, over the %d bound", len(rep.Text), maxReportBytes)
	}
	if rep.CutLines == 0 || rep.CutLines > 400 {
		t.Fatalf("cut lines = %d", rep.CutLines)
	}
	kept := strings.Count(rep.Text, "wd:tok")
	if kept+rep.CutLines != 400 {
		t.Errorf("kept %d + cut %d != 400 failed boards: a capped sample must carry both numbers", kept, rep.CutLines)
	}
	for _, want := range []string{"Run: " + runID(r), "Run record: ", "Failed boards (400 of 400):", fmt.Sprintf("%d line(s) cut at 24 KiB", rep.CutLines), "wd:tok000"} {
		if !strings.Contains(rep.Text, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(rep.Text, fmt.Sprintf("wd:tok%03d", 399)) {
		t.Errorf("the cut must take the end, not the start")
	}
}

// Text from a record is untrusted: control characters and newlines collapse so
// one failure stays one line, and a key or header in an error is not copied.
func TestRunsFailuresOneLineEachAndNoSecrets(t *testing.T) {
	_, stateDir := setup(t)
	r := histRec(1, outcomeFailed)
	r.Error = "boom\nVani run report\nRun: injected\x1b[31m"
	r.Boards = &boardsReport{Configured: 1, Failed: 1, Items: []boardReport{{ATS: "gh", Token: "a", Outcome: boardFailed,
		Error: "http 401: Authorization: Bearer sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAA and api_key=hunter2hunter2 sent"}}}
	r.BackendErrors = &backendErrorReport{Causes: []backendErrorCause{{Backend: "openrouter", Count: 1, Example: "bad key sk-or-v1-0123456789abcdef0123456789abcdef; X-Api-Key: abcdef123456"}}}
	writeLast(t, stateDir, r)
	rep := failuresOf(t)
	for _, prefix := range []string{"Run: ", "Vani run report"} {
		n := 0
		for _, l := range strings.Split(rep.Text, "\n") {
			if strings.HasPrefix(l, prefix) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("a newline in an error forged a line (%d lines start %q):\n%s", n, prefix, rep.Text)
		}
	}
	if strings.ContainsRune(rep.Text, '\x1b') {
		t.Errorf("control character copied")
	}
	for _, leak := range []string{"AAAAAAAAAAAAAAAA", "hunter2", "0123456789abcdef", "abcdef123456", "Bearer sk"} {
		if strings.Contains(rep.Text, leak) {
			t.Errorf("report leaks %q:\n%s", leak, rep.Text)
		}
	}
	if !strings.Contains(rep.Text, "[redacted]") {
		t.Errorf("nothing was redacted:\n%s", rep.Text)
	}
	// What is not a secret survives: the status code and the board.
	if !strings.Contains(rep.Text, "gh:a: http 401") {
		t.Errorf("redaction ate the error:\n%s", rep.Text)
	}
}

// A record that is a failure of the shape the contract names agrees with the
// Mac app's button rule: these are the cases LastRun.hasFailures is tested on.
func TestRunHasFailuresRule(t *testing.T) {
	at := histT0
	pidAlive = func(int) bool { return true }
	t.Cleanup(func() { pidAlive = processExists })
	mk := func(f func(*runRecord)) runRecord {
		r := histRec(1, outcomeOK)
		f(&r)
		return r
	}
	for name, tc := range map[string]struct {
		r    runRecord
		want bool
	}{
		"ok":                  {mk(func(*runRecord) {}), false},
		"ok with read boards": {mk(func(r *runRecord) { r.Boards = &boardsReport{Configured: 1, Read: 1} }), false},
		"skipped is not failed": {mk(func(r *runRecord) {
			r.Boards = &boardsReport{Configured: 1, Skipped: 1, Items: []boardReport{{Outcome: boardSkipped}}}
		}), false},
		"failed run":           {mk(func(r *runRecord) { r.Outcome = outcomeFailed }), true},
		"cancelled":            {mk(func(r *runRecord) { r.Outcome = outcomeCancelled }), false},
		"cancelled with error": {mk(func(r *runRecord) { r.Outcome, r.Error = outcomeCancelled, "context canceled" }), true},
		"failed board":         {mk(func(r *runRecord) { r.Boards = &boardsReport{Configured: 1, Failed: 1} }), true},
		"backend error": {mk(func(r *runRecord) {
			r.BackendErrors = &backendErrorReport{Causes: []backendErrorCause{{Backend: "x", Count: 1}}}
		}), true},
		"only unrecorded backend": {mk(func(r *runRecord) { r.BackendErrors = &backendErrorReport{Unrecorded: 2} }), true},
		"empty backend report":    {mk(func(r *runRecord) { r.BackendErrors = &backendErrorReport{} }), false},
		"running and alive":       {mk(func(r *runRecord) { r.Outcome, r.FinishedAt = outcomeRunning, time.Time{} }), false},
	} {
		if got := runHasFailures(tc.r, at); got != tc.want {
			t.Errorf("%s: runHasFailures = %v, want %v", name, got, tc.want)
		}
	}
}
