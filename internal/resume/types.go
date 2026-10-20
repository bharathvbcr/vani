// Package resume scores postings against the career knowledge base using
// DevPrism's headless MCP tools. DevPrism makes no model calls; everything
// here is deterministic given the knowledge base and the JD text.
//
// Personal data (name, contact lines) is never a model-visible parameter:
// typed wrappers fill those from the vault after a call has been decided.
package resume

import "context"

// Coverage is one must-have requirement's status against the knowledge base.
type Coverage struct {
	Requirement string `json:"requirement"`
	Status      string `json:"status"` // "covered" | "weak" | "missing"
}

// Gap is DevPrism resume_gap_analysis, reduced to what ranking needs.
type Gap struct {
	CoveragePct float64    `json:"coverage_pct"`
	MustHaves   []Coverage `json:"must_haves"`
	// NoMustHaves is true when DevPrism extracted no must-have requirement from
	// the JD (its extractionEmpty flag, or an empty item list). DevPrism reports
	// 0% coverage then (0 of 0), which is "unread", not "nothing covered": the
	// percentage must not be used as a measurement.
	NoMustHaves bool `json:"no_must_haves,omitempty"`
}

// Score is DevPrism resume_score_and_select, reduced to what ranking needs.
type Score struct {
	Total      float64            `json:"total"`
	Components map[string]float64 `json:"components,omitempty"`
	BlockIDs   []string           `json:"block_ids,omitempty"`
}

// Scorer ranks a JD against the knowledge base. PersonaID may be empty.
type Scorer interface {
	Gap(ctx context.Context, jd, personaID string) (Gap, error)
	Score(ctx context.Context, jd, personaID string) (Score, error)
}
