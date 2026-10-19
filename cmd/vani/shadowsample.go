package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// shadowGate decides which questions the shadow is asked (decide.Router's
// ShadowSample). A question is sampled when a hash of its record id (the
// posting key) falls in one bucket of every, so a posting is in or out of the
// sample on every run and the sample does not lean on how the primary read
// it. A sampled question is still skipped when slots, the shadow's in-flight
// limit, is full: a call queued behind another on a parallel-1 server would
// hold its posting for the queue and then time out into an "unavailable"
// reading. Postings run concurrently, so every field is safe for concurrent
// use.
type shadowGate struct {
	every int
	slots chan struct{} // nil: no in-flight limit

	considered atomic.Int64 // questions offered to the shadow
	asked      atomic.Int64 // sampled and asked
	busy       atomic.Int64 // sampled but skipped: the shadow was busy
}

func newShadowGate(every, inFlight int) *shadowGate {
	g := &shadowGate{every: max(every, 1)}
	if inFlight > 0 {
		g.slots = make(chan struct{}, inFlight)
	}
	return g
}

// inSample reports whether recordID is in a one-in-every sample.
func inSample(recordID string, every int) bool {
	if every <= 1 {
		return true
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(recordID))
	return h.Sum64()%uint64(every) == 0
}

// sample is decide.Router.ShadowSample. A true return holds a slot, which
// gatedShadow.Decide releases: the router follows every true with exactly one
// shadow call.
func (g *shadowGate) sample(q decide.Question) bool {
	g.considered.Add(1)
	if !inSample(q.RecordID, g.every) {
		return false
	}
	if g.slots != nil {
		select {
		case g.slots <- struct{}{}:
		default:
			g.busy.Add(1)
			return false
		}
	}
	g.asked.Add(1)
	return true
}

func (g *shadowGate) release() {
	if g.slots == nil {
		return
	}
	// Non-blocking: a call made outside the router (a test asking the shadow
	// directly) holds no slot and must not wait for one.
	select {
	case <-g.slots:
	default:
	}
}

// gatedShadow is the shadow backend behind a shadowGate; its Decide releases
// the slot the gate reserved.
type gatedShadow struct {
	decide.Backend
	gate *shadowGate
}

func (s gatedShadow) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	defer s.gate.release()
	return s.Backend.Decide(ctx, q)
}

// shadowInFlight is how many shadow calls may be out at once. A local
// llama-server serves its parallel setting (zero, llama.cpp's auto, and an
// endpoint Vani does not run are read as one), and one more call may wait
// behind those: it waits at most one call (p50 3.2 s for d3-mini), far inside
// the 20 s timeout, and with four postings advancing a no-queue limit of one
// skipped 2 of 7 sampled postings in BenchmarkAdvance where one waiting call
// skipped none. Any other shadow is not limited here.
func shadowInFlight(d config.Decide) int {
	if d.Shadow != "local" || d.Local == nil {
		return 0
	}
	if s := d.Local.Server; s != nil && s.Parallel > 0 {
		return s.Parallel + 1
	}
	return 2
}

// gateShadow puts rt's shadow behind a gate sampling one in every and
// allowing inFlight calls at once, and returns the gate. A router without a
// shadow is left alone.
func gateShadow(rt *decide.Router, every, inFlight int) *shadowGate {
	if rt.Shadow == nil {
		return nil
	}
	g := newShadowGate(every, inFlight)
	rt.Shadow = gatedShadow{Backend: rt.Shadow, gate: g}
	rt.ShadowSample = g.sample
	return g
}

// warnShadowSample prints shadowSampleNote to the run's warnings when there is one.
func (w *wired) warnShadowSample(r *pipeline.Runner) {
	if note := shadowSampleNote(r); note != "" {
		w.warnf("%s", note)
	}
}

// shadowSampleNote says how much of the run the shadow read, or "" with no
// gated shadow or nothing asked of it. Skipped-while-busy postings are named
// because they are in the hash sample but carry no shadow reading.
func shadowSampleNote(r *pipeline.Runner) string {
	if r == nil {
		return ""
	}
	t, ok := r.Triager.(*jdtriage.Triager)
	if !ok || t == nil {
		return ""
	}
	rt, ok := t.D.(*decide.Router)
	if !ok || rt == nil {
		return ""
	}
	gs, ok := rt.Shadow.(gatedShadow)
	if !ok {
		return ""
	}
	g := gs.gate
	n := g.considered.Load()
	if n == 0 {
		return ""
	}
	note := fmt.Sprintf("shadow %q: asked about %d of %d posting(s) (one in %d sampled)", gs.Name(), g.asked.Load(), n, g.every)
	if b := g.busy.Load(); b > 0 {
		note += fmt.Sprintf("; %d sampled posting(s) skipped while it was busy", b)
	}
	return note
}
