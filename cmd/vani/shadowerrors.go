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
	rt, ok := t.D.(*decide.Router)
	if !ok || rt == nil || rt.Shadow == nil {
		return ""
	}
	n := rt.ShadowErrors()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("shadow %q: %d call(s) or slot(s) failed, timed out or came back unusable this run; "+
		"failed calls leave no shadow reading, so `vani eval` does not count them as asked", rt.Shadow.Name(), n)
}
