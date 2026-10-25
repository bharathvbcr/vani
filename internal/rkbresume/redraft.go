package rkbresume

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

// A redraft asks Opus again for a job that already has a version: with the
// previous choice shown (so it chooses differently where the posting is better
// served), with some sections locked, or scoped to one section or one entry,
// and with an owner's one-line preference. Everything that is not re-picked is
// kept as it was, merged from the previous choice by this package, never taken
// from the model's reply.

// Codes of a redraft, beside the build's own.
const (
	// CodeBadInstruction means the owner's instruction is not one this package
	// will send: too long, not one printable line, or carrying what reads as an
	// email address or a phone number. Nothing was spent.
	CodeBadInstruction = "bad_instruction"
	// CodeBadScope means the scope or a lock names nothing the redraft can keep or
	// re-pick (not a section the model fills, not an entry on the previous page),
	// or asks for a scope or lock with no previous choice. Nothing was spent.
	CodeBadScope = "bad_scope"
	// CodeScope means the model's reply changed what the redraft keeps. It is
	// refused before anything is rendered; it shares the build's one retry, and
	// when the retry also fails the build fails with this code (a kept redraft
	// never falls back to the base selection, which would not keep it).
	CodeScope = "scope"
	// CodeRedraft means a redraft that keeps something (a scope or a lock) could not
	// make a page from the model's choices. The base selection is not built in its
	// place: it would not keep what the owner kept.
	CodeRedraft = "redraft"
)

// Bounds on a redraft's inputs. All are ceilings.
const (
	// MaxInstructionRunes bounds the owner's instruction (the resume store keeps the
	// same bound on the record).
	MaxInstructionRunes = 500
	// maxScopeRunes bounds a scope or lock name and maxLocks the locks: the resume
	// store keeps both on the record (resumestore.MaxNameRunes, MaxLocks) and
	// refuses a longer one, so a build must refuse them before it spends a turn.
	maxScopeRunes = 120
	maxLocks      = 16
	// maxPreviousBytes bounds the previous-choice block of the prompt.
	maxPreviousBytes = 6000
	// keptPromptBytes bounds the lines a scope or lock adds to the prompt.
	keptPromptBytes = 3000
)

// Tailor is what a redraft adds to a build. The zero value is a plain build.
// Instruction is the owner's text and Previous the owner's earlier choice: neither
// is page text, so neither is part of Request.
type Tailor struct {
	// Previous is the version the redraft starts from: its choice, in the
	// owner-edit shape (Result.SelectionJSON, resumestore.Meta.Selection).
	Previous json.RawMessage
	// Scope is a section title or an entry id of the previous page: only that part is
	// re-picked, every other part is kept. Empty re-picks the whole page.
	Scope string
	// Lock lists section titles kept as they are. A locked section is never the scope.
	Lock []string
	// Instruction is the owner's one-line preference, at most MaxInstructionRunes
	// printable runes, with no email address or phone number. It is placed in its
	// own block of the prompt, below the rules and unable to override them, and is
	// never rendered on the page. The version records it (scrubbed of the header's
	// strings) for a tailored build.
	Instruction string
}

func (t Tailor) kept() bool { return t.Scope != "" || len(t.Lock) > 0 }

// PromptBytes is the most bytes a redraft adds to a turn's prompt: the previous
// choice's ids, the kept sections, and the instruction (4 bytes a rune at most).
// PromptBound does not include it; a caller that redrafts adds it.
func (t Tailor) PromptBytes() int {
	n := 0
	if len(t.Previous) > 0 {
		n += maxPreviousBytes + 300
	}
	if t.kept() {
		n += keptPromptBytes
	}
	if t.Instruction != "" {
		n += MaxInstructionRunes*4 + 200
	}
	return n
}

// CheckTailor holds a Tailor to the rules that need no rkb and no bank: the
// instruction, the shape of the scope and lock names, and the combinations that
// cannot work. It is what Build checks first, before a lock or a turn; a dry run
// calls it to say the same thing a build would. Which section or entry a name
// means is known only once the bank is read (CodeBadScope again). It returns the
// Tailor as a build will use it: the instruction and names trimmed, the locks
// without duplicates.
func CheckTailor(t Tailor, allowBase bool) (Tailor, error) {
	n, err := normalizeTailor(t, allowBase)
	if err != nil {
		return t, err
	}
	return n, nil
}

func normalizeTailor(t Tailor, allowBase bool) (Tailor, *Error) {
	instr, err := checkInstruction(t.Instruction)
	if err != nil {
		return t, err
	}
	t.Instruction = instr
	if t.Scope != "" {
		s, serr := checkName("the scope", t.Scope)
		if serr != nil {
			return t, serr
		}
		t.Scope = s
	}
	if len(t.Lock) > maxLocks {
		return t, fail(CodeBadScope, "more than %d sections are locked", maxLocks)
	}
	var locks []string
	for _, l := range t.Lock {
		s, serr := checkName("a lock", l)
		if serr != nil {
			return t, serr
		}
		if !slices.Contains(locks, s) {
			locks = append(locks, s)
		}
	}
	t.Lock = locks
	if len(t.Previous) > maxEditBytes {
		return t, fail(CodeEdit, "the previous choice is over %d bytes", maxEditBytes)
	}
	if t.kept() && len(t.Previous) == 0 {
		return t, fail(CodeBadScope, "a scope or a lock keeps parts of the previous choice, and there is none")
	}
	if t.kept() && allowBase {
		return t, fail(CodeConfig, "a scope or a lock cannot be combined with the base fallback: the base selection would not keep what was kept")
	}
	return t, nil
}

// checkName holds a scope or lock name to one short printable line.
func checkName(what, s string) (string, *Error) {
	t := strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s) || t == "":
		return "", fail(CodeBadScope, "%s is empty or not valid text", what)
	case utf8.RuneCountInString(t) > maxScopeRunes:
		return "", fail(CodeBadScope, "%s is over %d characters", what, maxScopeRunes)
	}
	for _, r := range t {
		if !unicode.IsPrint(r) {
			return "", fail(CodeBadScope, "%s must be one printable line", what)
		}
	}
	return t, nil
}

// checkInstruction returns the instruction trimmed, or why it will not be sent.
// The message names the rule, never the text. An email address or a phone number
// is refused rather than scrubbed: the instruction goes to a model and into the
// version record, and personal data does neither.
func checkInstruction(s string) (string, *Error) {
	if s == "" {
		return "", nil
	}
	t := strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s):
		return "", fail(CodeBadInstruction, "the instruction is not valid text")
	case t == "":
		return "", fail(CodeBadInstruction, "the instruction is only whitespace; leave it out to give none")
	case utf8.RuneCountInString(t) > MaxInstructionRunes:
		return "", fail(CodeBadInstruction, "the instruction is %d characters, over %d", utf8.RuneCountInString(t), MaxInstructionRunes)
	}
	for _, r := range t {
		if !unicode.IsPrint(r) { // control (newline, tab), format (bidi marks), unassigned, or a space but " "
			return "", fail(CodeBadInstruction, "the instruction must be one printable line: it has a control, formatting or invisible character")
		}
	}
	if emailLike(t) {
		return "", fail(CodeBadInstruction, "the instruction looks like it holds an email address; leave contact details out")
	}
	if phoneLike(t) {
		return "", fail(CodeBadInstruction, "the instruction looks like it holds a phone number; leave contact details out")
	}
	return t, nil
}

// emailLike reports an "@" with text on both sides: an address, or enough like
// one that a model should not be given it.
func emailLike(s string) bool {
	at := strings.IndexByte(s, '@')
	for at >= 0 {
		before := at > 0 && !unicode.IsSpace(rune(s[at-1])) && s[at-1] != '@'
		after := at+1 < len(s) && !unicode.IsSpace(rune(s[at+1])) && s[at+1] != '@'
		if before && after {
			return true
		}
		next := strings.IndexByte(s[at+1:], '@')
		if next < 0 {
			return false
		}
		at += 1 + next
	}
	return false
}

const (
	// phoneSeparators are what may join the digit groups of a number: a space, a dot,
	// a hyphen, an en dash and the two parentheses, at most phoneMaxSeparator in a row
	// (") " and " (" are two).
	phoneSeparators   = " -–.()"
	phoneMaxSeparator = 2
	// phoneWindow is how many characters, from the first digit of a group to the last
	// digit of a later one, the digits of a number may be spread over.
	phoneWindow = 20
	// phoneDigits is how many digits make a number: a local number has seven.
	phoneDigits = 7
)

// phoneLike reports digit groups joined by spaces, dots, dashes or parentheses
// (phoneSeparators, at most two in a row) that total seven digits or more within
// phoneWindow characters, or one group of seven or more. 5551234567, 555-123-4567,
// (555) 123-4567, +1 555 123 4567, 555.123.4567, 1234 5678 and 555 1234 are all
// refused; a version ("3.12", "Go 1.26"), a count ("100,000") and a year are not.
//
// One shape is let through: a range of two years, written "2019-2024" or
// "2019–2024" (a hyphen or an en dash, no spaces; both groups four digits from 1900
// to 2099) and nothing else joined to it. Four-digit groups in any other shape are
// not years ("1234 5678" is a number, and so is "2019 2024": a space does not make a
// range), so the rule that let every run of four-digit groups pass is gone.
func phoneLike(s string) bool {
	rs := []rune(s)
	type group struct{ start, end int } // rune offsets, end exclusive
	for i := 0; i < len(rs); {
		if !unicode.IsDigit(rs[i]) {
			i++
			continue
		}
		var groups []group
		var seps []string // seps[k] joins groups[k] to groups[k+1]
		for i < len(rs) {
			j := i
			for j < len(rs) && unicode.IsDigit(rs[j]) {
				j++
			}
			groups = append(groups, group{i, j})
			i = j
			k := i
			for k < len(rs) && k-i < phoneMaxSeparator && strings.ContainsRune(phoneSeparators, rs[k]) {
				k++
			}
			if k == i || k >= len(rs) || !unicode.IsDigit(rs[k]) {
				break
			}
			seps = append(seps, string(rs[i:k]))
			i = k
		}
		if len(groups) == 2 && (seps[0] == "-" || seps[0] == "–") && yearGroup(rs[groups[0].start:groups[0].end]) && yearGroup(rs[groups[1].start:groups[1].end]) {
			continue
		}
		for a := range groups {
			digits := 0
			for b := a; b < len(groups) && (b == a || groups[b].end-groups[a].start <= phoneWindow); b++ {
				digits += groups[b].end - groups[b].start
				if digits >= phoneDigits {
					return true
				}
			}
		}
	}
	return false
}

// yearGroup reports four ASCII digits that read as a year, 1900 to 2099.
func yearGroup(rs []rune) bool {
	if len(rs) != 4 {
		return false
	}
	n := 0
	for _, r := range rs {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n >= 1900 && n <= 2099
}

// redraft is a resolved Tailor: the previous page, and what is kept.
type redraft struct {
	t Tailor
	// prevR is the previous choice as written (ids by section and entry), for the
	// prompt; prev is the same choice validated against the whole pool, set only
	// when something is kept (a whole redraft shows ids and needs no more).
	prevR reply
	prev  choice
	// keptSec holds the choosable sections kept whole: the locked ones, and with a
	// scope every section but the scoped one.
	keptSec map[int]bool
	// scopeSec is the section the scope names or contains (-1 without a scope);
	// scopeEnt the entry when the scope is an entry (the section's other entries are
	// kept).
	scopeSec int
	scopeEnt string
	// full is every bullet on offer: what the merged page is validated against (the
	// model's own part is validated against the smaller pool it was shown).
	full pool
	// refusal is the code of the last refusal of the model's reply (CodeScope or
	// CodeRedraft), for the error a kept redraft ends with.
	refusal string
}

func (rd *redraft) keeps() bool { return rd != nil && (len(rd.keptSec) > 0 || rd.scopeEnt != "") }

// scopedRun reports a scope: keywords and totals of the previous page stand.
func (rd *redraft) scopedRun() bool { return rd != nil && rd.scopeSec >= 0 }

func (rd *redraft) sectionKept(i int) bool { return rd != nil && rd.keptSec[i] }

// entryScoped reports a scope on one entry: its section's other entries are kept.
func (rd *redraft) entryScoped() bool { return rd != nil && rd.scopeEnt != "" }

// openBullets is how many bullets of the previous page are in the part the model
// fills.
func (rd *redraft) openBullets() int {
	n := 0
	for i, sc := range rd.prev.secs {
		switch {
		case rd.sectionKept(i):
		case i == rd.scopeSec && rd.scopeEnt != "":
			for _, e := range sc.entries {
				if e.id == rd.scopeEnt {
					n += len(e.bullets)
				}
			}
		default:
			n += len(sc.bullets)
			for _, e := range sc.entries {
				n += len(e.bullets)
			}
		}
	}
	return n
}

// resolveTailor reads the previous choice and the scope and locks against the
// base and the live pool, before anything is spent. It returns nil when there is
// nothing to resolve (a plain build).
func (b *builder) resolveTailor(req Request) *Error {
	t := b.cfg.Tailor
	if instr := b.cleanInstruction(t.Instruction); instr != "" {
		b.instr = instr
	}
	if len(t.Previous) == 0 {
		return nil
	}
	r, _, err := decodeEdit(t.Previous)
	if err != nil {
		return fail(CodeEdit, "the previous choice is not an edit this package reads: %v", err)
	}
	rd := &redraft{t: t, prevR: r, scopeSec: -1, keptSec: map[int]bool{}}
	b.rd = rd
	if !t.kept() {
		return nil
	}
	rd.full = b.buildPoolSized(func(string) bool { return true }, 0)
	if err := b.refuseIneligible(r, rd.full); err != nil {
		return err
	}
	// The previous page's own keywords were checked against the posting when it was
	// built; they are not asked to occur in it again (the posting may have changed).
	jd := keywordText(req) + "\n" + strings.Join(r.Keywords, "\n")
	prev, perr := b.ownerChoice(t.Previous, rd.full, jd)
	if perr != nil {
		return fail(CodeEdit, "the previous choice cannot be kept: %v", perr)
	}
	rd.prev = prev // with the previous page's overrides on its entries

	titles := map[string]int{}
	for i, s := range b.base.sections {
		if s.choosable {
			titles[s.title] = i
		}
	}
	locked := map[int]bool{}
	for _, l := range t.Lock {
		i, ok := titles[l]
		if !ok {
			return fail(CodeBadScope, "no section titled %q is filled by the model, so it cannot be locked", clip(l, 60))
		}
		locked[i] = true
	}
	if t.Scope != "" {
		if i, ok := titles[t.Scope]; ok {
			rd.scopeSec = i
		} else {
			found := false
			for i, sc := range prev.secs {
				for _, e := range sc.entries {
					if e.id == t.Scope {
						rd.scopeSec, rd.scopeEnt, found = i, e.id, true
					}
				}
			}
			if !found {
				return fail(CodeBadScope, "the scope %q is neither a section the model fills nor an entry of the previous page", clip(t.Scope, 60))
			}
		}
		if locked[rd.scopeSec] {
			return fail(CodeBadScope, "the scope is in a locked section")
		}
		for _, i := range titles {
			if i != rd.scopeSec {
				rd.keptSec[i] = true
			}
		}
	}
	for i := range locked {
		rd.keptSec[i] = true
	}
	free := false
	for _, i := range titles {
		if !rd.keptSec[i] {
			free = true
		}
	}
	if !free {
		return fail(CodeBadScope, "every section is locked, so there is nothing to redraft")
	}
	return nil
}

// cleanInstruction is the instruction as it enters the prompt and the record: the
// message markers removed, every string of the owner's header replaced, at most
// MaxInstructionRunes runes (the mask is longer than a short header string), and
// trimmed.
func (b *builder) cleanInstruction(s string) string {
	if s == "" {
		return ""
	}
	s = b.scrub.clean(llmbackend.Unmark(s))
	return strings.TrimSpace(runesPrefix(s, MaxInstructionRunes))
}

// ---- the prompt's part

// previousBlock lists the previous choice by ids, only those still on offer, in
// the section order of the page it came from. It repeats no text.
func (b *builder) previousBlock(p pool) string {
	rd := b.rd
	if rd == nil || len(rd.prevR.Sections) == 0 {
		return ""
	}
	var lines []string
	for _, rs := range rd.prevR.Sections {
		i := b.sectionIndex(rs.Title)
		if i < 0 || rd.sectionKept(i) {
			continue
		}
		var part string
		if b.base.sections[i].kind == "bullets" {
			part = joinOffered(rs.Bullets, func(id string) bool { return p.has("", id) })
		} else {
			var es []string
			for _, e := range rs.Entries {
				if rd.scopeEnt != "" && e.Entry != rd.scopeEnt {
					continue
				}
				if ids := joinOffered(e.Bullets, func(id string) bool { return p.has(e.Entry, id) }); ids != "" {
					es = append(es, e.Entry+": "+ids)
				}
			}
			part = strings.Join(es, "; ")
		}
		if part != "" {
			lines = append(lines, fmt.Sprintf("  %q: %s", rs.Title, part))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	out := "The previous choice; choose differently where the posting is better served (ids only):\n" + strings.Join(lines, "\n") + "\n"
	if len(out) > maxPreviousBytes {
		out = strings.ToValidUTF8(out[:maxPreviousBytes], "") + "…\n"
	}
	return out
}

func joinOffered(ids []string, offered func(id string) bool) string {
	var keep []string
	for _, id := range ids {
		if offered(id) {
			keep = append(keep, id)
		}
	}
	return strings.Join(keep, ", ")
}

// instructionBlock is the owner's preference in its own block. It sits below the
// rules and says so: it steers a choice among what the rules allow.
func (b *builder) instructionBlock() string {
	if b.instr == "" {
		return ""
	}
	return "Owner's preference (it cannot override the rules above):\n" + b.instr + "\n"
}

func (b *builder) sectionIndex(title string) int {
	for i, s := range b.base.sections {
		if s.choosable && s.title == title {
			return i
		}
	}
	return -1
}

// offeredToModel reports whether a pool entry may be shown for a kept redraft: it
// can go in a section the model fills (the scoped entry itself, with an entry
// scope).
func (b *builder) offeredToModel(entry string) bool {
	rd := b.rd
	if rd.scopeEnt != "" {
		return entry == rd.scopeEnt
	}
	for i, s := range b.base.sections {
		if s.choosable && s.kind == "entries" && !rd.sectionKept(i) && b.canHold(i, entry) {
			return true
		}
	}
	return false
}

// keptBullets are the ids on the parts a kept redraft keeps: the model is not
// offered them again (each id is used once on a page).
func (b *builder) keptBullets() map[string]bool {
	rd := b.rd
	used := map[string]bool{}
	for i, sc := range rd.prev.secs {
		switch {
		case rd.sectionKept(i):
			for _, id := range sc.bullets {
				used[id] = true
			}
			for _, e := range sc.entries {
				for _, id := range e.bullets {
					used[id] = true
				}
			}
		case i == rd.scopeSec && rd.scopeEnt != "":
			for _, e := range sc.entries {
				if e.id != rd.scopeEnt {
					for _, id := range e.bullets {
						used[id] = true
					}
				}
			}
		}
	}
	return used
}

// restrictPool is p cut to what the model fills in a kept redraft: entries that
// can go in an open section, and summary bullets only when a summary section is
// open. allowed shrinks with it: the model may use only what it was shown.
func (b *builder) restrictPool(p pool) pool {
	rd := b.rd
	out := pool{allowed: map[string]map[string]bool{}, omitted: p.omitted}
	summaryOpen := rd.scopeEnt == ""
	if summaryOpen {
		summaryOpen = false
		for i, s := range b.base.sections {
			if s.choosable && s.kind == "bullets" && !rd.sectionKept(i) {
				summaryOpen = true
			}
		}
	}
	if summaryOpen {
		out.summary = p.summary
		out.allowed[""] = p.allowed[""]
	}
	for _, e := range p.entries {
		if b.offeredToModel(e.id) {
			out.entries = append(out.entries, e)
			out.allowed[e.id] = p.allowed[e.id]
		}
	}
	return out
}

// ---- the reply's part

// scopeError is a refusal of a reply that changed what a redraft keeps.
type scopeError struct{ msg string }

func (e *scopeError) Error() string { return e.msg }

// refusalCode is the code a build that gives up after this refusal ends with.
func refusalCode(err error) string {
	var se *scopeError
	if errors.As(err, &se) {
		return CodeScope
	}
	return CodeRedraft
}

// parseTailored is parseChoice for a build that may keep parts of a previous page.
func (b *builder) parseTailored(text string, p pool, jd string) (choice, error) {
	if b.rd.keeps() {
		return b.parseKept(text, p, jd)
	}
	return b.parseChoice(text, p, jd)
}

// parseKept validates the model's reply to a redraft that keeps something, and
// returns the whole page: the kept parts from the previous choice, never from the
// reply, and the model's own parts. A reply may repeat a kept part exactly (it is
// ignored); one that changes a kept part is refused with a scopeError before the
// page is rendered. The merged page is then held to the page-safety rules and the
// steering rules as a whole, so a bullet the model picks that a kept part already
// uses, or an entry that duplicates one, is refused.
func (b *builder) parseKept(text string, p pool, jd string) (choice, error) {
	rd := b.rd
	if len(text) > maxReplyBytes {
		return choice{}, fmt.Errorf("the reply is over %d bytes", maxReplyBytes)
	}
	r, err := decodeReply(stripFence(text))
	if err != nil {
		return choice{}, err
	}
	r, err = b.dropRepeatedKept(r)
	if err != nil {
		return choice{}, err
	}
	if _, err := b.checkSafe(r, p, jd); err != nil { // only what the model was shown
		return choice{}, err
	}
	merged, err := b.mergeKept(r)
	if err != nil {
		return choice{}, err
	}
	ch, err := b.checkSafe(merged, rd.full, jd)
	if err != nil {
		return choice{}, err
	}
	if rd.scopedRun() {
		ch.keywords = rd.prev.keywords
	}
	// The previous page's overrides (an owner's title, lead, dates) stay with every
	// entry that stays on the page.
	ov := map[string]entryOverride{}
	for _, sc := range rd.prev.secs {
		for _, e := range sc.entries {
			if !e.over.zero() {
				ov[e.id] = e.over
			}
		}
	}
	onPage := map[string]bool{}
	for _, sc := range ch.secs {
		for _, e := range sc.entries {
			onPage[e.id] = true
		}
	}
	for id := range ov {
		if !onPage[id] {
			delete(ov, id)
		}
	}
	if err := checkOverrides(ov); err != nil {
		return choice{}, err
	}
	if err := attachOverrides(&ch, ov); err != nil {
		return choice{}, err
	}
	if err := b.checkSteering(r, ch, p); err != nil {
		return choice{}, err
	}
	return ch, nil
}

// prevSection is the previous choice's section i as a reply section.
func (b *builder) prevSection(i int) replySection {
	s, sc := b.base.sections[i], b.rd.prev.secs[i]
	rs := replySection{Title: s.title}
	switch s.kind {
	case "bullets":
		rs.Bullets = slices.Clone(sc.bullets)
	case "entries":
		for _, e := range sc.entries {
			ids := slices.Clone(e.bullets)
			if ids == nil {
				ids = []string{}
			}
			rs.Entries = append(rs.Entries, replyEntry{Entry: e.id, Bullets: ids})
		}
	}
	return rs
}

func sameEntries(a, b []replyEntry) bool {
	return slices.EqualFunc(a, b, func(x, y replyEntry) bool {
		return x.Entry == y.Entry && slices.Equal(x.Bullets, y.Bullets)
	})
}

// sameSection reports whether two reply sections say the same thing; an empty
// section says nothing, so it equals an absent one.
func sameSection(a, b replySection) bool {
	return a.Title == b.Title && slices.Equal(a.Bullets, b.Bullets) && sameEntries(a.Entries, b.Entries)
}

// dropRepeatedKept removes from r the parts it repeats exactly from what is kept
// and refuses (scopeError) a reply that names a kept part with anything else.
func (b *builder) dropRepeatedKept(r reply) (reply, error) {
	rd := b.rd
	out := reply{Keywords: r.Keywords}
	var changed []string
	for _, rs := range r.Sections {
		i := b.sectionIndex(rs.Title)
		switch {
		case i < 0:
			out.Sections = append(out.Sections, rs) // checkSafe refuses an unknown title
		case rd.sectionKept(i):
			if len(rs.Bullets) > 0 || len(rs.Entries) > 0 {
				if !sameSection(rs, b.prevSection(i)) {
					changed = append(changed, fmt.Sprintf("%q", rs.Title))
				}
			}
		case i == rd.scopeSec && rd.scopeEnt != "":
			prev := b.prevSection(i)
			keep := rs
			keep.Entries = nil
			for _, e := range rs.Entries {
				if e.Entry == rd.scopeEnt {
					keep.Entries = append(keep.Entries, e)
					continue
				}
				pi := slices.IndexFunc(prev.Entries, func(pe replyEntry) bool { return pe.Entry == e.Entry })
				switch {
				case pi < 0:
					changed = append(changed, "entry "+clip(e.Entry, 60)+" (not on the page)")
				case !slices.Equal(prev.Entries[pi].Bullets, e.Bullets):
					changed = append(changed, "entry "+clip(e.Entry, 60))
				}
			}
			out.Sections = append(out.Sections, keep)
		default:
			out.Sections = append(out.Sections, rs)
		}
	}
	if len(changed) > 0 {
		return reply{}, &scopeError{fmt.Sprintf("the reply changed what is kept as it is: %s; leave kept parts out of the answer", strings.Join(changed, ", "))}
	}
	return out, nil
}

// mergeKept is the whole page as a reply: the previous choice's kept sections
// (and kept entries of the scoped section) and the model's own parts, in the
// order the previous page had, with the project sections the model re-ordered
// back in the slots of the ones it may move.
func (b *builder) mergeKept(r reply) (reply, error) {
	rd := b.rd
	model := map[int]replySection{}
	var modelOrder []int
	reorderable := map[int]bool{}
	for _, i := range b.base.reorderable {
		reorderable[i] = true
	}
	for _, rs := range r.Sections {
		if i := b.sectionIndex(rs.Title); i >= 0 {
			model[i] = rs
			if reorderable[i] && !rd.sectionKept(i) {
				modelOrder = append(modelOrder, i)
			}
		}
	}
	for _, i := range b.base.reorderable { // a section the model left out follows, as checkSafe orders it
		if !rd.sectionKept(i) && !slices.Contains(modelOrder, i) {
			modelOrder = append(modelOrder, i)
		}
	}
	prevOrder := rd.prev.order
	if len(prevOrder) != len(b.base.reorderable) {
		prevOrder = b.base.reorderable
	}
	slots := make([]int, len(prevOrder))
	for j, i := range prevOrder {
		if rd.sectionKept(i) {
			slots[j] = i
			continue
		}
		slots[j], modelOrder = modelOrder[0], modelOrder[1:]
	}

	section := func(i int) (replySection, error) {
		switch {
		case rd.sectionKept(i):
			return b.prevSection(i), nil
		case i == rd.scopeSec && rd.scopeEnt != "":
			ms := model[i]
			var mine *replyEntry
			for k := range ms.Entries {
				if ms.Entries[k].Entry == rd.scopeEnt {
					mine = &ms.Entries[k]
				}
			}
			if mine == nil || len(mine.Bullets) == 0 {
				return replySection{}, fmt.Errorf("entry %q is missing from section %q: re-pick its bullets (1 to %d)", clip(rd.scopeEnt, 60), b.base.sections[i].title, MaxEntryBullets)
			}
			rs := b.prevSection(i)
			for k := range rs.Entries {
				if rs.Entries[k].Entry == rd.scopeEnt {
					rs.Entries[k] = *mine
				}
			}
			return rs, nil
		}
		ms, ok := model[i]
		if !ok {
			ms = replySection{Title: b.base.sections[i].title}
		}
		return ms, nil
	}
	// With a scope the page's keywords stand (parseKept puts them back); the model
	// is not asked for any.
	out := reply{}
	if !rd.scopedRun() {
		out.Keywords = r.Keywords
	}
	for i, s := range b.base.sections {
		if !s.choosable || reorderable[i] {
			continue
		}
		rs, err := section(i)
		if err != nil {
			return reply{}, err
		}
		if len(rs.Bullets) > 0 || len(rs.Entries) > 0 {
			out.Sections = append(out.Sections, rs)
		}
	}
	for _, i := range slots {
		rs, err := section(i)
		if err != nil {
			return reply{}, err
		}
		out.Sections = append(out.Sections, rs) // listed even when empty: its place is the order
	}
	return out, nil
}
