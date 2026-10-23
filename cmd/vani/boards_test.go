package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// fakeSource lists through a function, so a test decides per board what a
// listing returns.
type fakeSource struct {
	ats  posting.ATS
	list func(context.Context, sources.Board) ([]posting.Posting, error)
}

func (f fakeSource) ATS() posting.ATS { return f.ats }
func (f fakeSource) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	return f.list(ctx, b)
}

func boardPostings(b sources.Board, n int) []posting.Posting {
	out := make([]posting.Posting, n)
	for i := range out {
		out[i] = posting.Posting{Key: posting.Key(b.KeyPrefix() + fmt.Sprint(i+1)), Company: b.Company,
			Title: fmt.Sprintf("Engineer %d", i+1), Description: "about the role"}
	}
	return out
}

func tempReconciler(t *testing.T) *pipeline.Reconciler {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return pipeline.NewReconciler(l)
}

// warnLog collects warnf lines.
type warnLog struct {
	mu    sync.Mutex
	lines []string
}

func (w *warnLog) warnf(format string, a ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, fmt.Sprintf(format, a...))
}

func (w *warnLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.lines, "\n")
}

// A run's boards land in the report as read (with the listing's count and
// what it did to the ledger), failed (with the adapter's error: the Intel
// case, where one bad Workday detail fails the board) and skipped (no adapter,
// or the careers-page source disabled, with why). Each still warns.
func TestListBoardsRecordsReadFailedAndSkipped(t *testing.T) {
	gh := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	wd := sources.Board{ATS: posting.Workday, Token: "intel/External", Company: "Intel", Host: "intel.wd1.myworkdayjobs.com"}
	lv := sources.Board{ATS: posting.Lever, Token: "initech", Company: "Initech"}
	pg := sources.Board{ATS: posting.Page, Token: "hooli-careers", Company: "Hooli"}
	empty := sources.Board{ATS: posting.Ashby, Token: "quiet", Company: "Quiet"}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			return boardPostings(b, 2), nil
		}},
		posting.Workday: fakeSource{posting.Workday, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, errors.New(`workday "intel/External": posting "/job/US-Santa-Clara/X_JR1": http 500`)
		}},
		posting.Ashby: fakeSource{posting.Ashby, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return []posting.Posting{}, nil
		}},
	}
	var warn warnLog
	rep, err := listBoards(context.Background(), []sources.Board{gh, wd, lv, pg, empty}, srcs,
		errors.New("lightpanda not found"), tempReconciler(t).Reconcile, warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Configured != 5 || rep.Read != 2 || rep.Failed != 1 || rep.Skipped != 2 || rep.NotReached != 0 ||
		rep.Listed != 2 || rep.Unrecorded != 0 || len(rep.Items) != 5 {
		t.Fatalf("totals: %+v", rep)
	}
	got := rep.Items
	if g := got[0]; g.Outcome != boardRead || g.ATS != "gh" || g.Token != "acme" || g.Company != "Acme" ||
		g.Listed == nil || *g.Listed != 2 || g.Reconcile == nil || g.Reconcile.New != 2 || g.Error != "" {
		t.Errorf("read board: %+v (reconcile %+v)", g, g.Reconcile)
	}
	if g := got[1]; g.Outcome != boardFailed || g.Token != "intel/External" || g.Listed != nil || g.Reconcile != nil ||
		!strings.Contains(g.Error, "http 500") {
		t.Errorf("failed board: %+v", g)
	}
	if g := got[2]; g.Outcome != boardSkipped || g.Error != "no adapter for lv" {
		t.Errorf("no-adapter board: %+v", g)
	}
	if g := got[3]; g.Outcome != boardSkipped || g.Error != "careers-page source disabled this run: lightpanda not found" {
		t.Errorf("page board: %+v", g)
	}
	// A board that answered with nothing is read, and says it listed zero.
	if g := got[4]; g.Outcome != boardRead || g.Listed == nil || *g.Listed != 0 {
		t.Errorf("empty board: %+v", g)
	}
	for _, want := range []string{"board wd/intel/External: workday", "left unchanged", "board lv/initech: no adapter for lv; skipped",
		"board pg/hooli-careers: careers-page source disabled this run: lightpanda not found; skipped"} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("warnings lack %q:\n%s", want, warn.String())
		}
	}
	// The record's JSON is the contract status and the Mac app read.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"configured":5`, `"outcome":"failed"`, `"listed":0`, `"reconcile":{"new":2}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("json lacks %s: %s", want, raw)
		}
	}
}

// Cancelled mid-listing: boards read before it stay read, the board being
// listed is failed with its error, and the rest are never tried, counted as
// not reached, never as read.
func TestListBoardsCancelledMidListing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := sources.Board{ATS: posting.Greenhouse, Token: "a", Company: "A"}
	b := sources.Board{ATS: posting.Lever, Token: "b", Company: "B"}
	c := sources.Board{ATS: posting.Ashby, Token: "c", Company: "C"}
	reachedC := false
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			return boardPostings(b, 3), nil
		}},
		posting.Lever: fakeSource{posting.Lever, func(ctx context.Context, _ sources.Board) ([]posting.Posting, error) {
			cancel()
			return nil, fmt.Errorf("lever %q: %w", "b", ctx.Err())
		}},
		posting.Ashby: fakeSource{posting.Ashby, func(context.Context, sources.Board) ([]posting.Posting, error) {
			reachedC = true
			return nil, nil
		}},
	}
	var warn warnLog
	rep, err := listBoards(ctx, []sources.Board{a, b, c}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if reachedC {
		t.Error("a board after the cancellation was listed")
	}
	if rep.Configured != 3 || rep.Read != 1 || rep.Failed != 1 || rep.NotReached != 1 || rep.Listed != 3 || len(rep.Items) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	if rep.Items[0].Outcome != boardRead || rep.Items[1].Outcome != boardFailed || !strings.Contains(rep.Items[1].Error, "context canceled") {
		t.Errorf("items: %+v", rep.Items)
	}
}

// A ledger failure while reconciling fails the run; the board says it listed
// its postings and that the reconcile failed, and later boards are not reached.
func TestListBoardsReconcileFailure(t *testing.T) {
	a := sources.Board{ATS: posting.Greenhouse, Token: "a", Company: "A"}
	b := sources.Board{ATS: posting.Greenhouse, Token: "b", Company: "B"}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			return boardPostings(b, 4), nil
		}},
	}
	broken := func(context.Context, sources.Board, []posting.Posting, time.Time) (pipeline.Summary, error) {
		return pipeline.Summary{}, errors.New("ledger: disk full")
	}
	var warn warnLog
	rep, err := listBoards(context.Background(), []sources.Board{a, b}, srcs, nil, broken, warn.warnf, "")
	if err == nil || !strings.Contains(err.Error(), "reconcile gh/a: ledger: disk full") {
		t.Fatalf("err = %v", err)
	}
	if rep.Read != 0 || rep.Failed != 1 || rep.NotReached != 1 || len(rep.Items) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if e := rep.Items[0]; e.Listed == nil || *e.Listed != 4 || e.Error != "reconcile: ledger: disk full" || e.Reconcile != nil {
		t.Errorf("item: %+v", e)
	}
}

// An adapter error of any size and shape is recorded as one bounded line, and
// the board's names are bounded too.
func TestListBoardsBoundsAnOversizedError(t *testing.T) {
	huge := strings.Repeat("é<\n\x00\u202e ", 20000) // multibyte, newlines, NUL, a bidi override
	b := sources.Board{ATS: posting.Workday, Token: strings.Repeat("t", 1000), Company: strings.Repeat("c\n", 1000)}
	srcs := map[posting.ATS]sources.Source{
		posting.Workday: fakeSource{posting.Workday, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, errors.New(huge)
		}},
	}
	var warn warnLog
	rep, err := listBoards(context.Background(), []sources.Board{b}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, "")
	if err != nil {
		t.Fatal(err)
	}
	e := rep.Items[0]
	if n := utf8.RuneCountInString(e.Error); n > maxBoardErrorRunes+1 { // +1: the ellipsis
		t.Errorf("error is %d runes", n)
	}
	if strings.ContainsAny(e.Error, "\n\x00") {
		t.Errorf("error is not one line: %q", e.Error)
	}
	if utf8.RuneCountInString(e.Token) > maxBoardNameRunes+1 || utf8.RuneCountInString(e.Company) > maxBoardNameRunes+1 ||
		strings.Contains(e.Company, "\n") {
		t.Errorf("names not bounded: %q %q", e.Token, e.Company)
	}
}

// Many boards: the totals stay exact, the entries are bounded by count and by
// bytes, failed boards are kept before read ones, and the cut is counted.
func TestBoardsReportIsBoundedForManyBoards(t *testing.T) {
	const n = 1000
	worst := strings.Repeat("<", maxBoardErrorRunes) // each '<' is six bytes of JSON
	var tried []boardReport
	for i := range n {
		e := boardReport{ATS: "wd", Token: fmt.Sprintf("tenant%d/site", i), Company: "Co", Outcome: boardRead}
		if i%10 == 9 { // 100 failed boards, spread through the watchlist
			e.Outcome, e.Error = boardFailed, worst
		} else {
			listed := 1
			e.Listed, e.Reconcile = &listed, &reconcileCounts{Unchanged: 1}
		}
		tried = append(tried, e)
	}
	rep := boardsReportOf(n+5, tried)
	if rep.Configured != n+5 || rep.Read != 900 || rep.Failed != 100 || rep.NotReached != 5 || rep.Listed != 900 {
		t.Fatalf("totals: %+v", rep)
	}
	if len(rep.Items) > maxRecordedBoards || len(rep.Items)+rep.Unrecorded != n {
		t.Fatalf("%d items, %d unrecorded", len(rep.Items), rep.Unrecorded)
	}
	raw, err := json.Marshal(rep.Items)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxBoardsBytes {
		t.Errorf("items are %d bytes, over %d", len(raw), maxBoardsBytes)
	}
	// The byte bound cut the failed boards; every kept entry is a failed one,
	// and they are in watchlist order.
	last := -1
	for _, e := range rep.Items {
		if e.Outcome != boardFailed {
			t.Fatalf("a read board was kept while failed ones were cut: %+v", e)
		}
		var i int
		if _, err := fmt.Sscanf(e.Token, "tenant%d/site", &i); err != nil || i <= last {
			t.Fatalf("items out of watchlist order at %q", e.Token)
		}
		last = i
	}

	// With small entries the count bound applies: failed first, then read.
	var small []boardReport
	for i := range n {
		listed := 0
		e := boardReport{ATS: "gh", Token: fmt.Sprint(i), Outcome: boardRead, Listed: &listed}
		if i == n-1 {
			e = boardReport{ATS: "gh", Token: fmt.Sprint(i), Outcome: boardFailed, Error: "http 500"}
		}
		small = append(small, e)
	}
	rep = boardsReportOf(n, small)
	if len(rep.Items) != maxRecordedBoards || rep.Unrecorded != n-maxRecordedBoards {
		t.Fatalf("%d items, %d unrecorded", len(rep.Items), rep.Unrecorded)
	}
	if e := rep.Items[len(rep.Items)-1]; e.Outcome != boardFailed {
		t.Errorf("the failed board at the end of the watchlist was cut: %+v", e)
	}
}

// No board configured: an empty report, not a nil list.
func TestBoardsReportEmpty(t *testing.T) {
	rep, err := listBoards(context.Background(), nil, nil, nil, tempReconciler(t).Reconcile, (&warnLog{}).warnf, "")
	if err != nil || rep.Configured != 0 || rep.Items == nil || len(rep.Items) != 0 {
		t.Fatalf("report %+v, %v", rep, err)
	}
}

// The record's bounds hold together: a run with the most board entries, the
// most backend error causes and the largest error text of each still writes a
// record its reader accepts. A record past maxRunRecord would read as an
// unknown last run and hide everything in it.
func TestWorstCaseRunRecordIsReadable(t *testing.T) {
	var tried []boardReport
	for i := range 5000 {
		tried = append(tried, boardReport{ATS: "wd", Token: strings.Repeat("<", maxBoardNameRunes) + fmt.Sprint(i),
			Company: strings.Repeat("<", maxBoardNameRunes), Outcome: boardFailed, Error: strings.Repeat("<", maxBoardErrorRunes)})
	}
	boards := boardsReportOf(len(tried), tried)
	sink := newBackendErrors()
	for i := range maxRecordedBackendErrors + 5 {
		msg := truncateRunes(strings.Repeat("<", maxBackendErrorRunes*2), maxBackendErrorRunes)
		sink.add("openrouter/some-provider/some-model", fmt.Sprint(i), msg)
	}
	counts := map[string]int{}
	for _, s := range []string{"live", "triaged", "unsure", "scored", "explained", "digested", "dead", "dropped"} {
		counts[s] = 1 << 30
	}
	r := runRecord{PID: 1 << 30, StartedAt: time.Now(), FinishedAt: time.Now(), Outcome: outcomeFailed, Stage: stagePipeline,
		Error: strings.Repeat("<", maxRunErrorRunes), CountsBefore: counts, CountsAfter: counts,
		runDetail: runDetail{Boards: &boards, BackendErrors: sink.report(),
			Advance: &advanceReport{Eligible: 1 << 30, Limit: 5000, Attempted: 5000, Deferred: 1 << 30, Stopped: stopUnreadStreak}}}
	dir := t.TempDir()
	if err := writeRunRecord(dir, r); err != nil {
		t.Fatal(err)
	}
	got, err := readRunRecord(dir)
	if err != nil {
		t.Fatalf("worst-case record unreadable: %v", err)
	}
	// Headroom for a field a later change adds: half the reader's bound.
	if fi, err := os.Stat(runRecordPath(dir)); err != nil || fi.Size() > maxRunRecord/2 {
		t.Errorf("worst-case record is %v bytes (%v), over half of %d", fi.Size(), err, maxRunRecord)
	} else {
		t.Logf("worst-case record: %d bytes", fi.Size())
	}
	if got.Boards == nil || got.Boards.Failed != 5000 || len(got.Boards.Items)+got.Boards.Unrecorded != 5000 {
		t.Errorf("boards: %+v", got.Boards)
	}
}
