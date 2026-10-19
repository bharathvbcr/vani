// Package explain drafts the reasons catalog the digest's "why this role"
// notes are picked from (internal/decide/jdreasons), with one bounded,
// tool-less LLM turn. It runs once per target summary, not per posting, and
// sees only the user's own target summary: no posting text, no personal data.
// The reply is model output and is held to the catalog's rules; a reply that
// breaks one is refused whole, never trimmed to fit.
package explain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

const (
	DefaultMaxTokens = 4000
	MaxMaxTokens     = 8000
	DefaultTimeout   = 120 * time.Second
	MaxTimeout       = 300 * time.Second
)

const systemPrompt = `You draft a catalog of short, reusable reasons for a job seeker's daily digest of job postings.
A small decision model will later read each posting and pick at most one reason per category, so every
reason must be judgeable from a posting's text alone.

Reply with exactly one JSON object and nothing else (no prose, no markdown):
{"fit":[ITEM...],"gap":[ITEM...],"check":[ITEM...]}
where ITEM is {"id":"lower_snake_case","label":"...","description":"..."}.

- fit: 8 to 15 distinct reasons a posting can match the target roles (core technical focus, domain,
  seniority, kind of team or work).
- gap: 8 to 15 distinct gaps or risks a posting can carry for the target (a must-have skill area outside
  the target, a seniority mismatch, an unrelated domain, an unusual location or schedule demand).
- check: 6 to 15 distinct things worth verifying in or about a posting before applying (scope of the
  role, on-call load, team size, interview format, how current the posting is).

label: at most 80 characters of plain words, shown to the job seeker.
description: one or two sentences, at most 300 characters, saying when the reason applies; the decision
model judges postings by it.
ids are unique within a category, use only [a-z0-9_], and are never "none".
Do not name companies, salaries, people, links, or anything about the job seeker beyond the target below.`

// Config configures Draft.
type Config struct {
	Model string // required; the provider must serve it
	// TargetSummary is the user's own non-identifying description of the roles
	// they want. Required: the catalog is about it, and is keyed to its hash.
	TargetSummary string
	MaxTokens     int
	Timeout       time.Duration
}

// Draft asks p for a reasons catalog for cfg.TargetSummary and returns it
// validated. It does not write anything.
func Draft(ctx context.Context, p llm.Provider, cfg Config) (jdreasons.Catalog, error) {
	if p == nil {
		return jdreasons.Catalog{}, errors.New("explain: a provider is required")
	}
	if cfg.Model == "" {
		return jdreasons.Catalog{}, errors.New("explain: a model is required")
	}
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return jdreasons.Catalog{}, err
	}
	if summary == "" {
		return jdreasons.Catalog{}, errors.New("explain: target_summary is empty; a reasons catalog is drafted for a target")
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.MaxTokens < 0 || cfg.MaxTokens > MaxMaxTokens {
		return jdreasons.Catalog{}, fmt.Errorf("explain: max tokens %d outside (0, %d]", cfg.MaxTokens, MaxMaxTokens)
	}
	switch {
	case cfg.Timeout == 0:
		cfg.Timeout = DefaultTimeout
	case cfg.Timeout < 0 || cfg.Timeout > MaxTimeout:
		return jdreasons.Catalog{}, fmt.Errorf("explain: timeout %s outside (0, %s]", cfg.Timeout, MaxTimeout)
	}
	capab, ok := p.Capability(cfg.Model)
	if !ok {
		return jdreasons.Catalog{}, fmt.Errorf("explain: provider %q does not serve model %q", p.Name(), cfg.Model)
	}
	if err := capab.Validate(llm.Request{Model: cfg.Model, MaxTokens: cfg.MaxTokens}); err != nil {
		return jdreasons.Catalog{}, fmt.Errorf("explain: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	req := llm.Request{
		Model:     cfg.Model,
		System:    systemPrompt,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "Target roles (written by the job seeker): " + summary}}}},
		MaxTokens: cfg.MaxTokens,
	}
	llmbackend.Shape(p, &req)
	text, err := llmbackend.Turn(ctx, p, req)
	if err != nil {
		return jdreasons.Catalog{}, fmt.Errorf("explain: %w", err)
	}
	return parseReply(text, summary, fmt.Sprintf("vani reasons draft (%s %s)", p.Name(), cfg.Model))
}

// parseReply takes the outermost {...} of the reply (a model may still wrap
// it in a code fence) and decodes it strictly.
func parseReply(text, summary, draftedBy string) (jdreasons.Catalog, error) {
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return jdreasons.Catalog{}, errors.New("explain: the reply holds no JSON object")
	}
	d, err := jdreasons.ParseDraft([]byte(text[start : end+1]))
	if err != nil {
		return jdreasons.Catalog{}, fmt.Errorf("explain: reply: %w", err)
	}
	d.DraftedBy = draftedBy // the model does not get to say who drafted it
	c, err := d.Stamp(summary)
	if err != nil {
		return jdreasons.Catalog{}, fmt.Errorf("explain: draft refused: %w", err)
	}
	return c, nil
}
