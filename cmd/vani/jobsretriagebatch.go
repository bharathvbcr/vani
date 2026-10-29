package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani jobs retriage --batch KEY...` is the manual Sonnet reading for many
// postings at once: the same question, model, store, cap and Rule 7 as one
// (jobsretriage.go), with the ledger read once, the ask lock held for the whole
// batch and a few postings asked at a time.
//
// --dry-run answers one JSON object (retriageBatchPlan): each key's state and
// what asking would cost, for every eligible posting and for those Sonnet has no
// current reading of. A real run writes one JSON object per line as it goes
// (retriageBatchEvent: a start, an item per posting asked, and a done carrying
// the totals and why it stopped early, if it did), so the Mac app can show
// progress and keep what landed when the owner stops it.
//
// A posting that cannot be read (unknown, text gone, never triaged) is skipped
// before anything is paid. A failure about one posting (refused, unsure,
// unreadable reply, a provider error) moves on to the next; one that would fail
// every posting the same way (no key, the cap reached, the store or the config)
// stops the batch, and so do retriageBatchProviderStop provider failures in a
// row. Readings already kept stay kept, whatever stops it.

const (
	// maxRetriageBatch bounds one batch. It is defaultJobsLimit, the most rows
	// the Jobs list loads, so the whole list is one batch; it also bounds the
	// argv, the fan-out and the reply. The app's ⌘A asks for the first 100,
	// 250, 500 or 1000 of that list instead of selecting every row.
	maxRetriageBatch = 1000
	// defaultRetriageParallel and maxRetriageParallel bound how many postings
	// are asked at once.
	defaultRetriageParallel = 3
	maxRetriageParallel     = 8
	// retriageBatchProviderStop provider failures in a row stop a batch: the
	// provider is down, and every later posting would wait out its timeout.
	retriageBatchProviderStop = 3
	// rtAlreadyRead is the skip code of a posting with a current manual reading,
	// under --skip-read.
	rtAlreadyRead = "already_read"
)

// retriageBatchItem is one key's state before anything is asked. Skip names why
// it will not be asked (a code of retriageErrorReply); Read is "current" or
// "stale" when Sonnet has read the posting before.
type retriageBatchItem struct {
	Key    string `json:"key"`
	Skip   string `json:"skip,omitempty"`
	Reason string `json:"reason,omitempty"`
	Read   string `json:"read,omitempty"`
}

// retriageBatchPlan is --batch --dry-run's reply.
type retriageBatchPlan struct {
	Batch  bool                `json:"batch"`
	DryRun bool                `json:"dry_run"`
	Model  string              `json:"model"`
	Items  []retriageBatchItem `json:"items"`
	// Eligible is how many keys can be asked; AlreadyRead how many of those
	// Sonnet already has a current reading of.
	Eligible    int `json:"eligible"`
	AlreadyRead int `json:"already_read"`
	// Estimate is asking every eligible posting; EstimateUnread only those with
	// no current reading (what --skip-read asks).
	Estimate       *retriageEstimate `json:"estimate"`
	EstimateUnread *retriageEstimate `json:"estimate_unread"`
	MaxKeys        int               `json:"max_keys"`
}

// retriageBatchSummary is a batch's totals. Kept + Failed + NotAsked is the
// number the start event said would be asked.
type retriageBatchSummary struct {
	Kept     int `json:"kept"`
	Failed   int `json:"failed"`
	NotAsked int `json:"not_asked"`
	Skipped  int `json:"skipped"`
	// Changed is how many kept readings changed role_fit from what ranking used.
	Changed int     `json:"changed"`
	USD     float64 `json:"usd"`
	// Stopped is the code that ended the batch early, Message its words; both
	// absent when every posting was asked.
	Stopped      string   `json:"stopped,omitempty"`
	Message      string   `json:"message,omitempty"`
	NotAskedKeys []string `json:"not_asked_keys,omitempty"`
}

// retriageBatchEvent is one line of a batch run.
type retriageBatchEvent struct {
	Event string `json:"event"` // "start", "item" or "done"
	// start
	Model    string              `json:"model,omitempty"`
	Total    int                 `json:"total,omitempty"`
	Parallel int                 `json:"parallel,omitempty"`
	Skipped  []retriageBatchItem `json:"skipped,omitempty"`
	// item: Result on success, else Error and Code (and Cost when a paid call
	// completed).
	Key    string         `json:"key,omitempty"`
	Result *retriageReply `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
	Code   string         `json:"code,omitempty"`
	Cost   *retriageCost  `json:"cost,omitempty"`
	// done
	Summary *retriageBatchSummary `json:"summary,omitempty"`
}

// retriageBatchFatal are the codes that would fail every later posting the
// same way.
var retriageBatchFatal = map[string]bool{rtNoKey: true, rtCapReached: true, rtConfig: true, rtStore: true, rtLedger: true, rtBusy: true}

func (e *env) jobsRetriageBatch(ctx context.Context, raw []string, dry, skipRead bool, parallel int, asJSON bool) error {
	out := &batchOut{w: e.stdout, json: asJSON}
	keys, err := batchKeys(raw, parallel)
	if err != nil {
		return out.fail(err)
	}
	if dry {
		plan, err := e.retriageBatchPlan(ctx, keys)
		if err != nil {
			return out.fail(err)
		}
		out.plan(plan)
		return out.err
	}
	sum, err := e.retriageBatchRun(ctx, keys, skipRead, parallel, out)
	if err != nil {
		return out.fail(err)
	}
	if out.err != nil {
		return out.err
	}
	if sum.Stopped != "" {
		if asJSON {
			return errAlreadyReported
		}
		return fmt.Errorf("jobs retriage --batch: stopped early: %s", sum.Message)
	}
	return nil
}

// batchKeys checks the keys and the fan-out, and drops repeated keys (the first
// keeps its place).
func batchKeys(raw []string, parallel int) ([]posting.Key, error) {
	if len(raw) == 0 {
		return nil, usagef("jobs retriage --batch: expected at least one posting key")
	}
	if len(raw) > maxRetriageBatch {
		return nil, usagef("jobs retriage --batch: %d keys is more than one batch takes (%d)", len(raw), maxRetriageBatch)
	}
	if parallel < 1 || parallel > maxRetriageParallel {
		return nil, usagef("jobs retriage --batch: --parallel must be 1..%d", maxRetriageParallel)
	}
	keys := make([]posting.Key, 0, len(raw))
	seen := make(map[posting.Key]bool, len(raw))
	for _, s := range raw {
		k, err := checkJobKey(s)
		if err != nil {
			return nil, err
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// retriageBatchState is what a batch reads before it asks: the snapshot (each
// key's company read whole), the marks, the manual readings and each key's
// record or why it will not be asked.
type retriageBatchState struct {
	snap   ledger.Snapshot
	marks  map[posting.Key]marks.Entry
	manual *manualReadingSet
	items  []retriageBatchItem
	recs   map[posting.Key]ledger.Record
}

func (e *env) retriageBatchState(keys []posting.Key) (*retriageBatchState, error) {
	snap, m, err := e.jobsStateFor(batchScope(keys))
	if err != nil {
		return nil, rtErr(rtLedger, "%v", err)
	}
	set, _, warn := manualReadings(e.stateDir)
	if warn != "" {
		return nil, rtErr(rtStore, "%s", warn)
	}
	if set == nil {
		set = &manualReadingSet{m: map[posting.Key]manualtriage.Reading{}}
	}
	st := &retriageBatchState{snap: snap, marks: m, manual: set, recs: make(map[posting.Key]ledger.Record, len(keys))}
	st.items = make([]retriageBatchItem, len(keys))
	for i, k := range keys {
		it := retriageBatchItem{Key: string(k)}
		r, ok := findRecord(snap, k)
		switch {
		case !ok:
			it.Skip, it.Reason = rtUnknownKey, "no such posting in the ledger"
		case r.BodyEvicted || strings.TrimSpace(r.Posting.Description) == "":
			it.Skip, it.Reason = rtTextGone, "the ledger holds no text for it"
		case r.Status == posting.Live:
			it.Skip, it.Reason = rtUntriaged, "no run has triaged it yet"
		default:
			st.recs[k] = r
			if _, s, had := set.state(r); had {
				it.Read = map[bool]string{true: "current", false: "stale"}[s == manualtriage.Current]
			}
		}
		st.items[i] = it
	}
	return st, nil
}

// batchScope is rowScope for many keys: every record of each key's company is
// read whole, the rest lean. A key with no company reads every record whole.
func batchScope(keys []posting.Key) func(ledger.Snapshot) func(ledger.Record) bool {
	return func(lean ledger.Snapshot) func(ledger.Record) bool {
		cos := map[string]bool{}
		for _, k := range keys {
			r, ok := findRecord(lean, k)
			if !ok {
				continue
			}
			co := companyKey(r.Posting.Company)
			if co == "" {
				return func(ledger.Record) bool { return true }
			}
			cos[co] = true
		}
		if len(cos) == 0 {
			return nil
		}
		return func(x ledger.Record) bool { return cos[companyKey(x.Posting.Company)] }
	}
}

func (e *env) retriageBatchPlan(ctx context.Context, keys []posting.Key) (retriageBatchPlan, error) {
	st, err := e.retriageBatchState(keys)
	if err != nil {
		return retriageBatchPlan{}, err
	}
	ask, err := e.retriageSetup(ctx)
	if err != nil {
		return retriageBatchPlan{}, err
	}
	plan := retriageBatchPlan{Batch: true, DryRun: true, Model: ask.model, Items: st.items, MaxKeys: maxRetriageBatch}
	var all, unread []retriagePrompt
	for _, it := range st.items {
		if it.Skip != "" {
			continue
		}
		p, err := ask.question(ctx, st.recs[posting.Key(it.Key)])
		if err != nil {
			return retriageBatchPlan{}, rtErr(rtConfig, "%v", err)
		}
		plan.Eligible++
		all = append(all, p)
		if it.Read == "current" {
			plan.AlreadyRead++
		} else {
			unread = append(unread, p)
		}
	}
	plan.Estimate, plan.EstimateUnread = ask.estimate(ctx, all), ask.estimate(ctx, unread)
	return plan, nil
}

func (e *env) retriageBatchRun(ctx context.Context, keys []posting.Key, skipRead bool, parallel int, out *batchOut) (retriageBatchSummary, error) {
	st, err := e.retriageBatchState(keys)
	if err != nil {
		return retriageBatchSummary{}, err
	}
	ask, err := e.retriageSetup(ctx)
	if err != nil {
		return retriageBatchSummary{}, err
	}
	var todo []ledger.Record
	var skipped []retriageBatchItem
	for i, it := range st.items {
		if it.Skip == "" && skipRead && it.Read == "current" {
			it.Skip, it.Reason = rtAlreadyRead, "Sonnet already has a current reading of it"
			st.items[i] = it
		}
		if it.Skip != "" {
			skipped = append(skipped, it)
			continue
		}
		todo = append(todo, st.recs[posting.Key(it.Key)])
	}
	prompts := make([]retriagePrompt, len(todo))
	for i, r := range todo {
		if prompts[i], err = ask.question(ctx, r); err != nil {
			return retriageBatchSummary{}, rtErr(rtConfig, "%v", err)
		}
	}
	dir := manualtriage.Dir(e.stateDir)
	sum := retriageBatchSummary{Skipped: len(skipped)}
	if len(todo) == 0 {
		out.event(retriageBatchEvent{Event: "start", Model: ask.model, Parallel: parallel, Skipped: skipped})
		out.event(retriageBatchEvent{Event: "done", Summary: &sum})
		return sum, nil
	}

	unlock, err := manualtriage.TryAskLock(dir)
	if errors.Is(err, manualtriage.ErrBusy) {
		return retriageBatchSummary{}, rtErr(rtBusy, "jobs retriage: another manual reading is being asked right now; try again when it finishes")
	}
	if err != nil {
		return retriageBatchSummary{}, storeErr(err, nil)
	}
	defer unlock()
	pctx, cancel := context.WithTimeout(ctx, retriageAskTimeout)
	p, err := retriageProvider(pctx, ask.w)
	cancel()
	if err != nil {
		return retriageBatchSummary{}, mapProviderErr(pctx, err, "building the provider")
	}

	b := &retriageBatch{e: e, ask: ask, st: st, dir: dir, p: p, insights: map[string]*insight{}}
	out.event(retriageBatchEvent{Event: "start", Model: ask.model, Total: len(todo), Parallel: parallel, Skipped: skipped})

	jobs := make(chan int)
	asked := make([]bool, len(todo))
	var wg sync.WaitGroup
	for range min(parallel, len(todo)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				ev, ok := b.one(ctx, todo[i], prompts[i])
				if !ok {
					continue // stopped by the owner: not asked
				}
				b.mu.Lock()
				asked[i] = true
				b.tally(&sum, ev)
				b.mu.Unlock()
				out.event(ev)
			}
		}()
	}
dispatch:
	for i := range todo {
		if b.stopped() != "" {
			break
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()

	if ctx.Err() != nil && b.stop == "" {
		b.stop, b.stopMsg = rtCancelled, "stopped; the readings already made are kept"
	}
	for i, r := range todo {
		if !asked[i] {
			sum.NotAsked++
			sum.NotAskedKeys = append(sum.NotAskedKeys, string(r.Key))
		}
	}
	if sum.NotAsked > 0 {
		sum.Stopped, sum.Message = b.stop, b.stopMsg
	}
	out.event(retriageBatchEvent{Event: "done", Summary: &sum})
	return sum, nil
}

// retriageBatch is a run's shared state. mu guards the insights, the manual
// readings they rank with, the stop and the summary.
type retriageBatch struct {
	e                *env
	ask              *retriageAsker
	st               *retriageBatchState
	dir              string
	p                llm.Provider
	mu               sync.Mutex
	insights         map[string]*insight
	stop, stopMsg    string
	providerFailures int
}

func (b *retriageBatch) stopped() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stop
}

// insightFor is the rank context of r's company, built once per company and
// ranking with the batch's own copy of the manual readings, so a reading made
// in this batch counts in the next side read. Called with mu held.
func (b *retriageBatch) insightFor(r ledger.Record) (*insight, error) {
	co := companyKey(r.Posting.Company)
	if in, ok := b.insights[co]; ok {
		return in, nil
	}
	in, err := newInsightScoped(b.ask.cfg, nil, b.e.stateDir, jobinsight.DefaultScoreHalfLifeDays, b.st.snap, b.st.marks, co)
	if err != nil {
		return nil, err
	}
	in.manual, in.rc.Manual = b.st.manual, b.st.manual.value
	b.insights[co] = in
	return in, nil
}

// side is sideOf under mu.
func (b *retriageBatch) side(r ledger.Record) (*retriageSide, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	in, err := b.insightFor(r)
	if err != nil {
		return nil, err
	}
	return sideOf(in, r), nil
}

// one asks about r and says what happened; ok is false when the owner stopped
// the batch, or it stopped itself, before r was asked: that is not an item.
func (b *retriageBatch) one(ctx context.Context, r ledger.Record, pr retriagePrompt) (retriageBatchEvent, bool) {
	ev := retriageBatchEvent{Event: "item", Key: string(r.Key)}
	// Handed over before the stop was seen: not asked, and not paid for.
	if b.stopped() != "" || ctx.Err() != nil {
		return ev, false
	}
	before, err := b.side(r)
	if err != nil {
		ev.Error, ev.Code = err.Error(), rtLedger
		b.setStop(rtLedger, err.Error())
		return ev, true
	}
	actx, cancel := context.WithTimeout(ctx, retriageAskTimeout)
	defer cancel()
	rd, cost, re := b.ask.read(actx, b.p, b.dir, r, pr)
	if re != nil {
		if re.code == rtCancelled {
			return ev, false
		}
		ev.Error, ev.Code, ev.Cost = re.msg, re.code, re.cost
		b.noteFailure(re)
		return ev, true
	}
	b.mu.Lock()
	b.st.manual.m[r.Key] = rd
	b.providerFailures = 0
	b.mu.Unlock()
	after, err := b.side(r)
	if err != nil {
		ev.Error, ev.Code, ev.Cost = err.Error(), rtLedger, cost
		b.setStop(rtLedger, err.Error())
		return ev, true
	}
	after.Model, after.At = b.ask.model, &rd.At
	ev.Result = &retriageReply{Key: string(r.Key), Model: b.ask.model, Status: r.Status, Before: before, After: after, Cost: cost}
	return ev, true
}

func (b *retriageBatch) setStop(code, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stop == "" {
		b.stop, b.stopMsg = code, msg
	}
}

// noteFailure stops the batch on a failure every later posting would share,
// and on retriageBatchProviderStop provider failures in a row.
func (b *retriageBatch) noteFailure(re *retriageError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if re.code == rtProvider {
		b.providerFailures++
	} else {
		b.providerFailures = 0
	}
	switch {
	case b.stop != "":
	case retriageBatchFatal[re.code]:
		b.stop, b.stopMsg = re.code, re.msg
	case re.code == rtProvider && b.providerFailures >= retriageBatchProviderStop:
		b.stop, b.stopMsg = rtProvider, fmt.Sprintf("%d provider failures in a row: %s", b.providerFailures, re.msg)
	}
}

// tally adds an item to the summary. Called with mu held.
func (b *retriageBatch) tally(sum *retriageBatchSummary, ev retriageBatchEvent) {
	if ev.Cost != nil {
		sum.USD += ev.Cost.USD
	}
	if ev.Result == nil {
		sum.Failed++
		return
	}
	sum.Kept++
	if ev.Result.Cost != nil {
		sum.USD += ev.Result.Cost.USD
	}
	if ev.Result.Before.Slots["role_fit"].Value != ev.Result.After.Slots["role_fit"].Value {
		sum.Changed++
	}
}

// batchOut writes a batch's replies: JSON lines, or words for a person. err is
// the first write that failed.
type batchOut struct {
	mu   sync.Mutex
	w    io.Writer
	json bool
	err  error
}

func (o *batchOut) encode(v any) {
	if err := writeJSON(o.w, v); err != nil && o.err == nil {
		o.err = err
	}
}

func (o *batchOut) fail(err error) error {
	if !o.json {
		return err
	}
	rep := retriageErrorReply{Error: err.Error(), Code: rtConfig}
	var re *retriageError
	if errors.As(err, &re) {
		rep.Code, rep.Cost = re.code, re.cost
	} else {
		var ue usageError
		if errors.As(err, &ue) {
			rep.Code = rtBadRequest
		}
	}
	o.mu.Lock()
	o.encode(rep)
	o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	return errAlreadyReported
}

func (o *batchOut) plan(p retriageBatchPlan) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.json {
		o.encode(p)
		return
	}
	est := p.Estimate
	fmt.Fprintf(o.w, "would ask %s about %d of %d postings (%d already read by it): typically about $%.4f, at most $%.4f (%s); daily cap $%.2f\n",
		p.Model, p.Eligible, len(p.Items), p.AlreadyRead, est.TypicalUSD, est.MaxUSD, est.PriceSource, est.DailyCapUSD)
	if p.AlreadyRead > 0 {
		fmt.Fprintf(o.w, "with --skip-read: %d postings, typically about $%.4f, at most $%.4f\n", p.Eligible-p.AlreadyRead, p.EstimateUnread.TypicalUSD, p.EstimateUnread.MaxUSD)
	}
	if est.RemainingUSD != nil {
		fmt.Fprintf(o.w, "$%.2f of today's cap is left", *est.RemainingUSD)
		if est.FitAtMax != nil {
			fmt.Fprintf(o.w, "; %d of them fit in it at their most", *est.FitAtMax)
		}
		fmt.Fprintln(o.w)
	}
	for _, it := range p.Items {
		if it.Skip != "" {
			fmt.Fprintf(o.w, "  skip %s: %s\n", it.Key, it.Reason)
		}
	}
}

func (o *batchOut) event(ev retriageBatchEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.json {
		o.encode(ev)
		return
	}
	switch ev.Event {
	case "start":
		fmt.Fprintf(o.w, "asking %s about %d postings, %d at a time\n", ev.Model, ev.Total, ev.Parallel)
		for _, it := range ev.Skipped {
			fmt.Fprintf(o.w, "  skip %s: %s\n", it.Key, it.Reason)
		}
	case "item":
		if r := ev.Result; r != nil {
			fmt.Fprintf(o.w, "  %s: role_fit %s -> %s; relevance %.1f -> %.1f (about $%.4f)\n", ev.Key,
				orDash(r.Before.Slots["role_fit"].Value), r.After.Slots["role_fit"].Value, r.Before.Relevance, r.After.Relevance, r.Cost.USD)
		} else {
			fmt.Fprintf(o.w, "  %s: failed (%s): %s\n", ev.Key, ev.Code, ev.Error)
		}
	case "done":
		s := ev.Summary
		fmt.Fprintf(o.w, "kept %d, failed %d, not asked %d, skipped %d; role_fit changed on %d; about $%.4f\n", s.Kept, s.Failed, s.NotAsked, s.Skipped, s.Changed, s.USD)
		if s.Stopped != "" {
			fmt.Fprintf(o.w, "stopped early (%s): %s\n", s.Stopped, s.Message)
		}
		fmt.Fprintln(o.w, "This only changes the order: a manual reading never drops a posting or brings a dropped one back.")
	}
}
