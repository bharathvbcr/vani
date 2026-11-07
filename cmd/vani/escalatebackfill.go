package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The escalation backfill gives the postings triaged before decide.escalate was on
// (or while the tier was down) the reading a new triage would get: the slots the
// ranking reads where the primary was unsure are asked of the escalation tier and
// recorded beside the primary's, with the annotations a triage writes. The ranking
// then takes the tier's reading of them (jobinsight.RankContext.SlotFor).
//
// It runs inside the daily run (pipeline.RunStages.Backfill), after the advance
// phase, so it is always under the daemon's hold on the ledger: there is no command
// that runs it alone, and `vani run-request` asks the daemon for a run, which is
// how the owner backfills sooner than the schedule. It shares the run's escalator
// and so its max_per_run: new postings' triage is escalated first, the backfill
// spends what is left. Its calls go through the same metered provider as the
// triage's, so the llm daily cap bounds it; a refused or unreachable call reads as
// an outage, is not recorded, and is asked again by a later run. Rule 7: it only
// annotates, never drops or changes a status.

// escalateBackfillTimeout bounds the whole backfill pass. A variable only so
// tests need not wait.
var escalateBackfillTimeout = 30 * time.Minute

// Why a backfill stopped before every candidate was asked (escalateBackfillReport.Stopped).
const (
	backfillStopCap         = "cap"         // max_per_run was spent
	backfillStopUnavailable = "unavailable" // pipeline.MaxConsecutiveUnread postings in a row got no reading
	backfillStopLedger      = "ledger"      // a reading could not be written
	backfillStopCancelled   = "cancelled"
	backfillStopTimeout     = "timeout"
)

// escalateBackfillReport is what one backfill did, for the run record. Counts only:
// no posting key, text or reading value.
type escalateBackfillReport struct {
	// Candidates are the browsable postings with a slot to escalate; Slots how
	// many slots were sent to the tier; Answered how many it answered; Recorded
	// how many postings were written.
	Candidates int `json:"candidates"`
	Slots      int `json:"slots"`
	Answered   int `json:"answered"`
	Recorded   int `json:"recorded"`
	// Unavailable slots read as an outage (an unreachable tier, an exhausted
	// daily cap): they are not recorded and are asked again by a later run.
	Unavailable int `json:"unavailable,omitempty"`
	// Failed postings whose readings could not be written.
	Failed int `json:"failed,omitempty"`
	// Deferred slots were left for a later run because max_per_run was spent or
	// the pass stopped early.
	Deferred int    `json:"deferred,omitempty"`
	Stopped  string `json:"stopped,omitempty"`
	// Note says why the pass could not run, or ran on less than it should (marks
	// unreadable). Empty when it ran as asked.
	Note string `json:"note,omitempty"`
}

// backfillItem is one posting to ask the tier about and the slots to ask: asked maps
// each slot to the value of the primary's reading it is asked against.
type backfillItem struct {
	key   posting.Key
	post  posting.Posting
	slots []decide.Slot
	asked map[string]string
}

// backfillSlots are the slots the ranking reads: the fit and the slots its drop
// rules and rank penalties name (rankRules). Others are left to a triage.
func backfillSlots(cfg config.Config) map[string]bool {
	want := map[string]bool{"role_fit": true}
	rules, _ := rankRules(cfg)
	for _, r := range rules {
		want[r.Slot] = true
	}
	return want
}

// backfillItems are the postings of ordered (kept, best first) that have a wanted
// slot the primary read under belowP and the tier has not read (pipeline.ShadowPending).
// Order is kept; a posting with no such slot is left out.
func backfillItems(ordered []ledger.Record, belowP float64, want map[string]bool) []backfillItem {
	var out []backfillItem
	for _, r := range ordered {
		it := backfillItem{key: r.Key, post: r.Posting, asked: map[string]string{}}
		for _, spec := range jdtriage.Slots {
			if !want[spec.Name] {
				continue
			}
			s := jobinsight.SlotOf(r.Annotations, spec.Name)
			// An unusable p is not a low one (NaN fails the comparison).
			if !s.Answered || !s.HasP || !(s.P < belowP) || s.Value == "" {
				continue
			}
			if !pipeline.ShadowPending(r.Annotations, spec.Name, escalateName) {
				continue
			}
			opts := make([]string, len(spec.Options))
			for i, o := range spec.Options {
				opts[i] = o.Value
			}
			it.slots = append(it.slots, decide.Slot{Name: spec.Name, Options: opts})
			it.asked[spec.Name] = s.Value
		}
		if len(it.slots) > 0 {
			out = append(out, it)
		}
	}
	return out
}

// askFunc is a decide.Decider that answers from a closure.
type askFunc func(ctx context.Context, q decide.Question) (decide.Answer, error)

func (f askFunc) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	return f(ctx, q)
}

// escalatorOf is the run's escalator, or nil when decide.escalate is off or its
// tier could not be built this run.
func escalatorOf(r *pipeline.Runner) *escalator {
	if r == nil {
		return nil
	}
	t, ok := r.Triager.(*jdtriage.Triager)
	if !ok || t == nil {
		return nil
	}
	e, _ := t.D.(*escalator)
	return e
}

// escalateBackfill is the daily run's Backfill stage: it collects the candidates
// from the ledger and runs backfillPass. It never fails the run; what it did, or
// why it did not run, is in the report kept for the run record.
func (w *wired) escalateBackfill(ctx context.Context, runner *pipeline.Runner) {
	rep := w.runEscalateBackfill(ctx, runner)
	if rep == nil {
		return
	}
	w.mu.Lock()
	w.backfill = rep
	w.mu.Unlock()
	if rep.Note != "" {
		w.warnf("escalate backfill: %s", rep.Note)
	}
	if rep.Candidates > 0 || rep.Stopped != "" {
		w.warnf("escalate backfill: %d posting(s) with a slot to escalate; %d slot(s) asked, %d answered, %d posting(s) recorded, %d unavailable, %d failed, %d deferred%s",
			rep.Candidates, rep.Slots, rep.Answered, rep.Recorded, rep.Unavailable, rep.Failed, rep.Deferred, stoppedSuffix(rep.Stopped))
	}
}

func stoppedSuffix(s string) string {
	if s == "" {
		return ""
	}
	return " (stopped: " + s + ")"
}

func (w *wired) runEscalateBackfill(ctx context.Context, runner *pipeline.Runner) *escalateBackfillReport {
	w.mu.Lock()
	cfg := w.cfg
	w.mu.Unlock()
	if !cfg.Decide.EscalateEnabled() {
		return nil
	}
	if w.led == nil {
		return &escalateBackfillReport{Note: "not run: no ledger"}
	}
	esc := escalatorOf(runner)
	if esc == nil {
		return &escalateBackfillReport{Note: "not run: decide.escalate is on but the llm is disabled this run"}
	}
	var note string
	mk, err := marks.ReadMarks(marks.Dir(w.stateDir))
	if err != nil {
		// Kept postings are still read; only the marked ones that are not kept are not.
		note = "the Jobs marks could not be read: " + oneLine(err.Error(), maxStageNote)
		mk = nil
	}
	todo, _ := warmRecords(w.led.All(), mk, warmOrder(cfg)...)
	kept := todo[:0:0]
	for _, r := range todo {
		// Only kept postings: a closed one's last event day is what the digest footer counts.
		if r.Status == posting.Triaged || r.Status == posting.Unsure || r.Status == posting.Scored || r.Status == posting.Explained || r.Status == posting.Digested {
			kept = append(kept, r)
		}
	}
	items := backfillItems(kept, esc.belowP, backfillSlots(cfg))
	workers := cfg.AdvanceWorkers()
	if w.tuned.Workers > 0 {
		workers = w.tuned.Workers
	}
	rep := backfillPass(ctx, runner, esc, items, cfg.TargetSummary, workers)
	rep.Note = note
	return &rep
}

// backfillPass asks the tier about items in order, bounded by the escalator's
// max_per_run, escalateBackfillTimeout and ctx, with at most workers postings in
// flight, and records the readings through runner (Runner.RecordShadows).
func backfillPass(ctx context.Context, runner *pipeline.Runner, esc *escalator, items []backfillItem, summary string, workers int) escalateBackfillReport {
	rep := escalateBackfillReport{Candidates: len(items)}
	if len(items) == 0 {
		return rep
	}
	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithTimeout(ctx, escalateBackfillTimeout)
	defer cancel()

	var (
		mu         sync.Mutex
		streak     int
		stop       atomic.Bool
		firstError string
	)
	stopFor := func(why string) {
		mu.Lock()
		if rep.Stopped == "" {
			rep.Stopped = why
		}
		mu.Unlock()
		stop.Store(true)
	}
	process := func(it backfillItem, slots []decide.Slot) {
		var got map[string]decide.SlotReading
		d := askFunc(func(ctx context.Context, q decide.Question) (decide.Answer, error) {
			got = esc.askSlots(ctx, q, slots)
			return decide.Answer{}, nil
		})
		_, err := jdtriage.Triager{D: d, TargetSummary: summary}.Triage(ctx, it.post)
		if err != nil || got == nil {
			// The question could not be built (an unusable target summary): nothing was asked.
			mu.Lock()
			rep.Failed++
			if firstError == "" && err != nil {
				firstError = oneLine(err.Error(), maxStageNote)
			}
			mu.Unlock()
			return
		}
		asked := make(map[string]string, len(slots))
		answered, outage := 0, 0
		for _, s := range slots {
			asked[s.Name] = it.asked[s.Name]
			switch rd := got[s.Name].Reading; {
			case rd == decide.Answered:
				answered++
			case rd == decide.Unavailable || rd == decide.NotAsked:
				outage++
			}
		}
		n, werr := runner.RecordShadows(ctx, it.key, escalateName, asked, got)
		mu.Lock()
		rep.Slots += len(slots)
		rep.Answered += answered
		rep.Unavailable += outage
		if werr != nil && ctx.Err() == nil {
			rep.Failed++
			if firstError == "" {
				firstError = oneLine(werr.Error(), maxStageNote)
			}
		}
		if n > 0 {
			rep.Recorded++
		}
		if outage == len(slots) {
			streak++
		} else {
			streak = 0
		}
		tripped := streak >= pipeline.MaxConsecutiveUnread
		mu.Unlock()
		switch {
		case werr != nil && ctx.Err() == nil && !errors.Is(werr, context.Canceled):
			stopFor(backfillStopLedger)
		case tripped:
			stopFor(backfillStopUnavailable)
		}
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	sent := 0 // items dispatched, or begun and cut short by the cap
dispatch:
	for i, it := range items {
		// A worker is taken before anything is decided, so a stop a finishing
		// posting has just called (an outage streak, a failed write) is seen
		// before the next posting is reserved and asked.
		held := false
		select {
		case sem <- struct{}{}:
			held = true
		case <-ctx.Done():
		}
		release := func() {
			if held {
				<-sem
			}
		}
		if stop.Load() {
			release()
			break dispatch
		}
		if ctx.Err() != nil {
			why := backfillStopCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				why = backfillStopTimeout
			}
			stopFor(why)
			release()
			break dispatch
		}
		slots := esc.reserve(it.slots)
		if len(slots) == 0 {
			stopFor(backfillStopCap)
			release()
			break dispatch
		}
		sent = i + 1
		if len(slots) < len(it.slots) {
			mu.Lock()
			rep.Deferred += len(it.slots) - len(slots)
			mu.Unlock()
			stopFor(backfillStopCap)
		}
		wg.Add(1)
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()
			// The tier's own panics are contained in askSafely. Anything else is
			// counted as a failed posting and stops the pass: a backfill is never
			// worth ending the daemon for.
			defer func() {
				if p := recover(); p != nil {
					mu.Lock()
					rep.Failed++
					if firstError == "" {
						firstError = "panic: " + panicText(p)
					}
					mu.Unlock()
					stopFor(backfillStopLedger)
				}
			}()
			process(it, slots)
		}()
	}
	wg.Wait()
	for _, it := range items[sent:] {
		rep.Deferred += len(it.slots)
	}
	if firstError != "" {
		rep.Note = "a reading was not recorded: " + firstError
	}
	return rep
}
