package pipeline

import "github.com/bharathvbcr/decide"

// DropCandidates names the slots of a whose reading the policy would drop on:
// a rule's slot, answered with one of the rule's values by a backend whose
// calibration clears that rule's threshold. It is Evaluate's drop test, run
// over every rule instead of stopping at the first, so the two cannot drift
// (both go through calibrated). Order follows the rules; a slot appears once.
// It reads the answer only and decides nothing: callers use it to know which
// readings are worth a second opinion before the drop is applied.
func (p DropPolicy) DropCandidates(a decide.Answer) []string {
	var out []string
	seen := map[string]bool{}
	for _, rule := range p.Rules {
		sr, ok := a.Slots[rule.Slot]
		if !ok || seen[rule.Slot] || sr.Reading != decide.Answered || !contains(rule.Values, sr.Value) {
			continue
		}
		if _, ok := p.calibrated(slotBackend(a, sr), sr, rule); ok {
			seen[rule.Slot] = true
			out = append(out, rule.Slot)
		}
	}
	return out
}
