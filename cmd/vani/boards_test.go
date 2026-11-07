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
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
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

// withBoardListParallel sets how many boards list at once and restores it.
func withBoardListParallel(t *testing.T, n int) {
	t.Helper()
	prev := boardListParallel
	boardListParallel = n
	t.Cleanup(func() { boardListParallel = prev })
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
		rep.Listed != 2 || rep.Unrecorded != 0 || len(rep.Items) != 5 || rep.Added == nil || *rep.Added != 2 {
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
	// One fetch at a time, so the board after the cancellation cannot already
	// be inside List. Different hosts otherwise list together.
	withBoardListParallel(t, 1)
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
	if err != nil || rep.Configured != 0 || rep.Items == nil || len(rep.Items) != 0 || rep.Added == nil || *rep.Added != 0 {
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

func TestListBoardsTicksTheBoardItIsReading(t *testing.T) {
	gh := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	lv := sources.Board{ATS: posting.Lever, Token: "initech", Company: "Initech"}
	wd := sources.Board{ATS: posting.Workday, Token: "intel", Company: "Intel"}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			return boardPostings(b, 2), nil
		}},
		posting.Workday: fakeSource{posting.Workday, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, errors.New("http 500")
		}},
	}
	var ticks []listingTick
	ctx := withListingTick(context.Background(), func(tk listingTick) { ticks = append(ticks, tk) })
	var warn warnLog
	if _, err := listBoards(ctx, []sources.Board{gh, lv, wd}, srcs, nil, tempReconciler(t).Reconcile, warn.warnf, ""); err != nil {
		t.Fatal(err)
	}
	// Done ticks stay in watchlist order. A listing tick is emitted before that
	// board's own done tick. It can arrive before an earlier board is appended,
	// because a later host is listed at the same time. A skip has no listing tick.
	var done, reconciling []listingTick
	listingAt := map[string]int{}
	for i, tk := range ticks {
		switch tk.Phase {
		case progressPhaseListing:
			if _, ok := listingAt[tk.Board.Token]; ok {
				t.Errorf("two listing ticks for %s", tk.Board.Token)
			}
			listingAt[tk.Board.Token] = i
		case progressPhaseReconciling:
			reconciling = append(reconciling, tk)
		case progressPhaseDone:
			done = append(done, tk)
		default:
			t.Errorf("unknown phase %q", tk.Phase)
		}
	}
	if len(done) != 3 || done[0].Board.Token != "acme" || done[1].Board.Token != "initech" || done[2].Board.Token != "intel" {
		t.Fatalf("done ticks: %+v", ticks)
	}
	if done[0].Outcome != boardRead || done[0].Done != 1 || done[0].Read != 1 || done[0].Listed != 2 || done[0].Added != 2 {
		t.Errorf("gh done: %+v", done[0])
	}
	if done[1].Outcome != boardSkipped || done[1].Skipped != 1 || done[1].Read != 1 || done[1].Listed != 2 || done[1].Added != 2 {
		t.Errorf("skip done: %+v", done[1])
	}
	if _, ok := listingAt["initech"]; ok {
		t.Error("a skipped board was listed")
	}
	ghAt, ok := listingAt["acme"]
	if !ok || ticks[ghAt].Added != 0 || ticks[ghAt].Outcome != "" || ticks[ghAt].Index != 1 {
		t.Errorf("gh listing: %+v", ticks[ghAt])
	}
	wdAt, ok := listingAt["intel"]
	if !ok || ticks[wdAt].Outcome != "" || ticks[wdAt].Index != 3 {
		t.Errorf("wd listing: %+v", ticks[wdAt])
	}
	if len(reconciling) != 1 || reconciling[0].Board.Token != "acme" || reconciling[0].Added != 0 || reconciling[0].Outcome != "" {
		t.Errorf("reconciling: %+v", reconciling)
	}
	if done[2].Outcome != boardFailed || done[2].Failed != 1 || done[2].Skipped != 1 || done[2].Read != 1 ||
		done[2].Listed != 2 || done[2].Added != 2 || done[2].Done != 3 {
		t.Errorf("wd done: %+v", done[2])
	}
	ghDone := -1
	wdDone := -1
	for i, tk := range ticks {
		if tk.Phase == progressPhaseDone && tk.Board.Token == "acme" {
			ghDone = i
		}
		if tk.Phase == progressPhaseDone && tk.Board.Token == "intel" {
			wdDone = i
		}
	}
	if ghAt >= ghDone || wdAt >= wdDone {
		t.Fatalf("listing tick after its own done tick: %+v", ticks)
	}
	if !strings.Contains(warn.String(), "board gh/acme: reading (1 of 3)") {
		t.Errorf("warnings:\n%s", warn.String())
	}
}

func TestListBoardsTickCountsARateLimit(t *testing.T) {
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, srcutil.ErrRateLimited
		}},
	}
	var ticks []listingTick
	ctx := withListingTick(context.Background(), func(tk listingTick) { ticks = append(ticks, tk) })
	if _, err := listBoards(ctx, []sources.Board{b}, srcs, nil, tempReconciler(t).Reconcile, (&warnLog{}).warnf, ""); err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 2 || ticks[1].Phase != progressPhaseDone || ticks[1].Outcome != boardRateLimited ||
		ticks[1].Failed != 1 || ticks[1].RateLimited != 1 || ticks[1].Read != 0 || ticks[0].Outcome != "" {
		t.Fatalf("%+v", ticks)
	}
}

func TestListBoardsCancelLeavesTheBoardItWasReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	second := sources.Board{ATS: posting.Lever, Token: "later", Company: "Later"}
	var ticks []listingTick
	ctx = withListingTick(ctx, func(tk listingTick) {
		ticks = append(ticks, tk)
		if tk.Phase == progressPhaseListing {
			cancel()
		}
	})
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		posting.Lever: fakeSource{posting.Lever, func(context.Context, sources.Board) ([]posting.Posting, error) {
			t.Error("listed the next board")
			return nil, nil
		}},
	}
	_, err := listBoards(ctx, []sources.Board{first, second}, srcs, nil, tempReconciler(t).Reconcile, (&warnLog{}).warnf, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if len(ticks) == 0 || ticks[len(ticks)-1].Board.Token != "acme" {
		t.Fatalf("ticks %+v", ticks)
	}
	for _, tk := range ticks {
		if tk.Board.Token == "later" {
			t.Fatalf("ticked the next board: %+v", ticks)
		}
	}
}

// Boards on different hosts list at the same time. A second host that never
// enters List means the launcher is still walking the watchlist one board at
// a time.
func TestListBoardsFetchesDifferentHostsTogether(t *testing.T) {
	withBoardListParallel(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	entered := make(chan string, 2)
	hold := func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
		select {
		case entered <- b.Token:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-release:
			return boardPostings(b, 1), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	gh := sources.Board{ATS: posting.Greenhouse, Token: "a", Company: "A"}
	wd := sources.Board{ATS: posting.Workday, Token: "intel/External", Company: "Intel", Host: "intel.wd1.myworkdayjobs.com"}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, hold},
		posting.Workday:    fakeSource{posting.Workday, hold},
	}
	rec := tempReconciler(t)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		_, err := listBoards(ctx, []sources.Board{gh, wd}, srcs, nil, rec.Reconcile, (&warnLog{}).warnf, "")
		done <- err
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		finish()
		<-finished
	})
	got := map[string]bool{}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for len(got) < 2 {
		select {
		case tok := <-entered:
			got[tok] = true
		case err := <-done:
			t.Fatalf("listBoards returned before both hosts were in List: %v (entered %v)", err, got)
		case <-timer.C:
			t.Fatalf("hosts did not list together, entered %v", got)
		}
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Two boards on one host do not list together. The host is blocked only after
// a 429 is recorded, so overlapping its boards would spend the retry budget
// before that block is known.
func TestListBoardsOneBoardPerHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	var current, maxSeen atomic.Int32
	entered := make(chan struct{}, 1)
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			n := current.Add(1)
			for {
				old := maxSeen.Load()
				if n <= old || maxSeen.CompareAndSwap(old, n) {
					break
				}
			}
			defer current.Add(-1)
			if b.Token == "a" {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return boardPostings(b, 1), nil
		}},
	}
	rec := tempReconciler(t)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		_, err := listBoards(ctx, []sources.Board{
			{ATS: posting.Greenhouse, Token: "a", Company: "A"},
			{ATS: posting.Greenhouse, Token: "b", Company: "B"},
		}, srcs, nil, rec.Reconcile, (&warnLog{}).warnf, "")
		done <- err
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		finish()
		<-finished
	})
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("listBoards ended before the first board listed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("first board did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if n := maxSeen.Load(); n != 1 {
		t.Fatalf("same host overlapped: %d boards in List", n)
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := maxSeen.Load(); n != 1 {
		t.Fatalf("same host overlapped: %d boards in List", n)
	}
}

// Page boards share one browser, so two of them do not list together even
// when their page hosts differ.
func TestListBoardsPageBoardsDoNotOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	var current, maxSeen atomic.Int32
	entered := make(chan struct{}, 1)
	srcs := map[posting.ATS]sources.Source{
		posting.Page: fakeSource{posting.Page, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			n := current.Add(1)
			for {
				old := maxSeen.Load()
				if n <= old || maxSeen.CompareAndSwap(old, n) {
					break
				}
			}
			defer current.Add(-1)
			if b.Token == "one" {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return boardPostings(b, 1), nil
		}},
	}
	rec := tempReconciler(t)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		_, err := listBoards(ctx, []sources.Board{
			{ATS: posting.Page, Token: "one", Company: "One", URL: "https://one.example/jobs"},
			{ATS: posting.Page, Token: "two", Company: "Two", URL: "https://two.example/jobs"},
		}, srcs, nil, rec.Reconcile, (&warnLog{}).warnf, "")
		done <- err
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		finish()
		<-finished
	})
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("listBoards ended before the first page listed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("first page board did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if n := maxSeen.Load(); n != 1 {
		t.Fatalf("page boards overlapped: %d in List", n)
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := maxSeen.Load(); n != 1 {
		t.Fatalf("page boards overlapped: %d in List", n)
	}
}

func TestListBoardsEmptyWatchlistNamesNoPortal(t *testing.T) {
	var ticks []listingTick
	ctx := withListingTick(context.Background(), func(tk listingTick) { ticks = append(ticks, tk) })
	if _, err := listBoards(ctx, nil, nil, nil, tempReconciler(t).Reconcile, (&warnLog{}).warnf, ""); err != nil || len(ticks) != 0 {
		t.Fatalf("err %v ticks %+v", err, ticks)
	}
}

// Added counts every tried board. Cutting Items by count or by bytes does not
// change it, and a run that added nothing still writes 0.
func TestBoardsReportAddedStaysExactWhenItemsAreCut(t *testing.T) {
	n := maxRecordedBoards + 15
	tried := make([]boardReport, 0, n+1)
	for i := range n {
		listed := 1
		tried = append(tried, boardReport{ATS: "gh", Token: fmt.Sprint(i), Outcome: boardRead, Listed: &listed, Reconcile: &reconcileCounts{New: 3}})
	}
	tried = append(tried, boardReport{ATS: "gh", Token: "fail", Outcome: boardFailed, Error: "http 500"})
	rep := boardsReportOf(len(tried), tried)
	want := n * 3
	if rep.Added == nil || *rep.Added != want || len(rep.Items) >= len(tried) {
		t.Fatalf("added %v items %d of %d", rep.Added, len(rep.Items), len(tried))
	}
	kept := 0
	for _, e := range rep.Items {
		if e.Reconcile != nil {
			kept += e.Reconcile.New
		}
	}
	if kept == 0 || kept >= want {
		t.Fatalf("kept new %d, want a real cut of %d", kept, want)
	}

	huge := strings.Repeat("<", maxBoardErrorRunes)
	var wide []boardReport
	for i := range 200 {
		wide = append(wide, boardReport{ATS: "wd", Token: fmt.Sprintf("t%d", i), Outcome: boardFailed, Error: huge})
	}
	for i := range 10 {
		listed := 2
		wide = append(wide, boardReport{ATS: "gh", Token: fmt.Sprintf("r%d", i), Outcome: boardRead, Listed: &listed, Reconcile: &reconcileCounts{New: 5}})
	}
	rep = boardsReportOf(len(wide), wide)
	if rep.Added == nil || *rep.Added != 50 {
		t.Fatalf("byte-cut added %v", rep.Added)
	}
	kept = 0
	for _, e := range rep.Items {
		if e.Reconcile != nil {
			kept += e.Reconcile.New
		}
		if e.Outcome == boardRead {
			t.Fatalf("a read board was kept after the byte cut stopped: %+v", e)
		}
	}
	if kept != 0 {
		t.Fatalf("kept new %d from boards the byte cut dropped", kept)
	}

	zero := boardsReportOf(2, []boardReport{{ATS: "gh", Token: "a", Outcome: boardFailed}, {ATS: "gh", Token: "b", Outcome: boardRead, Reconcile: &reconcileCounts{New: 0}}})
	if zero.Added == nil || *zero.Added != 0 {
		t.Fatalf("zero %+v", zero.Added)
	}
	raw, err := json.Marshal(zero)
	if err != nil || !strings.Contains(string(raw), `"added":0`) {
		t.Fatalf("json %s %v", raw, err)
	}
}

// Per-tenant ATS boards (Pinpoint, Recruitee, BambooHR, Personio) resolve to
// distinct tenant hosts and list concurrently, and a fast board that finishes
// List while an earlier board is still in List releases its pool slot right
// away so later boards are not head-of-line blocked.
func TestListBoardsPerTenantHostsAndEarlySlotRelease(t *testing.T) {
	withBoardListParallel(t, 2)
	b0 := sources.Board{ATS: posting.Pinpoint, Token: "alpha", Company: "Alpha"}
	b1 := sources.Board{ATS: posting.Pinpoint, Token: "beta", Company: "Beta"}
	b2 := sources.Board{ATS: posting.Recruitee, Token: "gamma", Company: "Gamma"}
	if listFetchHost(b0) == listFetchHost(b1) {
		t.Fatalf("per-tenant boards share a fetch host: %q", listFetchHost(b0))
	}

	b0Entered := make(chan struct{})
	b0Release := make(chan struct{})
	b2Done := make(chan struct{})
	srcs := map[posting.ATS]sources.Source{
		posting.Pinpoint: fakeSource{posting.Pinpoint, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			if b.Token == "alpha" {
				close(b0Entered)
				select {
				case <-b0Release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return boardPostings(b, 1), nil
		}},
		posting.Recruitee: fakeSource{posting.Recruitee, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			close(b2Done)
			return boardPostings(b, 1), nil
		}},
	}
	done := make(chan error, 1)
	go func() {
		_, err := listBoards(context.Background(), []sources.Board{b0, b1, b2}, srcs, nil, tempReconciler(t).Reconcile, (&warnLog{}).warnf, "")
		done <- err
	}()
	select {
	case <-b0Entered:
	case <-time.After(3 * time.Second):
		t.Fatal("b0 did not enter List")
	}
	// b1 finishes while b0 is still blocked; if b1 releases its pool slot on
	// completion, b2 takes the freed slot and finishes before b0 is released.
	select {
	case <-b2Done:
	case <-time.After(3 * time.Second):
		close(b0Release)
		t.Fatal("b2 was blocked waiting for b0 to reconcile (head-of-line slot blocking)")
	}
	close(b0Release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A single board whose HTTP request times out (wrapping context.DeadlineExceeded
// while the run's own ctx is still live) is recorded as a failed board and does
// not cancel the rest of the watchlist.
func TestListBoardsBoardTimeoutDoesNotCancelRun(t *testing.T) {
	withBoardListParallel(t, 2)
	b0 := sources.Board{ATS: posting.Greenhouse, Token: "slow", Company: "Slow"}
	b1 := sources.Board{ATS: posting.Lever, Token: "fast", Company: "Fast"}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(context.Context, sources.Board) ([]posting.Posting, error) {
			return nil, fmt.Errorf("greenhouse slow: get: %w", context.DeadlineExceeded)
		}},
		posting.Lever: fakeSource{posting.Lever, func(_ context.Context, b sources.Board) ([]posting.Posting, error) {
			return boardPostings(b, 1), nil
		}},
	}
	rec := tempReconciler(t)
	rep, err := listBoards(context.Background(), []sources.Board{b0, b1}, srcs, nil, rec.Reconcile, (&warnLog{}).warnf, "")
	if err != nil {
		t.Fatalf("per-board timeout aborted the run: %v", err)
	}
	if rep.Failed != 1 || rep.Read != 1 || len(rep.Items) != 2 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if rep.Items[0].Outcome != boardFailed || rep.Items[1].Outcome != boardRead {
		t.Fatalf("unexpected item outcomes: %+v", rep.Items)
	}
}

// When an early board finishes List and releases its slot while a second board on
// the same host was already parked in the same scan pass, a later board on that
// host can take the freed slot and leave the parked board waiting in cond.Wait.
// If the early board's reconcile then fails, abandon must wake the launcher and
// drain all boards without deadlocking.
func TestListBoardsReconcileFailureWhileEarlierBoardParkedDoesNotDeadlock(t *testing.T) {
	withBoardListParallel(t, 2)
	b0 := sources.Board{ATS: posting.Greenhouse, Token: "a", Company: "A"}
	b1 := sources.Board{ATS: posting.Greenhouse, Token: "b", Company: "B"}
	b2 := sources.Board{ATS: posting.Lever, Token: "c", Company: "C"}
	b3 := sources.Board{ATS: posting.Greenhouse, Token: "d", Company: "D"}

	b0AllowReturn := make(chan struct{})
	b0Released := make(chan struct{})
	b3Entered := make(chan struct{})

	ctx := withListingTick(context.Background(), func(tk listingTick) {
		if tk.Board.Token == "c" && tk.Phase == progressPhaseListing {
			close(b0AllowReturn)
			<-b0Released
		}
	})

	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: fakeSource{posting.Greenhouse, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			switch b.Token {
			case "a":
				<-b0AllowReturn
				return boardPostings(b, 1), nil
			case "d":
				close(b3Entered)
				<-ctx.Done()
				return nil, ctx.Err()
			default:
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}},
		posting.Lever: fakeSource{posting.Lever, func(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	}

	reconcile := func(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (pipeline.Summary, error) {
		if b.Token == "a" {
			<-b3Entered
			return pipeline.Summary{}, errors.New("simulated ledger write error")
		}
		return pipeline.Summary{}, nil
	}

	// Detect when b0's goroutine has called release() by watching its send or
	// wrapping with a tiny wait after b0AllowReturn closes.
	go func() {
		<-b0AllowReturn
		time.Sleep(10 * time.Millisecond)
		close(b0Released)
	}()

	done := make(chan error, 1)
	go func() {
		_, err := listBoards(ctx, []sources.Board{b0, b1, b2, b3}, srcs, nil, reconcile, (&warnLog{}).warnf, "")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "simulated ledger write error") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock in listBoards abandon when an earlier board is parked in cond.Wait")
	}
}
