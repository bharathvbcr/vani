package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/bharathvbcr/Vani/internal/config"
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

// semanticStatus is `vani semantic status`, gathered once and then printed as
// text or JSON. A part that could not be read says so in its Error and the
// parts after it are absent, as the text stops there; the vector log and the
// idle warm do not depend on the profile and are always read.
type semanticStatus struct {
	Tessl      bool             `json:"tessl"`
	Configured bool             `json:"configured"`
	Snapshot   string           `json:"snapshot,omitempty"`
	WarmPerRun int              `json:"warm_per_run,omitempty"`
	Profile    *semanticProfileStatus `json:"profile,omitempty"`
	Scores     *semanticScores  `json:"scores,omitempty"`
	Vectors    *semanticVectors `json:"vectors,omitempty"`
	Idle       *idleState       `json:"idle,omitempty"`
	IdleError  string           `json:"idle_error,omitempty"`
}

type semanticProfileStatus struct {
	Error     string `json:"error,omitempty"`
	Chunks    int    `json:"chunks"`
	Truncated bool   `json:"truncated"`
}

// semanticScores is the score cache. Model is "" until a warm has run.
type semanticScores struct {
	Error         string              `json:"error,omitempty"`
	Cached        int                 `json:"cached"`
	Model         string              `json:"model"`
	Calibration   semanticCalibration `json:"calibration"`
	Stale         int                 `json:"stale"`
	StaleError    string              `json:"stale_error,omitempty"`
	ModelMismatch bool                `json:"model_mismatch"`
}

// semanticCalibration's State is ok, not_embedded (Reason says why) or
// unavailable (Reason is the error).
type semanticCalibration struct {
	State        string  `json:"state"`
	Reason       string  `json:"reason,omitempty"`
	Floor        float64 `json:"floor,omitempty"`
	Ceil         float64 `json:"ceil,omitempty"`
	AnchorChunks int     `json:"anchor_chunks,omitempty"`
}

// semanticVectors is the chunk-vector log (semantic.VectorStatus: read only,
// no lock, no model). A rebuild is known only from the last run's record and
// the idle warm's state, so it is reported from those.
type semanticVectors struct {
	Error          string `json:"error,omitempty"`
	Present        bool   `json:"present"`
	Records        int    `json:"records"`
	Bytes          int64  `json:"bytes"`
	LiveBytes      int64  `json:"live_bytes"`
	Model          string `json:"model,omitempty"`
	Version        int    `json:"version,omitempty"`
	ChunkVersion   int    `json:"chunk_version"`
	TornBytes      int64  `json:"torn_bytes"`
	RebuiltLastRun bool   `json:"rebuilt_last_run"`
	RebuiltIdle    bool   `json:"rebuilt_idle"`
}

func (e *env) cmdSemanticStatus(args []string) error {
	fs := e.newFlags("semantic status")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	st := semanticStatus{Tessl: semantic.TesslBuilt, Configured: cfg.SemanticOn()}
	if st.Configured {
		st.Snapshot, st.WarmPerRun = cfg.Semantic.Snapshot, cfg.SemanticWarmLimit()
		e.semanticScoresOf(&st, cfg)
		v := e.semanticVectorsOf()
		st.Vectors = &v
		if idle, err := readIdleState(e.stateDir); err != nil {
			st.IdleError = oneLine(err.Error(), 200)
		} else if idle.Kind != "" {
			st.Idle = &idle
		}
	}
	return e.reply(*asJSON, st, nil, func() { e.printSemanticStatus(e.stdout, st) })
}

// semanticScoresOf reads the profile and, when it loads, the score cache.
func (e *env) semanticScoresOf(st *semanticStatus, cfg config.Config) {
	ctx, cancel := boundedKBContext()
	defer cancel()
	prof, err := semanticProfile(ctx, cfg)
	if err != nil {
		st.Profile = &semanticProfileStatus{Error: oneLine(err.Error(), 200)}
		return
	}
	st.Profile = &semanticProfileStatus{Chunks: len(prof.Chunks), Truncated: prof.Truncated}
	m, err := semantic.OpenLookup(prof, semanticDir(e.stateDir))
	if err != nil {
		st.Scores = &semanticScores{Error: oneLine(err.Error(), 200)}
		return
	}
	sc := &semanticScores{Cached: m.Len(), Model: m.Model(), Calibration: e.calibrationOf(prof, m.Model())}
	if n, err := semantic.StaleScores(prof, semanticDir(e.stateDir)); err != nil {
		sc.StaleError = oneLine(err.Error(), 200)
	} else {
		sc.Stale = n
	}
	_, err = semantic.OpenLookupFor(prof, semanticDir(e.stateDir), semanticModelPrefix(cfg))
	sc.ModelMismatch = errors.Is(err, semantic.ErrModelMismatch)
	st.Scores = sc
}

// printSemanticStatus is the text form.
func (e *env) printSemanticStatus(w io.Writer, st semanticStatus) {
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	fmt.Fprintf(w, "this binary has the Tessl engine: %s\n", yn(st.Tessl))
	if !st.Tessl {
		fmt.Fprintln(w, "  (build with -tags tessl on darwin/arm64; without it semantic matching reports \"not run\")")
	}
	fmt.Fprintf(w, "configured (semantic.snapshot and kb.bank): %s\n", yn(st.Configured))
	if !st.Configured {
		return
	}
	fmt.Fprintf(w, "snapshot: %s\nwarm per run: %d\n", st.Snapshot, st.WarmPerRun)
	// The vector log and the idle warm do not depend on the profile: they are
	// shown whichever way the profile and the score cache read below.
	defer func() {
		printVectors(w, *st.Vectors)
		for _, l := range idleLines(e.stateDir, now()) {
			fmt.Fprintln(w, l)
		}
	}()
	if p := st.Profile; p.Error != "" {
		fmt.Fprintf(w, "profile: unavailable: %s\n", p.Error)
		return
	}
	trunc := ""
	if st.Profile.Truncated {
		trunc = " (truncated at the cap)"
	}
	fmt.Fprintf(w, "profile chunks: %d%s\n", st.Profile.Chunks, trunc)
	sc := st.Scores
	if sc.Error != "" {
		fmt.Fprintf(w, "score cache: unreadable: %s\n", sc.Error)
		return
	}
	fmt.Fprintf(w, "cached scores: %d\nmodel: %s\n", sc.Cached, orNoneYet(sc.Model))
	switch c := sc.Calibration; c.State {
	case "ok":
		fmt.Fprintf(w, "calibration: floor %.3f, ceiling %.3f, anchor chunks %d\n", c.Floor, c.Ceil, c.AnchorChunks)
	case "unavailable":
		fmt.Fprintf(w, "calibration: unavailable: %s\n", c.Reason)
	default:
		fmt.Fprintf(w, "calibration: not embedded yet (%s)\n", c.Reason)
	}
	if sc.StaleError != "" {
		fmt.Fprintf(w, "score cache: unreadable: %s\n", sc.StaleError)
	} else if n := sc.Stale; n > 0 {
		noun := "scores are"
		if n == 1 {
			noun = "score is"
		}
		fmt.Fprintf(w, "warning: %d cached %s for an earlier profile (the bank or target summary changed); run `vani semantic warm`\n", n, noun)
	}
	if sc.ModelMismatch {
		fmt.Fprintf(w, "warning: the cached scores are from another model than semantic.snapshot; run `vani semantic warm`\n")
	}
}

func orNoneYet(model string) string {
	if model == "" {
		return "none yet"
	}
	return model
}

// recordedModel is an Embedder that only names a model and never embeds, so the
// profile cache a warm stored can be read (semantic.NewMatcher) without loading
// anything.
type recordedModel string

func (r recordedModel) Model() string { return string(r) }

func (recordedModel) Embed(context.Context, []string, semantic.Kind) ([][]float32, error) {
	return nil, fmt.Errorf("%w: this matcher was opened to read the stored profile only", semantic.ErrUnavailable)
}

// calibrationOf reads the floor, ceiling and anchor chunk count the current
// profile's scores use from the profile cache the last warm stored. Until the
// profile has been embedded there are none, which is said, not shown as 0.
func (e *env) calibrationOf(prof semantic.Profile, model string) semanticCalibration {
	if model == "" {
		return semanticCalibration{State: "not_embedded", Reason: "no warm has scored this profile"}
	}
	m, err := semantic.NewMatcher(recordedModel(model), prof, semanticDir(e.stateDir))
	if err != nil {
		return semanticCalibration{State: "unavailable", Reason: oneLine(err.Error(), 200)}
	}
	c, ok := m.Calibration()
	if !ok {
		return semanticCalibration{State: "not_embedded", Reason: "the stored profile is missing or for another model"}
	}
	return semanticCalibration{State: "ok", Floor: c.Floor, Ceil: c.Ceil, AnchorChunks: c.AnchorChunks}
}

// semanticVectorsOf describes the chunk-vector log. A log it cannot read is
// said, not shown as empty.
func (e *env) semanticVectorsOf() semanticVectors {
	vs, err := semantic.VectorStatus(semanticDir(e.stateDir))
	if err != nil {
		return semanticVectors{Error: oneLine(err.Error(), 200), ChunkVersion: semantic.ChunkVersion}
	}
	v := semanticVectors{Present: vs.Present, Records: vs.Records, Bytes: vs.Bytes, LiveBytes: vs.LiveBytes,
		Model: vs.Model, Version: vs.Version, ChunkVersion: semantic.ChunkVersion, TornBytes: vs.TornBytes}
	if r, rerr := readRunRecord(e.stateDir); rerr == nil && r != nil && r.Semantic != nil && r.Semantic.VectorsRebuilt {
		v.RebuiltLastRun = true
	}
	if st, serr := readIdleState(e.stateDir); serr == nil && st.VectorsRebuilt {
		v.RebuiltIdle = true
	}
	return v
}

func printVectors(w io.Writer, v semanticVectors) {
	switch {
	case v.Error != "":
		fmt.Fprintf(w, "vectors: unreadable: %s\n", v.Error)
		return
	case !v.Present:
		fmt.Fprintln(w, "vectors: none yet (a warm stores them; a profile change then rescores from them without the model)")
	default:
		fmt.Fprintf(w, "vectors: %d posting(s), %.1f MB (%.1f MB in use), model %s, chunking v%d\n",
			v.Records, float64(v.Bytes)/(1<<20), float64(v.LiveBytes)/(1<<20), orNone(v.Model), v.Version)
		// The log is keyed on how postings are cut (ChunkVersion), not on the
		// scoring version, which changes without making any vector stale.
		if v.Version != v.ChunkVersion {
			fmt.Fprintf(w, "warning: the vectors were chunked under v%d, this binary chunks under v%d; the next warm replaces them\n", v.Version, v.ChunkVersion)
		}
	}
	if v.TornBytes > 0 {
		fmt.Fprintf(w, "vectors: %d byte(s) of an incomplete tail (the next warm cuts it, or an append is in flight)\n", v.TornBytes)
	}
	if v.RebuiltLastRun {
		fmt.Fprintln(w, "vectors: the last run found the log unreadable and started it again")
	}
	if v.RebuiltIdle {
		fmt.Fprintln(w, "vectors: the idle warm found the log unreadable and started it again")
	}
}
