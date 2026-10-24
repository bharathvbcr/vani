package main

import (
	"errors"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// cmdSemantic is `vani semantic warm|status`. Neither prints the knowledge
// base or any posting: counts, paths and reasons only.
func (e *env) cmdSemantic(args []string) error {
	if len(args) == 0 {
		return usagef("semantic: expected subcommand \"warm\" or \"status\"")
	}
	switch args[0] {
	case "warm":
		return e.cmdSemanticWarm(args[1:])
	case "status":
		return e.cmdSemanticStatus(args[1:])
	}
	return usagef("semantic: unknown subcommand %q (want warm or status)", args[0])
}

func (e *env) cmdSemanticWarm(args []string) error {
	fs := e.newFlags("semantic warm")
	max := fs.Int("max", 0, fmt.Sprintf("most postings to embed (default semantic.warm_per_run; at most %d)", semantic.MaxWarmDocs))
	if err := parse(fs, args); err != nil {
		return err
	}
	if *max < 0 || *max > semantic.MaxWarmDocs {
		return usagef("semantic warm: --max must be 1-%d", semantic.MaxWarmDocs)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if !cfg.SemanticOn() {
		return errors.New("semantic matching is not configured: set semantic.snapshot and kb.bank")
	}
	limit := *max
	if limit == 0 {
		limit = cfg.SemanticWarmLimit()
	}
	recs, err := e.ledgerRecords() // no lock: works while the daemon runs
	if err != nil {
		return err
	}
	mk, err := marks.ReadMarks(marks.Dir(e.stateDir))
	if err != nil {
		// Marked postings are not warmed without the marks; say so rather than
		// let a partial warm look complete.
		fmt.Fprintf(e.stderr, "vani: semantic warm: the marks store is unreadable (%v); only the kept postings are warmed\n", err)
	}
	ctx, stop := signalContext()
	defer stop()
	rep := warmSemantic(ctx, cfg, e.stateDir, recs, mk, limit)
	fmt.Fprintf(e.stdout, "scored %d, cached %d, failed %d, skipped %d\n", rep.Scored, rep.Cached, rep.Failed, rep.Skipped)
	if rep.Rebuilt {
		fmt.Fprintln(e.stdout, "the score cache was unreadable and was started again")
	}
	if rep.Note != "" {
		return errors.New(rep.Note)
	}
	return nil
}

func (e *env) cmdSemanticStatus(args []string) error {
	if err := parse(e.newFlags("semantic status"), args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	w := e.stdout
	fmt.Fprintf(w, "this binary has the Tessl engine: %s\n", yn(semantic.TesslBuilt))
	if !semantic.TesslBuilt {
		fmt.Fprintln(w, "  (build with -tags tessl on darwin/arm64; without it semantic matching reports \"not run\")")
	}
	fmt.Fprintf(w, "configured (semantic.snapshot and kb.bank): %s\n", yn(cfg.SemanticOn()))
	if !cfg.SemanticOn() {
		return nil
	}
	fmt.Fprintf(w, "snapshot: %s\nwarm per run: %d\n", cfg.Semantic.Snapshot, cfg.SemanticWarmLimit())
	ctx, cancel := boundedKBContext()
	defer cancel()
	prof, err := semanticProfile(ctx, cfg)
	if err != nil {
		fmt.Fprintf(w, "profile: unavailable: %s\n", oneLine(err.Error(), 200))
		return nil
	}
	trunc := ""
	if prof.Truncated {
		trunc = " (truncated at the cap)"
	}
	fmt.Fprintf(w, "profile chunks: %d%s\n", len(prof.Chunks), trunc)
	m, err := semantic.OpenLookup(prof, semanticDir(e.stateDir))
	if err != nil {
		fmt.Fprintf(w, "score cache: unreadable: %s\n", oneLine(err.Error(), 200))
		return nil
	}
	model := m.Model()
	if model == "" {
		model = "none yet"
	}
	fmt.Fprintf(w, "cached scores: %d\nmodel: %s\n", m.Len(), model)
	if n, err := semantic.StaleScores(prof, semanticDir(e.stateDir)); err != nil {
		fmt.Fprintf(w, "score cache: unreadable: %s\n", oneLine(err.Error(), 200))
	} else if n > 0 {
		noun := "scores are"
		if n == 1 {
			noun = "score is"
		}
		fmt.Fprintf(w, "warning: %d cached %s for an earlier profile (the bank or target summary changed); run `vani semantic warm`\n", n, noun)
	}
	if _, err := semantic.OpenLookupFor(prof, semanticDir(e.stateDir), semanticModelPrefix(cfg)); errors.Is(err, semantic.ErrModelMismatch) {
		fmt.Fprintf(w, "warning: the cached scores are from another model than semantic.snapshot; run `vani semantic warm`\n")
	}
	return nil
}
