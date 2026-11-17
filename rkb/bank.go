package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------- types

type Review struct {
	Round    int                       `json:"round"`
	Verdict  string                    `json:"verdict"`
	Note     string                    `json:"note,omitempty"`
	Rewrite  string                    `json:"rewrite,omitempty"`
	TextHash string                    `json:"text_hash"`
	By       string                    `json:"by"`
	At       string                    `json:"at,omitempty"`
	Extra    map[string]jsontext.Value `json:"-"`
}

type reviewAlias Review

func (r Review) MarshalJSON() ([]byte, error) {
	b, err := marshalNoEscape(reviewAlias(r))
	if err != nil {
		return nil, err
	}
	return withExtra(b, r.Extra)
}

func (r *Review) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*reviewAlias)(r)); err != nil {
		return err
	}
	x, err := captureExtra(data, reflect.TypeOf(reviewAlias{}))
	r.Extra = x
	return err
}

// Bullet is one sentence, written once in the bank and reused by any resume.
type Bullet struct {
	ID             string   `json:"-"`
	Text           string   `json:"text"`
	Need           string   `json:"need,omitempty"`
	Sources        []string `json:"sources,omitempty"`
	Links          []string `json:"links,omitempty"`
	UnquantifiedOK string   `json:"unquantified_ok,omitempty"`
	TestingOK      string   `json:"testing_ok,omitempty"`
	// ComparisonOK: why a figured work bullet has no baseline or comparator
	// (e.g. a first-of-its-kind capability with nothing to compare against).
	ComparisonOK string `json:"comparison_ok,omitempty"`
	// CautionOK: claim id -> why this sentence may use a figure whose
	// Claims.tex notes say CAUTION (e.g. the wording now matches the scope the
	// caution asks for). Written by the person or the drafter, read by lint.
	CautionOK map[string]string `json:"caution_ok,omitempty"`
	// SelfFixOK: why a lead that reads as fixing a defect is not the person
	// fixing their own bug (e.g. a merged patch to someone else's project).
	SelfFixOK string `json:"self_fix_ok,omitempty"`
	// CaveatOK: why a tail clause that undercuts the result must stay (e.g.
	// the owner's recorded call that a scope note travels with the figure).
	CaveatOK string `json:"caveat_ok,omitempty"`
	// Point: the one point this bullet makes, as a short key. Set, it replaces
	// the mechanism classes lint detects; two bullets on a page with the same
	// point are an error.
	Point string `json:"point,omitempty"`
	// Attested: a statement no artifact can back (employer work under NDA,
	// lab work), confirmed by the person in their own words. It passes the
	// statement gate but is listed in every manifest; never set it for them.
	Attested      string                    `json:"attested,omitempty"`
	AllowLiterals []string                  `json:"allow_literals,omitempty"`
	Glossed       []string                  `json:"glossed,omitempty"`
	Author        string                    `json:"author,omitempty"`
	Origin        []string                  `json:"origin,omitempty"`
	Review        map[string][]Review       `json:"review,omitempty"`
	Extra         map[string]jsontext.Value `json:"-"`
}

type Line struct {
	Label string                    `json:"label,omitempty"`
	Text  string                    `json:"text"`
	Extra map[string]jsontext.Value `json:"-"`
}

type lineAlias Line

func (l Line) MarshalJSON() ([]byte, error) {
	b, err := marshalNoEscape(lineAlias(l))
	if err != nil {
		return nil, err
	}
	return withExtra(b, l.Extra)
}

func (l *Line) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*lineAlias)(l)); err != nil {
		return err
	}
	x, err := captureExtra(data, reflect.TypeOf(lineAlias{}))
	l.Extra = x
	return err
}

type Header struct {
	Name     string                    `json:"name"`
	Headline string                    `json:"headline,omitempty"`
	Contact  []string                  `json:"contact"`
	Extra    map[string]jsontext.Value `json:"-"`
}

type headerAlias Header

func (h Header) MarshalJSON() ([]byte, error) {
	b, err := marshalNoEscape(headerAlias(h))
	if err != nil {
		return nil, err
	}
	return withExtra(b, h.Extra)
}

func (h *Header) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*headerAlias)(h)); err != nil {
		return err
	}
	x, err := captureExtra(data, reflect.TypeOf(headerAlias{}))
	h.Extra = x
	return err
}

type BankEntry struct {
	Title    string                    `json:"title,omitempty"`
	Org      string                    `json:"org,omitempty"`
	Dates    string                    `json:"dates,omitempty"`
	Stack    string                    `json:"stack,omitempty"`
	Location string                    `json:"location,omitempty"`
	Sub      string                    `json:"sub,omitempty"`
	Lead     string                    `json:"lead,omitempty"`
	Layout   string                    `json:"layout,omitempty"`
	Group    string                    `json:"group,omitempty"`
	Members  []string                  `json:"members,omitempty"`
	Links    []string                  `json:"links,omitempty"`
	Bullets  []string                  `json:"bullets,omitempty"`
	Origin   []string                  `json:"origin,omitempty"`
	Extra    map[string]jsontext.Value `json:"-"`
}

type bankEntryAlias BankEntry

func (e BankEntry) MarshalJSON() ([]byte, error) {
	b, err := marshalNoEscape(bankEntryAlias(e))
	if err != nil {
		return nil, err
	}
	return withExtra(b, e.Extra)
}

func (e *BankEntry) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*bankEntryAlias)(e)); err != nil {
		return err
	}
	x, err := captureExtra(data, reflect.TypeOf(bankEntryAlias{}))
	e.Extra = x
	return err
}

type Bank struct {
	About   string                `json:"_about,omitempty"`
	Headers map[string]Header     `json:"headers"`
	Entries map[string]*BankEntry `json:"entries"`
	Bullets map[string]*Bullet    `json:"bullets"`
	Lines   map[string][]Line     `json:"lines"`
	path    string
	guard   guard
	raw     []byte
	// allowDrop: field paths ($.bullets.<id>.need) a command clears on purpose.
	allowDrop []string
	Extra     map[string]jsontext.Value `json:"-"`
}

type bankAlias Bank

func (b *Bank) MarshalJSON() ([]byte, error) {
	out, err := marshalNoEscape((*bankAlias)(b))
	if err != nil {
		return nil, err
	}
	return withExtra(out, b.Extra)
}

func (b *Bank) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*bankAlias)(b)); err != nil {
		return err
	}
	x, err := captureExtra(data, reflect.TypeOf(bankAlias{}))
	b.Extra = x
	return err
}

type Meta struct {
	Target          string            `json:"target,omitempty"`
	Pages           int               `json:"pages,omitzero"`
	Paper           string            `json:"paper,omitempty"`
	BoldFigures     *bool             `json:"bold_figures,omitempty"`
	OutputBasename  string            `json:"output_basename,omitempty"`
	JDKeywords      []string          `json:"jd_keywords,omitempty"`
	MaxTestingShare *float64          `json:"max_testing_share,omitempty"`
	Style           map[string]string `json:"style,omitempty"`
	Origin          string            `json:"origin,omitempty"`
}

// Selection is one resume: which bank entries and bullets, in what order.
type SelEntry struct {
	Entry   string   `json:"entry"`
	Bullets []string `json:"bullets,omitempty"`
	// optional per-resume overrides of the bank entry
	Title string `json:"title,omitempty"`
	Stack string `json:"stack,omitempty"`
	Lead  string `json:"lead,omitempty"`
	Org   string `json:"org,omitempty"`
	Dates string `json:"dates,omitempty"`
}

func (s *SelEntry) UnmarshalJSON(b []byte) error {
	var id string
	if json.Unmarshal(b, &id) == nil {
		s.Entry = id
		return nil
	}
	type plain SelEntry
	return json.Unmarshal(b, (*plain)(s))
}

type SelSection struct {
	Title    string         `json:"title"`
	Kind     string         `json:"kind,omitempty"`
	Lead     string         `json:"lead,omitempty"`
	Bulleted bool           `json:"bulleted,omitzero"`
	Bullets  []string       `json:"bullets,omitempty"`
	Entries  []SelEntry     `json:"entries,omitempty"`
	Lines    jsontext.Value `json:"lines,omitempty"`
}

type Selection struct {
	Bank     string         `json:"bank"`
	Meta     Meta           `json:"meta"`
	Header   jsontext.Value `json:"header"`
	Sections []SelSection   `json:"sections"`
}

// Composed is what every renderer and check works on.
type Entry struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Org      string    `json:"org,omitempty"`
	Dates    string    `json:"dates,omitempty"`
	Stack    string    `json:"stack,omitempty"`
	Location string    `json:"location,omitempty"`
	Sub      string    `json:"sub,omitempty"`
	Lead     string    `json:"lead,omitempty"`
	Layout   string    `json:"layout,omitempty"`
	Members  []string  `json:"members,omitempty"`
	Links    []string  `json:"links,omitempty"`
	Bullets  []*Bullet `json:"bullets,omitempty"`
}

type Section struct {
	Title    string    `json:"title"`
	Kind     string    `json:"kind"`
	Lead     string    `json:"lead,omitempty"`
	Bulleted bool      `json:"bulleted,omitzero"`
	Bullets  []*Bullet `json:"bullets,omitempty"`
	Entries  []*Entry  `json:"entries,omitempty"`
	Lines    []Line    `json:"lines,omitempty"`
}

type Composed struct {
	Composed  bool       `json:"composed"`
	Selection string     `json:"selection"`
	BankPath  string     `json:"bank_path"`
	BankSHA   string     `json:"bank_sha256,omitempty"`
	Meta      Meta       `json:"meta"`
	Header    Header     `json:"header"`
	Sections  []*Section `json:"sections"`
}

// Bullets carry their id in composed files.
type bulletJSON struct {
	ID string `json:"id"`
	*bulletAlias
}
type bulletAlias Bullet

func (b *Bullet) MarshalJSON() ([]byte, error) {
	out, err := marshalNoEscape(bulletJSON{ID: b.ID, bulletAlias: (*bulletAlias)(b)})
	if err != nil {
		return nil, err
	}
	return withExtra(out, b.Extra)
}

func (b *Bullet) UnmarshalJSON(data []byte) error {
	var aux struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &aux)
	if err := json.Unmarshal(data, (*bulletAlias)(b)); err != nil {
		return err
	}
	if aux.ID != "" {
		b.ID = aux.ID
	}
	x, err := captureExtra(data, reflect.TypeOf(bulletAlias{}), "id")
	b.Extra = x
	return err
}

// ---------------------------------------------------------------- bank io

func loadBank(p string) *Bank {
	raw, g, err := readGuarded(p)
	if err != nil {
		die("bank: %v", err)
	}
	if !g.exists {
		die("bank: %s not found", p)
	}
	b, err := parseBank(p, raw)
	if err != nil {
		die("bank: %v", err)
	}
	b.guard, b.raw = g, raw
	return b
}

func parseBank(p string, raw []byte) (*Bank, error) {
	var b Bank
	if err := decodeJSONStrict(p, raw, &b); err != nil {
		return nil, err
	}
	if b.Headers == nil {
		b.Headers = map[string]Header{}
	}
	if b.Entries == nil {
		b.Entries = map[string]*BankEntry{}
	}
	if b.Bullets == nil {
		b.Bullets = map[string]*Bullet{}
	}
	if b.Lines == nil {
		b.Lines = map[string][]Line{}
	}
	for id, bl := range b.Bullets {
		if bl == nil {
			return nil, fmt.Errorf("%s: bullet %q is null", p, id)
		}
		bl.ID = id
	}
	for id, e := range b.Entries {
		if e == nil {
			return nil, fmt.Errorf("%s: entry %q is null", p, id)
		}
	}
	b.path = p
	return &b, nil
}

// encodeBank serialises the bank and refuses to drop any field the file had.
func encodeBank(b *Bank, old []byte) ([]byte, error) {
	nb, err := encodeJSON(b)
	if err != nil {
		return nil, err
	}
	if old != nil {
		var d []string
		for _, f := range droppedFields(old, nb) {
			if !contains(b.allowDrop, f) {
				d = append(d, f)
			}
		}
		if len(d) > 0 {
			return nil, fmt.Errorf("refusing to write %s: it would drop field(s) rkb does not model: %s (add them to the Bullet/BankEntry structs first)", b.path, humanList(d, 6))
		}
	}
	return nb, nil
}

// save writes the bank back only if nobody changed the file since it was
// loaded (compare-and-swap) and no field would be lost.
func (b *Bank) save() {
	nb, err := encodeBank(b, b.raw)
	if err != nil {
		die("%v", err)
	}
	if err := withLock(b.path, 2*time.Minute, func() error { return b.guard.commit(nb) }); err != nil {
		if errors.Is(err, errConflict) {
			die("%v. Another tool or agent edited the bank while rkb was working; nothing was written. Re-run the command.", err)
		}
		die("%v", err)
	}
	b.raw, b.guard = nb, guard{path: b.path, sha: sha256Bytes(nb), exists: true}
}

// updateBank is the safe read-modify-write for the bank: lock, load, mutate,
// compare-and-swap; on a conflict with an outside writer it reloads and
// re-applies mutate (which must only depend on the bank it is given).
// mutate returns false when there is nothing to write.
func updateBank(p string, mutate func(b *Bank) (bool, error)) error {
	return guardedUpdate(p, 4, func(old []byte, exists bool) ([]byte, error) {
		if !exists {
			return nil, fmt.Errorf("bank: %s not found", p)
		}
		b, err := parseBank(p, old)
		if err != nil {
			return nil, err
		}
		changed, err := mutate(b)
		if err != nil || !changed {
			return nil, err
		}
		return encodeBank(b, old)
	})
}

// ---------------------------------------------------------------- groups

type Group struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Members []string `json:"members"`
	Stack   string   `json:"stack"`
	Layer   string   `json:"layer"`
	Lead    string   `json:"lead"`
	Links   []string `json:"links"`
}

// loadGroups reads ecosystem groups from knowledge.json. A missing file gives
// no groups (compose then reports every entry that names one); a file that
// exists but does not parse is fatal rather than silently "no groups".
func loadGroups() (map[string]Group, error) {
	out := map[string]Group{}
	var kb struct {
		Ecosystem struct {
			Groups []Group `json:"groups"`
		} `json:"ecosystem"`
	}
	p := inRoot(cfg.KnowledgeJSON)
	if !fileExists(p) {
		if len(cfg.Groups) > 0 {
			for _, g := range cfg.Groups {
				out[g.ID] = g
			}
			return out, nil
		}
		return out, fmt.Errorf("%s not found", p)
	}
	if err := readJSON(p, &kb); err != nil {
		die("knowledge.json: %v", err)
	}
	for _, g := range kb.Ecosystem.Groups {
		if _, dup := out[g.ID]; dup {
			die("knowledge.json: ecosystem group %q defined twice", g.ID)
		}
		out[g.ID] = g
	}
	return out, nil
}

// ---------------------------------------------------------------- compose

// loadResume accepts a selection (bank-backed) or an already composed file.
func loadResume(p string) *Composed {
	c, errs := loadResumeErr(p)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "ERROR "+e)
		}
		die("%s: %d composition error(s)", p, len(errs))
	}
	return c
}

// loadResumeErr is loadResume that reports instead of exiting.
func loadResumeErr(p string) (*Composed, []string) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if d, err := dupKeys(raw); err != nil {
		return nil, []string{fmt.Sprintf("%s: invalid JSON: %v", p, err)}
	} else if len(d) > 0 {
		return nil, []string{fmt.Sprintf("%s: duplicate key(s) %s", p, humanList(d, 5))}
	}
	var probe struct {
		Composed bool   `json:"composed"`
		Bank     string `json:"bank"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", p, err)}
	}
	if probe.Composed {
		var c Composed
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, []string{fmt.Sprintf("%s: %v", p, err)}
		}
		return &c, nil
	}
	if probe.Bank == "" {
		return nil, []string{fmt.Sprintf("%s: not a resume selection (no \"bank\") and not a composed file", p)}
	}
	if bad := selectionUnknownKeys(raw); len(bad) > 0 {
		return nil, []string{fmt.Sprintf("%s: unknown field(s) %s (a typo here would silently drop content)", p, humanList(bad, 6))}
	}
	var sel Selection
	if err := json.Unmarshal(raw, &sel); err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", p, err)}
	}
	return composeErr(p, &sel)
}

// selectionUnknownKeys lists keys a selection file has that rkb does not read.
func selectionUnknownKeys(raw []byte) []string {
	var top map[string]jsontext.Value
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	allow := func(path string, m map[string]jsontext.Value, keys ...string) []string {
		var bad []string
		for k := range m {
			if !contains(keys, k) && !strings.HasPrefix(k, "_") {
				bad = append(bad, path+"."+k)
			}
		}
		return bad
	}
	bad := allow("$", top, "bank", "meta", "header", "sections")
	var meta map[string]jsontext.Value
	if json.Unmarshal(top["meta"], &meta) == nil {
		bad = append(bad, allow("$.meta", meta, "target", "pages", "paper", "bold_figures", "output_basename", "jd_keywords", "max_testing_share", "style", "origin")...)
	}
	var secs []map[string]jsontext.Value
	if json.Unmarshal(top["sections"], &secs) == nil {
		for i, sec := range secs {
			sp := fmt.Sprintf("$.sections[%d]", i)
			bad = append(bad, allow(sp, sec, "title", "kind", "lead", "bulleted", "bullets", "entries", "lines")...)
			var ents []jsontext.Value
			if json.Unmarshal(sec["entries"], &ents) == nil {
				for j, e := range ents {
					var em map[string]jsontext.Value
					if json.Unmarshal(e, &em) == nil {
						bad = append(bad, allow(fmt.Sprintf("%s.entries[%d]", sp, j), em, "entry", "bullets", "title", "stack", "lead", "org", "dates")...)
					}
				}
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func bankPathFor(selPath, bankRef string) string {
	if filepath.IsAbs(bankRef) {
		return bankRef
	}
	return filepath.Join(filepath.Dir(selPath), bankRef)
}

func composeErr(selPath string, sel *Selection) (*Composed, []string) {
	bp := bankPathFor(selPath, sel.Bank)
	bank := loadBank(bp)
	groups, gerr := loadGroups()
	var errs []string
	c := &Composed{Composed: true, Selection: selPath, BankPath: bp, BankSHA: bank.guard.sha, Meta: sel.Meta}

	// header: a name in bank.headers, or inline
	var hname string
	if json.Unmarshal(sel.Header, &hname) == nil {
		h, ok := bank.Headers[hname]
		if !ok {
			errs = append(errs, fmt.Sprintf("header %q not in bank.headers", hname))
		}
		c.Header = h
	} else if len(sel.Header) > 0 && string(sel.Header) != "null" {
		if err := json.Unmarshal(sel.Header, &c.Header); err != nil {
			errs = append(errs, fmt.Sprintf("header: %v", err))
		}
	} else if h, ok := bank.Headers["default"]; ok {
		c.Header = h
	}
	if c.Header.Name == "" {
		errs = append(errs, "header has no name")
	}

	getBullet := func(id, where string) *Bullet {
		b, ok := bank.Bullets[id]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: bullet %q not in bank", where, id))
			return &Bullet{ID: id, Text: "[missing bullet " + id + "]"}
		}
		return b
	}

	for si, ss := range sel.Sections {
		if strings.TrimSpace(ss.Title) == "" {
			errs = append(errs, fmt.Sprintf("section %d has no title", si+1))
		}
		kind := ss.Kind
		if kind == "" {
			switch {
			case len(ss.Entries) > 0:
				kind = "entries"
			case len(ss.Lines) > 0:
				kind = "lines"
			default:
				kind = "bullets"
			}
		}
		if kind != "bullets" && kind != "lines" && kind != "entries" {
			errs = append(errs, fmt.Sprintf("section %q: kind %q is not bullets, lines or entries", ss.Title, kind))
		}
		sec := &Section{Title: ss.Title, Kind: kind, Lead: ss.Lead, Bulleted: ss.Bulleted}
		switch kind {
		case "bullets":
			for _, id := range ss.Bullets {
				sec.Bullets = append(sec.Bullets, getBullet(id, ss.Title))
			}
		case "lines":
			var ref string
			if json.Unmarshal(ss.Lines, &ref) == nil {
				ls, ok := bank.Lines[ref]
				if !ok {
					errs = append(errs, fmt.Sprintf("%s: lines %q not in bank", ss.Title, ref))
				}
				sec.Lines = ls
			} else if len(ss.Lines) > 0 {
				if err := json.Unmarshal(ss.Lines, &sec.Lines); err != nil {
					errs = append(errs, fmt.Sprintf("%s: lines: %v", ss.Title, err))
				}
			}
		default:
			for _, se := range ss.Entries {
				be, ok := bank.Entries[se.Entry]
				if !ok {
					errs = append(errs, fmt.Sprintf("%s: entry %q not in bank", ss.Title, se.Entry))
					continue
				}
				e := &Entry{ID: se.Entry, Title: be.Title, Org: be.Org, Dates: be.Dates, Stack: be.Stack,
					Location: be.Location, Sub: be.Sub, Lead: be.Lead, Layout: be.Layout,
					Members: be.Members, Links: be.Links}
				if be.Group != "" {
					g, ok := groups[be.Group]
					if !ok {
						why := "not in knowledge.json ecosystem.groups"
						if gerr != nil {
							why = "cannot be resolved: " + gerr.Error()
						}
						errs = append(errs, fmt.Sprintf("entry %q: group %q %s", se.Entry, be.Group, why))
					}
					if ok {
						if e.Title == "" { // the group names the entry only when the bank entry doesn't
							e.Title = g.Title
							if len(e.Members) == 0 {
								e.Members = g.Members
							}
						}
						if e.Stack == "" {
							e.Stack = g.Stack
						}
						if e.Lead == "" {
							e.Lead = g.Lead
						}
						if len(e.Links) == 0 {
							e.Links = g.Links
						}
					}
				}
				for _, ov := range [][2]*string{{&e.Title, &se.Title}, {&e.Stack, &se.Stack}, {&e.Lead, &se.Lead}, {&e.Org, &se.Org}, {&e.Dates, &se.Dates}} {
					if *ov[1] != "" {
						*ov[0] = *ov[1]
					}
				}
				if e.Lead == "-" {
					e.Lead = ""
				}
				if strings.TrimSpace(e.Title) == "" {
					errs = append(errs, fmt.Sprintf("entry %q has no title", se.Entry))
				}
				ids := se.Bullets
				if ids == nil {
					ids = be.Bullets
				}
				for _, id := range ids {
					e.Bullets = append(e.Bullets, getBullet(id, se.Entry))
				}
				sec.Entries = append(sec.Entries, e)
			}
		}
		c.Sections = append(c.Sections, sec)
	}
	return c, errs
}

// allBullets yields every bullet with its section and entry.
type bulletRef struct {
	Sec *Section
	Ent *Entry
	B   *Bullet
}

func (c *Composed) allBullets() []bulletRef {
	var out []bulletRef
	for _, s := range c.Sections {
		for _, b := range s.Bullets {
			out = append(out, bulletRef{s, nil, b})
		}
		for _, e := range s.Entries {
			for _, b := range e.Bullets {
				out = append(out, bulletRef{s, e, b})
			}
		}
	}
	return out
}

// leads are checked like bullets (statement check), keyed by where they sit.
type leadRef struct {
	Key  string
	Text string
}

func (c *Composed) leads() []leadRef {
	var out []leadRef
	for _, s := range c.Sections {
		if s.Lead != "" {
			out = append(out, leadRef{"seclead:" + slug(s.Title, 40), s.Lead})
		}
		for _, e := range s.Entries {
			if e.Lead != "" {
				out = append(out, leadRef{"lead:" + e.ID, e.Lead})
			}
		}
	}
	return out
}

// resumeHash identifies the exact content a claims export was made for: every
// field the renderers print (header, section titles, kinds and leads, skills
// lines, entry title, members, org, dates, stack, location, sub, lead and
// layout, bullets) and the value of every figure token those fields use.
// vals maps a claim id to its value; a token with no value hashes as absent,
// so a claim that appears or disappears also changes the hash. Each field is
// length-prefixed, so no two different resumes encode to the same bytes.
func (c *Composed) resumeHash(vals map[string]string) string {
	var b strings.Builder
	put := func(k, v string) { fmt.Fprintf(&b, "%s=%d:%s;", k, len(v), v) }
	put("name", c.Header.Name)
	put("headline", c.Header.Headline)
	for i, x := range c.Header.Contact {
		put(fmt.Sprintf("contact%d", i), x)
	}
	for si, s := range c.Sections {
		p := fmt.Sprintf("s%d.", si)
		put(p+"title", s.Title)
		put(p+"kind", s.Kind)
		put(p+"lead", s.Lead)
		put(p+"bulleted", fmt.Sprint(s.Bulleted))
		for i, bl := range s.Bullets {
			put(fmt.Sprintf("%sb%d.id", p, i), bl.ID)
			put(fmt.Sprintf("%sb%d", p, i), bl.Text)
		}
		for i, l := range s.Lines {
			put(fmt.Sprintf("%sl%d.label", p, i), l.Label)
			put(fmt.Sprintf("%sl%d", p, i), l.Text)
		}
		for ei, e := range s.Entries {
			q := fmt.Sprintf("%se%d.", p, ei)
			put(q+"id", e.ID)
			put(q+"title", e.Title)
			put(q+"org", e.Org)
			put(q+"dates", e.Dates)
			put(q+"stack", e.Stack)
			put(q+"location", e.Location)
			put(q+"sub", e.Sub)
			put(q+"lead", e.Lead)
			put(q+"layout", e.Layout)
			for i, m := range e.Members {
				put(fmt.Sprintf("%smember%d", q, i), m)
			}
			for i, bl := range e.Bullets {
				put(fmt.Sprintf("%sb%d.id", q, i), bl.ID)
				put(fmt.Sprintf("%sb%d", q, i), bl.Text)
			}
		}
	}
	ids := map[string]bool{}
	for _, m := range tokenRe.FindAllStringSubmatch(b.String(), -1) {
		ids[m[1]] = true
	}
	for _, id := range sortedKeys(ids) {
		if v, ok := vals[id]; ok {
			put("claim."+id, v)
		} else {
			put("claim."+id+".absent", "")
		}
	}
	return sha1hex(b.String())
}

// claimValues maps claim id to value, for the resumeHash of a claims export.
func claimValues(claims map[string]*Claim) map[string]string {
	out := make(map[string]string, len(claims))
	for id, cl := range claims {
		out[id] = cl.Value
	}
	return out
}

// claimViewValues is the same map as read back from a claims.json.
func claimViewValues(cf *ClaimsFile) map[string]string {
	out := make(map[string]string, len(cf.Claims))
	for id, cv := range cf.Claims {
		out[id] = cv.Value
	}
	return out
}

// currentClaimValues reads the values Claims.tex holds now. A configured file
// that cannot be read or parsed is an error: a hash check that cannot see the
// claim values must not read as one that matched.
func currentClaimValues() (map[string]string, error) {
	cl, errs, _ := parseClaimsFiles(cfg.ClaimsTex, inRoot)
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return claimValues(cl), nil
}

func (c *Composed) boldFigures() bool { return c.Meta.BoldFigures == nil || *c.Meta.BoldFigures }

func (c *Composed) paper() string {
	p := strings.ToLower(strings.TrimSpace(c.Meta.Paper))
	if p == "" {
		p = strings.ToLower(cfg.Layout.Paper)
	}
	if p != "a4" {
		p = "letter"
	}
	return p
}

func (c *Composed) basename(selPath string) string {
	if c.Meta.OutputBasename != "" {
		return c.Meta.OutputBasename
	}
	return strings.TrimSuffix(filepath.Base(selPath), filepath.Ext(selPath))
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string, max int) string {
	s = strings.ToLower(plainText(s))
	s = strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
	if len(s) > max {
		s = strings.Trim(s[:max], "-")
	}
	return s
}

// ---------------------------------------------------------------- commands

func cmdCompose(a *args) {
	sel := a.need(0, "selection file")
	c := loadResume(sel)
	out := a.get("out", "")
	if out == "" {
		out = strings.TrimSuffix(sel, ".json") + ".composed.json"
	}
	writeJSON(out, c)
	vals, err := currentClaimValues()
	if err != nil {
		vals = nil // Claims.tex unreadable: the hash then marks every figure absent
	}
	fmt.Printf("wrote %s (%d bullets, hash %s)\n", out, len(c.allBullets()), c.resumeHash(vals))
}
