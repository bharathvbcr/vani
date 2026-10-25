package rkbresume

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Render and Pool's own codes and bounds.
const (
	// CodeEdit means the owner's edit is not an edit this package accepts: not the
	// edit shape, a bullet that is not on offer, a section or entry that cannot go
	// where it was put, an override that breaks the page-safety rules.
	CodeEdit = "bad_edit"

	// RenderDeadline bounds Render without fit (export, lint, render);
	// RenderFitDeadline adds rkb's fit. PoolDeadline bounds Pool. Each applies when
	// Config.Deadline is zero.
	RenderDeadline    = 100 * time.Second
	RenderFitDeadline = 260 * time.Second
	PoolDeadline      = 45 * time.Second
)

// Render builds the resume the owner edited. No model is used (cfg.Provider is
// ignored and never called). edit is the owner-edit JSON (Result.SelectionJSON is
// one): it is read as strictly as a model's reply, judged against a fresh pool,
// so eligibility is the live bank's, and held to the page-safety rules every
// choice obeys (ownerChoice), though not to the caps that steer the model.
//
// A bullet the edit names that is no longer eligible is refused with CodeIneligible
// and its id, never dropped: the owner decides what replaces it.
//
// fit=true runs rkb's fit as a build does and returns the PDF (Pages, FitLines,
// Checks.Fit). fit=false stops after rkb has rendered the page: no PDF, Pages 0,
// Checks.Fit "not_run", and Result.HTMLPath is rkb's page inside the folder
// Result.BuildDir, which the caller must remove (os.RemoveAll) when it is done:
// the page holds the owner's header. Only that page remains in it (the bank copy
// and the selection, which carries the header, are removed). The work-folder
// sweep removes a forgotten folder after MaxDeadline, but only when cfg.WorkDir
// is set; without it the folder is in the system temp folder and is never swept.
// Either way Source is SourceEdited.
//
// The build lock is taken as Build takes it; the Chrome lock only when fitting.
// The Result of an error carries what ran (Checks) and the fingerprints.
func Render(ctx context.Context, cfg Config, req Request, edit []byte, fit bool) (Result, error) {
	cfg.Provider, cfg.NoModel, cfg.Model = nil, "", ""
	if cfg.Deadline == 0 {
		cfg.Deadline = RenderDeadline
		if fit {
			cfg.Deadline = RenderFitDeadline
		}
	}
	cfg, nerr := cfg.normalize()
	if nerr != nil {
		return nothing(), nerr
	}
	// The edit's shape and overrides are judged before a lock is taken or rkb is run.
	r, ov, derr := decodeEdit(edit)
	if derr == nil {
		derr = checkOverrides(ov)
	}
	if derr != nil {
		return nothing(), fail(CodeEdit, "%v", derr)
	}
	ctx, b, done, berr := begin(ctx, cfg, true)
	if berr != nil {
		return nothing(), berr
	}
	keep := false
	defer func() { done(keep) }()

	res, rerr := b.render(ctx, req, edit, r, fit)
	if rerr != nil {
		res = b.partial()
		b.cost.stamp(&res)
		return res, rerr
	}
	b.cost.stamp(&res)
	if !fit {
		// Only the page stays for the caller: not the bank copy (every bullet and
		// the header it names), nor rkb's scratch.
		_ = os.Remove(b.bankPath)
		_ = os.RemoveAll(filepath.Join(b.dir, tmpDirName))
		_ = os.RemoveAll(filepath.Join(b.dir, "probe"))
		_ = os.Remove(filepath.Join(b.dir, "edited", "sel.json")) // carries the header
		_ = os.Remove(filepath.Join(b.dir, "edited", "claims.json"))
		res.BuildDir = b.dir
		keep = true
	} else {
		res.HTMLPath = ""
	}
	return res, nil
}

// render is Render's work inside a started build.
func (b *builder) render(ctx context.Context, req Request, edit []byte, r reply, fit bool) (Result, *Error) {
	if err := b.prepare(ctx, fit); err != nil {
		return Result{}, err
	}
	p := b.buildPoolSized(func(string) bool { return true }, 0)
	if err := b.refuseIneligible(r, p); err != nil {
		return Result{}, err
	}
	ch, err := b.ownerChoice(edit, p, keywordText(req))
	if err != nil {
		return Result{}, fail(CodeEdit, "%v", err)
	}
	if ch.count() == 0 {
		return Result{}, fail(CodeEdit, "the edit puts no bullet on the page")
	}
	out, aerr := b.attempt(ctx, "edited", ch, true, fit)
	if aerr != nil {
		return Result{}, aerr
	}
	if len(out.problems) > 0 {
		pr := out.problems[0]
		return Result{}, fail(pr.code(), "the edited resume could not be built (%s)", summarize(out.problems))
	}
	return b.finish(SourceEdited, "", out)
}

// refuseIneligible names the first bullet of the edit, in page order, that
// kbsync.Eligible has judged against: a bullet the bank no longer vouches for.
// A bullet that is merely not on offer where the edit put it (the wrong entry,
// a continuation without its predecessor) is checkSafe's to refuse.
func (b *builder) refuseIneligible(r reply, p pool) *Error {
	check := func(entry, id string) *Error {
		if p.has(entry, id) || b.el.Bullets[id] {
			return nil
		}
		why, listed := b.el.Why[id]
		if !listed {
			return nil
		}
		return fail(CodeIneligible, "bullet %q is no longer eligible (%s); replace it", clip(id, 60), clip(why, 200))
	}
	for _, s := range r.Sections {
		for _, id := range s.Bullets {
			if err := check("", id); err != nil {
				return err
			}
		}
		for _, e := range s.Entries {
			for _, id := range e.Bullets {
				if err := check(e.Entry, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
