package main

import (
	"context"
	"slices"

	"github.com/bharathvbcr/decide"
)

// slotFilter limits a backend to the slots keep accepts. A slot it declines
// reads NotAsked (detail "slot_filtered") without the inner backend being
// asked about it, so a declined slot costs nothing and, in the router, never
// overwrites an earlier backend's reading. decide.lappi.primary_slots uses it
// both ways: the Lappi chain instance keeps only the promoted slots, the
// OpenRouter model (d1-ultra) keeps only the others, and the Lappi shadow
// keeps only the slots not yet promoted.
type slotFilter struct {
	decide.Backend
	keep func(slot string) bool
}

var _ decide.Backend = slotFilter{}

// onlySlots keeps the slots in set.
func onlySlots(b decide.Backend, set []string) decide.Backend {
	return slotFilter{b, func(s string) bool { return slices.Contains(set, s) }}
}

// exceptSlots keeps every slot not in set.
func exceptSlots(b decide.Backend, set []string) decide.Backend {
	return slotFilter{b, func(s string) bool { return !slices.Contains(set, s) }}
}

// Decide asks the inner backend only about the kept slots.
func (f slotFilter) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	var kept []decide.Slot
	for _, s := range q.Slots {
		if f.keep(s.Name) {
			kept = append(kept, s)
		}
	}
	out := decide.Answer{Backend: f.Name(), Slots: make(map[string]decide.SlotReading, len(q.Slots))}
	for _, s := range q.Slots {
		if !f.keep(s.Name) {
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.NotAsked, Detail: "slot_filtered"}
		}
	}
	if len(kept) == 0 {
		return out, nil
	}
	sub := q
	sub.Slots = kept
	ans, err := f.Backend.Decide(ctx, sub)
	if err != nil {
		return decide.Answer{}, err
	}
	for name, rd := range ans.Slots {
		out.Slots[name] = rd
	}
	out.CostUSD = ans.CostUSD
	if ans.Backend != "" {
		out.Backend = ans.Backend
	}
	return out, nil
}
