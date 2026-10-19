package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/resume"
)

// kbStatePath is where kbsync remembers which block hashes DevPrism accepted.
func kbStatePath(stateDir string) string { return filepath.Join(stateDir, "kbsync", "state.json") }

// kbClient is what a real sync needs from DevPrism.
type kbClient interface {
	kbsync.ToolCaller
	Close() error
}

// startKB starts DevPrism for a real sync. Tests replace it with a fake.
var startKB = func(ctx context.Context, cfg resume.Config) (kbClient, error) {
	return resume.Start(ctx, cfg)
}

// rkbRunner runs the rkb binary; nil is kbsync's ExecRunner. Tests replace it.
var rkbRunner kbsync.Runner

func (e *env) cmdKB(args []string) error {
	if len(args) == 0 || args[0] != "sync" {
		return usagef("kb: expected sync")
	}
	return e.kbSync(args[1:])
}

// kbSync copies rkb's bank one way into DevPrism. A dry run reads no live
// DevPrism state and calls no tool: it plans as if DevPrism were empty, so
// every block shows as an add. A real sync reads DevPrism's blocks first,
// never deletes, and never confirms an overwrite DevPrism asks about.
func (e *env) kbSync(args []string) error {
	fs := e.newFlags("kb sync")
	dry := fs.Bool("dry-run", false, "plan only: read no live DevPrism state and send nothing")
	bank := fs.String("bank", "", "rkb bank.json (default kb.bank)")
	rkb := fs.String("rkb", "", "rkb binary (default kb.rkb)")
	codeRoot := fs.String("code-root", "", "folder holding career/ (default kb.code_root)")
	if err := parse(fs, args); err != nil {
		return err
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
	if !*dry && cfg.DevPrism.Binary == "" {
		return errors.New("kb sync: devprism.binary is not configured (use --dry-run to plan without DevPrism)")
	}

	ctx, cancel := signalContext()
	defer cancel()
	b, err := kbsync.Load(bankPath)
	if err != nil {
		return err
	}
	claims, err := kbsync.RKB{Binary: rkbPath, CodeRoot: root, Runner: rkbRunner}.Claims(ctx)
	if err != nil {
		return err
	}
	res := kbsync.ResolveBank(b, claims)
	state, err := kbsync.LoadState(kbStatePath(e.stateDir))
	if err != nil {
		return err
	}

	if *dry {
		plan := kbsync.Plan(res, kbsync.DevPrismState{Blocks: map[string]json.RawMessage{}}, *state, time.Now())
		fmt.Fprintln(e.stdout, "note: planned against an empty DevPrism; live DevPrism state was not read")
		return kbsync.DryRun(e.stdout, plan)
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
	plan := kbsync.Plan(res, current, *state, time.Now())
	out, err := kbsync.Apply(ctx, c, plan, state)
	fmt.Fprintln(e.stdout, "kb sync: "+out.Summary())
	for _, o := range out.Outcomes {
		if o.Status == kbsync.StatusNeedsConfirmation || o.Status == kbsync.StatusBlocked {
			fmt.Fprintf(e.stdout, "  %-18s %s: %s\n", o.Status, o.ID, o.Reason)
		}
	}
	return err
}
