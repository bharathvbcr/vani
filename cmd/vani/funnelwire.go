package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// notesBudgetFile is the notes' own daily cap, a file beside the OpenRouter
// triage cap (jev.json). Two files, so spending one never moves the other.
const notesBudgetFile = "jev-notes.json"

// triageModelID names everything a triage reading depends on besides the
// posting: the backends that may answer, and the question they are asked (the
// shared fixture and the target summary). A reading is inherited only under
// the same id, so changing a model, a backend order or the summary re-asks.
func triageModelID(cfg config.Config) string {
	h := sha256.New()
	h.Write(jdtriage.FixtureJSON)
	h.Write([]byte{0})
	h.Write([]byte(cfg.TargetSummary))
	h.Write([]byte{0})
	// The triager also asks the job-category slot (see runner), so the
	// question's shape is part of what a reading depends on.
	extra := jobcategory.DecideSlot()
	h.Write([]byte(fmt.Sprintf("%s=%v", extra.Name, extra.Options)))
	lappi := "off"
	if l := cfg.Decide.Lappi; l != nil {
		lappi = "shadow"
		if l.Primary {
			lappi = "primary"
		}
		// Per-slot promotion is a new routing, so it gets a new id that names
		// the promoted set. The three old values above stay byte-identical, or
		// every cached reading would be discarded.
		if ps := l.PromotedSlots(); len(ps) > 0 && !l.Primary {
			lappi = "slots:" + strings.Join(ps, "+")
		}
	}
	return fmt.Sprintf("or=%s;llm=%s/%s;lappi=%s;ctx=%s", cfg.Decide.OpenRouterModel, cfg.LLM.Provider, cfg.LLM.Model, lappi,
		hex.EncodeToString(h.Sum(nil))[:12])
}

// notesSpend is the notes' own daily cap, or nil when OpenRouter is not in use
// (triage spend is nil too, and notes are off). A cap that cannot be opened is
// an error, never an unmetered notes backend.
func notesSpend(stateDir string, cfg config.Config, triageSpend *decide.DailyCap) (*decide.DailyCap, error) {
	if triageSpend == nil {
		return nil, nil
	}
	c, err := decide.NewDailyCap(filepath.Join(stateDir, "budget", notesBudgetFile), cfg.Funnel.NotesCapUSD())
	if err != nil {
		return nil, fmt.Errorf("notes budget: %w", err)
	}
	return c, nil
}

// uncheckedCauses is a run's fail-open tally as the digest footer names it.
func uncheckedCauses(c pipeline.FailOpenCounts) []digest.Unchecked {
	return []digest.Unchecked{
		{Cause: "empty description", N: c.EmptyDescription},
		{Cause: "title unread", N: c.TitleUnread},
		{Cause: "location unreadable", N: c.LocationUnreadable},
		{Cause: "prescore failed", N: c.PrescoreFailed},
		{Cause: "other", N: c.Other},
	}
}
