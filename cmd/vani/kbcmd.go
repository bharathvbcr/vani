package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/Vani/internal/rkbexec"
)

// kbStatePath is where kbsync remembers which block hashes DevPrism accepted.
func kbStatePath(stateDir string) string { return filepath.Join(stateDir, "kbsync", "state.json") }

// kbBackupDir is where a --replace run keeps its dated copies of career.db.
func kbBackupDir(stateDir string) string { return filepath.Join(stateDir, "kbsync", "backups") }

// kbClient is what a real sync needs from DevPrism.
type kbClient interface {
	kbsync.ToolCaller
	Close() error
}

// startKB starts DevPrism for a real sync. Tests replace it with a fake.
var startKB = func(ctx context.Context, cfg resume.Config) (kbClient, error) {
	return resume.Start(ctx, cfg)
}

// rkbRunner runs the rkb binary; nil is rkbexec.ExecRunner. Tests replace it.
var rkbRunner rkbexec.Runner

// careerBackup copies DevPrism's career.db into dir and verifies the copy. Tests
// replace it.
var careerBackup = func(ctx context.Context, db, dir string, at time.Time) (kbsync.BackupInfo, error) {
	return kbsync.Backuper{}.Backup(ctx, db, dir, at)
}

// kbDeadline bounds a whole run that talks to DevPrism: every call has its own
// timeout, this caps the sum.
var kbDeadline = 15 * time.Minute

// defaultCareerDB is where DevPrism keeps career.db: its data dir is the
// platform config dir under HOME, nothing else (docs/PHASE0-devprism.md).
func defaultCareerDB() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("kb sync: cannot find DevPrism's data dir (%v); pass --career-db", err)
	}
	return filepath.Join(dir, "DevPrism", "career.db"), nil
}

func (e *env) cmdKB(args []string) error {
	if len(args) == 0 || args[0] != "sync" {
		return usagef("kb: expected sync")
	}
	return e.kbSync(args[1:])
}

// maxReportedIDs caps how many ids a report lists; the count is always printed.
const maxReportedIDs = 40

// kbSync copies the approved part of rkb's bank one way into DevPrism. What is
// approved is decided by kbsync.ResolvePool: only bullets that a kb.selections
// file lists and that pass the eligibility rule (Eligible) are sent, so
// superseded wordings, unreviewed drafts and CAUTION claims never reach
// DevPrism's ranking. With no kb.selections nothing is sent.
//
// A dry run without --replace reads no live DevPrism state and calls no tool:
// it plans as if DevPrism were empty, so every block shows as an add. A real
// sync reads DevPrism's blocks first, never deletes, and confirms an overwrite
// DevPrism asks about only with --confirm, which prints what each one drops
// before confirming it; without the flag such a block is reported as needing
// confirmation and left alone.
//
// --replace also deletes the DevPrism blocks the approved pool does not
// produce (replace.go lists the rules). It copies career.db first, upserts the
// pool, and deletes only if every pool block landed. With --dry-run it reads
// DevPrism's block list (career_get_profile, which changes nothing) and prints
// what it would delete and spare; it calls no mutating tool and makes no copy.
func (e *env) kbSync(args []string) error {
	fs := e.newFlags("kb sync")
	dry := fs.Bool("dry-run", false, "plan only: send nothing (with --replace, read DevPrism's block list to show what would be deleted)")
	bank := fs.String("bank", "", "rkb bank.json (default kb.bank)")
	rkb := fs.String("rkb", "", "rkb binary (default kb.rkb)")
	codeRoot := fs.String("code-root", "", "folder holding career/ (default kb.code_root)")
	confirm := fs.Bool("confirm", false, "confirm the overwrites DevPrism asks about in this run, after printing what each drops or rewrites")
	replace := fs.Bool("replace", false, "also delete the DevPrism blocks the approved pool does not produce, after copying career.db and syncing the pool")
	keep := fs.String("keep", "", "with --replace: comma-separated DevPrism block ids to spare")
	allKinds := fs.Bool("delete-all-kinds", false, "with --replace: also delete orphans of kinds the sync does not write (publication, certification, ...)")
	maxDelete := fs.Int("max-delete", kbsync.DefaultMaxDeletes, fmt.Sprintf("with --replace: refuse to delete more than this many blocks (1..%d)", kbsync.MaxDeletesCeiling))
	careerDB := fs.String("career-db", "", "with --replace: DevPrism's career.db to copy first (default: DevPrism's data dir under HOME)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *dry && *confirm {
		return usagef("kb sync: --confirm cannot be used with --dry-run, which sends nothing")
	}
	if !*replace {
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		for _, n := range []string{"keep", "delete-all-kinds", "max-delete", "career-db"} {
			if set[n] {
				return usagef("kb sync: --%s needs --replace", n)
			}
		}
	}
	if *maxDelete < 1 || *maxDelete > kbsync.MaxDeletesCeiling {
		return usagef("kb sync: --max-delete %d is outside 1..%d", *maxDelete, kbsync.MaxDeletesCeiling)
	}
	keepIDs := map[string]bool{}
	for _, id := range strings.Split(*keep, ",") {
		if id = strings.TrimSpace(id); id != "" {
			keepIDs[id] = true
		}
	}
	if *careerDB != "" && !filepath.IsAbs(*careerDB) {
		return usagef("kb sync: --career-db %q must be an absolute path", *careerDB)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	pick := func(flagVal, cfgVal string) string {
		if flagVal != "" {
			return flagVal
		}
		return cfgVal
	}
	bankPath, rkbPath := pick(*bank, cfg.KB.Bank), pick(*rkb, cfg.KB.RKB)
	root := pick(*codeRoot, cfg.KB.CodeRoot)
	if bankPath == "" || rkbPath == "" {
		return usagef("kb sync: needs --bank and --rkb (or kb.bank and kb.rkb in the config)")
	}
	for name, p := range map[string]string{"--bank": bankPath, "--rkb": rkbPath, "--code-root": root} {
		if p != "" && !filepath.IsAbs(p) {
			return usagef("kb sync: %s %q must be an absolute path", name, p)
		}
	}
	livePlan := !*dry || *replace // reads DevPrism's blocks
	if livePlan && cfg.DevPrism.Binary == "" {
		if *dry {
			return errors.New("kb sync: --replace --dry-run reads DevPrism's block list, so devprism.binary must be configured")
		}
		return errors.New("kb sync: devprism.binary is not configured (use --dry-run to plan without DevPrism)")
	}
	if len(cfg.KB.Selections) == 0 {
		return usagef("kb sync: kb.selections is empty; list the rkb selection files in use (resumes/*.json) so the sync knows which bullets are approved. It never falls back to the whole bank")
	}

	ctx, cancel := signalContext()
	defer cancel()
	if livePlan {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, kbDeadline)
		defer stop()
	}
	b, err := kbsync.Load(bankPath)
	if err != nil {
		return err
	}
	sels, err := kbsync.LoadSelections(cfg.KB.Selections) // one bad file stops the run: it must not shrink the pool quietly
	if err != nil {
		return err
	}
	claims, err := kbsync.RKB{Binary: rkbPath, CodeRoot: root, Runner: rkbRunner}.Claims(ctx)
	if err != nil {
		return err
	}
	pool, err := kbsync.ResolvePool(b, claims, sels)
	if err != nil {
		return err
	}
	res := pool.Resolved
	state, err := kbsync.LoadState(kbStatePath(e.stateDir))
	if err != nil {
		return err
	}

	if *dry && !*replace {
		plan := kbsync.Plan(res, kbsync.DevPrismState{Blocks: map[string]json.RawMessage{}}, *state, time.Now())
		fmt.Fprintln(e.stdout, "note: planned against an empty DevPrism; live DevPrism state was not read")
		e.reportPool(pool, true)
		return kbsync.DryRun(e.stdout, plan)
	}
	e.reportPool(pool, *dry)

	// The copy comes before DevPrism is started, so nothing of ours is writing
	// while it is taken, and before anything is sent.
	var backup kbsync.BackupInfo
	if *replace && !*dry {
		db := *careerDB
		if db == "" {
			if db, err = defaultCareerDB(); err != nil {
				return err
			}
		}
		if backup, err = careerBackup(ctx, db, kbBackupDir(e.stateDir), now()); err != nil {
			return fmt.Errorf("kb sync: no career.db backup, so nothing was changed: %w", err)
		}
		fmt.Fprintf(e.stdout, "backup: %s (%d blocks, %d bytes)\n", backup.Path, len(backup.BlockIDs), backup.Bytes)
		if backup.PruneErr != nil {
			fmt.Fprintf(e.stdout, "note: could not prune older backups: %v\n", backup.PruneErr)
		}
	}

	c, err := startKB(ctx, resume.Config{BinaryPath: cfg.DevPrism.Binary, PinnedToolsHash: cfg.DevPrism.ToolsHash})
	if err != nil {
		return fmt.Errorf("devprism: %w", err)
	}
	defer c.Close()
	current, err := kbsync.FetchState(ctx, c)
	if err != nil {
		return err
	}
	if *replace && !*dry {
		if err := kbsync.CheckBackupMatches(backup, current); err != nil {
			return fmt.Errorf("kb sync: %w", err)
		}
	}
	plan := kbsync.Plan(res, current, *state, time.Now())
	var rp kbsync.ReplacePlan
	var guard error
	if *replace {
		if rp, err = kbsync.PlanReplace(plan, current, kbsync.ReplaceOptions{Keep: keepIDs, AllKinds: *allKinds}); err != nil {
			return err
		}
		guard = rp.Check(plan, *maxDelete)
	}
	if *dry {
		if err := kbsync.DryRunReplace(e.stdout, plan, rp); err != nil {
			return err
		}
		return guard
	}
	if guard != nil {
		return guard
	}

	var confirmer kbsync.Confirmer
	if *confirm {
		confirmer = e.showLoss
	}
	out, err := kbsync.Apply(ctx, c, plan, state, confirmer)
	if err != nil {
		// The run stopped early: the counts are only what was done before it.
		fmt.Fprintln(e.stdout, "kb sync: stopped early; partial result so far: "+out.Summary())
	} else {
		fmt.Fprintln(e.stdout, "kb sync: "+out.Summary())
	}
	for _, o := range out.Outcomes {
		switch o.Status {
		case kbsync.StatusNeedsConfirmation, kbsync.StatusBlocked, kbsync.StatusConfirmRejected, kbsync.StatusStale, kbsync.StatusVariant:
			fmt.Fprintf(e.stdout, "  %-18s %s: %s\n", o.Status, o.ID, o.Reason)
		}
	}
	if err != nil || !*replace {
		return err
	}
	if err := out.Landed(); err != nil {
		return err
	}
	return e.kbDelete(ctx, c, rp, state, current)
}

// kbDelete deletes a replace run's orphans. --replace is the consent for the
// list printed here and no other: each block named is confirmed with DevPrism
// when it asks, a block it did not name never is.
func (e *env) kbDelete(ctx context.Context, c kbClient, rp kbsync.ReplacePlan, state *kbsync.SyncState, before kbsync.DevPrismState) error {
	fmt.Fprintf(e.stdout, "kb sync --replace: deleting %d blocks, sparing %d\n", len(rp.Delete), len(rp.Keep))
	for _, d := range rp.Delete {
		fmt.Fprintf(e.stdout, "  delete  %s [%s] %q (%d bullets, %d facts)\n", d.ID, d.Kind, d.Title, d.Bullets, d.Facts)
	}
	for _, k := range rp.Keep {
		fmt.Fprintf(e.stdout, "  spared  %s [%s] %q: %s\n", k.ID, k.Kind, k.Title, k.Why)
	}
	out, err := kbsync.ApplyDeletes(ctx, c, rp, state, func(string, kbsync.Loss) bool { return true })
	if err != nil {
		fmt.Fprintln(e.stdout, "kb sync --replace: stopped early; partial result so far: "+out.Summary())
	} else {
		fmt.Fprintln(e.stdout, "kb sync --replace: "+out.Summary())
	}
	for _, o := range out.Outcomes {
		if o.Status != kbsync.StatusDeleted {
			fmt.Fprintf(e.stdout, "  %-18s %s: %s\n", o.Status, o.ID, o.Reason)
		}
	}
	if err != nil {
		return err
	}
	// Read DevPrism back: the report is what it holds, not what it said it did.
	after, ferr := kbsync.FetchState(ctx, c)
	if ferr != nil {
		return fmt.Errorf("kb sync --replace: could not read DevPrism back: %w", ferr)
	}
	fmt.Fprintf(e.stdout, "DevPrism now holds %d blocks (it held %d)\n", len(after.Blocks), len(before.Blocks))
	// A block left in place is not the outcome asked for, so the run does not
	// exit as a success.
	if left := out.NeedsConfirmation + out.Rejected + out.Stale; left > 0 {
		return fmt.Errorf("kb sync --replace: %d block(s) were not deleted (see above); run it again", left)
	}
	return nil
}

// reportPool prints what the approved pool is. verbose adds, capped, the
// listed entries that are withheld and the listed bullets that failed the rule,
// with ids and reasons only (never bullet text).
func (e *env) reportPool(p kbsync.Pool, verbose bool) {
	fmt.Fprintln(e.stdout, p.Summary())
	if !verbose {
		return
	}
	for i, w := range p.Withheld {
		if i == maxReportedIDs {
			fmt.Fprintf(e.stdout, "  ... and %d more withheld entries\n", len(p.Withheld)-i)
			break
		}
		fmt.Fprintf(e.stdout, "  withheld entry  %s: %s\n", w.Entry, w.Why)
	}
	ids := p.Eligible.Ineligible()
	fmt.Fprintf(e.stdout, "  %d bullets a selection lists are not eligible\n", len(ids))
	for i, id := range ids {
		if i == maxReportedIDs {
			fmt.Fprintf(e.stdout, "  ... and %d more ineligible bullets\n", len(ids)-i)
			break
		}
		fmt.Fprintf(e.stdout, "  ineligible      %s: %s\n", id, p.Eligible.Why[id])
	}
}

// maxLossText caps each bullet or fact text showLoss prints.
const maxLossText = 160

// showLoss is the --confirm Confirmer: it prints exactly what DevPrism says
// the overwrite of one block would drop or rewrite, then confirms it. The
// flag is the consent; it covers only the blocks DevPrism asks about in this
// run, and each is printed before its confirmation is sent.
func (e *env) showLoss(id string, loss kbsync.Loss) bool {
	fmt.Fprintf(e.stdout, "confirming %s; DevPrism will discard:\n", id)
	show := func(what string, items []kbsync.LostItem) {
		for _, it := range items {
			text := it.Text
			if text == "" {
				text = "(no text in DevPrism's copy)"
			}
			if r := []rune(text); len(r) > maxLossText {
				text = string(r[:maxLossText]) + "..."
			}
			fmt.Fprintf(e.stdout, "  %s %s: %q\n", what, it.ID, text)
		}
	}
	show("dropped bullet", loss.DroppedBullets)
	show("dropped fact", loss.DroppedFacts)
	show("locked bullet rewritten, was", loss.LockedRewritten)
	return true
}
