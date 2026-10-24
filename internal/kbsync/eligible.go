package kbsync

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Eligibility is the one rule for which bank bullets may leave the career
// pipeline: into DevPrism (kb sync), into an answer draft, or onto a resume
// Vani builds. rkb's bank holds superseded wordings, bullets that cite figures
// Claims.tex has since withdrawn, and drafts no reader has passed; none of
// those may reach an employer. A bullet is eligible when all of these hold:
//
//  1. A current selection (config kb.selections: rkb resumes/*.json the owner
//     maintains) lists it, under an entry for an entry's bullet. A selection is
//     the owner's statement that the bullet is in use; the bank alone is not.
//  2. Every {{clm}} token it uses resolves to a value.
//  3. No token it uses is flagged CAUTION in Claims.tex unless the bullet's
//     caution_ok names that token (rkb lint.go:426, the final gate's rule).
//  4. rkb's reader gate (lint.go:444-485), for an HR and a hiring-manager
//     reader each: the latest round-2 verdict on this exact wording is a pass
//     (wording = text_hash, the first 12 hex of sha1 over the resolved plain
//     text, rkb util.go sha1hex), and there is a second pass, a round-1 pass or
//     a round-2 pass by another reviewer; the round-2 reviewer is named, is not
//     "drafter" and is not the bullet's author; one reviewer is not both HR and
//     hiring manager.
//
// The rule is a subset of rkb's final gate: it does not re-run figure blind
// passes, statement checks, the say-it-once rules or page fit. A resume Vani
// builds still goes through rkb lint and fit. Selections are read with rkb's
// defaults (bank.go:607-698): a section without a kind is "entries" when it
// lists entries, "lines" when it has lines, else "bullets"; an entry without
// a bullets key shows every bullet its bank entry lists.
type Eligibility struct {
	// Bullets is every eligible bullet id.
	Bullets map[string]bool
	// Why says, per bullet a selection lists, why it is not eligible. A bullet
	// no selection lists is absent from both maps.
	Why map[string]string
	// EntryBullets maps entry id -> its eligible bullet ids, in the order of
	// the first selection that lists them.
	EntryBullets map[string][]string
	// Summary lists eligible summary bullets (a "bullets" section), first-seen order.
	Summary []string
}

// Allowed reports whether bullet id may leave the pipeline under entry. An
// empty entry asks about a summary bullet.
func (e Eligibility) Allowed(entry, id string) bool {
	if !e.Bullets[id] {
		return false
	}
	if entry == "" {
		for _, s := range e.Summary {
			if s == id {
				return true
			}
		}
		return false
	}
	for _, b := range e.EntryBullets[entry] {
		if b == id {
			return true
		}
	}
	return false
}

// Selection is the part of an rkb selection file (rkb bank.go:206-240) the
// eligibility rule reads: which entries and bullets it puts on a page.
type Selection struct {
	Path     string
	Sections []SelSection
}

// SelSection is one section of a selection. Kind is rkb's: "entries",
// "bullets" (a summary) or "lines" (skills, publications; not read).
// ParseSelection fills an omitted Kind the way rkb does.
type SelSection struct {
	Title   string          `json:"title"`
	Kind    string          `json:"kind"`
	Bullets []string        `json:"bullets"`
	Entries []SelEntry      `json:"entries"`
	Lines   json.RawMessage `json:"lines"`
}

// SelEntry is one entry on a selection's page with the bullets it shows.
// AllBullets is set when the selection omits the bullets key: rkb then shows
// every bullet the bank entry lists. An explicit empty list shows none.
type SelEntry struct {
	Entry      string   `json:"entry"`
	Bullets    []string `json:"bullets"`
	AllBullets bool     `json:"-"`
}

// MaxSelectionBytes caps one selection file (the largest real one is ~20 KiB).
const MaxSelectionBytes = 1 << 20

// LoadSelection reads one rkb selection file strictly: bounded, one object,
// no duplicate keys, ids well formed.
func LoadSelection(path string) (Selection, error) {
	f, err := os.Open(path)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: selection: %v", ErrBank, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxSelectionBytes+1))
	if err != nil {
		return Selection{}, fmt.Errorf("%w: selection %s: %v", ErrBank, path, err)
	}
	s, err := ParseSelection(raw)
	if err != nil {
		return Selection{}, fmt.Errorf("%s: %w", path, err)
	}
	s.Path = path
	return s, nil
}

// LoadSelections loads every file in paths (config kb.selections), in order.
// No paths is ErrNoSelection: the bank alone never vouches for a bullet.
func LoadSelections(paths []string) ([]Selection, error) {
	if len(paths) == 0 {
		return nil, ErrNoSelection
	}
	sels := make([]Selection, 0, len(paths))
	for _, p := range paths {
		s, err := LoadSelection(p)
		if err != nil {
			return nil, err
		}
		sels = append(sels, s)
	}
	return sels, nil
}

// ParseSelection is LoadSelection on bytes already read.
func ParseSelection(raw []byte) (Selection, error) {
	if len(raw) > MaxSelectionBytes {
		return Selection{}, fmt.Errorf("%w: selection larger than %d bytes", ErrBank, MaxSelectionBytes)
	}
	if err := checkJSON(raw); err != nil {
		return Selection{}, fmt.Errorf("%w: selection: %v", ErrBank, err)
	}
	var top struct {
		Sections []SelSection `json:"sections"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&top); err != nil {
		return Selection{}, fmt.Errorf("%w: selection: %v", ErrBank, err)
	}
	if len(top.Sections) == 0 {
		return Selection{}, fmt.Errorf("%w: selection has no sections", ErrBank)
	}
	for i := range top.Sections {
		sec := &top.Sections[i]
		if sec.Kind == "" { // rkb bank.go:611-621
			switch {
			case len(sec.Entries) > 0:
				sec.Kind = "entries"
			case len(sec.Lines) > 0 && string(sec.Lines) != "null":
				sec.Kind = "lines"
			default:
				sec.Kind = "bullets"
			}
		}
		for j := range sec.Entries {
			sec.Entries[j].AllBullets = sec.Entries[j].Bullets == nil // rkb bank.go:692-695
		}
	}
	for _, sec := range top.Sections {
		for _, id := range sec.Bullets {
			if err := checkID("selected bullet", id); err != nil {
				return Selection{}, err
			}
		}
		for _, e := range sec.Entries {
			if err := checkID("selected entry", e.Entry); err != nil {
				return Selection{}, err
			}
			for _, id := range e.Bullets {
				if err := checkID("selected bullet", id); err != nil {
					return Selection{}, err
				}
			}
		}
	}
	return Selection{Sections: top.Sections}, nil
}

// Readers whose pass rule 4 requires.
var eligibleReaders = []string{"hr", "hm"}

// Eligible applies the rule to every bullet the selections list. A selection
// naming an entry or bullet the bank lacks, or a bullet under an entry that
// does not list it, makes that bullet ineligible (with the reason) rather than
// failing the whole set: one stale selection must not stop drafting.
func Eligible(b Bank, c Claims, sels []Selection) Eligibility {
	el := Eligibility{Bullets: map[string]bool{}, Why: map[string]string{}, EntryBullets: map[string][]string{}}
	judged := map[string]string{} // bullet id -> "" (eligible) or why not
	judge := func(id string) string {
		if why, ok := judged[id]; ok {
			return why
		}
		why := bulletWhy(b, c, id)
		judged[id] = why
		return why
	}
	note := func(id, why string) {
		if why == "" {
			el.Bullets[id] = true
			delete(el.Why, id)
		} else if !el.Bullets[id] {
			el.Why[id] = why
		}
	}
	seenSummary := map[string]bool{}
	seenUnder := map[string]map[string]bool{}
	for _, s := range sels {
		for _, sec := range s.Sections {
			switch sec.Kind {
			case "bullets":
				for _, id := range sec.Bullets {
					why := judge(id)
					note(id, why)
					if why == "" && !seenSummary[id] {
						seenSummary[id] = true
						el.Summary = append(el.Summary, id)
					}
				}
			case "entries":
				for _, se := range sec.Entries {
					ent, ok := b.Entries[se.Entry]
					ids := se.Bullets
					if se.AllBullets {
						ids = ent.Bullets
					}
					for _, id := range ids {
						why := judge(id)
						switch {
						case why != "":
						case !ok:
							why = "its entry " + se.Entry + " is not in the bank"
						case !contains(ent.Bullets, id):
							why = "entry " + se.Entry + " does not list it"
						}
						note(id, why)
						if why != "" {
							continue
						}
						if seenUnder[se.Entry] == nil {
							seenUnder[se.Entry] = map[string]bool{}
						}
						if !seenUnder[se.Entry][id] {
							seenUnder[se.Entry][id] = true
							el.EntryBullets[se.Entry] = append(el.EntryBullets[se.Entry], id)
						}
					}
				}
			}
		}
	}
	return el
}

// bulletWhy is "" for a bullet that passes rules 2-4, else the first reason.
func bulletWhy(b Bank, c Claims, id string) string {
	bl, ok := b.Bullets[id]
	if !ok {
		return "not in the bank"
	}
	for _, t := range tokenIDs(bl.Text) {
		if _, ok := c.Values[t]; !ok {
			return "claim " + t + " has no value"
		}
		if c.Caution[t] && strings.TrimSpace(bl.CautionOK[t]) == "" {
			return "claim " + t + " is flagged CAUTION in Claims.tex and caution_ok does not clear it"
		}
	}
	cur := wordingHash(rkbPlain(bl.Text, c.Values))
	by := map[string]string{}
	for _, reader := range eligibleReaders {
		r2, ok := readerPassed(bl.Review[reader], cur)
		if !ok {
			return strings.ToUpper(reader) + " reader has not passed this wording in both rounds"
		}
		if r2 == "" || r2 == "drafter" || r2 == bl.Author {
			return strings.ToUpper(reader) + " round-2 review has no independent reviewer"
		}
		by[reader] = r2
	}
	if by["hr"] == by["hm"] {
		return "the same reviewer played HR and hiring manager"
	}
	return ""
}

// wordingHash is rkb's sha1hex (util.go): the first 12 hex characters of sha1,
// the form a review's text_hash records.
func wordingHash(plain string) string {
	sum := sha1.Sum([]byte(plain))
	return hex.EncodeToString(sum[:])[:12]
}

// readerPassed is rkb's two-pass rule for one reader (lint.go:449-476): the
// latest round-2 verdict on this wording is a pass (r2), and there is another
// pass, a round-1 pass (on any wording, as rkb counts it) or a round-2 pass by
// a different reviewer. It returns r2's reviewer.
func readerPassed(rs []Review, cur string) (string, bool) {
	var r2 *Review
	for i, r := range rs {
		if r.Round == 2 && r.TextHash == cur {
			if r.Verdict == "pass" {
				r2 = &rs[i]
			} else {
				r2 = nil
			}
		}
	}
	if r2 == nil {
		return "", false
	}
	for _, r := range rs {
		if r.Verdict == "pass" && (r.Round == 1 || (r.Round == 2 && r.By != r2.By)) {
			return r2.By, true
		}
	}
	return "", false
}

// rkbPlain is rkb's resolvePlain (lint.go:236, render.go:19): tokens resolved,
// then links reduced to their text, bold and italic markers removed, in that
// order. It differs from ResolveText, which keeps link targets for DevPrism.
func rkbPlain(s string, claims map[string]string) string {
	s = tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := claims[tokenRe.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	})
	s = linkRe.ReplaceAllString(s, "$1")
	s = boldRe.ReplaceAllString(s, "$1")
	return italicRe.ReplaceAllString(s, "$1$2$3")
}

// Restrict is b cut to what may leave the pipeline: each entry keeps only the
// bullets e allows under it, in the bank's order, and an entry left with none
// is dropped whole, so a role the owner no longer lists contributes no title
// and no stack terms either (the semantic profile and the kbmatch digest are
// built from this cut).
func (e Eligibility) Restrict(b Bank) Bank {
	out := Bank{Entries: map[string]Entry{}, Bullets: map[string]Bullet{}}
	for id, en := range b.Entries {
		var keep []string
		for _, bid := range en.Bullets {
			if e.Allowed(id, bid) {
				keep = append(keep, bid)
			}
		}
		if len(keep) == 0 {
			continue
		}
		en.Bullets = keep
		out.Entries[id] = en
		for _, bid := range keep {
			out.Bullets[bid] = b.Bullets[bid]
		}
	}
	return out
}

// Ineligible lists the ids a selection listed that are not eligible, sorted.
func (e Eligibility) Ineligible() []string {
	ids := make([]string, 0, len(e.Why))
	for id := range e.Why {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
