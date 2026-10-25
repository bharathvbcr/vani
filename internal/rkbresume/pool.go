package rkbresume

import (
	"context"
	"encoding/json"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// PoolBullet is one eligible bullet the editor may place. Text is the wording
// as rkb prints it (claim tokens resolved, links reduced to their text, bold
// and italic markers removed: kbsync.RKBPlain), so the editor shows what the
// page will say; TextHash is that wording's 12-hex hash, the form a version
// records. Follows is set for a continuation: the bullet it must directly follow,
// under the same entry.
type PoolBullet struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	TextHash string `json:"text_hash"`
	Follows  string `json:"follows,omitempty"`
}

// PoolEntry is an entry with eligible bullets and where it may go: Sections are
// the titles of the sections it may be placed in (a job only in the work
// history, a project in any project section, or where the owner's selections put
// it). Title, Org and Dates are the bank entry's own; an edit may override them.
type PoolEntry struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Org      string       `json:"org,omitempty"`
	Dates    string       `json:"dates,omitempty"`
	Sections []string     `json:"sections"`
	Bullets  []PoolBullet `json:"bullets"`
}

// PoolView is what the editor works from. It holds eligibility only (the rule
// kbsync.Eligible applies to the live bank now): whether a figure rkb has not
// double-verified would be dropped is decided when a page is built. It never
// holds the header, a path or any text outside the bank's own bullets and entry
// titles. An eligible bullet that continues the one before it (its text refers
// back) is left out when that predecessor is not eligible under the same entry:
// no page may carry it, so none is offered.
type PoolView struct {
	// Summary are the eligible summary bullets, SummarySections the titles of the
	// sections that take them.
	Summary         []PoolBullet `json:"summary"`
	SummarySections []string     `json:"summary_sections"`
	Entries         []PoolEntry  `json:"entries"`
	// Base is the base selection's choice (filtered to what is eligible now) in
	// the owner-edit shape: the editor's starting point and "reset to base".
	Base         json.RawMessage `json:"base"`
	Fingerprints Fingerprints    `json:"fingerprints"`
}

// Pool reads the live bank and selections and returns every eligible bullet,
// for the editor. No model is used and rkb renders nothing: rkb is run once, to
// export the claims the wording is resolved with. It takes no build lock (it
// only reads) and is bounded by PoolDeadline when cfg.Deadline is zero. The pool
// is the same for every job: req is accepted so Pool and Render share a call
// shape, and is not read.
func Pool(ctx context.Context, cfg Config, req Request) (PoolView, error) {
	_ = req
	cfg.Provider, cfg.NoModel, cfg.Model, cfg.Tailor = nil, "", "", Tailor{}
	if cfg.Deadline == 0 {
		cfg.Deadline = PoolDeadline
	}
	cfg, nerr := cfg.normalize()
	if nerr != nil {
		return PoolView{}, nerr
	}
	ctx, b, done, berr := begin(ctx, cfg, false)
	if berr != nil {
		return PoolView{}, berr
	}
	defer done(false)
	if err := b.prepare(ctx, false); err != nil {
		return PoolView{}, err
	}
	v, perr := b.poolView()
	if perr != nil {
		return PoolView{}, perr
	}
	return v, nil
}

func (b *builder) poolView() (PoolView, *Error) {
	p := b.buildPoolSized(func(string) bool { return true }, 0)
	bullet := func(pb poolBullet) PoolBullet {
		bl := b.bank.Bullets[pb.id]
		plain := kbsync.RKBPlain(bl.Text, b.claims.Values)
		return PoolBullet{ID: pb.id, Text: plain, TextHash: kbsync.WordingHash(plain), Follows: pb.follows}
	}
	v := PoolView{Summary: []PoolBullet{}, SummarySections: []string{}, Entries: []PoolEntry{}, Fingerprints: b.fp}
	for _, pb := range p.summary {
		v.Summary = append(v.Summary, bullet(pb))
	}
	for _, s := range b.base.sections {
		if s.choosable && s.kind == "bullets" {
			v.SummarySections = append(v.SummarySections, s.title)
		}
	}
	for _, pe := range p.entries {
		e := PoolEntry{ID: pe.id, Title: pe.title, Org: pe.org, Dates: pe.dates, Sections: []string{}, Bullets: []PoolBullet{}}
		for i, s := range b.base.sections {
			if s.choosable && s.kind == "entries" && b.canHold(i, pe.id) {
				e.Sections = append(e.Sections, s.title)
			}
		}
		for _, pb := range pe.bullets {
			e.Bullets = append(e.Bullets, bullet(pb))
		}
		v.Entries = append(v.Entries, e)
	}
	base, err := b.base.baseChoice(b.el.Allowed, b.isCont).editJSON(b.base)
	if err != nil {
		return PoolView{}, fail(CodeKB, "the base choice cannot be written as an edit: %v", err)
	}
	v.Base = base
	return v, nil
}
