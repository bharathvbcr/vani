package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/decide"
)

// escalateName is the backend name escalated readings are recorded under:
// triage.<slot>.shadow_backend carries it. llmbackend.Config.Name refuses the
// names other backends answer under, so no other backend can claim it. The
// ranker owns the name (jobinsight.ShadowEscalate): it keys the confidence it
// gives an escalation's disagreement on it, so the two cannot drift.
const escalateName = jobinsight.ShadowEscalate

// escalateBackend is the escalation tier's backend: the Claude model
// llm.triage_model (llm.model when empty) behind errorLogged, so its calls,
// readings and errors land in the run's backend_stats under "escalate" and a
// dead tier trips the same breaker as any backend.
//
// It is built on p, the metered provider the triage fallback already uses, never
// a second w.provider(): that opens the day's budget ledger again, and two
// ledgers on one file would each bound only themselves. With one provider the
// llm daily_usd cap bounds every LLM token this run spends. It returns nil when
// decide.escalate is off.
func (w *wired) escalateBackend(p llm.Provider) (decide.Backend, error) {
	if !w.cfg.Decide.EscalateEnabled() {
		return nil, nil
	}
	lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.EffectiveTriageModel(), Name: escalateName})
	if err != nil {
		return nil, fmt.Errorf("escalate backend: %w", err)
	}
	return logErrors(lb, w.warnf, w.backendErrs), nil
}

// escalating is the Decider the triager asks: router itself unless
// decide.escalate is on, so a disabled run is exactly the plain router.
func (w *wired) escalating(router decide.Decider, esc decide.Backend) decide.Decider {
	if !w.cfg.Decide.EscalateEnabled() {
		return router
	}
	if esc == nil {
		w.warnf("decide.escalate is on but the llm is disabled this run; no slot is escalated")
		return router
	}
	return newEscalator(router, decide.NewRouter(esc), w.cfg.Decide.EscalateBelowP(), w.cfg.Decide.EscalateMaxPerRun(), w.warnf)
}

// escalator is the decide.Decider that measures a cheaper Claude tier against
// the primary's low-confidence readings (decide.escalate). MEASURE MODE: it only
// ever adds to Answer.Shadow, which decide/types.go defines as "informational
// and never authoritative: nothing may act on them, and DropPolicy must ignore
// them". The primary Slots, their Liquid D calibration (the .p annotation) and the
// error return are the primary's, untouched; whatever goes wrong on the
// escalation side is recorded in the shadow reading and the backend meter, never
// in the answer.
//
// Which slots: those the primary chain Answered with a Liquid D calibration whose
// probability is below belowP. An outage reading (Unavailable, Failed, NotAsked)
// is not a low-confidence answer, and the posting is read again on a later run;
// a slot the LLM answered itself carries no Liquid D calibration; Lappi readings
// carry a Lappi one. Neither is a candidate.
//
// MEASURE MODE FOR DROPS AND FOR RANKING: DropPolicy ignores Shadow, so
// escalation never changes what is dropped. The ranker (branch rank-relevance)
// skips its DisagreeConf confidence cut for shadow_backend=escalate until a
// slot is promoted by config, so it does not change ranking either.
// triageAnnotations still writes triage.<slot>.disagree=true whenever an
// Answered shadow value differs from the primary's, on purpose: `vani eval`'s
// Promotion report reads that key to measure the cheap tier against the primary.
//
// decide.escalate and decide.shadow are exclusive in the config because both
// write Answer.Shadow for the same slots (one slot has one shadow reading).
//
// Bounds: maxPerRun slots per run (one escalator per run, so the counter resets
// with the run), and the escalate backend is on the metered provider, so the llm
// daily cap bounds the spend as well.
type escalator struct {
	primary decide.Decider
	// ask is a Router over the escalate backend alone. The Router, not the bare
	// backend, because it checks every value against the slot's options (Rule 7),
	// contains a panic, and tags each reading "backend=escalate", which is what
	// triageAnnotations reads shadow_backend from.
	ask     decide.Decider
	belowP  float64
	maxRun  int64
	warnf   func(format string, a ...any)
	used    atomic.Int64 // slots reserved for escalation this run
	skipped atomic.Int64 // candidate slots not escalated because maxRun was reached
	once    sync.Once    // prints the exhaustion line once
}

var _ decide.Decider = (*escalator)(nil)

func newEscalator(primary, ask decide.Decider, belowP float64, maxPerRun int, warnf func(string, ...any)) *escalator {
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	return &escalator{primary: primary, ask: ask, belowP: belowP, maxRun: int64(maxPerRun), warnf: warnf}
}

// Escalated is how many slots this run has sent to the escalation tier.
func (e *escalator) Escalated() int64 { return e.used.Load() }

// Skipped is how many candidate slots this run left alone for lack of budget in
// max_per_run.
func (e *escalator) Skipped() int64 { return e.skipped.Load() }

// Decide implements decide.Decider.
func (e *escalator) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	a, err := e.primary.Decide(ctx, q)
	if err != nil || ctx.Err() != nil {
		return a, err
	}
	slots := e.reserve(e.candidates(q, a))
	if len(slots) == 0 {
		return a, nil
	}
	sub := q
	sub.Slots = slots
	got := e.askSafely(ctx, sub)

	// a is the primary's answer by value; its Slots map is shared and left
	// alone, and Shadow is copied before it is written.
	shadow := make(map[string]decide.SlotReading, len(a.Shadow)+len(slots))
	for name, rd := range a.Shadow {
		shadow[name] = rd
	}
	for _, s := range slots {
		rd, ok := got[s.Name]
		if !ok {
			rd = decide.SlotReading{Reading: decide.Unavailable, Detail: "missing_slot;backend=" + escalateName}
		}
		shadow[s.Name] = rd
	}
	a.Shadow = shadow
	return a, nil
}

// candidates are the triage slots of q whose primary reading is a calibrated
// answer under belowP and has no shadow reading yet, in q's slot order. Only
// jdtriage.Slots escalate: they are what `vani eval` measures for promotion, so
// an appended slot (the job category) would spend max_per_run on readings no
// report reads.
func (e *escalator) candidates(q decide.Question, a decide.Answer) []decide.Slot {
	var out []decide.Slot
	for _, s := range q.Slots {
		if !isTriageSlot(s.Name) {
			continue
		}
		sr, ok := a.Slots[s.Name]
		if !ok || sr.Reading != decide.Answered || sr.Jev == nil {
			continue
		}
		// NaN fails the comparison and is not escalated: an unusable probability
		// is not a low one.
		if !(sr.Jev.Probability < e.belowP) {
			continue
		}
		if _, has := a.Shadow[s.Name]; has {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isTriageSlot(name string) bool {
	for _, s := range jdtriage.Slots {
		if s.Name == name {
			return true
		}
	}
	return false
}

// reserve takes up to len(slots) of the run's remaining max_per_run, atomically
// across the concurrent workers, and returns the slots it took. The rest are
// counted in Skipped.
func (e *escalator) reserve(slots []decide.Slot) []decide.Slot {
	if len(slots) == 0 {
		return nil
	}
	for {
		used := e.used.Load()
		room := e.maxRun - used
		if room <= 0 {
			e.skip(int64(len(slots)))
			return nil
		}
		n := min(int64(len(slots)), room)
		if e.used.CompareAndSwap(used, used+n) {
			if n < int64(len(slots)) {
				e.skip(int64(len(slots)) - n)
			}
			return slots[:n]
		}
	}
}

func (e *escalator) skip(n int64) {
	e.skipped.Add(n)
	e.once.Do(func() {
		e.warnf("escalate: max_per_run %d reached; further low-confidence slots are not escalated this run", e.maxRun)
	})
}

// askSafely asks the escalation tier and returns its readings by slot. Any
// failure (an error, a panic) reads Unavailable on every slot: the escalation
// side can never fail the posting.
func (e *escalator) askSafely(ctx context.Context, sub decide.Question) (readings map[string]decide.SlotReading) {
	fail := func(detail string) map[string]decide.SlotReading {
		m := make(map[string]decide.SlotReading, len(sub.Slots))
		for _, s := range sub.Slots {
			m[s.Name] = decide.SlotReading{Reading: decide.Unavailable, Detail: detail + ";backend=" + escalateName}
		}
		return m
	}
	defer func() {
		if recover() != nil {
			readings = fail("panic")
		}
	}()
	ans, err := e.ask.Decide(ctx, sub)
	if err != nil {
		return fail("error")
	}
	return ans.Slots
}
