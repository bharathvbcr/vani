package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/inbox"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/localserver"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/amazon"
	"github.com/bharathvbcr/Vani/internal/sources/apple"
	"github.com/bharathvbcr/Vani/internal/sources/ashby"
	"github.com/bharathvbcr/Vani/internal/sources/atlassian"
	"github.com/bharathvbcr/Vani/internal/sources/avature"
	"github.com/bharathvbcr/Vani/internal/sources/bamboohr"
	"github.com/bharathvbcr/Vani/internal/sources/deshaw"
	"github.com/bharathvbcr/Vani/internal/sources/eightfold"
	"github.com/bharathvbcr/Vani/internal/sources/feed"
	"github.com/bharathvbcr/Vani/internal/sources/gem"
	"github.com/bharathvbcr/Vani/internal/sources/google"
	"github.com/bharathvbcr/Vani/internal/sources/greenhouse"
	"github.com/bharathvbcr/Vani/internal/sources/ibm"
	"github.com/bharathvbcr/Vani/internal/sources/jibe"
	"github.com/bharathvbcr/Vani/internal/sources/lever"
	"github.com/bharathvbcr/Vani/internal/sources/oraclehcm"
	"github.com/bharathvbcr/Vani/internal/sources/personio"
	"github.com/bharathvbcr/Vani/internal/sources/pinpoint"
	"github.com/bharathvbcr/Vani/internal/sources/recruitee"
	"github.com/bharathvbcr/Vani/internal/sources/rippling"
	"github.com/bharathvbcr/Vani/internal/sources/smartrecruiters"
	"github.com/bharathvbcr/Vani/internal/sources/talentbrew"
	"github.com/bharathvbcr/Vani/internal/sources/tiktok"
	"github.com/bharathvbcr/Vani/internal/sources/workable"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
)

// advanceKind names who asks for the advance phase. The callers share one
// engine (pipeline.Advance); what differs between them is the AdvancePlan
// advancePlan builds, so a difference is a named field set in one place.
type advanceKind int

const (
	kindRun     advanceKind = iota // the daily run (runOnce)
	kindInbox                      // the daemon's between-runs inbox poll (pollInbox)
	kindRescore                    // `vani rescore` (rescoreWith)
)

// baseAdvancePlan is the plan every caller starts from: the cap and
// concurrency it was given and the fixed differences of its kind.
//
//   - kindRun and kindInbox fill the funnel counts (fail-open and notes gate) and
//     warn when a check could not run; the run keeps them in its record.
//   - kindRescore advances exactly the rescorable records and never prints the
//     advance_per_run cap line (its cap is --limit).
func baseAdvancePlan(kind advanceKind, limit, workers int, warn func(string, ...any)) pipeline.AdvancePlan {
	p := pipeline.AdvancePlan{
		Limit: limit, Workers: workers, Warn: warn,
		Order: func(todo, all []ledger.Record) { orderAdvance(todo, all, nil) },
	}
	switch kind {
	case kindRun, kindInbox:
		p.FillFunnel = true
	case kindRescore:
		p.Select, p.QuietCap = rescorable, true
	}
	return p
}

// advancePlan is the plan for a run or an inbox poll over what the wired app
// holds; policy is the runner's drop policy (its title rules). The differences
// between the two are all here:
//
//	field       kindRun                          kindInbox
//	Limit       the tuned cap, else advance_per_run   advance_per_run (a tune is not read)
//	Skip        the tuned --list-only            never
//	Order       title rank, then the semantic    title rank only
//	            knowledge-base order when on
//	            US first: the tuned --us-first   US first: advance_us_first
//	            or advance_us_first
//	FillFunnel  yes (the run record + warnings)    yes (warnings; it keeps no record)
func (w *wired) advancePlan(kind advanceKind, policy pipeline.DropPolicy) pipeline.AdvancePlan {
	w.mu.Lock()
	limit, workers := w.cfg.AdvanceLimit(), w.cfg.AdvanceWorkers()
	capOverride, listOnly := w.tuned.Advance, w.tuned.ListOnly
	usFirst := withUSFirst(w.tuned.USFirst || w.cfg.AdvanceUSFirst)
	inboxUSFirst := withUSFirst(w.cfg.AdvanceUSFirst)
	tuned := w.tuned
	w.mu.Unlock()
	rank := w.titleRank()
	p := baseAdvancePlan(kind, limit, workers, w.warnf)
	p.TitleDrop = policy.PrefilterDropper()
	switch kind {
	case kindRun:
		if capOverride > 0 {
			p.Limit = capOverride
		}
		p.Skip = listOnly
		// The semantic order reads the cache the warm before the advance phase
		// fills, so it is resolved when the phase orders, not when it is planned.
		p.Order = func(todo, all []ledger.Record) { orderAdvance(todo, all, rank, w.advanceOrder(), usFirst) }
		scopeAdvance(&p, tuned, w.warnf)
	case kindInbox:
		// An inbox poll is not a run and takes no run options; the config's
		// advance_us_first orders it all the same.
		p.Order = func(todo, all []ledger.Record) { orderAdvance(todo, all, rank, inboxUSFirst) }
	}
	// Counts only. The callback has no posting key; an inbox or rescore with
	// no live run id is a no-op inside noteLive's writer.
	p.Progress = func(attempted, limit, eligible int) {
		w.noteLive(runProgress{Stage: progressAdvance, Attempted: attempted, Limit: limit, Eligible: eligible})
	}
	return p
}

// skipsAdvance reports whether the run being started is a --list-only one (the
// tuned override), which never asks a decision backend anything.
func (w *wired) skipsAdvance() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tuned.ListOnly
}

// runOnce lists every board, reconciles it, then advances postings. It fails
// as a whole only when no board could be read or the ledger fails; a single
// bad board is a warning, and its postings are left untouched (an unread
// board must never count as "every posting disappeared"). What happened to
// each board reaches the run record (listBoards). The order of the stages is
// pipeline.Run's; this assembles what each stage does.
func (w *wired) runOnce(ctx context.Context) error {
	var (
		d       *egress.Client
		runner  *pipeline.Runner
		protect func(posting.Key) bool
	)
	return pipeline.Run(ctx, pipeline.RunStages{
		Start: func(ctx context.Context) (func(), error) {
			w.noteLive(runProgress{Stage: progressPreparing})
			var stop func()
			var err error
			d, runner, stop, err = w.startRun(ctx, !w.skipsAdvance())
			return stop, err
		},
		Drain: func(ctx context.Context) {
			w.noteLive(runProgress{Stage: progressInbox})
			_, _ = w.drainSpool(ctx)
		},
		Boards: func(ctx context.Context) (pipeline.BoardCounts, error) {
			protect = w.markProtector()
			return w.readBoards(ctx, d, runner.Policy, protect)
		},
		NearDups: func(ctx context.Context) error {
			w.noteLive(runProgress{Stage: progressNearDup})
			_, err := w.annotateNearDups(ctx, protect, time.Now())
			return err
		},
		Exclusions: func(ctx context.Context) error {
			w.noteLive(runProgress{Stage: progressExclusions})
			return w.applyExclusions(ctx, runner.Policy, protect, time.Now(), runner.FailOpen)
		},
		Logos: func(ctx context.Context) {
			w.noteLive(runProgress{Stage: progressLogos})
			w.refreshLogos(ctx)
		},
		Plan: func() pipeline.AdvancePlan { return w.advancePlan(kindRun, runner.Policy) },
		Warm: func(ctx context.Context) {
			w.noteLive(runProgress{Stage: progressWarm})
			w.warmBeforeAdvance(ctx, w.led.All(), runner.Policy.PrefilterDropper())
		},
		Deps: func() pipeline.AdvanceDeps {
			return pipeline.AdvanceDeps{Records: w.led.All(), Advance: runner.Advance, Runner: runner}
		},
		Advanced: func(rep pipeline.AdvanceReport) {
			w.mu.Lock()
			w.advance = &rep
			w.mu.Unlock()
			w.warnShadowNotes(runner)
		},
		Backfill: func(ctx context.Context) {
			w.noteLive(runProgress{Stage: progressBackfill})
			w.escalateBackfill(ctx, runner)
		},
	})
}

// readBoards assembles the board sources of a run and lists and reconciles every
// configured board through them. The careers-page browser and its proxy live
// for this listing only. The board report is kept for the run record.
func (w *wired) readBoards(ctx context.Context, d *egress.Client, policy pipeline.DropPolicy, protect func(posting.Key) bool) (pipeline.BoardCounts, error) {
	srcs := apiSources(d)
	resolved := ""
	if w.stateDir != "" {
		// Completed pay checks (empty answer or 404). A skip is not recorded,
		// so the next run asks for it. Absent when this process has no state dir.
		resolved = filepath.Join(w.stateDir, "sources", "greenhouse-pay.json")
	}
	gh, err := greenhouse.NewWithPay(d, greenhouse.Pay{
		Known: func(k posting.Key) (posting.Posting, bool) {
			r, ok := w.led.Get(k)
			return r.Posting, ok
		},
		Report: func(b sources.Board, st greenhouse.PayStats) {
			w.warnf("board %s/%s: pay %+v", b.ATS, b.Token, st)
			// NotFound is a closed posting, not a fetch that failed.
			w.noteBoardFetch(b, boardFetch{PayFailed: st.Failed, PaySkipped: st.Skipped, PayError: st.FirstError})
		},
		Resolved: resolved,
		Warn:     w.warnf,
	})
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.Greenhouse] = gh
	ab, err := w.ashbySource(d, policy)
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.Ashby] = ab
	// Workday costs a detail request per posting: stored postings are reused
	// and new ones the title rule drops are never fetched, so a large board
	// spends at most workday.MaxDetails requests a run.
	wd, err := workday.NewWith(d, workday.Options{
		Stored:     w.storedFor,
		DropListed: policy.ListedDrops(),
		Report: func(b sources.Board, st workday.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Deferred: st.Deferred, Gone: st.Gone})
		},
	})
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.Workday] = wd
	// SmartRecruiters is the same shape: a detail request per posting, so the
	// same bounds (stored postings reused, listed rows prefiltered, at most
	// smartrecruiters.MaxDetails requests a board a run).
	sr, err := smartrecruiters.NewWith(d, smartrecruiters.Options{
		Stored:     w.storedFor,
		DropListed: policy.ListedDrops(),
		Report: func(b sources.Board, st smartrecruiters.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Deferred: st.Deferred, Gone: st.Gone})
		},
	})
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.SmartRecruiters] = sr
	// Oracle HCM costs a detail request per posting too, bounded the same way.
	oc, err := oraclehcm.NewWith(d, oraclehcm.Options{
		Stored: w.storedFor,
		Report: func(b sources.Board, st oraclehcm.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Deferred: st.Deferred, Gone: st.Gone})
		},
	})
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.OracleHCM] = oc
	// Rippling's list has no description either: the same bounds as
	// SmartRecruiters (stored postings reused, listed rows prefiltered, at most
	// rippling.MaxDetails requests a board a run).
	rp, err := rippling.NewWith(d, rippling.Options{
		Stored:     w.storedFor,
		DropListed: policy.ListedDrops(),
		Report: func(b sources.Board, st rippling.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Deferred: st.Deferred})
		},
	})
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.Rippling] = rp
	// A TalentBrew job page is up to 700 KB and the sitemap names nothing else,
	// so every posting costs one: bounded as Rippling's is, at
	// talentbrew.MaxDetails pages a board a run.
	tb, err := talentbrew.NewWith(d, talentbrew.Options{
		Stored:     w.storedFor,
		DropListed: policy.ListedDrops(),
		Report: func(b sources.Board, st talentbrew.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Deferred: st.Deferred, Gone: st.Gone})
		},
	})
	if err != nil {
		return pipeline.BoardCounts{}, err
	}
	srcs[posting.TalentBrew] = tb
	srcs[posting.Eightfold] = eightfold.NewWith(d, eightfold.Options{
		Report: func(b sources.Board, st eightfold.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Gone: st.Gone})
		},
	})
	// Avature: a detail page per posting, bounded like Workday's (stored
	// postings reused, listed rows prefiltered, at most avature.MaxDetails
	// requests a board a run).
	srcs[posting.Avature] = avature.NewWith(d, avature.Options{
		Stored:     w.storedFor,
		DropListed: policy.ListedDrops(),
		Report: func(b sources.Board, st avature.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Deferred: st.Deferred, Gone: st.Gone})
		},
	})
	srcs[posting.Apple] = apple.NewWith(d, apple.Options{
		Report: func(b sources.Board, st apple.Stats) {
			w.warnf("board %s/%s: details %+v", b.ATS, b.Token, st)
			w.noteBoardFetch(b, boardFetch{Gone: st.Gone})
		},
	})
	var pageErr error
	if w.hasPageBoard() {
		src, stop, err := w.pageSource(ctx, d)
		if err != nil {
			// Page boards are skipped with a warning below; API boards still run.
			w.warnf("careers-page boards disabled: %v", err)
			pageErr = err
		} else {
			defer stop()
			srcs[posting.Page] = src
		}
	}

	// One index of the ledger for the whole loop: only Reconcile writes to the
	// ledger here (the adapters read it), as Reconciler requires.
	rec := pipeline.NewReconciler(w.led)
	rec.Protect(protect)
	w.noteLive(runProgress{Stage: progressListing, Total: len(w.cfg.Watchlist)})
	ctx = withListingTick(ctx, func(tk listingTick) {
		w.noteLive(runProgress{
			Stage: progressListing, Phase: tk.Phase, Index: tk.Index, Total: tk.Total,
			Done: tk.Done, Read: tk.Read, Failed: tk.Failed, RateLimited: tk.RateLimited,
			Skipped: tk.Skipped, Listed: tk.Listed,
			ATS: string(tk.Board.ATS), Token: tk.Board.Token, Company: tk.Board.Company,
			Outcome: tk.Outcome,
		})
	})
	boards, err := listBoards(ctx, w.cfg.Watchlist, srcs, pageErr, rec.Reconcile, w.warnf, filepath.Join(w.stateDir, "hosts", "cooldown.json"))
	w.mu.Lock()
	w.applyFetchesLocked(&boards)
	w.boards = &boards
	w.mu.Unlock()
	return pipeline.BoardCounts{Configured: boards.Configured, Read: boards.Read}, err
}

// annotateNearDups runs the near-duplicate stage over the ledger, with the
// embedding cosine as its optional second opinion (nearDupSimilarity). The
// shingle rules decide every status; the cosine is only an annotation, and
// without it the stage is the shingle stage alone.
func (w *wired) annotateNearDups(ctx context.Context, protect func(posting.Key) bool, now time.Time) (pipeline.NearDupReport, error) {
	sim, finish := w.nearDupSimilarity(ctx)
	nd, err := pipeline.AnnotateNearDuplicates(ctx, w.led, now, pipeline.NearDupOptions{Protected: protect, Similarity: sim})
	finish() // saves the vector cache once, whatever the stage returned
	if err != nil {
		err = fmt.Errorf("near-duplicates: %w", err)
	}
	s := summarizeNearDup(nd, err)
	w.finishSimilarity(s)
	w.mu.Lock()
	w.nearDup = s
	w.mu.Unlock()
	if err != nil {
		return nd, err
	}
	w.warnf("near-duplicates: %+v", nd)
	return nd, nil
}

// applyExclusions drops the kept postings the configured exclusions now match
// and reopens the excluded ones they no longer match, before anything is
// advanced, so editing exclude takes effect on the whole ledger at the next
// run. Marked postings are never dropped. A Live posting a location rule could
// not read is kept and counted on fo (pipeline.CauseLocationUnreadable).
func (w *wired) applyExclusions(ctx context.Context, p pipeline.DropPolicy, protect func(posting.Key) bool, now time.Time, fo *pipeline.FailOpen) error {
	ch, err := pipeline.ApplyExclusions(ctx, w.led, now, p, protect, fo)
	w.mu.Lock()
	w.exclusions = summarizeExclusions(ch)
	w.mu.Unlock()
	if err != nil {
		return fmt.Errorf("exclusions: %w", err)
	}
	if ch.BodyUnavailable > 0 {
		w.warnf("exclusions: %d posting(s) left as they are because their stored description could not be read back", ch.BodyUnavailable)
	}
	if len(ch.Drop) > 0 || len(ch.Reopen) > 0 {
		w.warnf("exclusions: dropped %d posting(s), returned %d to live", len(ch.Drop), len(ch.Reopen))
	}
	return nil
}

// startRun readies what one pass over the ledger needs to triage: the egress
// client, the decision stages and, when configured, the local decision model's
// server. stop ends the server; call it when the pass is done. advance says
// whether the pass will ask a decision backend anything: one that will fails
// here, before any board is read, when none can be built (runnerFor); a
// --list-only run does not need one.
func (w *wired) startRun(ctx context.Context, advance bool) (*egress.Client, *pipeline.Runner, func(), error) {
	d, err := egress.New(egress.Config{AllowedHosts: w.cfg.AllowedHosts, MaxBodyBytes: maxBoardBytes})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("egress: %w", err)
	}
	// A local decision model configured with a server is started for this run
	// only, and only when the run will ask a backend something: a list-only
	// run (advance false) never does, and loading a model for it costs seconds
	// and gigabytes for nothing. If it does not come up, the run goes on without
	// the shadow. The endpoint lives exactly as long as its server: a daemon
	// reuses w, and a failed start must never inherit the last run's stopped
	// address.
	w.localEndpoint = ""
	errs := newBackendErrors()
	w.mu.Lock()
	w.backendErrs, w.boards, w.advance, w.semPre, w.backfill = errs, nil, nil, nil, nil
	w.nearDup, w.exclusions, w.logos, w.shadow = nil, nil, nil, nil
	w.explain, w.fetches = nil, nil
	w.simOff, w.simNote = "", ""
	// Written here, before any board is read and before runnerFor asks a
	// backend. noteExplain and noteShadow read it from that goroutine.
	w.recordGaps = advance
	// A new run retries a Keychain read that failed in the last one (keychain.go).
	w.keychainRun++
	// This run's llm budget file is today's UTC day. The daemon keeps one
	// wired across days; leaving the day where the first run pinned it bills
	// every later run against that file. budgetLedgerPath will not move it
	// again until the next run, so a pass that crosses UTC midnight stays on
	// one ledger.
	w.day = budgetClock().UTC().Format("2006-01-02")
	w.mu.Unlock()
	stop := func() {}
	if l := w.cfg.Decide.Local; advance && l != nil && l.Server != nil {
		srv, err := localserver.Start(ctx, localserver.Config{
			Binary: l.Server.Binary, Model: l.Server.ModelPath, ContextSize: l.Server.ContextSize,
			StartTimeout: time.Duration(l.Server.StartTimeoutMS) * time.Millisecond,
			BatchSize:    l.Server.BatchSize, Parallel: l.Server.Parallel,
		})
		if err != nil {
			w.warnf("local decision model not started; running without the shadow: %v", err)
			w.mu.Lock()
			w.shadow = &shadowNote{Local: oneLine(err.Error(), maxStageNote)}
			w.mu.Unlock()
		} else {
			stop = func() { w.localEndpoint = ""; srv.Stop() }
			w.localEndpoint = srv.Endpoint
		}
	}
	runner, err := w.runnerFor(ctx, d, advance)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	return d, runner, stop, nil
}

// drainSpool folds the spooled extension batches into the ledger and reports
// what it did. A failure is a warning: the batch stays spooled and is folded
// next time, and the run's boards do not depend on it. It returns what the
// drain left Live, for a caller that advances them itself.
func (w *wired) drainSpool(ctx context.Context) (drainResult, error) {
	res, err := drainInbox(ctx, w.led, w.stateDir, w.markProtector())
	if res.Report.Folded > 0 {
		w.warnf("inbox: folded %d batch(es), %d job(s): %d new, %d duplicate", res.Report.Folded, res.Report.Jobs, res.New, res.Duplicate)
	}
	if n := len(res.Report.Bad); n > 0 {
		w.warnf("inbox: %d unreadable file(s) moved to %s; they are not retried", n, filepath.Join(w.stateDir, inbox.Dir, inbox.BadDir))
	}
	if res.Report.Unreadable > 0 {
		w.warnf("inbox: %d file(s) could not be read; they stay for the next drain", res.Report.Unreadable)
	}
	if res.Report.Remaining > 0 {
		w.warnf("inbox: %d more file(s) wait for the next drain", res.Report.Remaining)
	}
	if err != nil && ctx.Err() == nil {
		w.warnf("inbox: drain stopped, the rest waits: %v", err)
	}
	return res, err
}

// pollInbox is the daemon's between-runs pass: fold what the extension
// spooled and advance the postings that landed Live, bounded by
// advance_per_run, through the same pipeline.Advance the daily run uses. The caller
// serialises it with the daily run (one ledger writer at a time).
func (w *wired) pollInbox(ctx context.Context) (err error) {
	st := inboxStatus{At: now().UTC()}
	defer func() {
		if werr := writeInboxStatus(w.stateDir, st); werr != nil {
			w.warnf("inbox: could not record the poll: %s", oneLine(werr.Error(), maxStageNote))
		}
	}()
	n, err := inbox.Pending(filepath.Join(w.stateDir, inbox.Dir))
	if err != nil {
		st.Error = err.Error()
		return err
	}
	if n == 0 {
		return nil
	}
	res, derr := w.drainSpool(ctx)
	st.Folded, st.Bad, st.Unreadable = res.Report.Folded, len(res.Report.Bad), res.Report.Unreadable
	st.Remaining, st.New, st.Duplicate, st.Live = res.Report.Remaining, res.New, res.Duplicate, len(res.Live)
	if derr != nil && ctx.Err() == nil {
		st.Error = derr.Error()
	}
	if len(res.Live) == 0 {
		if ctx.Err() != nil && st.Error == "" {
			st.Error = ctx.Err().Error()
		}
		return ctx.Err()
	}
	recs := make([]ledger.Record, 0, len(res.Live))
	for _, k := range res.Live {
		if r, ok := w.led.Get(k); ok {
			recs = append(recs, r)
		}
	}
	if testHookAdvanceNew != nil {
		err = testHookAdvanceNew(ctx, recs)
		if err != nil {
			st.Error = err.Error()
		}
		return err
	}
	_, runner, stop, err := w.startRun(ctx, true)
	if err != nil {
		// No backend, no egress: the postings stay Live for the next run.
		w.warnf("inbox: %d new job(s) wait for the next run: %v", len(recs), err)
		st.Error = err.Error()
		return nil
	}
	defer stop()
	_, err = pipeline.Advance(ctx, w.advancePlan(kindInbox, runner.Policy), pipeline.AdvanceDeps{Records: recs, Advance: runner.Advance, Runner: runner})
	w.warnShadowNotes(runner)
	if err != nil {
		st.Error = err.Error()
	}
	return err
}

// testHookAdvanceNew, set only by tests, replaces pollInbox's advance phase
// with a function that receives the records it would have advanced.
var testHookAdvanceNew func(ctx context.Context, recs []ledger.Record) error

// markProtector says which postings the user marked, which dedupe must never
// demote to Duplicate (protectMarked).
func (w *wired) markProtector() func(posting.Key) bool { return protectMarked(w.stateDir, w.warnf) }

// storedFor is every posting the ledger holds under b, whatever its status:
// what an adapter may reuse instead of fetching again.
func (w *wired) storedFor(b sources.Board) []posting.Posting {
	prefix := b.KeyPrefix()
	var out []posting.Posting
	for _, r := range w.led.All() {
		if strings.HasPrefix(string(r.Key), prefix) {
			// A closed record's text is not resident; the adapter reuses it, so read it back.
			out = append(out, ledger.Full(w.led, r).Posting)
		}
	}
	return out
}

// ashbySource is the Ashby adapter with the application-form read. The posting
// API has no form; the job page does, at a request per posting. A stored form is
// reused while a posting's text is unchanged, a posting the policy drops is
// never asked, and a board spends at most ashby.MaxFormsPerBoard requests a run.
func (w *wired) ashbySource(d egress.Doer, policy pipeline.DropPolicy) (sources.Source, error) {
	return ashby.NewWithForms(d, ashby.FormOptions{
		Known: func(k posting.Key) (posting.Posting, bool) {
			r, ok := w.led.Get(k)
			return r.Posting, ok
		},
		Skip: policy.ListedDrops(),
		Report: func(b sources.Board, st ashby.FormStats) {
			w.warnf("board %s/%s: forms %+v", b.ATS, b.Token, st)
		},
	})
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
		posting.Greenhouse:      greenhouse.New(d),
		posting.Lever:           lever.New(d),
		posting.Ashby:           ashby.New(d),
		posting.Workday:         workday.New(d),
		posting.Amazon:          amazon.New(d),
		posting.Google:          google.New(d),
		posting.Apple:           apple.New(d),
		posting.Eightfold:       eightfold.New(d),
		posting.Jibe:            jibe.New(d),
		posting.Gem:             gem.New(d),
		posting.SmartRecruiters: smartrecruiters.New(d),
		posting.IBM:             ibm.New(d),
		posting.Avature:         avature.New(d),
		posting.DEShaw:          deshaw.New(d),
		posting.Atlassian:       atlassian.New(d),
		posting.OracleHCM:       oraclehcm.New(d),
		posting.Rippling:        rippling.New(d),
		posting.Workable:        workable.New(d),
		posting.Recruitee:       recruitee.New(d),
		posting.Pinpoint:        pinpoint.New(d),
		posting.BambooHR:        bamboohr.New(d),
		posting.Personio:        personio.New(d),
		posting.TikTok:          tiktok.New(d),
		posting.TalentBrew:      talentbrew.New(d),
		posting.Feed:            feed.New(d),
	}
}
