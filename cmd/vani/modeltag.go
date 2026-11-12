package main

import (
	"context"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// modelTagged is a decision backend whose every reading names the model it
// asked (pipeline.TagModel). The LLM backend's name ("llm") does not say which
// Claude model answered, and llm.triage_model changes between runs, so when it
// is the shadow (decide.shadow "llm") the ledger records the model as
// triage.<slot>.shadow_model. A primary reading's tag is carried in its Detail
// only; nothing records it.
type modelTagged struct {
	decide.Backend
	model string
}

// Decide implements decide.Backend. The answer's slot map is copied before it
// is written, so a backend that reuses its map is never changed.
func (m modelTagged) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	a, err := m.Backend.Decide(ctx, q)
	if len(a.Slots) > 0 {
		slots := make(map[string]decide.SlotReading, len(a.Slots))
		for name, sr := range a.Slots {
			sr.Detail = pipeline.TagModel(sr.Detail, m.model)
			slots[name] = sr
		}
		a.Slots = slots
	}
	return a, err
}
