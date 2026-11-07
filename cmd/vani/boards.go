package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// How many boards list at once. API boards on different hosts share
// boardListParallel. Boards on one host stay one at a time: a 429 blocks
// that host, and overlapping its boards would spend the retry budget before
// the block is known. Page boards also share one Lightpanda process, so only
// one of them runs. Reconcile stays on one goroutine, in watchlist order,
// because the ledger index assumes each board sees the writes of the boards
// before it. Tests lower boardListParallel.
const hostListParallel = 1

var boardListParallel = maxBoardListParallel

const (
	maxBoardListParallel = 16
	pageListParallel     = 1
)

// Bounds on the run record's board report. Totals stay exact past them; only
// the per-board entries are cut, and the cut is counted (Unrecorded).
const (
	// maxRecordedBoards bounds boardsReport.Items.
	maxRecordedBoards = 500
	// maxBoardsBytes bounds the compact JSON of boardsReport.Items, so a
	// worst-case report cannot push the record past maxRunRecord.
	maxBoardsBytes = 48 << 10
	// maxBoardErrorRunes bounds one board's error line.
	maxBoardErrorRunes = 300
	// maxBoardNameRunes bounds a board's token and company as recorded.
	maxBoardNameRunes = 120
)

// reconcileFunc folds one board's listing into the ledger
// (pipeline.Reconciler.Reconcile).
type reconcileFunc func(ctx context.Context, b sources.Board, listed []posting.Posting, now time.Time) (pipeline.Summary, error)

// listBoards lists every board and reconciles each listing in watchlist order.
// A board with no source is skipped (pageErr, when set, is why the careers-page
// source is disabled); a board whose listing fails is left unchanged in the
// ledger, because an unread board must never count as "every posting
// disappeared". Both are warnings, not a failed run. It returns an error only
// for cancellation or a reconcile (ledger) failure, and the report says what
// happened to every board tried before that, however it ended.
//
// Fetches of boards on different hosts run together, up to boardListParallel.
// One host is fetched by one board at a time, and page boards one at a time.
// A board waiting on a busy host does not block a later board whose host is
// free, and it does not hold a pool slot while it waits. A cancellation stops
// boards that have not started. A reconcile failure cancels fetches still in
// flight and does not apply them.
//
// The listing is bounded as a whole, not only board by board: every board's
// waiting to retry is charged to one run budget (srcutil.RunRetryBudget), and a
// host that kept answering 429 is remembered, within the run and, through
// cooldownFile, from one run to the next (srcutil.HostThrottle). The next run
// sends such a host one probe request and stops at once if it is still
// throttled. An empty cooldownFile keeps the memory to this run. None of this
// reaches the ledger: a board that is not asked is a board that failed.
func listBoards(ctx context.Context, watchlist []sources.Board, srcs map[posting.ATS]sources.Source, pageErr error,
	reconcile reconcileFunc, warnf func(string, ...any), cooldownFile string) (boardsReport, error) {
	throttle, saveThrottle := openHostThrottle(cooldownFile, warnf)
	ctx, stop := context.WithCancel(ctx)
	// Cooldowns are saved after in-flight fetches return, so a 429 seen by a
	// board the run then stops is still remembered. Stop unblocks those fetches.
	// The launcher waits for the fetches it started; Add and Wait stay in that
	// one goroutine.
	var fetchWG sync.WaitGroup
	var triedMu sync.Mutex
	waiting := false
	var launcherDone chan struct{}
	defer saveThrottle()
	defer func() {
		stop()
		if waiting {
			<-launcherDone
		}
	}()
	ctx = srcutil.WithHostThrottle(srcutil.WithRunRetryBudget(ctx, srcutil.RunRetryBudget), throttle)
	tried := make([]boardReport, 0, len(watchlist))
	snapshot := func() []boardReport {
		triedMu.Lock()
		defer triedMu.Unlock()
		return append([]boardReport(nil), tried...)
	}
	appendTried := func(e boardReport) []boardReport {
		triedMu.Lock()
		defer triedMu.Unlock()
		tried = append(tried, e)
		return append([]boardReport(nil), tried...)
	}
	report := func() boardsReport { return boardsReportOf(len(watchlist), snapshot()) }
	total := len(watchlist)
	if total == 0 {
		return report(), nil
	}

	results := make([]chan listFetch, total)
	for i := range results {
		results[i] = make(chan listFetch, 1)
	}
	gate := newListGate(boardListParallel)
	launcherDone = make(chan struct{})
	waiting = true
	go func() {
		defer close(launcherDone)
		defer fetchWG.Wait()
		launchBoardFetches(ctx, watchlist, srcs, pageErr, warnf, gate, results, &fetchWG, snapshot)
	}()

	// abandon ends the run. The slot stays held until here, so stop is visible
	// before another board can take it. Later results are released and not
	// reconciled.
	abandon := func(res listFetch, from int) {
		stop()
		if res.release != nil {
			res.release()
		}
		for j := from; j < total; j++ {
			later := <-results[j]
			if later.release != nil {
				later.release()
			}
		}
	}
	for i, b := range watchlist {
		res := <-results[i]
		index := i + 1
		e := boardReport{ATS: string(b.ATS), Token: oneLine(b.Token, maxBoardNameRunes), Company: oneLine(b.Company, maxBoardNameRunes)}
		switch res.kind {
		case fetchNotStarted:
			err := ctx.Err()
			if err == nil {
				err = context.Canceled
			}
			abandon(res, i+1)
			return report(), err
		case fetchSkipped:
			e.Outcome, e.Error = boardSkipped, oneLine(res.why, maxBoardErrorRunes)
			emitListing(ctx, progressPhaseDone, index, total, b, appendTried(e), gate.active())
			continue
		case fetchFailedBeforeList:
			e.Outcome, e.Error = boardFailed, oneLine(res.err.Error(), maxBoardErrorRunes)
			emitListing(ctx, progressPhaseDone, index, total, b, appendTried(e), gate.active())
			err := ctx.Err()
			if err == nil {
				err = res.err
			}
			abandon(res, i+1)
			return report(), err
		}
		if res.err != nil {
			e.Outcome, e.Error = boardFailed, oneLine(res.err.Error(), maxBoardErrorRunes)
			next := "left unchanged"
			if errors.Is(res.err, srcutil.ErrRateLimited) {
				e.Outcome = boardRateLimited
				next = "left unchanged, will retry next run"
			}
			// A later board can cancel the run after this listing returned, and a
			// single board's HTTP timeout wraps context.DeadlineExceeded while the
			// run's ctx is still live. Only this board's own run cancellation ends
			// the run here.
			if res.canceled {
				emitListing(ctx, progressPhaseDone, index, total, b, appendTried(e), gate.active())
				abandon(res, i+1)
				return report(), res.err
			}
			if res.release != nil {
				res.release()
			}
			emitListing(ctx, progressPhaseDone, index, total, b, appendTried(e), gate.active())
			warnf("board %s/%s: %v; %s", b.ATS, b.Token, res.err, next)
			continue
		}
		n := len(res.listed)
		e.Listed = &n
		emitListing(ctx, progressPhaseReconciling, index, total, b, snapshot(), gate.active())
		sum, err := reconcile(ctx, b, res.listed, time.Now())
		if err != nil {
			e.Outcome, e.Error = boardFailed, oneLine("reconcile: "+err.Error(), maxBoardErrorRunes)
			emitListing(ctx, progressPhaseDone, index, total, b, appendTried(e), gate.active())
			abandon(res, i+1)
			return report(), fmt.Errorf("reconcile %s/%s: %w", b.ATS, b.Token, err)
		}
		e.Outcome, e.Reconcile = boardRead, reconcileCountsOf(sum)
		if res.release != nil {
			res.release()
		}
		emitListing(ctx, progressPhaseDone, index, total, b, appendTried(e), gate.active())
		warnf("board %s/%s: %+v", b.ATS, b.Token, sum)
	}
	return report(), nil
}

type fetchKind int

const (
	fetchDone fetchKind = iota
	fetchSkipped
	fetchNotStarted
	fetchFailedBeforeList
)

// listFetch is one board's listing, sent in watchlist order.
type listFetch struct {
	kind     fetchKind
	listed   []posting.Posting
	err      error
	canceled bool
	why      string
	release  func() // returns the host and pool slots; nil when none were taken
}

// launchBoardFetches starts each board's List as slots allow and sends one
// listFetch per board, so the caller can reconcile in watchlist order.
// A busy host parks that board and the scan continues, so a later board on a
// free host lists at the same time. Each result channel gets one send.
func launchBoardFetches(ctx context.Context, watchlist []sources.Board, srcs map[posting.ATS]sources.Source, pageErr error,
	warnf func(string, ...any), gate *listGate, results []chan listFetch, fetchWG *sync.WaitGroup, snapshot func() []boardReport) {
	total := len(watchlist)
	pending := make([]int, 0, total)
	for i, b := range watchlist {
		if _, ok := srcs[b.ATS]; !ok {
			why := fmt.Sprintf("no adapter for %s", b.ATS)
			if b.ATS == posting.Page {
				why = "careers-page source disabled this run"
				if pageErr != nil {
					why += ": " + pageErr.Error()
				}
			}
			warnf("board %s/%s: %s; skipped", b.ATS, b.Token, why)
			results[i] <- listFetch{kind: fetchSkipped, why: why}
			continue
		}
		pending = append(pending, i)
	}

	// freed counts slot releases. The scan records it under mu before looking
	// for work, and waits only while it is unchanged, so a release cannot be
	// missed between "nothing could start" and the wait.
	var mu sync.Mutex
	var freed int
	cond := sync.NewCond(&mu)
	stopWake := context.AfterFunc(ctx, func() {
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	})
	defer stopWake()
	stopAll := func(items []int) {
		for _, i := range items {
			results[i] <- listFetch{kind: fetchNotStarted}
		}
	}

	for len(pending) > 0 {
		mu.Lock()
		baseline := freed
		mu.Unlock()
		if ctx.Err() != nil {
			stopAll(pending)
			return
		}
		var parked []int
		progressed := false
		halt := false
		for n, i := range pending {
			if ctx.Err() != nil {
				parked = append(parked, pending[n:]...)
				halt = true
				break
			}
			b := watchlist[i]
			give, ok := gate.tryAcquire(b)
			if !ok {
				parked = append(parked, i)
				continue
			}
			// In parallel mode (gate.parallel > 1), a finished fetch returns its
			// host and pool slots right away so an earlier slow board waiting to
			// reconcile cannot block later boards from fetching. In single-slot
			// mode or on cancellation, the caller releases after reconcile.
			var once sync.Once
			release := func() {
				once.Do(func() {
					give()
					mu.Lock()
					freed++
					cond.Signal()
					mu.Unlock()
				})
			}
			index := i + 1
			warnf("board %s/%s: reading (%d of %d)", b.ATS, b.Token, index, total)
			emitListing(ctx, progressPhaseListing, index, total, b, snapshot(), gate.active())
			if err := ctx.Err(); err != nil {
				release()
				results[i] <- listFetch{kind: fetchFailedBeforeList, err: err}
				parked = append(parked, pending[n+1:]...)
				halt = true
				break
			}
			fetchWG.Add(1)
			progressed = true
			go func(i int, b sources.Board, src sources.Source, release func()) {
				defer fetchWG.Done()
				listed, err := src.List(srcutil.WithRetryBudget(ctx, srcutil.BoardRetryBudget), b)
				canceled := ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
				if gate.parallel > 1 && !canceled {
					release()
				}
				results[i] <- listFetch{kind: fetchDone, listed: listed, err: err, canceled: canceled, release: release}
			}(i, b, srcs[b.ATS], release)
		}
		if halt {
			stopAll(parked)
			return
		}
		pending = parked
		if len(pending) == 0 || progressed {
			continue
		}
		mu.Lock()
		for freed == baseline && ctx.Err() == nil {
			cond.Wait()
		}
		mu.Unlock()
	}
}

// listGate hands out fetch slots. The host slot is taken first, then the
// shared pool, so a board waiting on a busy host does not hold a pool slot.
type listGate struct {
	parallel int
	global   chan struct{}
	page     chan struct{}
	inFlight atomic.Int32
	mu       sync.Mutex
	hosts    map[string]chan struct{}
}

func newListGate(parallel int) *listGate {
	if parallel < 1 {
		parallel = 1
	}
	return &listGate{
		parallel: parallel,
		global:   make(chan struct{}, parallel),
		page:     make(chan struct{}, pageListParallel),
		hosts:    map[string]chan struct{}{},
	}
}

func (g *listGate) active() int {
	return max(int(g.inFlight.Load()), 0)
}

// tryAcquire takes the host slot and then the pool without waiting. The caller
// parks the board when ok is false and tries a later one, so a busy host does
// not fill the pool with waiters.
func (g *listGate) tryAcquire(b sources.Board) (release func(), ok bool) {
	host := g.host(listFetchHost(b))
	if !tryTake(host) {
		return nil, false
	}
	pool := g.global
	if b.ATS == posting.Page {
		pool = g.page
	}
	if !tryTake(pool) {
		giveSlot(host)
		return nil, false
	}
	g.inFlight.Add(1)
	return func() {
		g.inFlight.Add(-1)
		giveSlot(pool)
		giveSlot(host)
	}, true
}

func (g *listGate) host(name string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch := g.hosts[name]
	if ch == nil {
		ch = make(chan struct{}, hostListParallel)
		g.hosts[name] = ch
	}
	return ch
}

func tryTake(sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func giveSlot(sem chan struct{}) { <-sem }

// listFetchHost is the host whose in-flight listing this board shares.
// Per-tenant ATSs (Workday, Oracle HCM, Eightfold, Jibe, Avature, TalentBrew,
// Recruitee, Pinpoint, BambooHR, Personio, Feed, Page) resolve to their tenant
// host via config.BoardHost so different companies on those ATSs list in
// parallel; boards on a shared ATS API host (Greenhouse, Ashby, Lever, etc.)
// share that host slot.
func listFetchHost(b sources.Board) string {
	if h, ok := config.BoardHost(b); ok && h != "" {
		return strings.ToLower(h)
	}
	if h := strings.ToLower(strings.TrimSpace(b.Host)); h != "" {
		return h
	}
	if b.ATS == posting.Page || b.ATS == posting.Feed {
		if u, err := url.Parse(strings.TrimSpace(b.URL)); err == nil {
			if h := strings.ToLower(u.Hostname()); h != "" {
				return h
			}
		}
	}
	if r := strings.ToLower(strings.TrimSpace(b.Region)); r != "" {
		return "ats:" + string(b.ATS) + ":" + r
	}
	return "ats:" + string(b.ATS)
}

// listingTick is one moment of listBoards, carried on the context so the
// existing callers keep their signature. Counts exclude the board still in
// flight: they are tallied from the boards already appended.
type listingTick struct {
	Phase                                            string
	Index, Total                                     int
	Board                                            sources.Board
	Done, Read, Failed, RateLimited, Skipped, Listed int
	// Added is the sum of reconcile.new on boards already appended, including
	// zero. The board still in flight is not in it.
	Added int
	// Outcome is set only for phase done, and only when the last appended
	// report is this board. Empty while the board is still in flight.
	Outcome string
	// InFlight is how many board fetches hold a gate slot at this moment.
	InFlight int
}

type listingTickFunc func(listingTick)

type listingTickKey struct{}

func withListingTick(ctx context.Context, fn listingTickFunc) context.Context {
	if fn == nil || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, listingTickKey{}, fn)
}

// listingTickMu serialises progress callbacks. Fetches emit a listing tick
// from their launcher while reconcile emits from the caller.
var listingTickMu sync.Mutex

func noteListing(ctx context.Context, tk listingTick) {
	if ctx == nil {
		return
	}
	fn, _ := ctx.Value(listingTickKey{}).(listingTickFunc)
	if fn == nil {
		return
	}
	listingTickMu.Lock()
	defer listingTickMu.Unlock()
	fn(tk)
}

func tallyTried(tried []boardReport) (done, read, failed, limited, skipped, listed, added int) {
	for _, e := range tried {
		done++
		switch e.Outcome {
		case boardRead:
			read++
			if e.Listed != nil {
				listed += *e.Listed
			}
			if e.Reconcile != nil {
				added += e.Reconcile.New
			}
		case boardFailed:
			failed++
		case boardRateLimited:
			failed++
			limited++
		case boardSkipped:
			skipped++
		}
	}
	return
}

func emitListing(ctx context.Context, phase string, index, total int, b sources.Board, tried []boardReport, inFlight int) {
	done, read, failed, limited, skipped, listed, added := tallyTried(tried)
	tk := listingTick{
		Phase: phase, Index: index, Total: total, Board: b,
		Done: done, Read: read, Failed: failed, RateLimited: limited, Skipped: skipped, Listed: listed,
		Added: added, InFlight: inFlight,
	}
	if phase == progressPhaseDone && len(tried) > 0 {
		last := tried[len(tried)-1]
		if last.ATS == string(b.ATS) && last.Token == oneLine(b.Token, maxBoardNameRunes) {
			tk.Outcome = last.Outcome
		}
	}
	noteListing(ctx, tk)
}

// boardsClock is the time the host cooldowns are read and stamped by; a
// variable only so tests can set it.
var boardsClock = time.Now

// openHostThrottle loads the cooldowns the last run left in path (none when path
// is empty) and returns the run's throttle and the function that writes the
// cooldowns back. A file that cannot be used is a warning, not a failed run, and
// the write replaces it. Nothing is written when nothing changed.
func openHostThrottle(path string, warnf func(string, ...any)) (*srcutil.HostThrottle, func()) {
	var prior map[string]srcutil.HostCooldown
	var loadWarn string
	if path != "" {
		prior, loadWarn = srcutil.LoadCooldowns(path, boardsClock())
		if loadWarn != "" {
			warnf("%s", loadWarn)
		}
	}
	throttle := srcutil.NewHostThrottle(prior, boardsClock)
	return throttle, func() {
		if path == "" {
			return
		}
		after := throttle.Cooldowns()
		same := maps.EqualFunc(prior, after, func(a, b srcutil.HostCooldown) bool { return a.Runs == b.Runs && a.Last.Equal(b.Last) })
		if same && loadWarn == "" {
			return
		}
		if err := srcutil.SaveCooldowns(path, after, boardsClock()); err != nil {
			warnf("host cooldowns not saved: %v", err)
		}
	}
}

func reconcileCountsOf(s pipeline.Summary) *reconcileCounts {
	return &reconcileCounts{New: s.New, Changed: s.Changed, Rerendered: s.Rerendered, Kept: s.Kept, Redated: s.Redated,
		Refreshed: s.Refreshed, Revived: s.Revived, Unchanged: s.Unchanged,
		Reappeared: s.Reappeared, Missed: s.Missed, Dead: s.Dead, Duplicate: s.Duplicate, Invalid: s.Invalid}
}

// boardsReportOf totals the tried boards out of configured and keeps as many
// entries as the bounds allow: failed and skipped boards first (they are why
// the report exists), then read ones, each group in watchlist order. Items
// keeps watchlist order.
func boardsReportOf(configured int, tried []boardReport) boardsReport {
	r := boardsReport{Configured: configured, NotReached: max(configured-len(tried), 0), Items: []boardReport{}}
	added := 0
	for _, e := range tried {
		if e.Reconcile != nil {
			added += e.Reconcile.New
		}
		switch e.Outcome {
		case boardRead:
			r.Read++
			if e.Listed != nil {
				r.Listed += *e.Listed
			}
		case boardFailed:
			r.Failed++
		case boardRateLimited:
			r.Failed++
			r.RateLimited++
		case boardSkipped:
			r.Skipped++
		}
	}
	// Every tried board, not the cut Items. Zero is a real total.
	r.Added = progressCount(added)
	// The first entry that does not fit ends the selection, so a read board is
	// never kept while a failed or skipped one is cut.
	keep := make([]bool, len(tried))
	kept, size := 0, 0
fill:
	for _, readPass := range []bool{false, true} {
		for i, e := range tried {
			if (e.Outcome == boardRead) != readPass {
				continue
			}
			raw, err := json.Marshal(e)
			if err != nil || kept >= maxRecordedBoards || size+len(raw)+1 > maxBoardsBytes {
				break fill // the rest are counted below as unrecorded
			}
			keep[i], kept, size = true, kept+1, size+len(raw)+1
		}
	}
	for i, e := range tried {
		if keep[i] {
			r.Items = append(r.Items, e)
		} else {
			r.Unrecorded++
		}
	}
	return r
}
