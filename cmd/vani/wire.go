package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/llm/gemini"
	"github.com/bharathvbcr/Manvi/manvi/llm/transport"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/localserver"
	"github.com/bharathvbcr/Vani/internal/notify"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/amazon"
	"github.com/bharathvbcr/Vani/internal/sources/apple"
	"github.com/bharathvbcr/Vani/internal/sources/ashby"
	"github.com/bharathvbcr/Vani/internal/sources/eightfold"
	"github.com/bharathvbcr/Vani/internal/sources/google"
	"github.com/bharathvbcr/Vani/internal/sources/greenhouse"
	"github.com/bharathvbcr/Vani/internal/sources/lever"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
	"github.com/bharathvbcr/decide/lappi"
)

// The OpenRouter model's daily cap lives in <state dir>/budget/jev.json under
// spend key "jev": the names it had before the backend was named after its
// model. Keeping them carries a day's spend across the rename.
const (
	openRouterBudgetFile = "jev.json"
	openRouterSpendKey   = "jev"
)

// openRouterName is the configured OpenRouter model's backend name, or ""
// when none is configured (then no backend drops on a probability).
func (w *wired) openRouterName() string {
	if !w.cfg.OpenRouterConfigured() {
		return ""
	}
	return w.cfg.Decide.OpenRouterName()
}

// shadowName maps decide.shadow to the backend name it selects: the role
// "openrouter" answers under the model's name, "local" under the model and
// question mode (config.Local.BackendName); the others are their names.
func (w *wired) shadowName() string {
	switch w.cfg.Decide.Shadow {
	case config.ShadowOpenRouter:
		return w.openRouterName()
	case "local":
		if l := w.cfg.Decide.Local; l != nil {
			return l.BackendName()
		}
	}
	return w.cfg.Decide.Shadow
}

// maxBoardBytes bounds one board listing. Greenhouse returns a whole board,
// descriptions included, in one response (GitLab's was 3.6 MB on 2026-10-18).
const maxBoardBytes = 32 << 20

// wireApp builds the real components. It opens only what every command needs
// (the ledger); network clients and child processes start on the first run,
// so `vani status` never spawns DevPrism or reads an API key.
func wireApp(cfg config.Config, stateDir string, warn io.Writer) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	led, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if warn == nil {
		warn = io.Discard
	}
	w := &wired{cfg: cfg, stateDir: stateDir, led: led, warn: warn}
	return &App{
		Ledger:    led,
		RunOnce:   w.runOnce,
		RunReport: w.runReport,
		// The Vani Apply app posts run notices itself while it runs; the
		// daemon's are the fallback for when it does not.
		Notifier: notify.UnlessHeld(notify.AppLockPath(stateDir), notify.New(nil)),
		Closer:   w.close,
	}, nil
}

type wired struct {
	cfg      config.Config
	stateDir string
	led      *ledger.File
	warn     io.Writer
	warnMu   sync.Mutex // serialises writes to warn

	mu     sync.Mutex
	scorer *lazyScorer

	// localEndpoint is the address of the local decision model's server when
	// runOnce started one; empty otherwise.
	localEndpoint string

	// backendErrs collects the current run's decision-backend errors; runOnce
	// replaces it at the start of every run. advance is the last run's
	// advance report. Both are read by runReport, under mu.
	backendErrs *backendErrors
	advance     *advanceReport
}

// runReport is what the last runOnce left for the run record.
func (w *wired) runReport() runDetail {
	w.mu.Lock()
	defer w.mu.Unlock()
	return runDetail{Advance: w.advance, BackendErrors: w.backendErrs.report()}
}

func (w *wired) close() error {
	w.mu.Lock()
	s := w.scorer
	w.mu.Unlock()
	var errs []error
	if s != nil {
		errs = append(errs, s.Close())
	}
	errs = append(errs, w.led.Close())
	return errors.Join(errs...)
}

// warnf prints one warning line. Postings advance concurrently and their
// backends warn from their own goroutines, so lines are written under warnMu,
// whole.
func (w *wired) warnf(format string, a ...any) {
	line := fmt.Sprintf("vani: "+format+"\n", a...)
	w.warnMu.Lock()
	defer w.warnMu.Unlock()
	_, _ = io.WriteString(w.warn, line)
}

// runOnce lists every board, reconciles it, then advances postings. It fails
// as a whole only when no board could be read or the ledger fails; a single
// bad board is a warning, and its postings are left untouched (an unread
// board must never count as "every posting disappeared").
func (w *wired) runOnce(ctx context.Context) error {
	d, err := egress.New(egress.Config{AllowedHosts: w.cfg.AllowedHosts, MaxBodyBytes: maxBoardBytes})
	if err != nil {
		return fmt.Errorf("egress: %w", err)
	}
	// A local decision model configured with a server is started for this run
	// only. If it does not come up, the run goes on without the shadow. The
	// endpoint lives exactly as long as its server: a daemon reuses w, and a
	// failed start must never inherit the last run's stopped address.
	w.localEndpoint = ""
	errs := newBackendErrors()
	w.mu.Lock()
	w.backendErrs, w.advance = errs, nil
	w.mu.Unlock()
	if l := w.cfg.Decide.Local; l != nil && l.Server != nil {
		srv, err := localserver.Start(ctx, localserver.Config{
			Binary: l.Server.Binary, Model: l.Server.ModelPath, ContextSize: l.Server.ContextSize,
			StartTimeout: time.Duration(l.Server.StartTimeoutMS) * time.Millisecond,
			BatchSize:    l.Server.BatchSize, Parallel: l.Server.Parallel,
		})
		if err != nil {
			w.warnf("local decision model not started; running without the shadow: %v", err)
		} else {
			defer func() { w.localEndpoint = ""; srv.Stop() }()
			w.localEndpoint = srv.Endpoint
		}
	}
	// Assemble the stages first: a run that cannot triage must not fetch
	// boards for nothing.
	runner, err := w.runner(ctx, d)
	if err != nil {
		return err
	}
	srcs := apiSources(d)
	gh, err := greenhouse.NewWithPay(d, greenhouse.Pay{
		Known: func(k posting.Key) (posting.Posting, bool) {
			r, ok := w.led.Get(k)
			return r.Posting, ok
		},
		Report: func(b sources.Board, st greenhouse.PayStats) {
			w.warnf("board %s/%s: pay %+v", b.ATS, b.Token, st)
		},
	})
	if err != nil {
		return err
	}
	srcs[posting.Greenhouse] = gh
	if w.hasPageBoard() {
		src, stop, err := w.pageSource(ctx, d)
		if err != nil {
			// Page boards are skipped with a warning below; API boards still run.
			w.warnf("careers-page boards disabled: %v", err)
		} else {
			defer stop()
			srcs[posting.Page] = src
		}
	}

	// One index of the ledger for the whole loop: only Reconcile writes to the
	// ledger here (the adapters read it), as Reconciler requires.
	rec := pipeline.NewReconciler(w.led)
	read := 0
	for _, b := range w.cfg.Watchlist {
		src, ok := srcs[b.ATS]
		if !ok {
			if b.ATS == posting.Page {
				w.warnf("board %s/%s: careers-page source disabled this run; skipped", b.ATS, b.Token)
			} else {
				w.warnf("board %s/%s: no adapter for %s; skipped", b.ATS, b.Token, b.ATS)
			}
			continue
		}
		listed, err := src.List(ctx, b)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.warnf("board %s/%s: %v; left unchanged", b.ATS, b.Token, err)
			continue
		}
		read++
		sum, err := rec.Reconcile(ctx, b, listed, time.Now())
		if err != nil {
			return fmt.Errorf("reconcile %s/%s: %w", b.ATS, b.Token, err)
		}
		w.warnf("board %s/%s: %+v", b.ATS, b.Token, sum)
	}
	if len(w.cfg.Watchlist) > 0 && read == 0 {
		return errors.New("no board could be read")
	}

	rep, err := advanceAll(ctx, w.led.All(), w.titleRank(), w.cfg.AdvanceLimit(), w.cfg.AdvanceWorkers(), titleDropper(runner.Policy), runner.Advance, w.warnf)
	w.mu.Lock()
	w.advance = &rep
	w.mu.Unlock()
	w.warnShadowErrors(runner)
	w.warnShadowSample(runner)
	return err
}

// dropPolicy is the configured drop rules, and the title reader that applies
// them before any backend is asked. The run and `vani prefilter` share it.
func dropPolicy(cfg config.Config, openRouter string) pipeline.DropPolicy {
	rules := make([]pipeline.DropRule, 0, len(cfg.DropRules))
	for _, r := range cfg.DropRules {
		rules = append(rules, pipeline.DropRule{Slot: r.Slot, Values: r.Values, MinOpenRouterProb: r.MinOpenRouterProb, MinLappiScore: r.MinLappiScore})
	}
	p := pipeline.DropPolicy{Rules: rules, OpenRouter: openRouter}
	if s := cfg.TitleSignals; len(s.Read) > 0 {
		p.ReadTitle = func(title string) []pipeline.TitleReading {
			reads := s.Reads(title)
			out := make([]pipeline.TitleReading, len(reads))
			for i, r := range reads {
				out[i] = pipeline.TitleReading{Slot: r.Slot, Value: r.Value, Matched: r.Matched}
			}
			return out
		}
	}
	return p
}

// titleDropper reports whether the runner would drop a record by its title
// alone (a live posting the policy's title reading drops), or is nil when
// the policy reads no titles.
func titleDropper(p pipeline.DropPolicy) func(ledger.Record) bool {
	if p.ReadTitle == nil {
		return nil
	}
	return func(r ledger.Record) bool {
		if r.Status != posting.Live {
			return false
		}
		_, _, dropped := p.EvaluateTitle(r.Posting.Title)
		return dropped
	}
}

// titleRank orders triage by the configured title signals, or is nil (ledger
// order) when none are configured.
func (w *wired) titleRank() func(posting.Posting) int {
	s := w.cfg.TitleSignals
	if len(s.Include) == 0 && len(s.Exclude) == 0 {
		return nil
	}
	return func(p posting.Posting) int { return s.Rank(p.Title) }
}

// apiSources are the adapters for every board kind but careers pages, which
// need a per-run proxy and browser.
func apiSources(d egress.Doer) map[posting.ATS]sources.Source {
	return map[posting.ATS]sources.Source{
		posting.Greenhouse: greenhouse.New(d),
		posting.Lever:      lever.New(d),
		posting.Ashby:      ashby.New(d),
		posting.Workday:    workday.New(d),
		posting.Amazon:     amazon.New(d),
		posting.Google:     google.New(d),
		posting.Apple:      apple.New(d),
		posting.Eightfold:  eightfold.New(d),
	}
}

// maxConsecutiveUnread stops a run's advance phase once this many postings in
// a row could not be read by any decision backend: a refused key or an outage
// fails every call, and spending the whole cap on it repeats the same failure
// (and the shadow's work) hundreds of times.
const maxConsecutiveUnread = 20

// maxConsecutiveFailed does the same for any other stage failure (DevPrism
// down, the explainer refused): that many in a row is an outage, not a bad
// posting, and the run fails rather than publish a digest that hides it.
const maxConsecutiveFailed = 20

// testHookCounted, set only by tests, is called on advanceAll's goroutine
// after each finished posting is counted, with whether the phase has decided
// to stop. A test uses it to finish postings in an exact order.
var testHookCounted func(key posting.Key, stopped bool)

// errAdvanceFailing marks a run stopped because postings kept failing a stage
// other than triage.
var errAdvanceFailing = errors.New("advance: postings keep failing a stage")

// maxUnreadExamples bounds the per-posting lines for unread postings; the rest
// are counted in one summary line.
const maxUnreadExamples = 3

// maxDistinctCauses bounds how many different advance failures a run reports
// one by one; later new causes are only counted.
const maxDistinctCauses = 20

// advanceAll advances up to limit postings (config advance_per_run). They go
// highest title rank first, then freshest first (orderAdvance), ties in
// ledger order, so the cap spends itself on new postings with the titles the
// active track wants. Order only orders: every eligible posting is still advanced, this run or a
// later one. A posting no backend could read stays Live for the next run. The
// run fails, so a shell loop stops and no empty digest is published, when no
// backend read maxConsecutiveUnread postings in a row, when maxConsecutiveFailed
// in a row failed another stage, or when none of the postings it tried
// advanced. Any other stage failure is printed once per cause (the posting key
// and provider request id are not part of the cause) and counted after that.
// The report says what happened however the phase ended, so the run record
// can show what the cap or a stop left waiting.
//
// Up to workers postings (config advance_concurrency) are advanced at a time.
// They start in rank order and their results are counted on this goroutine,
// so the report, the warnings and the stop rules need no lock. The ledger
// serialises its own writes, and each posting is advanced by one goroutine
// only. The streaks are counted in rank (dispatch) order, not finishing
// order: a posting that ends a streak resets only the postings dispatched
// before it. A stop (unread or failure streak, cancellation) starts no
// further posting but waits for those in flight, so nothing is written to
// the ledger after advanceAll returns. A streak stop therefore attempts at
// most maxConsecutiveUnread (or maxConsecutiveFailed) postings after the
// last posting that ended the streak, plus workers-1 in flight, plus any
// postings dropped by title among them (they count as attempted but end and
// extend no streak).
//
// titleDrop, when set, reports whether advancing r drops it by its title
// without asking any backend. Such a posting counts toward limit, but it is no
// backend reading: it neither ends an unread streak nor keeps the phase from
// failing when no backend read anything.
func advanceAll(ctx context.Context, recs []ledger.Record, rank func(posting.Posting) int, limit, workers int, titleDrop func(ledger.Record) bool, advance func(context.Context, posting.Key) error, warnf func(string, ...any)) (advanceReport, error) {
	if limit <= 0 {
		return advanceReport{}, fmt.Errorf("advance limit %d must be above 0", limit)
	}
	if workers <= 0 {
		return advanceReport{}, fmt.Errorf("advance concurrency %d must be above 0", workers)
	}
	var todo []ledger.Record
	for _, r := range recs {
		switch r.Status {
		case posting.Live, posting.Triaged, posting.Unsure, posting.Scored:
			todo = append(todo, r)
		}
	}
	orderAdvance(todo, recs, rank)
	rep := advanceReport{Eligible: len(todo), Limit: limit}
	done := func(stopped string) advanceReport {
		rep.Deferred = rep.Eligible - rep.Attempted
		rep.Stopped = stopped
		return rep
	}
	n := min(len(todo), limit)
	// The stop rules count in dispatch (rank) order, not finishing order. A
	// streak counts the finished postings of its kind dispatched after the
	// last finished posting that ends it (since), so a posting dispatched
	// before an outage that finishes after postings dispatched behind it
	// cannot reset their count. A stop therefore comes once maxConsecutive*
	// postings dispatched after that posting have finished, with at most
	// workers-1 others in flight: Attempted <= since+1+maxConsecutive*+
	// workers-1, plus the title drops among them, which are attempted but
	// neutral.
	const (
		pending byte = iota
		wasRead
		wasUnread
		wasFailed
		neutral // dropped by title or cut short by cancellation: ends nothing
	)
	outcomes := make([]byte, n)
	type streakOf struct {
		kind         byte
		since, count int
	}
	unreadRun, failRun := streakOf{kind: wasUnread, since: -1}, streakOf{kind: wasFailed, since: -1}
	next := 0 // postings dispatched so far
	fold := func(s *streakOf, i int) {
		switch c := outcomes[i]; {
		case c == s.kind:
			if i > s.since {
				s.count++
			}
		case c == wasRead || c == wasUnread || c == wasFailed:
			if i > s.since {
				s.since, s.count = i, 0
				for j := i + 1; j < next; j++ {
					if outcomes[j] == s.kind {
						s.count++
					}
				}
			}
		}
	}
	type cause struct {
		first posting.Key
		more  int
	}
	causes := map[string]*cause{}
	var order []string
	uncounted, byTitle := 0, 0
	// reportCauses prints the counts the first line of each cause left out.
	reportCauses := func() {
		for _, k := range order {
			if c := causes[k]; c.more > 0 {
				warnf("%d more posting(s) failed the same way as %s; they wait for the next run", c.more, c.first)
			}
		}
		if uncounted > 0 {
			warnf("%d more posting(s) failed with other errors (more than %d distinct causes); they wait for the next run", uncounted, maxDistinctCauses)
		}
	}
	stopped := ""
	var cancelErr error
	// classify counts one finished posting into the report and says what it
	// was for the stop rules. free: the posting was dropped by its title, so
	// no backend was asked.
	classify := func(key posting.Key, free bool, err error) byte {
		if err != nil && ctx.Err() != nil {
			// Cut short by cancellation: neither unread nor failed.
			if stopped == "" {
				stopped, cancelErr = stopCancelled, ctx.Err()
			}
			return neutral
		}
		if err == nil && free {
			byTitle++
			return neutral
		}
		if errors.Is(err, pipeline.ErrTriageUnavailable) {
			rep.Unread++
			if rep.Unread <= maxUnreadExamples {
				warnf("advance %s: %v", key, err)
			}
			return wasUnread
		}
		if err == nil {
			return wasRead
		}
		rep.Failed++
		ek := errorKey(strings.ReplaceAll(err.Error(), string(key), "<posting>"))
		switch c, seen := causes[ek]; {
		case seen:
			c.more++
		case len(causes) >= maxDistinctCauses:
			uncounted++
		default:
			causes[ek] = &cause{first: key}
			order = append(order, ek)
			warnf("advance %s: %v", key, err)
		}
		return wasFailed
	}
	// count folds the posting dispatched i-th into the report and the stop rules.
	count := func(i int, key posting.Key, free bool, err error) {
		outcomes[i] = classify(key, free, err)
		fold(&unreadRun, i)
		fold(&failRun, i)
		if stopped != "" {
			return
		}
		switch {
		case unreadRun.count >= maxConsecutiveUnread:
			stopped = stopUnreadStreak
		case failRun.count >= maxConsecutiveFailed:
			stopped = stopFailStreak
		}
	}

	type result struct {
		idx      int
		key      posting.Key
		free     bool
		err      error
		panicked bool
		value    any
	}
	// Buffered to workers, so a finished posting never blocks its goroutine.
	results := make(chan result, workers)
	inflight := 0
	var panicked *result
	for {
		for stopped == "" && panicked == nil && next < n && inflight < workers {
			if err := ctx.Err(); err != nil {
				stopped, cancelErr = stopCancelled, err
				break
			}
			idx, key := next, todo[next].Key
			// Decided here, before the posting advances and its status moves.
			free := titleDrop != nil && titleDrop(todo[next])
			next++
			inflight++
			rep.Attempted++
			go func() {
				// A panic is carried back and re-raised on the caller's
				// goroutine, where the daemon contains it as it did before
				// postings ran concurrently.
				defer func() {
					if p := recover(); p != nil {
						results <- result{idx: idx, key: key, panicked: true, value: p}
					}
				}()
				err := advance(ctx, key)
				results <- result{idx: idx, key: key, free: free, err: err}
			}()
		}
		if inflight == 0 {
			break
		}
		r := <-results
		inflight--
		if r.panicked {
			if panicked == nil {
				panicked = &r
			}
			continue
		}
		count(r.idx, r.key, r.free, r.err)
		if testHookCounted != nil {
			testHookCounted(r.key, stopped != "")
		}
	}
	if panicked != nil {
		panic(fmt.Sprintf("advance %s: %v", panicked.key, panicked.value))
	}
	switch stopped {
	case stopCancelled:
		return done(stopCancelled), cancelErr
	case stopUnreadStreak:
		return done(stopUnreadStreak), fmt.Errorf("no decision backend read %d postings in a row; stopped advancing after %d (they stay live; see the backend errors above): %w",
			maxConsecutiveUnread, rep.Attempted, pipeline.ErrTriageUnavailable)
	case stopFailStreak:
		reportCauses()
		return done(stopFailStreak), fmt.Errorf("%d postings in a row failed a stage; stopped advancing after %d (they wait for the next run; see the errors above): %w",
			maxConsecutiveFailed, rep.Attempted, errAdvanceFailing)
	}
	if len(todo) > limit {
		warnf("advance cap %d reached; %d posting(s) wait for the next run (advance_per_run)", limit, len(todo)-rep.Attempted)
	}
	reportCauses()
	if byTitle > 0 {
		warnf("%d posting(s) dropped by title before triage (title_signals.read); no backend was asked", byTitle)
	}
	if rep.Unread > 0 {
		if rep.Unread == rep.Attempted-byTitle {
			return done(stopNoneRead), fmt.Errorf("no decision backend read any posting: %d of %d unread (they stay live; see the backend errors above): %w",
				rep.Unread, rep.Attempted, pipeline.ErrTriageUnavailable)
		}
		warnf("%d posting(s) could not be read by any decision backend; they stay live for the next run", rep.Unread)
	}
	// Title drops asked no stage, so like the unread check above they neither
	// count as advancing nor hide a stage that failed everything else.
	if asked := rep.Attempted - byTitle; asked > 0 && rep.Unread+rep.Failed == asked {
		return done(stopNoneAdvanced), fmt.Errorf("no posting advanced: %d failed a stage and %d were unread, of %d attempted (they wait for the next run; see the errors above): %w",
			rep.Failed, rep.Unread, rep.Attempted, errAdvanceFailing)
	}
	if rep.Attempted >= limit && rep.Attempted < rep.Eligible {
		return done(stopCap), nil
	}
	return done(""), nil
}

// lightpandaTimeout bounds one page load, a process start included.
const lightpandaTimeout = 60 * time.Second

func (w *wired) hasPageBoard() bool {
	for _, b := range w.cfg.Watchlist {
		if b.ATS == posting.Page {
			return true
		}
	}
	return false
}

// pageProxyConfig is the egress proxy for careers pages. One lightpanda
// process serves a whole board and keeps its CONNECT tunnel to a host open
// across the board's pages, so a tunnel may live as long as a board does; at
// the 5 min default a large board's tunnel is cut, and lightpanda re-sends
// the request in flight unpaced (measured 2026-10-19).
var pageProxyConfig = egress.ProxyConfig{MaxLifetime: page.BoardTimeout}

// pageSource starts the per-run egress proxy that lightpanda must tunnel
// through, so a careers page gets the same allowlist, https-only and
// private-address rules as an API board, and the browser that runs through
// it. Both live for this run only; stop ends the browser, then the proxy. No
// lightpanda process starts until a page board is listed.
func (w *wired) pageSource(ctx context.Context, c *egress.Client) (sources.Source, func(), error) {
	bin, err := lightpandaBinary(w.cfg.LightpandaBinary)
	if err != nil {
		return nil, nil, err
	}
	p, err := egress.NewProxy(c, pageProxyConfig)
	if err != nil {
		return nil, nil, err
	}
	addr, token, err := p.Start(ctx)
	if err != nil {
		_ = p.Close()
		return nil, nil, fmt.Errorf("egress proxy: %w", err)
	}
	proxy := page.ProxyInfo{Addr: addr, Token: token}
	run, browser, err := pageBrowser(bin, w.cfg.PageInterval(), c, proxy)
	if err != nil {
		_ = p.Close()
		return nil, nil, err
	}
	stop := func() {
		if err := browser.Close(); err != nil {
			w.warnf("lightpanda close: %v", err)
		}
		if err := p.Close(); err != nil {
			w.warnf("egress proxy close: %v", err)
		}
	}
	return page.New(run, proxy), stop, nil
}

// pageBrowser is lightpanda at bin, one process per board, with every page
// load spaced per host by the longer of interval (config page_interval_ms)
// and the Crawl-delay in that host's robots.txt, which is read through d.
// The *MCPBrowser is returned for Close.
func pageBrowser(bin string, interval time.Duration, d egress.Doer, proxy page.ProxyInfo) (*page.Paced, *page.MCPBrowser, error) {
	flags, err := page.Flags(proxy)
	if err != nil {
		return nil, nil, err
	}
	browser, err := page.NewMCPBrowser(bin, flags, lightpandaTimeout)
	if err != nil {
		return nil, nil, err
	}
	paced, err := page.NewRobotsPaced(browser, d, interval)
	if err != nil {
		return nil, nil, fmt.Errorf("page_interval_ms: %w", err)
	}
	return paced, browser, nil
}

// lightpandaBinary resolves the configured binary, or "lightpanda" on PATH,
// to an absolute path. MCPBrowser refuses a relative one.
func lightpandaBinary(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	p, err := exec.LookPath("lightpanda")
	if err != nil {
		return "", fmt.Errorf("lightpanda not found on PATH (set lightpanda_binary): %w", err)
	}
	return filepath.Abs(p)
}

// localBackend builds the shadow client for a decision model served on this
// Mac. By default it gets exactly the request Jev gets (the combined question,
// all slots, the same option descriptions), so `vani eval` compares models,
// not prompts; questions "per_slot" asks each slot its fixture family's own
// question instead. llama.cpp's /v1/systemone takes the state as a plain string.
//
// The backend is named after the model and the question mode
// (config.Local.BackendName), so two candidate models are two shadow backends in
// the ledger and `vani eval` and never pool.
func localBackend(l *config.Local, endpoint string) (decide.Backend, error) {
	return localBackendNamed(l, endpoint, l.BackendName())
}

// localBackendNamed is localBackend under an explicit backend name, for callers
// (`vani bench`) whose backend name never reaches the ledger.
func localBackendNamed(l *config.Local, endpoint, name string) (decide.Backend, error) {
	if name == "" {
		return nil, errors.New("the local model leaves no shadow backend name")
	}
	if endpoint == "" {
		return nil, errors.New("no endpoint: the local server was not started this run")
	}
	timeout := 20 * time.Second
	if l.TimeoutMS > 0 {
		timeout = time.Duration(l.TimeoutMS) * time.Millisecond
	}
	d, err := egress.NewLoopback(timeout+5*time.Second, 0)
	if err != nil {
		return nil, err
	}
	var instr map[string]string
	if l.Questions == "per_slot" {
		instr = make(map[string]string, len(jdtriage.Families))
		for _, f := range jdtriage.Families {
			instr[f.Slot.Name] = f.Question
		}
	}
	return jev.New(jev.Config{
		Name:         name,
		Endpoint:     endpoint,
		Model:        l.Model,
		Timeout:      timeout,
		MaxAttempts:  1, // a local server that failed once will fail again this second
		State:        func(q decide.Question) any { return string(q.Context) },
		Descriptions: jdtriage.Descriptions(),
		Instructions: instr,
	}, d)
}

// runner assembles today's pipeline stages. The LLM pieces are rebuilt per
// run so the daily budget ledger rolls over with the date.
func (w *wired) runner(ctx context.Context, d egress.Doer) (*pipeline.Runner, error) {
	var backends []decide.Backend
	// One cap for every OpenRouter client this run (triage and notes): two
	// DailyCaps on one file would each lock only themselves. The file and its
	// spend key keep their pre-rename name "jev" (jev.Config's default
	// SpendKey), so renaming the backend did not reset a day's spend.
	var spend *decide.DailyCap
	if w.cfg.OpenRouterConfigured() {
		var err error
		spend, err = decide.NewDailyCap(filepath.Join(w.stateDir, "budget", openRouterBudgetFile), w.cfg.Decide.DailyUSD.OpenRouter)
		if err != nil {
			return nil, fmt.Errorf("openrouter budget: %w", err)
		}
		jb, err := jev.New(jev.Config{
			Name:         w.cfg.Decide.OpenRouterName(),
			Model:        w.cfg.Decide.OpenRouterModel,
			KeyEnv:       w.cfg.Decide.OpenRouterKeyEnv,
			Descriptions: jdtriage.Descriptions(),
			Spend:        spend,
			SpendKey:     openRouterSpendKey,
		}, d)
		if err != nil {
			w.warnf("openrouter (%s) disabled: %v", w.cfg.Decide.OpenRouterModel, err)
		} else {
			backends = append(backends, logErrors(jb, w.warnf, w.backendErrs))
		}
	}

	if w.cfg.LLM.Provider != "" {
		p, err := w.provider(ctx)
		if err != nil {
			w.warnf("llm disabled: %v", err)
		} else {
			lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.Model})
			if err != nil {
				return nil, fmt.Errorf("llm backend: %w", err)
			}
			backends = append(backends, logErrors(lb, w.warnf, w.backendErrs))
		}
	}
	// Lappi is local and free. It is the shadow until `vani eval` reports it
	// promotable; promoted, it goes first so the OpenRouter model and the LLM only see what it
	// abstained on.
	if l := w.cfg.Decide.Lappi; l != nil {
		// Lappi trains one slot per family, so each triage slot goes to its
		// own family with the fixture's exact question.
		routes := make([]lappi.Route, len(jdtriage.Families))
		for i, f := range jdtriage.Families {
			routes[i] = lappi.Route{Task: jdtriage.Task, Slot: f.Slot.Name, Family: f.Task, Question: f.Question}
		}
		lb, err := lappi.NewBackend(lappi.BackendConfig{
			App:     "vani",
			Routes:  routes,
			Socket:  l.Socket,
			Timeout: time.Duration(l.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			w.warnf("lappi disabled: %v", err)
		} else if l.Primary {
			backends = append([]decide.Backend{logErrors(lb, w.warnf, w.backendErrs)}, backends...)
		} else {
			backends = append(backends, logErrors(lb, w.warnf, w.backendErrs))
		}
	}

	// A local decision model (llama.cpp /v1/systemone) speaks the Jev wire shape,
	// so it is a second Jev client under its own name, reaching only this Mac.
	// It is only ever the shadow (config enforces it): measured, never deciding.
	if l := w.cfg.Decide.Local; l != nil {
		endpoint := l.Endpoint
		if l.Server != nil {
			endpoint = w.localEndpoint // set by runOnce when it started the server
		}
		lb, err := localBackend(l, endpoint)
		if err != nil {
			w.warnf("local decision model disabled: %v", err)
		} else {
			backends = append(backends, logErrors(lb, w.warnf, w.backendErrs))
		}
	}

	// The shadow backend is asked alongside the chain and never decides, so it
	// leaves the chain: asking it twice would pay twice for one answer.
	var shadow decide.Backend
	if name := w.shadowName(); name != "" {
		for i, b := range backends {
			if b.Name() == name {
				shadow = b
				backends = append(backends[:i:i], backends[i+1:]...)
				break
			}
		}
		if shadow == nil {
			w.warnf("shadow %q unavailable; running without a shadow", name)
		}
	}
	if len(backends) == 0 {
		return nil, errors.New("no decision backend is available (configure decide.openrouter_model or llm)")
	}
	router := decide.NewRouter(backends...)
	router.Shadow = shadow
	gateShadow(router, w.cfg.Decide.ShadowSampleEvery(), shadowInFlight(w.cfg.Decide))

	explainer, skipExplain := w.notes(d, spend)
	r := &pipeline.Runner{
		Ledger:      w.led,
		Triager:     &jdtriage.Triager{D: router, TargetSummary: w.cfg.TargetSummary},
		Explainer:   explainer,
		Policy:      dropPolicy(w.cfg, w.openRouterName()),
		SkipExplain: skipExplain,
	}
	x, err := loadLCA(w.stateDir)
	if err != nil {
		return nil, err
	}
	if x != nil {
		r.Badger = lcaBadger{x}
	}
	ex, err := loadEmployerView(w.stateDir, w.warnf)
	if err != nil {
		return nil, err
	}
	if ex != nil {
		r.Employer = employerLookup{ex}
		r.PreferEmployers = w.cfg.EmployerPreferences
	} else if len(w.cfg.EmployerPreferences) > 0 {
		w.warnf("employer_preferences set but no employer lists imported; no rank boost (docs/EMPLOYER.md)")
	}
	if w.cfg.DevPrism.Binary == "" {
		w.warnf("devprism.binary empty; scoring disabled, the digest will not be ranked (docs/FOLLOWUPS.md)")
		r.SkipScoring = true
	} else {
		r.Scorer = w.lazyScorer()
	}
	return r, nil
}

// provider builds the configured Manvi adapter, metered by a budget ledger
// that is one file per UTC day, so decide.daily_usd.llm is a daily cap.
func (w *wired) provider(ctx context.Context) (llm.Provider, error) {
	in, out, err := w.cfg.LLM.Prices()
	if err != nil {
		return nil, err
	}
	name := w.cfg.LLM.Provider
	resolver := credentials.NewResolver()
	if _, err := resolver.Resolve(name); err != nil {
		return nil, err
	}
	resolve := func() (credentials.Secret, error) { return resolver.Resolve(name) }
	var inner llm.Provider
	switch name {
	case "gemini":
		inner = gemini.New("", resolve)
	case "anthropic":
		// As Manvi's own CLI does: ANTHROPIC_WORKSPACE_ID names the workspace
		// for a key not scoped to one (Anthropic refuses such a key's requests
		// with a 400 otherwise), and ANTHROPIC_BASE_URL overrides the host.
		a, err := anthropic.NewForWorkspace(os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv(credentials.AnthropicWorkspaceEnv), resolve)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", credentials.AnthropicWorkspaceEnv, err)
		}
		inner = a
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	day := time.Now().UTC().Format("2006-01-02")
	limit := int64(w.cfg.Decide.DailyUSD.LLM * 1e9)
	l, err := budget.Open(ctx, filepath.Join(w.stateDir, "budget", "llm-"+day+".json"), limit)
	if err != nil {
		return nil, fmt.Errorf("llm budget: %w", err)
	}
	prices := budget.Prices{Rates: budget.Rates{Input: in, Output: out, CacheWrite: in, CacheRead: in}}
	return &budget.Provider{Inner: pricedProvider{Provider: inner, prices: prices}, Ledger: l}, nil
}

// pricedProvider meters the adapter at the configured prices (the operator's
// conservative figures, which win over any table the adapter publishes).
//
// Embedding llm.Provider exposes only that interface, so every optional
// contract budget.Provider looks for on its inner provider is forwarded here
// by hand. Without AttemptGateSupported, budget.Provider refuses every request
// before it is sent ("does not route every attempt through the admission gate").
type pricedProvider struct {
	llm.Provider
	prices budget.Prices
}

func (p pricedProvider) PriceOf(string) (budget.Prices, bool) { return p.prices, true }

// AttemptGateSupported is true only when the adapter itself makes that promise.
func (p pricedProvider) AttemptGateSupported() bool {
	g, ok := p.Provider.(transport.AttemptGatedProvider)
	return ok && g.AttemptGateSupported()
}

// AppliedMaxTokens forwards the adapter's output bound; without one it is what
// budget.Provider would assume: the request's bound, else the model ceiling.
func (p pricedProvider) AppliedMaxTokens(req llm.Request) int {
	if r, ok := p.Provider.(budget.MaxTokensReporter); ok {
		return r.AppliedMaxTokens(req)
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	c, _ := p.Provider.Capability(req.Model)
	return c.MaxOutputTokens
}

// RejectionUnbilled forwards the adapter's rule; without one nothing is unbilled.
func (p pricedProvider) RejectionUnbilled(status int) bool {
	r, ok := p.Provider.(budget.UnbilledRejector)
	return ok && r.RejectionUnbilled(status)
}

// ReplayableOn forwards the adapter's rule; without one only the same model replays.
func (p pricedProvider) ReplayableOn(fromModel, toModel string) bool {
	if r, ok := p.Provider.(llm.ReasoningReplayer); ok {
		return r.ReplayableOn(fromModel, toModel)
	}
	return fromModel == toModel
}

func (w *wired) lazyScorer() *lazyScorer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.scorer == nil {
		w.scorer = &lazyScorer{cfg: resume.Config{BinaryPath: w.cfg.DevPrism.Binary, PinnedToolsHash: w.cfg.DevPrism.ToolsHash}}
	}
	return w.scorer
}

// scorerRetry is how long a failed DevPrism start is remembered before the
// next use tries again: long enough that one run does not restart a broken
// binary for every posting, short enough that a daemon's next daily run
// recovers from a transient failure (a binary being replaced, a reboot race).
const scorerRetry = 10 * time.Minute

// scorerClient is what lazyScorer uses of a started DevPrism.
type scorerClient interface {
	Gap(ctx context.Context, jd, persona string) (resume.Gap, error)
	Score(ctx context.Context, jd, persona string) (resume.Score, error)
	Close() error
}

// lazyScorer starts DevPrism on first use, so commands that never score never
// spawn it. A failed start is remembered for scorerRetry: retrying a broken
// binary on every posting would only repeat the error, but a daemon must not
// keep a transient failure for the rest of its life.
type lazyScorer struct {
	cfg resume.Config
	// start and now are resume.Start and time.Now; tests replace them.
	start func(context.Context, resume.Config) (scorerClient, error)
	now   func() time.Time

	mu       sync.Mutex
	client   scorerClient
	err      error
	failedAt time.Time
}

func (s *lazyScorer) get(ctx context.Context) (scorerClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	if s.err != nil && now().Sub(s.failedAt) < scorerRetry {
		return nil, fmt.Errorf("devprism: %w", s.err)
	}
	start := s.start
	if start == nil {
		start = func(ctx context.Context, cfg resume.Config) (scorerClient, error) {
			c, err := resume.Start(ctx, cfg)
			if err != nil {
				return nil, err
			}
			return c, nil
		}
	}
	c, err := start(ctx, s.cfg)
	if err != nil {
		if ctx.Err() != nil {
			// A cancelled start says nothing about the binary; do not remember it.
			return nil, fmt.Errorf("devprism: %w", err)
		}
		s.err, s.failedAt = err, now()
		return nil, fmt.Errorf("devprism: %w", err)
	}
	s.client, s.err = c, nil
	return c, nil
}

func (s *lazyScorer) Gap(ctx context.Context, jd, persona string) (resume.Gap, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Gap{}, err
	}
	return c.Gap(ctx, jd, persona)
}

func (s *lazyScorer) Score(ctx context.Context, jd, persona string) (resume.Score, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Score{}, err
	}
	return c.Score(ctx, jd, persona)
}

func (s *lazyScorer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	c := s.client
	s.client = nil // a later use starts a fresh DevPrism
	return c.Close()
}
