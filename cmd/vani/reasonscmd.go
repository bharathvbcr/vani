package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/explain"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

// reasonsProvider builds the metered provider for a catalog draft. Tests
// replace it.
var reasonsProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return w.provider(ctx) }

// notesEndpoint points the notes Jev client at a test server; empty is Jev's own
// endpoint.
var notesEndpoint string

func (e *env) reasonsPath() string { return filepath.Join(e.stateDir, jdreasons.FileName) }

// cmdReasons manages the catalog the digest's notes are picked from.
func (e *env) cmdReasons(args []string) error {
	if len(args) == 0 {
		return usagef("reasons: expected subcommand \"draft\", \"import\" or \"check\"")
	}
	switch args[0] {
	case "draft":
		return e.reasonsDraft(args[1:])
	case "check":
		return e.reasonsCheck(args[1:])
	case "import":
		return e.reasonsImport(args[1:])
	}
	return usagef("reasons: unknown subcommand %q", args[0])
}

// reasonsDraft asks the configured LLM, once, for a catalog for the current
// target summary (spending from the daily llm budget) and writes it for the
// user to review and edit.
func (e *env) reasonsDraft(args []string) error {
	fs := e.newFlags("reasons draft")
	force := fs.Bool("force", false, "replace an existing catalog (it may hold your edits)")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		return errors.New("reasons draft: no llm provider is configured")
	}
	path := e.reasonsPath()
	if !*force { // refuse before spending, not after
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("reasons draft: %s exists (pass --force to replace it)", path)
		}
	}
	ctx, cancel := signalContext()
	defer cancel()
	p, err := reasonsProvider(ctx, &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr})
	if err != nil {
		return err
	}
	c, err := explain.Draft(ctx, p, explain.Config{Model: cfg.LLM.Model, TargetSummary: cfg.TargetSummary})
	if err != nil {
		return err
	}
	if err := jdreasons.Write(path, c, *force); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "wrote %s: %d fit, %d gap, %d check reasons\nreview or edit it; the next run picks each posting's note from it\n",
		path, len(c.Fit), len(c.Gap), len(c.Check))
	return nil
}

// maxImportBytes caps a draft file read by `vani reasons import`.
const maxImportBytes = 256 << 10

// reasonsImport checks a catalog drafted outside the API (by hand, or in an
// agent session), keys it to the current target summary and writes it. It
// spends nothing and refuses to replace a catalog without --force.
func (e *env) reasonsImport(args []string) error {
	fs := e.newFlags("reasons import")
	force := fs.Bool("force", false, "replace an existing catalog (it may hold your edits)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err.Error()}
	}
	if fs.NArg() != 1 {
		return usagef("reasons import: expected one draft file")
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxImportBytes {
		return fmt.Errorf("reasons import: %s is larger than %d bytes", fs.Arg(0), maxImportBytes)
	}
	d, err := jdreasons.ParseDraft(raw)
	if err != nil {
		return err
	}
	c, err := d.Stamp(cfg.TargetSummary)
	if err != nil {
		return err
	}
	path := e.reasonsPath()
	if err := jdreasons.Write(path, c, *force); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "wrote %s: %d fit, %d gap, %d check reasons for the current target summary\n", path, len(c.Fit), len(c.Gap), len(c.Check))
	return nil
}

// reasonsCheck validates the catalog against the current target summary,
// for after a hand edit.
func (e *env) reasonsCheck(args []string) error {
	if err := parse(e.newFlags("reasons check"), args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	path := e.reasonsPath()
	c, err := jdreasons.Load(path, cfg.TargetSummary)
	if errors.Is(err, jdreasons.ErrStale) {
		return fmt.Errorf("%s: %w; run `vani reasons draft --force`", path, err)
	}
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("no reasons catalog at %s; run `vani reasons draft`", path)
	}
	fmt.Fprintf(e.stdout, "ok: %s: %d fit, %d gap, %d check reasons\n", path, len(c.Fit), len(c.Gap), len(c.Check))
	if c.DraftedBy != "" {
		fmt.Fprintf(e.stdout, "drafted by %s\n", c.DraftedBy)
	}
	return nil
}

// notes builds the digest's "why this role" picker: Jev picks one reason per
// category from the catalog, so no model writes text per posting. It returns
// the explainer and whether the explain stage is switched off:
//   - no catalog, a stale one, or no Jev in the config: off. Scored postings
//     reach the digest without a note.
//   - a catalog that will not load, or Jev configured but not available this
//     run (no key in this shell): paused. Postings stay scored until a run
//     can pick, rather than being marked explained with no note for good.
func (w *wired) notes(d egress.Doer, capUSD *decide.DailyCap) (pipeline.Explainer, bool) {
	c, err := jdreasons.Load(filepath.Join(w.stateDir, jdreasons.FileName), w.cfg.TargetSummary)
	switch {
	case errors.Is(err, jdreasons.ErrStale):
		w.warnf("notes off: %v; run `vani reasons draft --force`", err)
		return nil, true
	case err != nil:
		w.warnf("notes paused: %v (fix it, or check it with `vani reasons check`)", err)
		return nil, false
	case c == nil:
		w.warnf("notes off: no reasons catalog; run `vani reasons draft` to have one drafted")
		return nil, true
	case !w.cfg.OpenRouterConfigured():
		w.warnf("notes off: picking notes needs decide.openrouter_model")
		return nil, true
	}
	var spend decide.Spend
	if capUSD != nil {
		spend = capUSD
	}
	jb, err := jev.New(jev.Config{
		Name:         w.cfg.Decide.OpenRouterName(),
		Endpoint:     notesEndpoint,
		Model:        w.cfg.Decide.OpenRouterModel,
		KeyEnv:       w.cfg.Decide.OpenRouterKeyEnv,
		Descriptions: c.Descriptions(),
		Instructions: jdreasons.Instructions(),
		Spend:        spend,
		SpendKey:     openRouterSpendKey,
	}, d)
	if err != nil {
		w.warnf("notes paused: %v", err)
		return nil, false
	}
	return jdreasons.Picker{D: decide.NewRouter(logErrors(jb, w.warnf, w.backendErrs)), Catalog: *c, TargetSummary: w.cfg.TargetSummary}, false
}
