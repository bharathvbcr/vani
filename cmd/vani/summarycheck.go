package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/Vani/internal/track"
)

// config.target_summary enters every model prompt the pipeline builds (triage,
// reasons, drafts, the teacher and the bench). A track's summary is checked
// against the owner's identifying words when it is applied (track.Check), but
// a summary written straight into the config was not. checkPromptSummary is
// that check for the effective summary, whichever way it got there, with the
// same matcher (track.Words.Found), and every place that builds a prompt from
// the config calls it: wireApp and reloadConfig for the run, the daemon and
// the other pipeline commands, loadPromptConfig for the commands that talk to
// a model on their own. It only narrows what is sent: a refusal sends nothing,
// a pass sends what was already sent.

// errTargetSummary marks a refusal to build model prompts from target_summary.
// Its messages never quote a matched term or any part of the summary.
var errTargetSummary = errors.New("config target_summary")

// errMotivationNotes marks a refusal to send motivation_notes to a model. Its
// messages never quote a matched term or any part of the note.
var errMotivationNotes = errors.New("config motivation_notes")

// checkMotivationNotes returns nil when notes may enter the drafter's prompt.
// The owner's identifying-words file is not applied (drafts keep project
// names), but the built-in list is: contact handles and links (@, linkedin,
// github) and work-authorization wording (visa, h-1b, opt, sponsorship,
// nonprofit, cap-exempt...) never go to a model. A note that names one is
// refused, so the owner rewords it; an innocent use of one of those words is
// refused too, which is the price of one matcher for every text bound for a
// provider. It only narrows what is sent.
func checkMotivationNotes(notes string) error {
	if track.Blank(notes) {
		return nil
	}
	n := len(track.BuiltinWords().Found(notes))
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%w: contains %d term(s) from the built-in list (contact handles or links, or work-authorization wording; the terms are not shown); the note is sent to the drafter's model. Reword it without them (vani config motivation --set)", errMotivationNotes, n)
}

// checkPromptSummary returns nil when cfg.TargetSummary may enter a model
// prompt. It fails closed: an unreadable identifying-words list, and a missing
// or term-less one unless allowMissing (--allow-missing-identifying-words, the
// flag the kb_match path already takes; the built-in list is still applied),
// refuse as a found term does. An empty summary carries nothing to check.
func checkPromptSummary(cfg config.Config, stateDir string, allowMissing bool) error {
	if track.Blank(cfg.TargetSummary) {
		return nil
	}
	dir := track.Dir(cfg, stateDir)
	w, err := track.LoadWords(dir)
	if err != nil {
		return fmt.Errorf("%w: the identifying-words list cannot be read (%v), so the summary cannot be checked; no model prompt is built from it until the list can be read", errTargetSummary, err)
	}
	if !w.Personal && !allowMissing {
		state := "has no terms"
		if w.Missing {
			state = "does not exist"
		}
		return fmt.Errorf("%w: %s %s in %s, so the owner's identifying terms cannot be checked against the summary; no model prompt is built from it (add the file, or pass --%s where the command takes it)",
			errTargetSummary, track.WordsFile, state, dir, kbAllowMissingFlag)
	}
	if n := len(w.Found(cfg.TargetSummary)); n > 0 {
		return fmt.Errorf("%w: contains %d term(s) from the identifying-words list (the terms are not shown); no model prompt is built from it. Edit target_summary in the config to describe the roles without them, or apply a track whose summary is clean (vani track apply)",
			errTargetSummary, n)
	}
	return nil
}

// loadPromptConfig is loadConfig for a command that builds a model prompt from
// the config: it refuses, before anything is built or sent, a config whose
// target_summary fails checkPromptSummary.
func (e *env) loadPromptConfig(allowMissing bool) (config.Config, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return cfg, err
	}
	if err := checkPromptSummary(cfg, e.stateDir, allowMissing); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// genericTargetNote is what status and the run say when no target_summary is
// set: the semantic ranking still runs, against the generic default target.
func genericTargetNote(cfg config.Config) string {
	return fmt.Sprintf("target_summary is empty, so ranking compares postings with the generic default target (%q), not your roles; set target_summary in the config, or apply a track (vani track apply)",
		semantic.DefaultTarget)
}

// targetSummaryFacet is status's answer for config.target_summary: whether a
// model prompt may be built from it, and whether it is the generic default.
type targetSummaryFacet struct {
	facet
	// GenericDefault is true when target_summary is empty and the ranking uses
	// the generic default target.
	GenericDefault bool `json:"generic_default,omitempty"`
}

func (e *env) targetSummaryFacetOf(cfg config.Config, cfgErr error) targetSummaryFacet {
	switch {
	case cfgErr != nil:
		return targetSummaryFacet{facet: facet{Reason: "not checked: the config could not be read"}}
	case track.Blank(cfg.TargetSummary):
		return targetSummaryFacet{facet: facet{OK: true, Reason: genericTargetNote(cfg)}, GenericDefault: true}
	}
	if err := checkPromptSummary(cfg, e.stateDir, false); err != nil {
		return targetSummaryFacet{facet: facet{Reason: oneLine(err.Error(), 600)}}
	}
	return targetSummaryFacet{facet: facet{OK: true}}
}

// kbFacet is status's answer for the knowledge-base bank's age. The only
// timestamp the bank carries is the modification time of the bank file (its
// JSON records none, and kb sync's state is about DevPrism blocks, not the
// bank), so that is what it reports: a bank file copied or touched without a
// real edit reads as fresh.
type kbFacet struct {
	facet
	Modified      time.Time `json:"modified,omitzero"`
	AgeDays       *int      `json:"age_days,omitempty"`
	StaleAfterDays int       `json:"stale_after_days,omitempty"`
	Stale         bool      `json:"stale,omitempty"`
}

// kbFacetOf is nil when no bank is configured: there is nothing to be stale.
func kbFacetOf(cfg config.Config, at time.Time) *kbFacet {
	if cfg.KB.Bank == "" {
		return nil
	}
	st, err := os.Stat(cfg.KB.Bank)
	if err != nil {
		return &kbFacet{facet: facet{Reason: "the bank file (kb.bank) could not be read, so its age is unknown: " + oneLine(err.Error(), 200)}}
	}
	mod := st.ModTime()
	age := max(at.Sub(mod), 0)
	days := int(age / (24 * time.Hour))
	limit := cfg.KBStaleAfter()
	f := &kbFacet{facet: facet{OK: true}, Modified: mod.UTC(), AgeDays: &days, StaleAfterDays: int(limit / (24 * time.Hour))}
	if age > limit {
		f.OK, f.Stale = false, true
		f.Reason = fmt.Sprintf("the knowledge base (kb.bank) was last written %d days ago, over the %d-day limit (kb.stale_after_days); the profile ranking uses it. Update it with rkb, or raise the limit",
			days, f.StaleAfterDays)
	}
	return f
}

// profileLines are the lines `vani status` adds about the target summary and
// the knowledge base: an error for a summary that may not enter a prompt, a
// warning for the generic default target or a stale bank.
func (e *env) profileLines(cfg config.Config, at time.Time) []string {
	var out []string
	switch t := e.targetSummaryFacetOf(cfg, nil); {
	case t.GenericDefault:
		out = append(out, "warning: "+t.Reason)
	case !t.OK:
		out = append(out, "error: "+t.Reason)
	}
	if k := kbFacetOf(cfg, at); k != nil && !k.OK {
		out = append(out, "warning: "+k.Reason)
	}
	return out
}
