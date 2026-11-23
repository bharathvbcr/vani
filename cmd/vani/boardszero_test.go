package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const listsNothingText = "lists nothing (check the board token)"

// zeroDay is one run's good read of b on day d (from zeroT0) listing n postings.
type zeroDay struct {
	d int
	n int // listed; -1 is a good read whose count is unknown
	// failed makes the attempt a failed read instead.
	failed bool
}

var zeroT0 = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

// zeroRun folds the days into a state dir's board health the way a run does
// (record), watching b, and returns the state dir.
func zeroRun(t *testing.T, b sources.Board, days []zeroDay) string {
	t.Helper()
	state := t.TempDir()
	h := openBoardHealth(state, func(string, ...any) {})
	for _, d := range days {
		e := bhRead(b)
		switch {
		case d.failed:
			e = bhFail(b, "boom")
		case d.n < 0:
			e.Listed = nil
		default:
			n := d.n
			e.Listed = &n
		}
		h.record([]sources.Board{b}, []boardReport{e}, true, zeroT0.Add(time.Duration(d.d)*24*time.Hour), func(string, ...any) {})
	}
	return state
}

func zeroStatus(t *testing.T, state string, watched []sources.Board, at time.Time) string {
	t.Helper()
	cfg := config.Default()
	cfg.Watchlist = watched
	e := &env{stateDir: state}
	return strings.Join(boardHealthLines(e.boardHealthFacet(cfg, at), at), "\n")
}

func zeros(first, last int) []zeroDay {
	var out []zeroDay
	for d := first; d <= last; d++ {
		out = append(out, zeroDay{d: d})
	}
	return out
}

func putLedgerPosting(t *testing.T, state string, b sources.Board, id string, st posting.Status) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := posting.GreenhouseKey(b.Token, id)
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: b.Company, Title: "Engineer", URL: "https://x.test/" + id}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: zeroT0, Key: k, Status: st, Posting: &p}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// A watched board whose good reads have listed 0 for at least a week, with
// nothing of its in the ledger, is named on status: a mistyped token reads as
// a board with no openings, and nothing else flags it.
func TestBoardThatAlwaysListsNothingIsNamed(t *testing.T) {
	b := bhBoard("typo")
	state := zeroRun(t, b, zeros(0, 8))
	at := zeroT0.Add(8*24*time.Hour + time.Hour)
	lines := zeroStatus(t, state, []sources.Board{b}, at)
	for _, want := range []string{listsNothingText, "typo (gh:typo)", "2026-11-01"} {
		if !strings.Contains(lines, want) {
			t.Errorf("status missing %q:\n%s", want, lines)
		}
	}
	// Attention, never a failure: it is not in the stale list and the board
	// still reads as healthy.
	if strings.Contains(lines, "stale boards") {
		t.Errorf("a board that reads fine was named stale:\n%s", lines)
	}
}

// Not named: too short a span, a board that listed once in between, a first
// day, many runs in one day, and a board whose ledger holds postings of its.
func TestBoardListingNothingIsNamedOnlyWhenItIsLongAndLedgerEmpty(t *testing.T) {
	at := zeroT0.Add(10 * 24 * time.Hour)
	b := bhBoard("b")
	cases := []struct {
		name string
		days []zeroDay
		want bool
	}{
		{"seven days exactly", zeros(0, 7), true},
		{"just under seven days", append(zeros(0, 5), zeroDay{d: 6}), false},
		{"first day", zeros(0, 0), false},
		{"zero for one day then five", append(zeros(0, 0), zeroDay{d: 1, n: 5}), false},
		{"zeros, then a listing, then too few zeros", append(append(zeros(0, 7), zeroDay{d: 8, n: 5}), zeros(9, 10)...), false},
		{"zeros, a listing, then five days of zeros", append(append(zeros(0, 3), zeroDay{d: 4, n: 5}), zeros(5, 10)...), false},
		{"a listing, then a week of zeros", append([]zeroDay{{d: 0, n: 5}}, zeros(1, 8)...), true},
		{"a failed read between zeros neither counts nor resets", append(append(zeros(0, 3), zeroDay{d: 4, failed: true}), zeros(5, 8)...), true},
		{"an unknown count is not a zero", append(append(zeros(0, 3), zeroDay{d: 4, n: -1}), zeros(5, 9)...), false},
	}
	for _, c := range cases {
		state := zeroRun(t, b, c.days)
		got := strings.Contains(zeroStatus(t, state, []sources.Board{b}, at), listsNothingText)
		if got != c.want {
			t.Errorf("%s: named %v, want %v", c.name, got, c.want)
		}
	}

	// Thirty reads inside one day are not a week.
	var many []zeroDay
	for i := 0; i < 30; i++ {
		many = append(many, zeroDay{d: 9})
	}
	if strings.Contains(zeroStatus(t, zeroRun(t, b, many), []sources.Board{b}, at), listsNothingText) {
		t.Error("thirty runs in one day were read as a week")
	}

	// The ledger is the witness: any posting of the board, open or dead,
	// means the board has listed before and is not a mistyped token.
	for _, st := range []posting.Status{posting.Live, posting.Dead, posting.Dropped} {
		state := zeroRun(t, b, zeros(0, 8))
		putLedgerPosting(t, state, b, "1", st)
		if strings.Contains(zeroStatus(t, state, []sources.Board{b}, at), listsNothingText) {
			t.Errorf("a board with a %s posting in the ledger was named", st)
		}
	}
	// A posting of another board does not clear it.
	state := zeroRun(t, b, zeros(0, 8))
	putLedgerPosting(t, state, bhBoard("other"), "1", posting.Live)
	if !strings.Contains(zeroStatus(t, state, []sources.Board{b}, at), listsNothingText) {
		t.Error("another board's posting cleared the board")
	}
}

// A board off the watchlist is not named, and a ledger that cannot be read
// says the check did not run instead of passing the board.
func TestBoardListingNothingNeedsWatchingAndReadableLedger(t *testing.T) {
	b := bhBoard("typo")
	at := zeroT0.Add(9 * 24 * time.Hour)
	state := zeroRun(t, b, zeros(0, 8))
	if strings.Contains(zeroStatus(t, state, []sources.Board{bhBoard("elsewhere")}, at), listsNothingText) {
		t.Error("an unwatched board was named")
	}
	// ledger is a file, so its events file cannot be opened.
	if err := os.WriteFile(filepath.Join(state, "ledger"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := zeroStatus(t, state, []sources.Board{b}, at)
	if strings.Contains(lines, listsNothingText) || !strings.Contains(lines, "not checked") {
		t.Errorf("an unreadable ledger:\n%s", lines)
	}
}

// A streak stamped far ahead of the clock is unreadable, not a week old.
func TestBoardListingNothingIgnoresAClockThatRanAhead(t *testing.T) {
	b := bhBoard("typo")
	state := t.TempDir()
	future := zeroT0.Add(400 * 24 * time.Hour)
	doc := boardHealthDoc{Version: boardHealthVersion, Updated: zeroT0, Boards: map[string]boardHealthRec{
		boardHealthKey("gh", "typo"): {LastOK: zeroT0, Outcome: boardRead, ZeroSince: future, ZeroReads: 50},
	}}
	if err := saveBoardHealth(boardHealthPath(state), doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(zeroStatus(t, state, []sources.Board{b}, zeroT0.Add(24*time.Hour)), listsNothingText) {
		t.Error("a streak from the future was named")
	}
}

// runs show names it for the latest run, in the detail report, as attention:
// the run is not a failure.
func TestRunsShowNamesABoardThatListsNothing(t *testing.T) {
	b := bhBoard("typo")
	state := zeroRun(t, b, zeros(0, 8))
	at := zeroT0.Add(8*24*time.Hour + time.Hour)
	zero := 0
	rec := histRec(1, outcomeOK)
	rec.StartedAt = at.Add(-time.Hour)
	rec.FinishedAt = at
	rec.Boards = &boardsReport{Configured: 1, Read: 1, Items: []boardReport{
		{ATS: "gh", Token: b.Token, Company: b.Company, Outcome: boardRead, Listed: &zero}}}
	e := &env{stateDir: state, stderr: &bytes.Buffer{}}

	rep := e.runReportOf(runRef{ID: runID(rec), Source: "last", Rec: rec}, at, true)
	if !strings.Contains(rep.Text, listsNothingText) || !strings.Contains(rep.Text, "typo (gh:typo)") {
		t.Fatalf("report:\n%s", rep.Text)
	}
	if rep.HasFailures {
		t.Error("a board that lists nothing failed the run")
	}
	// An older run's report is not told today's health.
	old := e.runReportOf(runRef{ID: runID(rec), Source: "history", Rec: rec}, at, true)
	if strings.Contains(old.Text, listsNothingText) {
		t.Errorf("a history run carried the live streak:\n%s", old.Text)
	}
	// And the failures-only report stays about failures.
	if f := e.runReportOf(runRef{ID: runID(rec), Source: "last", Rec: rec}, at, false); strings.Contains(f.Text, listsNothingText) {
		t.Errorf("failures report carried it:\n%s", f.Text)
	}
}

// Status folds the ledger once: the zero-board check takes the fold status
// already holds instead of reading the whole ledger again (a second unbounded
// fold made status take 9-19 s on a 1.1 GB ledger, 2026-11-23). A board whose
// postings the fold holds is not named, and nothing is read.
func TestZeroBoardCheckUsesTheFoldItIsGiven(t *testing.T) {
	b := bhBoard("held")
	state := zeroRun(t, b, zeros(0, 8))
	putLedgerPosting(t, state, b, "1", posting.Live)
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(state, "ledger"), nil)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	old := statusLedgerRead
	statusLedgerRead = func(dir string) (ledger.Snapshot, error) { reads++; return old(dir) }
	t.Cleanup(func() { statusLedgerRead = old })
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{b}
	at := zeroT0.Add(8*24*time.Hour + time.Hour)
	e := &env{stateDir: state}

	f := e.boardHealthFacetFrom(cfg, at, &foldedLedger{recs: snap.Records})
	if reads != 0 || len(f.ListsNothing) != 0 || f.ListsNothingNote != "" {
		t.Errorf("with a fold given: %d read(s), named %+v, note %q", reads, f.ListsNothing, f.ListsNothingNote)
	}
	// Without one (the text status, the run), it reads the ledger itself, once.
	if f := e.boardHealthFacet(cfg, at); reads != 1 || len(f.ListsNothing) != 0 {
		t.Errorf("without a fold: %d read(s), named %+v", reads, f.ListsNothing)
	}
}

// A fold that could not be read in time leaves the check undone and says so:
// nothing is named, and no second read of the ledger is started.
func TestZeroBoardCheckSkipsWhenTheGivenFoldFailed(t *testing.T) {
	b := bhBoard("typo")
	state := zeroRun(t, b, zeros(0, 8))
	reads := 0
	old := statusLedgerRead
	statusLedgerRead = func(dir string) (ledger.Snapshot, error) { reads++; return old(dir) }
	t.Cleanup(func() { statusLedgerRead = old })
	cfg := config.Default()
	cfg.Watchlist = []sources.Board{b}
	at := zeroT0.Add(8*24*time.Hour + time.Hour)
	e := &env{stateDir: state}
	f := e.boardHealthFacetFrom(cfg, at, &foldedLedger{why: "the ledger could not be read: ledger: the read did not finish within 8s"})
	if reads != 0 || len(f.ListsNothing) != 0 || !strings.Contains(f.ListsNothingNote, "not checked") || !strings.Contains(f.ListsNothingNote, "did not finish") {
		t.Errorf("%d read(s), named %+v, note %q", reads, f.ListsNothing, f.ListsNothingNote)
	}
}
