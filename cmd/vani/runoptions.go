package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A run is started by the daemon's schedule or by hand (`vani run-once`, which
// the Mac app's Pipeline tab starts). The run record says which, and what a
// manual run changed, so a run that listed three boards is never read as the
// whole watchlist's.
const (
	triggerSchedule = "schedule"
	triggerManual   = "manual"
)

// maxRunBoards bounds --board: a selection longer than this is the whole
// watchlist, and the run record carries the selection.
const maxRunBoards = 500

// runOptions are a manual run's overrides (run-once's flags). The zero value is
// the run as the config describes it, which is the run the daemon makes.
type runOptions struct {
	// Advance caps this run's advance phase (triage, score, explain); 0 keeps
	// the config's advance_per_run.
	Advance int `json:"advance,omitempty"`
	// Workers sets how many postings this run's advance phase advances in
	// parallel (1..16); 0 keeps the config's advance_concurrency.
	Workers int `json:"workers,omitempty"`
	// Boards limits the listing phase to these watchlist boards, as "ats:token";
	// empty lists every board. Postings of the boards left out are untouched.
	Boards []string `json:"boards,omitempty"`
	// Companies, States, Areas, Remote and OrRemote limit the advance phase
	// to the postings in that scope (jobview.Scope, read as `jobs list` reads
	// the flags of the same names), already folded: company keys, USPS codes
	// and areas, sorted. The postings outside it are untouched and wait for a
	// later run. None set advances every posting waiting for a stage.
	Companies []string `json:"companies,omitempty"`
	States    []string `json:"states,omitempty"`
	Areas     []string `json:"areas,omitempty"`
	Remote    bool     `json:"remote,omitempty"`
	OrRemote  bool     `json:"or_remote,omitempty"`
	// ListOnly lists and reconciles boards and skips the advance phase: nothing
	// is triaged, scored or explained, and no decision model is called, so none
	// needs to be configured (the runner is built without a backend, wire.go
	// runnerFor). Title drops, exclusions, near-duplicates and logos still run.
	ListOnly bool `json:"list_only,omitempty"`
	// NoDigest skips the digest and the knowledge-base warm made for it. The
	// run's roles stay pending and the next digest lists them.
	NoDigest bool `json:"no_digest,omitempty"`
	// USFirst advances postings in the United States before the rest this run,
	// as the config's advance_us_first does for every run. It only orders.
	USFirst bool `json:"us_first,omitempty"`
}

func (o runOptions) isZero() bool {
	return o.Advance == 0 && o.Workers == 0 && len(o.Boards) == 0 && !o.ListOnly && !o.NoDigest && !o.USFirst && !o.scoped()
}

// recorded is the options as the run record keeps them: nil for none.
func (o runOptions) recorded() *runOptions {
	if o.isZero() {
		return nil
	}
	return &o
}

// tunes reports whether the options change the pipeline itself (App.Tune).
func (o runOptions) tunes() bool {
	return o.Advance != 0 || o.Workers != 0 || o.ListOnly || o.USFirst || o.scoped()
}

// runOptionFlags registers a manual run's flags on fs (run-once and
// run-request take the same ones). The boards land in the second value; the
// caller copies them into the options after parsing.
func runOptionFlags(fs *flag.FlagSet) (*runOptions, *boardRefs) {
	var opts runOptions
	var boards boardRefs
	fs.IntVar(&opts.Advance, "advance", 0, fmt.Sprintf("triage, score and explain at most N postings this run (1..%d; default advance_per_run)", config.MaxAdvancePerRun))
	fs.IntVar(&opts.Workers, "workers", 0, fmt.Sprintf("advance up to N postings in parallel this run (1..%d; default advance_concurrency)", config.MaxAdvanceConcurrency))
	fs.Var(&boards, "board", "list only this watchlist board, as ats:token (repeatable; default every board)")
	cmd := fs.Name()
	fs.Var(scopeFlag{&opts.Companies, func(raw string) (map[string]bool, error) { return companySet(cmd, raw) }}, "company",
		"triage, score and explain only these companies' postings (comma list of exact names, any case, as jobs list --company takes them; repeatable)")
	fs.Var(scopeFlag{&opts.States, func(raw string) (map[string]bool, error) { return stateSet(cmd, raw) }}, "state",
		"triage, score and explain only postings in these US states (comma list of codes or names; none = no state listed; as jobs list --state)")
	fs.Var(scopeFlag{&opts.Areas, func(raw string) (map[string]bool, error) { return areaSet(cmd, raw) }}, "area",
		"triage, score and explain only postings in these areas, as the location names them (comma list; none = no area listed; as jobs list --area)")
	fs.BoolVar(&opts.Remote, "remote", false, "triage, score and explain only postings the board lists as remote (as jobs list --remote)")
	fs.BoolVar(&opts.OrRemote, "or-remote", false, "with --state or --area, also advance postings the board lists as remote")
	fs.BoolVar(&opts.ListOnly, "list-only", false, "list and reconcile boards only: no triage, scoring or explaining (no decision backend is called, and none needs to be configured)")
	fs.BoolVar(&opts.NoDigest, "no-digest", false, "write no digest: the run's roles wait for the next one")
	fs.BoolVar(&opts.USFirst, "us-first", false, "advance postings in the United States before the rest (as advance_us_first does); it only orders")
	return &opts, &boards
}

// boardRefs is a repeatable --board flag.
type boardRefs []string

func (b *boardRefs) String() string { return strings.Join(*b, ",") }

func (b *boardRefs) Set(v string) error {
	if len(*b) >= maxRunBoards {
		return fmt.Errorf("at most %d --board flags", maxRunBoards)
	}
	if _, _, err := parseBoardRef(v); err != nil {
		return err
	}
	*b = append(*b, v)
	return nil
}

// parseBoardRef splits "ats:token" at the first colon: an ATS code has none,
// and a token (a Workday "tenant/site") may hold anything else.
func parseBoardRef(ref string) (posting.ATS, string, error) {
	ats, token, ok := strings.Cut(ref, ":")
	ats, token = strings.TrimSpace(ats), strings.TrimSpace(token)
	if !ok || ats == "" || token == "" {
		return "", "", fmt.Errorf("board %q is not ats:token", ref)
	}
	// ATS codes are lower case; tokens compare ignoring case (sameBoard).
	return posting.ATS(strings.ToLower(ats)), token, nil
}

// errBoardSelection is a --board selection the watchlist cannot satisfy: the
// run stops before listing anything rather than list more than was asked.
var errBoardSelection = errors.New("board selection")

// selectBoards returns the watchlist boards refs name, in watchlist order and
// each once. A ref naming no board is an error, never a smaller run.
func selectBoards(watchlist []sources.Board, refs []string) ([]sources.Board, error) {
	want := make([]sources.Board, 0, len(refs))
	for _, ref := range refs {
		ats, token, err := parseBoardRef(ref)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errBoardSelection, err)
		}
		probe := sources.Board{ATS: ats, Token: token}
		found := false
		for _, b := range watchlist {
			if sameBoard(b, probe) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s is not on the watchlist", errBoardSelection, ref)
		}
		want = append(want, probe)
	}
	var out []sources.Board
	for _, b := range watchlist {
		for _, w := range want {
			if sameBoard(b, w) {
				out = append(out, b)
				break
			}
		}
	}
	return out, nil
}

// checkRunOptions refuses options no run can honour, before anything starts.
func checkRunOptions(o runOptions, watchlist []sources.Board) error {
	if o.Advance < 0 || o.Advance > config.MaxAdvancePerRun {
		return usagef("--advance %d must be within 0..%d (0 means advance_per_run)", o.Advance, config.MaxAdvancePerRun)
	}
	if o.Workers < 0 || o.Workers > config.MaxAdvanceConcurrency {
		return usagef("--workers %d must be within 0..%d (0 means advance_concurrency)", o.Workers, config.MaxAdvanceConcurrency)
	}
	if o.ListOnly && o.Advance != 0 {
		return usagef("--list-only skips the advance phase; --advance has nothing to cap")
	}
	if o.ListOnly && o.Workers != 0 {
		return usagef("--list-only skips the advance phase; --workers has nothing to run")
	}
	if o.ListOnly && o.USFirst {
		return usagef("--list-only skips the advance phase; --us-first has nothing to order")
	}
	if err := checkScope(o); err != nil {
		return err
	}
	if len(o.Boards) > 0 {
		if _, err := selectBoards(watchlist, o.Boards); err != nil {
			return usagef("%v", err)
		}
	}
	return nil
}
