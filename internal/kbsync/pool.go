package kbsync

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNoSelection is returned when there is no selection to draw the approved
// pool from. A sync with no selection sends nothing: it never falls back to the
// whole bank, which holds superseded wordings and unreviewed drafts.
var ErrNoSelection = errors.New("kbsync: no selection: set kb.selections to the rkb selection files in use")

// Withheld is an entry a selection lists that the sync does not send, and why.
type Withheld struct {
	Entry string
	Why   string
}

// Pool is what a sync may send: the bank cut down to the approved bullets of
// the entries a selection lists (Eligible, eligible.go, is the one rule).
type Pool struct {
	Resolved Resolved
	// Eligible is the rule's verdict on every bullet a selection lists.
	Eligible Eligibility
	// Withheld lists listed entries that are not synced, sorted by entry id.
	Withheld []Withheld
	// BankEntries and BankBullets are the whole bank's sizes, so a report can
	// say how much of it the pool leaves out.
	BankEntries, BankBullets int
}

// ResolvePool resolves only what the owner's selections approve. An entry is
// synced when a selection lists it and at least one of its bullets is eligible
// under it, or when it is an education entry a selection lists (a degree is a
// fact, not a bullet; rkb selections list it with no bullets). A synced block
// carries exactly its eligible bullets, in the bank's order for the entry. An
// entry no selection lists is not synced, however good its bullets are: the
// selection is the owner's statement that it is in use.
//
// The block hash covers the bullet list, so a block whose bullets are all
// eligible hashes as it always did and is not re-sent; one that loses a bullet
// to the rule hashes differently, and its update drops that bullet in DevPrism
// (buildUpdate), which DevPrism gates behind a confirmation.
func ResolvePool(b Bank, c Claims, sels []Selection) (Pool, error) {
	if len(sels) == 0 {
		return Pool{}, ErrNoSelection
	}
	el := Eligible(b, c, sels)
	listed := map[string]bool{}
	for _, s := range sels {
		for _, sec := range s.Sections {
			if sec.Kind != "entries" {
				continue
			}
			for _, se := range sec.Entries {
				listed[se.Entry] = true
			}
		}
	}
	p := Pool{Eligible: el, BankEntries: len(b.Entries), BankBullets: len(b.Bullets)}
	cut := Bank{Entries: map[string]Entry{}, Bullets: b.Bullets}
	for _, id := range sortedKeys(listed) {
		e, ok := b.Entries[id]
		if !ok {
			p.Withheld = append(p.Withheld, Withheld{id, "not in the bank"})
			continue
		}
		var kept []string
		for _, bid := range e.Bullets {
			if el.Allowed(id, bid) {
				kept = append(kept, bid)
			}
		}
		if len(kept) == 0 && KindOf(e) != KindEducation {
			p.Withheld = append(p.Withheld, Withheld{id, "no bullet of it is eligible"})
			continue
		}
		e.Bullets = kept
		cut.Entries[id] = e
	}
	p.Resolved = ResolveBank(cut, c)
	// Unattached counts bank bullets no synced entry lists; against the cut
	// bank that is nearly the whole bank, and says nothing about the pool.
	p.Resolved.Unattached = 0
	p.Resolved.Cautioned = uncleared(cut, c)
	sort.Slice(p.Withheld, func(i, j int) bool { return p.Withheld[i].Entry < p.Withheld[j].Entry })
	return p, nil
}

// uncleared lists CAUTION claims that a bullet in b uses without caution_ok
// clearing them. Eligible admits none, so for a pool this is empty; it is
// computed from what will be sent, not assumed, so a change to the rule that
// let one through would show in the plan.
func uncleared(b Bank, c Claims) []string {
	set := map[string]bool{}
	for _, e := range b.Entries {
		for _, bid := range e.Bullets {
			bl := b.Bullets[bid]
			for _, t := range tokenIDs(bl.Text) {
				if c.Caution[t] && strings.TrimSpace(bl.CautionOK[t]) == "" {
					set[t] = true
				}
			}
		}
	}
	return sortedKeys(set)
}

// Summary is a one-line account of the pool for a report.
func (p Pool) Summary() string {
	entries, bullets, variants := 0, 0, 0
	for _, blk := range p.Resolved.Blocks {
		switch {
		case blk.VariantOf != "":
			variants++
		case blk.Blocked == "":
			entries++
			bullets += len(blk.Bullets)
		}
	}
	s := fmt.Sprintf("approved pool: %d entries, %d bullets of the bank's %d entries and %d bullets; %d listed entries withheld",
		entries, bullets, p.BankEntries, p.BankBullets, len(p.Withheld))
	if variants > 0 {
		s += fmt.Sprintf("; %d more are rkb variants and not synced", variants)
	}
	return s
}
