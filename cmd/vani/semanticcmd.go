package main

import (
	"context"
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
	fmt.Fprintln(e.stdout, rep.countsLine())
	if rep.Rebuilt {
		fmt.Fprintln(e.stdout, "the score cache was unreadable and was started again")
	}
	if l := rep.vectorsLine(); l != "" {
		fmt.Fprintln(e.stdout, l)
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
	// The vector log and the idle warm do not depend on the profile: they are
	// shown whichever way the profile and the score cache read below.
	defer func() {
		e.printVectorStatus()
		for _, l := range idleLines(e.stateDir, now()) {
			fmt.Fprintln(w, l)
		}
	}()
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
	e.printCalibration(prof, m.Model())
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

// recordedModel is an Embedder that only names a model and never embeds, so the
// profile cache a warm stored can be read (semantic.NewMatcher) without loading
// anything.
type recordedModel string

func (r recordedModel) Model() string { return string(r) }

func (recordedModel) Embed(context.Context, []string, semantic.Kind) ([][]float32, error) {
	return nil, fmt.Errorf("%w: this matcher was opened to read the stored profile only", semantic.ErrUnavailable)
}

// printCalibration shows the floor, ceiling and anchor chunk count the current
// profile's scores use, read from the profile cache the last warm stored. Until
// the profile has been embedded there are none, which is said, not shown as 0.
func (e *env) printCalibration(prof semantic.Profile, model string) {
	w := e.stdout
	if model == "" {
		fmt.Fprintln(w, "calibration: not embedded yet (no warm has scored this profile)")
		return
	}
	m, err := semantic.NewMatcher(recordedModel(model), prof, semanticDir(e.stateDir))
	if err != nil {
		fmt.Fprintf(w, "calibration: unavailable: %s\n", oneLine(err.Error(), 200))
		return
	}
	c, ok := m.Calibration()
	if !ok {
		fmt.Fprintln(w, "calibration: not embedded yet (the stored profile is missing or for another model)")
		return
	}
	fmt.Fprintf(w, "calibration: floor %.3f, ceiling %.3f, anchor chunks %d\n", c.Floor, c.Ceil, c.AnchorChunks)
}

// printVectorStatus describes the chunk-vector log (semantic.VectorStatus: read
// only, no lock, no model). A log it cannot read is said, not shown as empty; a
// rebuild is known only from the last run's record and the idle warm's state,
// so it is reported from those.
func (e *env) printVectorStatus() {
	w := e.stdout
	vs, err := semantic.VectorStatus(semanticDir(e.stateDir))
	switch {
	case err != nil:
		fmt.Fprintf(w, "vectors: unreadable: %s\n", oneLine(err.Error(), 200))
		return
	case !vs.Present:
		fmt.Fprintln(w, "vectors: none yet (a warm stores them; a profile change then rescores from them without the model)")
	default:
		fmt.Fprintf(w, "vectors: %d posting(s), %.1f MB (%.1f MB in use), model %s, chunking v%d\n",
			vs.Records, float64(vs.Bytes)/(1<<20), float64(vs.LiveBytes)/(1<<20), orNone(vs.Model), vs.Version)
		// The log is keyed on how postings are cut (ChunkVersion), not on the
		// scoring version, which changes without making any vector stale.
		if vs.Version != semantic.ChunkVersion {
			fmt.Fprintf(w, "warning: the vectors were chunked under v%d, this binary chunks under v%d; the next warm replaces them\n", vs.Version, semantic.ChunkVersion)
		}
	}
	if vs.TornBytes > 0 {
		fmt.Fprintf(w, "vectors: %d byte(s) of an incomplete tail (the next warm cuts it, or an append is in flight)\n", vs.TornBytes)
	}
	if r, rerr := readRunRecord(e.stateDir); rerr == nil && r != nil && r.Semantic != nil && r.Semantic.VectorsRebuilt {
		fmt.Fprintln(w, "vectors: the last run found the log unreadable and started it again")
	}
	if st, serr := readIdleState(e.stateDir); serr == nil && st.VectorsRebuilt {
		fmt.Fprintln(w, "vectors: the idle warm found the log unreadable and started it again")
	}
}
