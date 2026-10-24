package main

import (
	"fmt"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// warnShadowErrors prints shadowErrorsNote to the run's warnings when there is one.
func (w *wired) warnShadowErrors(r *pipeline.Runner) {
	if note := shadowErrorsNote(r); note != "" {
		w.warnf("%s", note)
	}
}

// shadowErrorsNote reports how many shadow calls failed in this run, or "" when
// there is no shadow or it never failed. The router counts a shadow call that
// errored or timed out, and a slot the shadow left out; the first records no
// triage.<slot>.shadow_reading at all, so `vani eval` cannot see it: its "asked"
// counts omit these postings and the shadow's coverage reads better than it is.
// The count is per run because the router is rebuilt per run.
func shadowErrorsNote(r *pipeline.Runner) string {
	if r == nil {
		return ""
	}
	t, ok := r.Triager.(*jdtriage.Triager)
	if !ok || t == nil {
		return ""
	}
	rt := routerUnder(t.D)
	if rt == nil || rt.Shadow == nil {
		return ""
	}
	n := rt.ShadowErrors()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("shadow %q: %d call(s) or slot(s) failed, timed out or came back unusable this run; "+
		"failed calls leave no shadow reading, so `vani eval` does not count them as asked", rt.Shadow.Name(), n)
}

// routerOf is the run's decision router. An escalator or a drop-candidate
// shadow wraps it; the shadow's own counters stay on the inner router.
// Anything else records nothing and does not panic.
func routerOf(r *pipeline.Runner) *decide.Router {
	if r == nil {
		return nil
	}
	t, ok := r.Triager.(*jdtriage.Triager)
	if !ok || t == nil {
		return nil
	}
	return routerUnder(t.D)
}

func routerUnder(d decide.Decider) *decide.Router {
	for i := 0; d != nil && i < 4; i++ {
		switch t := d.(type) {
		case *decide.Router:
			return t
		case *escalator:
			d = t.primary
		case *dropShadower:
			d = t.primary
		default:
			return nil
		}
	}
	return nil
}

// shadowMissCounts is how many shadow calls failed and how many sampled
// postings were skipped while the shadow was busy. Both are zero when this
// run has no shadow.
func shadowMissCounts(r *pipeline.Runner) (int, int) {
	rt := routerOf(r)
	if rt == nil || rt.Shadow == nil {
		return 0, 0
	}
	var busy int64
	if gs, ok := rt.Shadow.(gatedShadow); ok && gs.gate != nil {
		busy = gs.gate.busy.Load()
	}
	return boundedCount(rt.ShadowErrors()), boundedCount(busy)
}

func boundedCount(n int64) int {
	if n <= 0 {
		return 0
	}
	const maxInt = int64(^uint(0) >> 1)
	if n > maxInt {
		return int(maxInt)
	}
	return int(n)
}
