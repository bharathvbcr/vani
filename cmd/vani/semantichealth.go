package main

import (
	"fmt"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The semantic health line: on how many open postings the knowledge-base
// similarity is evidence, as the ranker reads it (a fresh, non-thin cached
// score for the posting's current text), and when that is low, why. The
// similarity carries a third of the match's weight; with it unknown the prior
// fills its share and the order rests on the other parts, which nothing else
// says. It reads the score cache and the vector log only: no model, no
// scoring.

// Semantic health levels, by the share of open postings with a usable score.
const (
	semHealthOK  = "ok"   // at least semHealthWarnBelow
	semHealthLow = "warn" // at least semHealthBadBelow
	semHealthBad = "bad"
)

const (
	semHealthWarnBelow = 0.80
	semHealthBadBelow  = 0.50
)

type semanticHealth struct {
	Level string `json:"level"`
	// Open counts the open postings; Known those with a usable score, Thin
	// those whose score rests on too little text, Missing those with none.
	Open    int `json:"open"`
	Known   int `json:"known"`
	Thin    int `json:"thin"`
	Missing int `json:"missing"`
	// StaleProfile counts cached scores written for another profile (the
	// knowledge base changed since); Vectors the postings in the vector log,
	// which a warm can rescore without the model. -1 when unreadable.
	StaleProfile int `json:"stale_profile"`
	Vectors      int `json:"vectors"`
	// Cause says why the share is low, when it is.
	Cause string `json:"cause,omitempty"`
}

// semanticHealthOf counts recs' open postings against the score cache. With
// semantic matching off it is nil.
func (e *env) semanticHealthOf(cfg config.Config, recs []ledger.Record) *semanticHealth {
	if !cfg.SemanticOn() {
		return nil
	}
	profile := onceProfile(cfg)
	m, warning := semanticLookupKept(cfg, e.stateDir, nil, profile)
	h := semanticHealth{StaleProfile: -1, Vectors: -1}
	// noScores: the cache opened and holds nothing for this profile. The
	// lookup's "no scores yet" is then not the cause to name: a profile change
	// or the warm budget is, and the counts below say which.
	noScores := false
	if prof, err := profile(); err == nil {
		dir := semanticDir(e.stateDir)
		if n, err := semantic.StaleScores(prof, dir); err == nil {
			h.StaleProfile = n
		}
		if m == nil {
			if mm, err := semantic.OpenLookupFor(prof, dir, semanticModelPrefix(cfg)); err == nil && mm.Len() == 0 {
				noScores = true
			}
		}
	}
	if noScores {
		warning = ""
	}
	if v := e.semanticVectorsOf(); v.Error == "" {
		h.Vectors = v.Records
	}
	for _, r := range recs {
		if !open(r.Status) {
			continue
		}
		h.Open++
		if m == nil {
			h.Missing++
			continue
		}
		switch mt, ok := semanticScore(m, r); {
		case !ok:
			h.Missing++
		case mt.Thin:
			h.Thin++
		default:
			h.Known++
		}
	}
	h.classify(warning, cfg.SemanticWarmLimit())
	return &h
}

// classify sets Level and Cause from the counts; warning is the lookup's own
// (why no matcher opened), warmPerRun the run's embedding budget.
func (h *semanticHealth) classify(warning string, warmPerRun int) {
	if h.Open == 0 {
		h.Level = semHealthOK
		return
	}
	share := float64(h.Known) / float64(h.Open)
	switch {
	case share >= semHealthWarnBelow:
		h.Level = semHealthOK
		return
	case share >= semHealthBadBelow:
		h.Level = semHealthLow
	default:
		h.Level = semHealthBad
	}
	switch {
	case warning != "":
		h.Cause = warning
	case h.StaleProfile > 0:
		h.Cause = fmt.Sprintf("%d cached scores were written for an earlier knowledge-base profile; `vani semantic warm` rescores the postings whose vectors are stored (%s) without embedding", h.StaleProfile, vectorCount(h.Vectors))
	case h.Missing > 0:
		h.Cause = fmt.Sprintf("%d open postings have no score and the vector log holds %s; each run embeds at most %d (semantic.warm_per_run), so `vani semantic warm --max N` closes the gap sooner", h.Missing, vectorCount(h.Vectors), warmPerRun)
	default:
		h.Cause = fmt.Sprintf("%d open postings have too little text to score", h.Thin)
	}
}

func vectorCount(n int) string {
	if n < 0 {
		return "an unreadable count of postings"
	}
	return fmt.Sprintf("%d postings", n)
}

// Line is the one-line text form.
func (h semanticHealth) Line() string {
	pct := 0.0
	if h.Open > 0 {
		pct = 100 * float64(h.Known) / float64(h.Open)
	}
	s := fmt.Sprintf("semantic similarity: %s, known on %d of %d open postings (%.0f%%; %d thin, %d without a score)", h.Level, h.Known, h.Open, pct, h.Thin, h.Missing)
	if h.Cause != "" {
		s += ": " + h.Cause
	}
	return s
}
