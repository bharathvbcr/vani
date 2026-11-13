package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

var bhNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func bhBoard(token string) sources.Board {
	return sources.Board{ATS: posting.Greenhouse, Token: token, Company: token}
}

func bhRead(b sources.Board) boardReport {
	n := 3
	return boardReport{ATS: string(b.ATS), Token: b.Token, Company: b.Company, Outcome: boardRead, Listed: &n}
}

func bhFail(b sources.Board, why string) boardReport {
	return boardReport{ATS: string(b.ATS), Token: b.Token, Company: b.Company, Outcome: boardFailed, Error: why}
}

func bhHealth(t *testing.T) *boardHealth {
	t.Helper()
	return openBoardHealth(t.TempDir(), func(string, ...any) {})
}

// A board is stale when its last good read is older than 36 hours, by age
// alone: a board no run reached since (Fails 0) is named as well as one that
// fails.
func TestBoardStaleByAgeAlone(t *testing.T) {
	watched := []sources.Board{bhBoard("fresh"), bhBoard("edge"), bhBoard("over"), bhBoard("unreached"), bhBoard("never")}
	doc := boardHealthDoc{Version: boardHealthVersion, Boards: map[string]boardHealthRec{
		boardHealthKey("gh", "fresh"):     {LastOK: bhNow.Add(-30 * time.Hour), Outcome: boardRead},
		boardHealthKey("gh", "edge"):      {LastOK: bhNow.Add(-boardStaleAfter), Outcome: boardRead},
		boardHealthKey("gh", "over"):      {LastOK: bhNow.Add(-boardStaleAfter - time.Minute), Fails: 1, FailSince: bhNow.Add(-time.Hour), Outcome: boardFailed, LastError: "boom"},
		boardHealthKey("gh", "unreached"): {LastOK: bhNow.Add(-5 * 24 * time.Hour), Outcome: boardRead}, // no run reached it
	}}
	stale, n := staleOf(doc, watched, bhNow)
	if n != 2 || len(stale) != 2 || stale[0].Token != "unreached" || stale[1].Token != "over" {
		t.Fatalf("stale = %+v (%d)", stale, n)
	}
	if line := staleBoardLine(stale[0], bhNow); !strings.Contains(line, "no run has tried it since") || !strings.Contains(line, "120h0m0s") {
		t.Fatalf("a board no run reached: %q", line)
	}
	un, un2 := untrackedOf(doc, watched)
	if un2 != 1 || len(un) != 1 || un[0] != "gh:never" {
		t.Fatalf("untracked = %v (%d)", un, un2)
	}
}

// A time stored far past now (a run under a clock that ran ahead) is unreadable,
// so it cannot hold a board fresh once the clock is right; a time a little ahead
// reads as fresh, not old.
func TestBoardHealthFarFutureStampsAreUnreadable(t *testing.T) {
	watched := []sources.Board{bhBoard("ahead"), bhBoard("slightly")}
	doc := boardHealthDoc{Version: boardHealthVersion, Boards: map[string]boardHealthRec{
		boardHealthKey("gh", "ahead"):    {LastOK: bhNow.Add(5 * 365 * 24 * time.Hour), FailSince: bhNow.Add(-50 * time.Hour), Fails: 2},
		boardHealthKey("gh", "slightly"): {LastOK: bhNow.Add(20 * time.Minute)},
	}}
	stale, n := staleOf(doc, watched, bhNow)
	if n != 1 || stale[0].Token != "ahead" || stale[0].LastOK != nil {
		t.Fatalf("stale = %+v", stale) // the far-future last good read is unknown; its failure span stands
	}
	r := doc.Boards[boardHealthKey("gh", "ahead")]
	got := applyAttempt(r, bhFail(bhBoard("ahead"), "x"), bhNow, false)
	if got.LastOK != (time.Time{}) {
		t.Fatalf("a far-future last good read survived an attempt: %+v", got)
	}
}

// The file bounds agree: the worst entry fits, the worst file fits, an error is
// cut on a rune boundary, and a file past the bound is ignored with a warning.
func TestBoardHealthBoundsAndWorstCaseFitsItsFile(t *testing.T) {
	long := strings.Repeat("é", 400) // two bytes a rune
	if got := healthErrorText(long); len(got) > maxHealthErrorBytes || !utf8.ValidString(got) {
		t.Fatalf("error text %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	doc := boardHealthDoc{Version: boardHealthVersion, Boards: map[string]boardHealthRec{}}
	for i := 0; i < maxBoardHealthEntries; i++ {
		key := boardHealthKey("gh", fmt.Sprintf("%0120d", i)) // the longest token the run record keeps
		doc.Boards[key] = boardHealthRec{LastOK: bhNow, FailSince: bhNow, LastTried: bhNow, Fails: maxBoardFailCount,
			Outcome: boardRateLimited, LastError: healthErrorText(long), Seeded: true, DeferredSince: bhNow, ShortSince: bhNow}
	}
	raw, err := encodeBoardHealth(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxBoardHealthFile {
		t.Fatalf("worst case is %d bytes, bound %d", len(raw), maxBoardHealthFile)
	}
	if per := len(raw) / maxBoardHealthEntries; per > maxBoardHealthEntryBytes {
		t.Fatalf("an entry is %d bytes, bound %d", per, maxBoardHealthEntryBytes)
	}
	path := boardHealthPath(t.TempDir())
	if err := saveBoardHealth(path, doc); err != nil {
		t.Fatal(err)
	}
	if got, ok, warn := loadBoardHealth(path); !ok || warn != "" || len(got.Boards) != maxBoardHealthEntries {
		t.Fatalf("round trip: %d boards ok=%v %q", len(got.Boards), ok, warn)
	}
	// Past the bound: ignored, never half read.
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), maxBoardHealthFile+10), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok, warn := loadBoardHealth(path); ok || !strings.Contains(warn, "over") || len(got.Boards) != 0 {
		t.Fatalf("an oversized file was read: ok=%v %q", ok, warn)
	}
	// Corrupt: replaced with a warning, run not failed.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, warn := loadBoardHealth(path); ok || !strings.Contains(warn, "ignored") {
		t.Fatalf("a corrupt file: ok=%v %q", ok, warn)
	}
}

// A save that fails is surfaced (a warning and the run record's health_error),
// and the run still gets the stale list from what it folded.
func TestBoardHealthSaveFailureIsSurfaced(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "boards"), []byte("a file where the directory should be"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warns []string
	warnf := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }
	h := &boardHealth{path: boardHealthPath(state), doc: boardHealthDoc{Version: boardHealthVersion, Boards: map[string]boardHealthRec{
		boardHealthKey("gh", "old"): {LastOK: bhNow.Add(-48 * time.Hour)},
	}}}
	stale, n, saveErr := h.record([]sources.Board{bhBoard("old")}, nil, true, bhNow, warnf)
	if saveErr == "" || len(warns) == 0 || !strings.Contains(warns[0], "not saved") {
		t.Fatalf("save failure hidden: %q %v", saveErr, warns)
	}
	if n != 1 || stale[0].Token != "old" {
		t.Fatalf("stale list lost on a failed save: %+v", stale)
	}
}

// A run cut to some boards folds only what it tried and prunes nothing; a run
// that listed the whole watchlist prunes against the whole watchlist; the board
// whose failure ended the run says nothing about the board.
func TestBoardHealthRecordScopesAndPrunes(t *testing.T) {
	h := bhHealth(t)
	a, b, c := bhBoard("a"), bhBoard("b"), bhBoard("c")
	ended := bhFail(c, "context canceled")
	ended.endedRun = true
	h.doc.Boards[boardHealthKey("gh", "gone")] = boardHealthRec{LastOK: bhNow.Add(-time.Hour)}
	h.doc.Boards[boardHealthKey("gh", "b")] = boardHealthRec{LastOK: bhNow.Add(-2 * time.Hour)}
	// A --board run of a alone: b and the removed board stay.
	h.record([]sources.Board{a, b}, []boardReport{bhRead(a)}, false, bhNow, func(string, ...any) {})
	if _, ok := h.doc.Boards[boardHealthKey("gh", "gone")]; !ok {
		t.Fatal("an incomplete run pruned")
	}
	if r := h.doc.Boards[boardHealthKey("gh", "b")]; !r.LastOK.Equal(bhNow.Add(-2 * time.Hour)) {
		t.Fatalf("a board this run did not try changed: %+v", r)
	}
	// A complete listing of the whole watchlist prunes the removed board, and
	// leaves the ended board out.
	h.record([]sources.Board{a, b, c}, []boardReport{bhRead(a), bhRead(b), ended}, true, bhNow, func(string, ...any) {})
	if _, ok := h.doc.Boards[boardHealthKey("gh", "gone")]; ok {
		t.Fatal("a complete run did not prune the removed board")
	}
	if _, ok := h.doc.Boards[boardHealthKey("gh", "c")]; ok {
		t.Fatal("the board that ended the run was recorded")
	}
	// A board removed and added back starts fresh: no entry, so untracked.
	if un, n := untrackedOf(h.doc, []sources.Board{a, b, c}); n != 1 || un[0] != "gh:c" {
		t.Fatalf("untracked = %v", un)
	}
}

// Failures are counted from the last good read, a good read resets them, and a
// rate-limited or skipped attempt is not a good read.
func TestApplyAttemptCountsFailuresFromTheLastGoodRead(t *testing.T) {
	b := bhBoard("x")
	r := applyAttempt(boardHealthRec{}, bhRead(b), bhNow.Add(-72*time.Hour), false)
	for i := 1; i <= 3; i++ {
		r = applyAttempt(r, bhFail(b, "boom"), bhNow.Add(time.Duration(-70+i)*time.Hour), false)
	}
	skipped := boardReport{ATS: "gh", Token: "x", Outcome: boardSkipped, Error: "cooldown"}
	r = applyAttempt(r, skipped, bhNow, false)
	if r.Fails != 4 || r.Outcome != boardSkipped || !r.LastOK.Equal(bhNow.Add(-72*time.Hour)) || !r.FailSince.Equal(bhNow.Add(-69*time.Hour)) {
		t.Fatalf("%+v", r)
	}
	r = applyAttempt(r, bhRead(b), bhNow, false)
	if r.Fails != 0 || !r.FailSince.IsZero() || !r.LastOK.Equal(bhNow) || r.LastError != "" {
		t.Fatalf("a good read did not reset: %+v", r)
	}
}

// Seeding from the kept run history rebuilds records, marks them seeded and
// never writes the file (the run's record does).
func TestBoardHealthSeedsFromRunHistoryOnce(t *testing.T) {
	state := t.TempDir()
	rec := histRec(1, outcomeOK)
	rec.StartedAt = bhNow.Add(-30 * time.Hour)
	b := bhBoard("seed")
	rec.Boards = &boardsReport{Configured: 1, Failed: 1, Items: []boardReport{bhFail(b, "http 500")}}
	putHistoryRaw(t, state, rec)
	h := openBoardHealth(state, func(string, ...any) {})
	r, ok := h.doc.Boards[boardHealthKey("gh", "seed")]
	if !ok || !r.Seeded || r.Fails != 1 {
		t.Fatalf("seeded record: %+v ok=%v", r, ok)
	}
	if _, err := os.Stat(boardHealthPath(state)); err == nil {
		t.Fatal("opening wrote the file")
	}
}

// Status never says "no stale board" about what it does not know.
func TestBoardHealthStatusLines(t *testing.T) {
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{bhBoard("tracked"), bhBoard("new")}
	state := t.TempDir()
	e := &env{stateDir: state}
	lines := strings.Join(boardHealthLines(e.boardHealthFacet(cfg, bhNow), bhNow), "\n")
	if !strings.Contains(lines, "no run has recorded it yet") {
		t.Fatalf("a missing file read as healthy:\n%s", lines)
	}
	doc := boardHealthDoc{Version: boardHealthVersion, Updated: bhNow.Add(-60 * time.Hour), Boards: map[string]boardHealthRec{
		boardHealthKey("gh", "tracked"): {LastOK: bhNow.Add(-60 * time.Hour), Fails: 3, FailSince: bhNow.Add(-50 * time.Hour), Outcome: boardRateLimited, LastError: "429"},
	}}
	if err := saveBoardHealth(boardHealthPath(state), doc); err != nil {
		t.Fatal(err)
	}
	lines = strings.Join(boardHealthLines(e.boardHealthFacet(cfg, bhNow), bhNow), "\n")
	for _, want := range []string{"last updated 60h0m0s ago", "stale boards (1)", "tracked (gh:tracked)", "not tracked yet (1", "gh:new", "last rate_limited"} {
		if !strings.Contains(lines, want) {
			t.Errorf("status missing %q:\n%s", want, lines)
		}
	}
}

// Prefiltered rows are counted per board and in total.
func TestPrefilteredIsCountedPerBoardAndInTotal(t *testing.T) {
	w := &wired{}
	a, b := bhBoard("a"), bhBoard("b")
	w.noteBoardFetch(a, boardFetch{Prefiltered: 4})
	w.noteBoardFetch(a, boardFetch{Prefiltered: 1})
	w.noteBoardFetch(b, boardFetch{Prefiltered: 7})
	rep := boardsReportOf(2, []boardReport{bhRead(a), bhRead(b)})
	w.mu.Lock()
	w.applyFetchesLocked(&rep)
	w.mu.Unlock()
	if rep.Prefiltered != 12 || rep.Items[0].Prefiltered != 5 || rep.Items[1].Prefiltered != 7 {
		t.Fatalf("%+v", rep)
	}
	if got := boardFetchSummary(&rep); !strings.Contains(got, "12 new rows prefiltered (never stored)") {
		t.Fatalf("summary %q", got)
	}
}

// A mass absence or a clock behind is a failure in the run record, and a
// running run does not read as clean.
func TestRunReportNamesMassAbsenceClockBehindAndRunningRuns(t *testing.T) {
	b := bhBoard("snap")
	listed := 0
	rec := histRec(1, outcomeOK)
	rc := &reconcileCounts{Open: 42, Absent: 42, Mass: true, MassMarked: 42}
	rec.Boards = &boardsReport{Configured: 1, Read: 1, Mass: 1, MassMarked: 42, Items: []boardReport{
		{ATS: "gh", Token: b.Token, Company: "Snap", Outcome: boardRead, Listed: &listed, Reconcile: rc}}}
	if !runHasFailures(rec, histT0) {
		t.Fatal("a mass absence was not reported as needing a look")
	}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	if !text.HasFailures || !strings.Contains(text.Text, "Mass absences (1)") || !strings.Contains(text.Text, "42 of 42 open postings absent") {
		t.Fatalf("report:\n%s", text.Text)
	}
	if !strings.Contains(text.Text, "mass absence(s), 42 posting(s) newly held") {
		t.Fatalf("boards line missing the mass count:\n%s", text.Text)
	}

	// A listing that only carries earlier marks is not news.
	carry := histRec(2, outcomeOK)
	carry.Boards = &boardsReport{Configured: 1, Read: 1, Held: 42, Items: []boardReport{
		{ATS: "gh", Token: b.Token, Outcome: boardRead, Listed: &listed, Reconcile: &reconcileCounts{Open: 42, Absent: 42, Held: 42, Mass: true}}}}
	if runHasFailures(carry, histT0) {
		t.Fatal("a listing that only carried a mass hold was a failure")
	}

	behind := histRec(3, outcomeOK)
	n := 5
	behind.Boards = &boardsReport{Configured: 1, Read: 1, ClockBehind: 1, Items: []boardReport{
		{ATS: "gh", Token: "x", Outcome: boardRead, Listed: &n, Reconcile: &reconcileCounts{Open: 9, Absent: 4, ClockBehind: true}}}}
	if !runHasFailures(behind, histT0) {
		t.Fatal("a clock behind the ledger was silent: nothing is being aged")
	}
	text = (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(behind), Rec: behind}, histT0, true)
	// Changed 2026-11-12 (review F1): the report says what it saw, events stamped
	// ahead of the clock (and which), not that the clock reads behind now.
	if !strings.Contains(text.Text, "Not aged (1)") || !strings.Contains(text.Text, "events stamped ahead of the run's clock") {
		t.Fatalf("report:\n%s", text.Text)
	}

	running := histRec(4, outcomeRunning)
	running.PID = os.Getpid()
	running.StartedAt = histT0.Add(-time.Minute)
	running.FinishedAt = time.Time{}
	text = (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(running), Rec: running}, histT0, true)
	if strings.Contains(text.Text, "No failures in this run.") || !strings.Contains(text.Text, "recorded when the run finishes") {
		t.Fatalf("a running run read as clean:\n%s", text.Text)
	}
}
