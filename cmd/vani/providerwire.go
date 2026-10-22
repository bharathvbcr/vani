package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/llm/gemini"
	"github.com/bharathvbcr/Manvi/manvi/llm/transport"
)

// budgetClock is the one clock the llm budget day comes from (a seam for tests).
var budgetClock = time.Now

// budgetLedgerPath is the llm budget ledger file for this run's UTC day. The day
// is read from budgetClock once and kept, so the provider that meters into the
// ledger and anything that reads the day's spend or cap (vani teacher) name the
// same file even when the run crosses UTC midnight.
func (w *wired) budgetLedgerPath() string {
	w.dayOnce.Do(func() { w.day = budgetClock().UTC().Format("2006-01-02") })
	return filepath.Join(w.stateDir, "budget", "llm-"+w.day+".json")
}

// provider builds the configured Manvi adapter, metered by a budget ledger
// that is one file per UTC day, so decide.daily_usd.llm is a daily cap.
func (w *wired) provider(ctx context.Context) (llm.Provider, error) {
	in, out, err := w.cfg.LLM.Prices()
	if err != nil {
		return nil, err
	}
	name := w.cfg.LLM.Provider
	resolver := credentials.NewResolver()
	if _, err := resolver.Resolve(name); err != nil {
		return nil, err
	}
	resolve := func() (credentials.Secret, error) { return resolver.Resolve(name) }
	var inner llm.Provider
	switch name {
	case "gemini":
		inner = gemini.New("", resolve)
	case "anthropic":
		// As Manvi's own CLI does: ANTHROPIC_WORKSPACE_ID names the workspace
		// for a key not scoped to one (Anthropic refuses such a key's requests
		// with a 400 otherwise), and ANTHROPIC_BASE_URL overrides the host.
		a, err := anthropic.NewForWorkspace(os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv(credentials.AnthropicWorkspaceEnv), resolve)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", credentials.AnthropicWorkspaceEnv, err)
		}
		inner = a
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	limit := int64(w.cfg.Decide.DailyUSD.LLM * 1e9)
	l, err := budget.Open(ctx, w.budgetLedgerPath(), limit)
	if err != nil {
		return nil, fmt.Errorf("llm budget: %w", err)
	}
	return &budget.Provider{Inner: newPricedProvider(inner, w.cfg.LLM.Model, in, out), Ledger: l}, nil
}

// pricedProvider meters the adapter at the configured prices for the configured
// model (the operator's conservative figures, which win over any table the
// adapter publishes) and at the adapter's table for any other model (pricing.go).
//
// Embedding llm.Provider exposes only that interface, so every optional
// contract budget.Provider looks for on its inner provider is forwarded here
// by hand. Without AttemptGateSupported, budget.Provider refuses every request
// before it is sent ("does not route every attempt through the admission gate").
type pricedProvider struct {
	llm.Provider
	prices budget.Prices
	model  string // the configured model; empty meters every model at prices
}

// AttemptGateSupported is true only when the adapter itself makes that promise.
func (p pricedProvider) AttemptGateSupported() bool {
	g, ok := p.Provider.(transport.AttemptGatedProvider)
	return ok && g.AttemptGateSupported()
}

// AppliedMaxTokens forwards the adapter's output bound; without one it is what
// budget.Provider would assume: the request's bound, else the model ceiling.
func (p pricedProvider) AppliedMaxTokens(req llm.Request) int {
	if r, ok := p.Provider.(budget.MaxTokensReporter); ok {
		return r.AppliedMaxTokens(req)
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	c, _ := p.Provider.Capability(req.Model)
	return c.MaxOutputTokens
}

// RejectionUnbilled forwards the adapter's rule; without one nothing is unbilled.
func (p pricedProvider) RejectionUnbilled(status int) bool {
	r, ok := p.Provider.(budget.UnbilledRejector)
	return ok && r.RejectionUnbilled(status)
}

// ReplayableOn forwards the adapter's rule; without one only the same model replays.
func (p pricedProvider) ReplayableOn(fromModel, toModel string) bool {
	if r, ok := p.Provider.(llm.ReasoningReplayer); ok {
		return r.ReplayableOn(fromModel, toModel)
	}
	return fromModel == toModel
}
