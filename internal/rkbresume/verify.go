package rkbresume

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A stored version was built from bullets rkb had double-verified for the
// sentences that version uses. That is a property of the whole choice, not of a
// bullet: rkb judges double_verified against the sentences of the selection it is
// given (`claims export --used-in`), so the plain export Pool reads shows no claim
// verified at all (measured on the owner's bank: 340 claims, 0 double-verified
// without a selection, 42 for the base selection, 13 when every bullet is "used").
// A flag moves when a claim's verification passes, its source file or the
// validator report change, and only an export of the version's own choice sees it.

// Verification is what PoolAndVerify found for one stored choice.
type Verification struct {
	// Unverified lists the choice's bullets, in page order, that use a figure rkb
	// does not double-verify for this choice now (or does not know at all).
	Unverified []string
	// Err is why the choice could not be verified: it does not read as an edit, it
	// names a bullet that is not on offer, or rkb could not be run. A choice that
	// could not be verified is never one that passed.
	Err string
}

// maxVerify bounds the choices one PoolAndVerify call exports (a job keeps at
// most 20 versions).
const maxVerify = 64

// PoolAndVerify is Pool and, for each of edits (the choices of stored versions, in
// the owner-edit shape), one `claims export --used-in` of that choice: the same
// export a build makes of the page it renders, so its answer is the one a rebuild
// would reach. rkb is run once for the pool and once per choice; it takes no build
// lock (it renders nothing and fits no page) and is bounded by PoolDeadline when
// cfg.Deadline is zero. The result has one Verification per edit, in order. A
// choice that cannot be read against the live bank (a bullet no longer offered, a
// section the base no longer has) says so in its own Err and costs no rkb run; only
// a failure of the pool itself, or a cancelled or expired context, is the error.
func PoolAndVerify(ctx context.Context, cfg Config, edits [][]byte) (PoolView, []Verification, error) {
	cfg.Provider, cfg.NoModel, cfg.Model, cfg.Tailor = nil, "", "", Tailor{}
	if cfg.Deadline == 0 {
		cfg.Deadline = PoolDeadline
	}
	cfg, nerr := cfg.normalize()
	if nerr != nil {
		return PoolView{}, nil, nerr
	}
	if len(edits) > maxVerify {
		return PoolView{}, nil, fail(CodeConfig, "%d choices to verify, over %d", len(edits), maxVerify)
	}
	ctx, b, done, berr := begin(ctx, cfg, false)
	if berr != nil {
		return PoolView{}, nil, berr
	}
	defer done(false)
	if err := b.prepare(ctx, false); err != nil {
		return PoolView{}, nil, err
	}
	view, perr := b.poolView()
	if perr != nil {
		return PoolView{}, nil, perr
	}
	full := b.buildPoolSized(func(string) bool { return true }, 0)
	out := make([]Verification, len(edits))
	for i, raw := range edits {
		v, err := b.verifyEdit(ctx, full, raw, i)
		if err != nil {
			return PoolView{}, nil, err // the context ended: nothing partial is passed off as a result
		}
		out[i] = v
	}
	return view, out, nil
}

// verifyEdit exports one stored choice and names the bullets rkb does not verify
// for it. A non-nil error is the context's.
func (b *builder) verifyEdit(ctx context.Context, full pool, raw []byte, n int) (Verification, *Error) {
	r, _, derr := decodeEdit(raw)
	if derr != nil {
		return Verification{Err: "the stored choice does not read as an edit: " + clip(derr.Error(), 200)}, nil
	}
	// The choice's own keywords were checked against its posting when it was built.
	ch, cerr := b.ownerChoice(raw, full, strings.Join(r.Keywords, "\n"))
	if cerr != nil {
		return Verification{Err: "the stored choice cannot be placed against the bank now: " + clip(cerr.Error(), 200)}, nil
	}
	if ch.count() == 0 {
		return Verification{Err: "the stored choice puts no bullet on the page"}, nil
	}
	d := filepath.Join(b.dir, fmt.Sprintf("verify%d", n))
	if err := os.MkdirAll(d, 0o700); err != nil {
		return Verification{Err: "no private temp folder: " + clip(err.Error(), 100)}, nil
	}
	cf, err := b.export(ctx, d, ch)
	if err != nil {
		if ctx.Err() != nil {
			return Verification{}, ctxError(ctx)
		}
		return Verification{Err: clip(err.Msg, 240)}, nil
	}
	return Verification{Unverified: b.unverifiedBullets(ch, cf)}, nil
}
