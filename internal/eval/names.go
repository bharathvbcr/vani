package eval

// LegacyOpenRouterName is the name the OpenRouter decision model answered
// under until 2026-10-19, when Vani began naming it after its model
// (config.Decide.DecisionName, e.g. "d1-ultra"). It is decide/jev's
// default backend name, and ledger rows written before then carry it as a
// slot's primary backend.
const LegacyOpenRouterName = "jev"

// LegacyOpenRouterModel is the backend name of the model that wrote every
// LegacyOpenRouterName row: liquid/d1-ultra was configured from before the
// first triage until the rename (d1 pre-registration, amendment 2). Legacy
// rows are read as its, never as whichever model is configured now, so a
// later model switch (to liquid/d1.2-ultra, "d1-2-ultra") does not credit
// d1-ultra's readings to the new model.
const LegacyOpenRouterModel = "d1-ultra"

// LegacyLocalShadow is the shadow backend name every local decision model
// recorded under until the model was named in it (config.Local.BackendName,
// "local-<model>-<mode>"). Rows filed under it are read as written: they may
// pool more than one local model.
const LegacyLocalShadow = "local"

// Names tells the eval which backend is the configured OpenRouter model. When
// one is configured, old rows filed under LegacyOpenRouterName are read as
// LegacyOpenRouterModel's, the model that wrote them, not the configured one.
// The ledger is append-only and never rewritten; this mapping is applied on
// read, to a slot's primary backend (triage.<slot>.backend) only. A shadow's
// backend (triage.<slot>.shadow_backend) and every other name is read as
// written, so a shadow reading can never be credited to the primary.
type Names struct {
	// OpenRouter is the configured OpenRouter model's backend name; empty
	// when none is configured, which leaves legacy rows under their old name.
	OpenRouter string
	// LLMModel is the model the "llm" backend runs triage on (llm.triage_model,
	// else llm.model); empty when no LLM is configured. The ledger records only
	// the backend name "llm", so reports that name a shadow add the model.
	LLMModel string
}

// primary returns the name a slot's recorded primary backend is reported
// under, and whether it was mapped from the legacy name.
func (n Names) primary(recorded string) (string, bool) {
	if recorded == LegacyOpenRouterName && n.OpenRouter != "" && n.OpenRouter != LegacyOpenRouterName {
		return LegacyOpenRouterModel, true
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
