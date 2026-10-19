package eval

// LegacyOpenRouterName is the name the OpenRouter decision model answered
// under until 2026-10-19, when Vani began naming it after its model
// (config.Decide.OpenRouterName, e.g. "d1-ultra"). It is decide/jev's
// default backend name, and ledger rows written before then carry it as a
// slot's primary backend.
const LegacyOpenRouterName = "jev"

// Names tells the eval which backend is the configured OpenRouter model, so
// that old rows filed under LegacyOpenRouterName are read as that model's.
// The ledger is append-only and never rewritten; this mapping is applied on
// read, to a slot's primary backend (triage.<slot>.backend) only. A shadow's
// backend (triage.<slot>.shadow_backend) and every other name is read as
// written, so a shadow reading can never be credited to the primary.
//
// The mapping assumes the legacy rows came from the model configured now.
// For Vani's live state that held: the config named liquid/d1-ultra from
// before the first triage until the rename (d1 pre-registration, amendment 2).
type Names struct {
	// OpenRouter is the configured OpenRouter model's backend name; empty
	// when none is configured, which leaves legacy rows under their old name.
	OpenRouter string
}

// primary returns the name a slot's recorded primary backend is reported
// under, and whether it was mapped from the legacy name.
func (n Names) primary(recorded string) (string, bool) {
	if recorded == LegacyOpenRouterName && n.OpenRouter != "" && n.OpenRouter != LegacyOpenRouterName {
		return n.OpenRouter, true
	}
	return recorded, false
}

// probability names the backend whose recorded probabilities the threshold
// tables are built from.
func (n Names) probability() string {
	if n.OpenRouter != "" {
		return n.OpenRouter
	}
	return LegacyOpenRouterName
}
