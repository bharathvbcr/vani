package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/track"
)

func (e *env) cmdTrack(args []string) error {
	if len(args) == 0 {
		return usagef("track: expected list, show or use")
	}
	switch args[0] {
	case "list":
		return e.trackList(args[1:])
	case "show":
		return e.trackShow(args[1:])
	case "use":
		return e.trackUse(args[1:])
	}
	return usagef("track: unknown subcommand %q", args[0])
}

// trackArg parses flags and the one positional id argument, and checks the
// id before it reaches a path.
func trackArg(flags *flag.FlagSet, name string, args []string) (string, error) {
	if err := parsePositional(flags, args); err != nil {
		return "", err
	}
	if flags.NArg() != 1 {
		return "", usagef("track %s: expected one track id", name)
	}
	id := flags.Arg(0)
	if !config.ValidTrackID(id) {
		return "", usagef("track %s: %q is not a track id", name, id)
	}
	return id, nil
}

func (e *env) trackList(args []string) error {
	if err := parse(e.newFlags("track list"), args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	dir := track.Dir(cfg, e.stateDir)
	es, err := track.List(dir)
	if err != nil {
		return fmt.Errorf("%w (put track files in %s, or set tracks_dir; see docs/TRACKS.md)", err, dir)
	}
	if len(es) == 0 {
		fmt.Fprintf(e.stdout, "no tracks in %s\n", dir)
		return nil
	}
	bad := 0
	for _, en := range es {
		mark := " "
		if en.ID == cfg.Track {
			mark = "*"
		}
		if en.Err != nil {
			bad++
			fmt.Fprintf(e.stdout, "%s %-32s unreadable: %v\n", mark, en.ID, en.Err)
			continue
		}
		fmt.Fprintf(e.stdout, "%s %-32s %s\n", mark, en.ID, en.Decider.Label)
	}
	if bad > 0 {
		return fmt.Errorf("%d track file(s) in %s could not be read", bad, dir)
	}
	return nil
}

// checked loads and checks one track. It returns the problems found and the
// checks that could not run, which it also prints on stderr: an empty problem
// list is a pass only when notRun is empty too.
func (e *env) checked(cfg config.Config, id string) (d track.Decider, probs, notRun []string, err error) {
	dir := track.Dir(cfg, e.stateDir)
	d, err = track.Load(dir, id)
	if err != nil {
		return track.Decider{}, nil, nil, err
	}
	words, err := track.LoadWords(dir)
	if err != nil {
		return track.Decider{}, nil, nil, err
	}
	if !words.Personal {
		notRun = append(notRun, "personal-name words")
		fmt.Fprintf(e.stderr, "vani: identifying-word check: built-in list only; personal names were NOT checked (no %s in %s)\n", track.WordsFile, dir)
	}
	var bank track.Bank
	if cfg.KB.Bank == "" {
		notRun = append(notRun, "evidence")
		fmt.Fprintln(e.stderr, "vani: evidence check skipped: kb.bank is not set")
	} else if bank, err = track.LoadBank(cfg.KB.Bank); err != nil {
		return track.Decider{}, nil, nil, err
	}
	return d, track.Check(d, words, bank), notRun, nil
}

func (e *env) trackShow(args []string) error {
	id, err := trackArg(e.newFlags("track show"), "show", args)
	if err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	d, probs, notRun, err := e.checked(cfg, id)
	if err != nil {
		return err
	}
	w := e.stdout
	fmt.Fprintf(w, "%s: %s\n", d.ID, d.Label)
	if d.Description != "" {
		fmt.Fprintf(w, "  %s\n", d.Description)
	}
	fmt.Fprintf(w, "\ntarget_summary:\n  %s\n\ndrop_rules:\n", strings.Join(strings.Fields(d.TargetSummary), " "))
	printRules(w, d.DropRules)
	fmt.Fprintf(w, "\nseniority: want %v, stretch %v, drop %v\n", d.Seniority.Want, d.Seniority.Stretch, d.Seniority.Drop)
	fmt.Fprintf(w, "employer preferences (rank only, never drop): %v\n", d.EmployerPreferences.Prefer)
	if len(d.WatchlistSearch) > 0 {
		fmt.Fprintf(w, "watchlist searches: %s\n", strings.Join(d.WatchlistSearch, "; "))
	}
	for _, g := range d.Gaps {
		fmt.Fprintf(w, "gap: %s\n", g)
	}
	return problemsErr(w, probs, notRun)
}

// problemsErr prints the verdict. "ok" is printed only when every check ran
// and found nothing; a check that did not run is "incomplete" and an error.
func problemsErr(w io.Writer, probs, notRun []string) error {
	if len(probs) > 0 {
		fmt.Fprintln(w, "\ncheck failed:")
		for _, p := range probs {
			fmt.Fprintf(w, "  - %s\n", p)
		}
		return fmt.Errorf("track has %d problem(s)", len(probs))
	}
	if len(notRun) > 0 {
		fmt.Fprintf(w, "\ncheck: incomplete (not run: %s)\n", strings.Join(notRun, ", "))
		return fmt.Errorf("track check incomplete: not run: %s", strings.Join(notRun, ", "))
	}
	fmt.Fprintln(w, "\ncheck: ok")
	return nil
}

func printRules(w io.Writer, rules []config.DropRule) {
	if len(rules) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, r := range rules {
		fmt.Fprintf(w, "  %s in %v (openrouter p >= %v, lappi >= %v)\n", r.Slot, r.Values, r.MinOpenRouterProb, r.MinLappiScore)
	}
}

func (e *env) trackUse(args []string) error {
	flags := e.newFlags("track use")
	dryRun := flags.Bool("dry-run", false, "show the change without saving the config")
	allowUnchecked := flags.Bool("allow-unchecked", false, "apply the track although a check could not run (its target_summary enters every triage prompt unchecked)")
	id, err := trackArg(flags, "use", args)
	if err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	d, probs, notRun, err := e.checked(cfg, id)
	if err != nil {
		return err
	}
	if len(probs) > 0 {
		_ = problemsErr(e.stdout, probs, notRun)
		return fmt.Errorf("track %s not applied: %d problem(s); the config is unchanged", id, len(probs))
	}
	if len(notRun) > 0 && !*allowUnchecked {
		return fmt.Errorf("track %s not applied: check did not run: %s; the config is unchanged (make the check runnable, or pass --allow-unchecked)", id, strings.Join(notRun, ", "))
	}
	// The track applied before, so its rules on slots this track does not
	// cover can be removed too. Unreadable means those rules are kept.
	var prev *track.Decider
	if cfg.Track != "" {
		if p, err := track.Load(track.Dir(cfg, e.stateDir), cfg.Track); err != nil {
			fmt.Fprintf(e.stderr, "vani: previous track %s unreadable (%v); rules it set on slots %s does not cover are kept\n", cfg.Track, err, id)
		} else {
			prev = &p
		}
	}
	out, ch, err := track.Apply(cfg, d, prev)
	if err != nil {
		return fmt.Errorf("track %s not applied: %w", id, err)
	}
	w := e.stdout
	fmt.Fprintf(w, "track %s: %s\nreplaced drop rules:\n", d.ID, d.Label)
	printRules(w, ch.Removed)
	fmt.Fprintln(w, "kept drop rules (track-independent):")
	printRules(w, ch.Kept)
	fmt.Fprintln(w, "added drop rules:")
	printRules(w, ch.Added)
	if old := strings.Join(strings.Fields(cfg.TargetSummary), " "); old != "" && old != strings.Join(strings.Fields(out.TargetSummary), " ") {
		origin := "set by track " + cfg.Track
		if cfg.Track == "" {
			origin = "written by hand, not by a track"
		}
		fmt.Fprintf(w, "target_summary replaced; the previous one (%s) was:\n  %s\n", origin, old)
	}
	fmt.Fprintf(w, "employer_preferences = %v\n", out.EmployerPreferences)
	fmt.Fprintf(w, "title_signals (include/exclude order triage; read drops only through a drop rule) = %s\n", out.TitleSignals)
	if *dryRun {
		fmt.Fprintln(w, "dry run: config not saved")
		return nil
	}
	if err := config.Save(e.configPath, out); err != nil {
		return err
	}
	fmt.Fprintf(w, "saved %s\n", e.configPath)
	if len(out.EmployerPreferences) > 0 {
		// Existence only: loading the index here would cost a full load for
		// a warning. A corrupt index still fails loudly on the next run.
		if _, err := os.Stat(employerIndexPath(e.stateDir)); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(e.stderr, "vani: employer_preferences set but no employer lists are imported; no rank boost until `vani employer import` (docs/EMPLOYER.md)")
		} else if err != nil {
			return err
		}
	}
	return nil
}
