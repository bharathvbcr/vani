package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// dropShadowing is the Decider the triager asks when decide.shadow_mode is
// drop_candidates: base itself otherwise. With no shadow backend this run (the
// wiring already warned) it is base too, and no slot carries a shadow reading.
func (w *wired) dropShadowing(base decide.Decider, shadow decide.Backend) decide.Decider {
	if shadow == nil {
		return base
	}
	return newDropShadower(base, decide.NewRouter(shadow), shadow.Name(), shadow.Serves, dropPolicy(w.cfg, w.openRouterName()), shadowInFlight(w.cfg.Decide))
}

// dropShadower asks the shadow about every slot the drop policy would drop on
// (decide.shadow_mode drop_candidates), before the drop is applied. It is the
// escalator's shape and keeps its contract: it only ever adds to Answer.Shadow,
// which DropPolicy ignores (Rule 7), so the drop still follows the primary's
// rule alone. The primary Slots and the error return are the primary's,
// untouched. A shadow that cannot answer is recorded Unavailable on the slot,
// never as an agreement: triageAnnotations writes .disagree only for an
// Answered shadow, and the report counts anything else as unavailable.
//
// Unlike the one-in-N sample, a candidate is not skipped when the shadow is
// busy: the call waits for a slot (inFlight, the local server's parallelism)
// until the context ends, so every candidate carries a reading or an explicit
// unavailable one.
type dropShadower struct {
	primary  decide.Decider
	ask      decide.Decider // a Router over the shadow backend alone (Rule 7 check, panic containment, backend tag)
	name     string
	serves   func(task string) bool
	policy   pipeline.DropPolicy
	slots    chan struct{} // nil: no in-flight limit
	cands    atomic.Int64  // candidate slots seen
	answered atomic.Int64  // candidate slots the shadow answered
}

var _ decide.Decider = (*dropShadower)(nil)

func newDropShadower(primary, ask decide.Decider, name string, serves func(string) bool, p pipeline.DropPolicy, inFlight int) *dropShadower {
	d := &dropShadower{primary: primary, ask: ask, name: name, serves: serves, policy: p}
	if inFlight > 0 {
		d.slots = make(chan struct{}, inFlight)
	}
	return d
}

// Candidates and Answered are this run's candidate slots and how many of them
// the shadow answered.
func (d *dropShadower) Candidates() int64 { return d.cands.Load() }
func (d *dropShadower) Answered() int64   { return d.answered.Load() }

// Decide implements decide.Decider.
func (d *dropShadower) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	a, err := d.primary.Decide(ctx, q)
	if err != nil || ctx.Err() != nil {
		return a, err
	}
	slots := d.candidates(q, a)
	if len(slots) == 0 {
		return a, nil
	}
	d.cands.Add(int64(len(slots)))
	sub := q
	sub.Slots = slots
	got := d.askSafely(ctx, sub)

	shadow := make(map[string]decide.SlotReading, len(a.Shadow)+len(slots))
	for name, rd := range a.Shadow {
		shadow[name] = rd
	}
	for _, s := range slots {
		rd, ok := got[s.Name]
		if !ok {
			rd = decide.SlotReading{Reading: decide.Unavailable, Detail: "missing_slot;backend=" + d.name}
		}
		if rd.Reading == decide.Answered {
			d.answered.Add(1)
		}
		shadow[s.Name] = rd
	}
	a.Shadow = shadow
	return a, nil
}

// candidates are q's triage slots the policy would drop on given a, with no
// shadow reading yet, in q's slot order.
func (d *dropShadower) candidates(q decide.Question, a decide.Answer) []decide.Slot {
	drop := map[string]bool{}
	for _, name := range d.policy.DropCandidates(a) {
		drop[name] = true
	}
	var out []decide.Slot
	for _, s := range q.Slots {
		if !drop[s.Name] || !isTriageSlot(s.Name) {
			continue
		}
		if _, has := a.Shadow[s.Name]; has {
			continue
		}
		out = append(out, s)
	}
	return out
}

// askSafely returns the shadow's readings by slot; any failure (not serving the
// task, a cancelled wait, an error, a panic) reads Unavailable on every slot.
func (d *dropShadower) askSafely(ctx context.Context, sub decide.Question) (readings map[string]decide.SlotReading) {
	fail := func(detail string) map[string]decide.SlotReading {
		m := make(map[string]decide.SlotReading, len(sub.Slots))
		for _, s := range sub.Slots {
			m[s.Name] = decide.SlotReading{Reading: decide.Unavailable, Detail: detail + ";backend=" + d.name}
		}
		return m
	}
	defer func() {
		if recover() != nil {
			readings = fail("panic")
		}
	}()
	if d.serves != nil && !d.serves(sub.Task) {
		return fail("not_served")
	}
	if d.slots != nil {
		select {
		case d.slots <- struct{}{}:
			defer func() { <-d.slots }()
		case <-ctx.Done():
			return fail("cancelled")
		}
	}
	ans, err := d.ask.Decide(ctx, sub)
	if err != nil {
		return fail("error")
	}
	return ans.Slots
}

// dropShadowNote says how many drop candidates the shadow read this run, or ""
// when the run has no drop-candidate shadow or saw no candidate.
func dropShadowNote(r *pipeline.Runner) string {
	if r == nil {
		return ""
	}
	t, ok := r.Triager.(*jdtriage.Triager)
	if !ok || t == nil {
		return ""
	}
	d := t.D
	if e, ok := d.(*escalator); ok && e != nil {
		d = e.primary
	}
	ds, ok := d.(*dropShadower)
	if !ok || ds == nil || ds.Candidates() == 0 {
		return ""
	}
	return fmt.Sprintf("shadow %q: read %d of %d drop candidate slot(s); the rest are recorded unavailable", ds.name, ds.Answered(), ds.Candidates())
}
