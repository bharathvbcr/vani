package main

import (
	"sync"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
)

// buildFunnel assembles the runner's funnel hooks. scoring is whether DevPrism
// is on. relevance is the notes gate's reader (notesRelevance). The gate is on
// either way: relevance leans on triage's role_fit when there is no coverage.
func buildFunnel(cfg config.Config, scoring bool, notes *decide.DailyCap, relevance func(ledger.Record) (float64, bool)) pipeline.Funnel {
	g := &pipeline.NotesGate{MinRelevance: cfg.Funnel.NotesFloor(), Relevance: relevance, SpendKey: openRouterSpendKey}
	if notes != nil {
		g.Spend = notes
	}
	return pipeline.Funnel{
		FailOpen:            &pipeline.FailOpen{},
		Cache:               pipeline.NewReadCache(),
		ModelID:             triageModelID(cfg),
		MinDescriptionRunes: cfg.Funnel.DescriptionMin(),
		ScoreFirst:          scoring && !cfg.Funnel.ScoreAfterTriage,
		Notes:               g,
	}
}

// notesRelevance reads a record's age-free relevance: jobinsight.Result.Relevance
// from the same ranking context the Jobs views build (newInsightFor), the number
// the app shows and --min-relevance filters on. DevPrism coverage 0 is one input
// to it, never "unknown". ok is false only when the relevance rests on no known
// part at all (Result.RelevanceKnown is 0: it fell back to priors), or the
// context could not be built; the gate then defers the note. The context is
// built once, from the ledger as it stands when the first record is asked about.
func notesRelevance(cfg config.Config, stateDir string, all func() []ledger.Record, warnf func(string, ...any)) func(ledger.Record) (float64, bool) {
	var (
		once sync.Once
		in   *insight
	)
	return func(r ledger.Record) (float64, bool) {
		once.Do(func() {
			var err error
			in, err = newInsightFor(cfg, nil, stateDir, jobinsight.DefaultScoreHalfLifeDays, ledger.Snapshot{Records: all()}, nil)
			if err != nil {
				warnf("notes relevance unavailable, notes deferred: %v", err)
				in = nil
			}
		})
		if in == nil {
			return 0, false
		}
		res := jobinsight.Score(jobinsight.InputsFor(r, in.rc))
		return res.Relevance, res.RelevanceKnown > 0
	}
}
