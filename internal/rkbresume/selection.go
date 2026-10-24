package rkbresume

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// outputBasename names every file rkb writes for a built resume and appears in
// its output lines. A constant, so nothing about the owner or the job reaches
// them.
const outputBasename = "resume"

// tokenRe is rkb's claim token (rkb render.go:12, as kbsync's tokenRe). kbsync
// keeps its copy unexported.
var tokenRe = regexp.MustCompile(`\{\{(clm[A-Za-z]+)\}\}`)

// tokens lists the distinct claim ids a bullet's text uses.
func tokens(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range tokenRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// baseSel is the base selection as the owner wrote it. Everything this package
// does not choose stays in the raw maps and is written back unchanged.
type baseSel struct {
	top      map[string]json.RawMessage
	meta     map[string]json.RawMessage
	pages    int
	sections []baseSection
	// expIdx is the experience section; reorderable the project sections after
	// it (reorderableSections).
	expIdx      int
	reorderable []int
}

type baseSection struct {
	raw     json.RawMessage
	fields  map[string]json.RawMessage
	title   string
	kind    string // "bullets", "entries" or "lines"
	bullets []string
	entries []baseEntry
	// choosable: the section's content is chosen (a bullets section, or an
	// entries section in which some entry lists bullets). Education-like
	// sections, whose entries list none, and lines sections are kept as written.
	choosable bool
}

type baseEntry struct {
	fields  map[string]json.RawMessage
	id      string
	bullets []string
}

// loadBase reads the base selection strictly: bounded, one object, no duplicate
// keys (kbsync.ParseSelection), and a shape this package can rewrite without
// letting rkb pull in bullets the owner did not list.
func loadBase(path string, maxPages int) (*baseSel, *Error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fail(CodeKB, "base selection: %v", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, kbsync.MaxSelectionBytes+1))
	if err != nil {
		return nil, fail(CodeKB, "base selection: %v", err)
	}
	if _, err := kbsync.ParseSelection(raw); err != nil {
		return nil, fail(CodeKB, "base selection: %v", err)
	}
	b := &baseSel{}
	if err := json.Unmarshal(raw, &b.top); err != nil || b.top == nil {
		return nil, fail(CodeKB, "base selection is not a JSON object")
	}
	for _, k := range []string{"header", "meta", "sections"} {
		if len(b.top[k]) == 0 {
			return nil, fail(CodeKB, "base selection has no %q", k)
		}
	}
	if err := json.Unmarshal(b.top["meta"], &b.meta); err != nil || b.meta == nil {
		return nil, fail(CodeKB, "base selection's meta is not an object")
	}
	var meta struct {
		Pages int `json:"pages"`
	}
	_ = json.Unmarshal(b.top["meta"], &meta)
	if meta.Pages < 1 || meta.Pages > maxPages {
		return nil, fail(CodeConfig, "base selection's meta.pages is %d; a built resume is 1 to %d pages", meta.Pages, maxPages)
	}
	b.pages = meta.Pages
	var secs []json.RawMessage
	if err := json.Unmarshal(b.top["sections"], &secs); err != nil || len(secs) == 0 {
		return nil, fail(CodeKB, "base selection has no sections")
	}
	titles := map[string]bool{}
	for i, rs := range secs {
		s, err := parseBaseSection(rs)
		if err != nil {
			return nil, fail(CodeKB, "base selection section %d: %v", i+1, err)
		}
		if s.choosable {
			if titles[s.title] {
				return nil, fail(CodeKB, "base selection has two chosen sections titled %q", s.title)
			}
			titles[s.title] = true
		}
		b.sections = append(b.sections, s)
	}
	b.expIdx, b.reorderable = reorderableSections(b.sections)
	return b, nil
}

func parseBaseSection(rs json.RawMessage) (baseSection, error) {
	s := baseSection{raw: rs}
	if err := json.Unmarshal(rs, &s.fields); err != nil || s.fields == nil {
		return s, fmt.Errorf("not an object")
	}
	_ = json.Unmarshal(s.fields["title"], &s.title)
	_ = json.Unmarshal(s.fields["kind"], &s.kind)
	switch s.kind {
	case "lines":
		return s, nil
	case "bullets":
		if err := json.Unmarshal(s.fields["bullets"], &s.bullets); err != nil {
			return s, fmt.Errorf("bullets is not a list of ids")
		}
		s.choosable = true
		return s, nil
	case "entries":
	default:
		// rkb infers a missing kind, kbsync.Eligible does not read such a
		// section, so its bullets could not be checked.
		return s, fmt.Errorf("kind %q is not bullets, entries or lines", s.kind)
	}
	var ents []json.RawMessage
	if err := json.Unmarshal(s.fields["entries"], &ents); err != nil {
		return s, fmt.Errorf("entries is not a list")
	}
	for _, re := range ents {
		e := baseEntry{}
		if err := json.Unmarshal(re, &e.fields); err != nil || e.fields == nil {
			return s, fmt.Errorf("an entry is not an object")
		}
		if err := json.Unmarshal(e.fields["entry"], &e.id); err != nil || e.id == "" {
			return s, fmt.Errorf("an entry has no id")
		}
		// An entry without a bullets list makes rkb take every bullet the bank
		// has for it, eligible or not.
		if _, ok := e.fields["bullets"]; !ok {
			return s, fmt.Errorf("entry %q has no bullets list", e.id)
		}
		if err := json.Unmarshal(e.fields["bullets"], &e.bullets); err != nil {
			return s, fmt.Errorf("entry %q: bullets is not a list of ids", e.id)
		}
		if len(e.bullets) > 0 {
			s.choosable = true
		}
		s.entries = append(s.entries, e)
	}
	return s, nil
}

// choice is what goes on the page: for each base section, either its bullets
// or its entries with their bullets, in order.
type choice struct {
	secs     []secChoice // parallel to baseSel.sections
	keywords []string    // nil keeps the base selection's own
	// order is the order of the reorderable sections on the page: a permutation
	// of baseSel.reorderable. Nil keeps the base order.
	order []int
}

type secChoice struct {
	bullets []string
	entries []entChoice
}

type entChoice struct {
	id      string
	bullets []string
	// keepEmpty: the base lists the entry with no bullets (a heading only).
	keepEmpty bool
	// heading: the entry stays as a heading when every bullet of it is dropped
	// (baseSel.keepsHeading). A job does not disappear from the work history
	// because the evidence under it could not be verified.
	heading bool
}

// keeps reports whether the entry stays on the page with no bullets.
func (e entChoice) keeps() bool { return e.keepEmpty || e.heading }

// filterList keeps the ids of one list for which keep is true, in order. A
// continuation (isCont) survives only when the bullet directly before it in
// the list survived too, so dropping a bullet never leaves one that refers to
// it; the first bullet of a list cannot be a continuation. cascaded lists the
// continuations dropped for that reason alone.
func filterList(ids []string, keep, isCont func(id string) bool) (kept, cascaded []string) {
	prev := false
	for i, id := range ids {
		ok := keep(id)
		if ok && isCont(id) && (i == 0 || !prev) {
			ok = false
			cascaded = append(cascaded, id)
		}
		if ok {
			kept = append(kept, id)
		}
		prev = ok
	}
	return kept, cascaded
}

// listsBare reports whether the base lists entry in section i with no bullets.
func (b *baseSel) listsBare(i int, entry string) bool {
	for _, e := range b.sections[i].entries {
		if e.id == entry {
			return len(e.bullets) == 0
		}
	}
	return false
}

// keepsHeading reports whether an entry of section i stays on the page as a
// heading when every bullet of it is dropped as unverified: every entry of the
// work history (the experience section), and any entry the base lists bare. A
// project entry the base lists with bullets is not kept bare: a project heading
// with no evidence says nothing, so it goes with its last bullet.
func (b *baseSel) keepsHeading(i int, entry string) bool {
	return i == b.expIdx || b.listsBare(i, entry)
}

// entryHomes says in which base sections each entry may be placed: those the
// base lists it in, and those of the same title in any other selection. A
// selection's section is matched by title, since that is how the owner groups
// work (an entry listed under "Experience" is a job, under a project title a
// project). An entry with no home cannot be placed at all.
func entryHomes(base *baseSel, sels []kbsync.Selection) map[string]map[int]bool {
	homes := map[string]map[int]bool{}
	add := func(entry string, i int) {
		if homes[entry] == nil {
			homes[entry] = map[int]bool{}
		}
		homes[entry][i] = true
	}
	byTitle := map[string]int{}
	for i, s := range base.sections {
		if s.choosable && s.kind == "entries" {
			byTitle[s.title] = i
			for _, e := range s.entries {
				add(e.id, i)
			}
		}
	}
	for _, sel := range sels {
		for _, sec := range sel.Sections {
			i, ok := byTitle[sec.Title]
			if !ok || sec.Kind != "entries" {
				continue
			}
			for _, e := range sec.Entries {
				add(e.Entry, i)
			}
		}
	}
	return homes
}

// baseChoice is the base selection filtered to what allowed lets through. A
// continuation whose predecessor in the base's own list is filtered out goes
// with it; a coherent base is unchanged. An entry none of whose bullets is
// eligible is dropped (a bare entry is shown only when the base lists it bare),
// but one that loses its bullets later, to a figure rkb has not verified, may
// stay as a heading (keepsHeading).
func (b *baseSel) baseChoice(allowed func(entry, id string) bool, isCont func(id string) bool) choice {
	ch := choice{secs: make([]secChoice, len(b.sections))}
	for i, s := range b.sections {
		if !s.choosable {
			continue
		}
		sc := &ch.secs[i]
		sc.bullets, _ = filterList(s.bullets, func(id string) bool { return allowed("", id) }, isCont)
		for _, e := range s.entries {
			ec := entChoice{id: e.id, keepEmpty: len(e.bullets) == 0, heading: b.keepsHeading(i, e.id)}
			ec.bullets, _ = filterList(e.bullets, func(id string) bool { return allowed(e.id, id) }, isCont)
			if len(ec.bullets) > 0 || ec.keepEmpty {
				sc.entries = append(sc.entries, ec)
			}
		}
	}
	return ch
}

// count is the number of bullets chosen.
func (c choice) count() int {
	n := 0
	for _, s := range c.secs {
		n += len(s.bullets)
		for _, e := range s.entries {
			n += len(e.bullets)
		}
	}
	return n
}

// without removes bullets and the continuations that followed them, then the
// entries they leave empty, except entries that keep their heading
// (entChoice.keeps). dependents lists the continuations removed only
// because their predecessor went.
func (c choice) without(drop map[string]bool, isCont func(id string) bool) (out choice, dependents []string) {
	out = choice{secs: make([]secChoice, len(c.secs)), keywords: c.keywords, order: c.order}
	keep := func(id string) bool { return !drop[id] }
	for i, s := range c.secs {
		var cas []string
		out.secs[i].bullets, cas = filterList(s.bullets, keep, isCont)
		dependents = append(dependents, cas...)
		for _, e := range s.entries {
			ne := entChoice{id: e.id, keepEmpty: e.keepEmpty, heading: e.heading}
			ne.bullets, cas = filterList(e.bullets, keep, isCont)
			dependents = append(dependents, cas...)
			if len(ne.bullets) > 0 || ne.keeps() {
				out.secs[i].entries = append(out.secs[i].entries, ne)
			}
		}
	}
	return out, dependents
}

// bulletIDs lists every chosen bullet id, in page order.
func (c choice) bulletIDs() []string {
	var ids []string
	for _, s := range c.secs {
		ids = append(ids, s.bullets...)
		for _, e := range s.entries {
			ids = append(ids, e.bullets...)
		}
	}
	return ids
}

// reorderableSections are the project sections the model may put in any order:
// the run of choosable entries sections that follows the first choosable
// entries section (the experience section, which stays first). Summary, the
// experience section and everything after the run (skills, education,
// publications) keep their place. The run stops at the first section that is
// not a choosable entries section.
func reorderableSections(secs []baseSection) (exp int, run []int) {
	exp = -1
	for i, s := range secs {
		if s.choosable && s.kind == "entries" {
			exp = i
			break
		}
	}
	if exp < 0 {
		return -1, nil
	}
	for i := exp + 1; i < len(secs) && secs[i].choosable && secs[i].kind == "entries"; i++ {
		run = append(run, i)
	}
	return exp, run
}

// sequence is the order sections are written in: the base order, with the
// reorderable run in the order the choice names.
func (b *baseSel) sequence(ch choice) []int {
	seq := make([]int, len(b.sections))
	for i := range seq {
		seq[i] = i
	}
	if len(ch.order) == len(b.reorderable) {
		for k, pos := range b.reorderable {
			seq[pos] = ch.order[k]
		}
	}
	return seq
}

// selectionJSON is the selection file rkb reads: the base with the chosen
// content, the bank as an absolute path and a constant output name.
func (b *baseSel) selectionJSON(bankPath string, ch choice) ([]byte, error) {
	top := make(map[string]json.RawMessage, len(b.top))
	for k, v := range b.top {
		top[k] = v
	}
	bank, _ := json.Marshal(bankPath)
	top["bank"] = bank
	meta := make(map[string]json.RawMessage, len(b.meta)+2)
	for k, v := range b.meta {
		meta[k] = v
	}
	name, _ := json.Marshal(outputBasename)
	meta["output_basename"] = name
	if ch.keywords != nil {
		kw, _ := json.Marshal(ch.keywords)
		meta["jd_keywords"] = kw
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	top["meta"] = mb

	var secs []json.RawMessage
	for _, i := range b.sequence(ch) {
		s := b.sections[i]
		if !s.choosable {
			secs = append(secs, s.raw)
			continue
		}
		sc := ch.secs[i]
		fields := make(map[string]json.RawMessage, len(s.fields))
		for k, v := range s.fields {
			fields[k] = v
		}
		switch s.kind {
		case "bullets":
			if len(sc.bullets) == 0 {
				continue
			}
			fields["bullets"], _ = json.Marshal(sc.bullets)
		case "entries":
			if len(sc.entries) == 0 {
				continue
			}
			ents := make([]map[string]json.RawMessage, 0, len(sc.entries))
			for _, e := range sc.entries {
				m := map[string]json.RawMessage{}
				for _, be := range s.entries {
					if be.id == e.id {
						for k, v := range be.fields {
							m[k] = v
						}
						break
					}
				}
				m["entry"], _ = json.Marshal(e.id)
				ids := e.bullets
				if ids == nil {
					ids = []string{} // never null: rkb reads a missing list as every bullet
				}
				m["bullets"], _ = json.Marshal(ids)
				ents = append(ents, m)
			}
			fields["entries"], _ = json.Marshal(ents)
		}
		rs, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		secs = append(secs, rs)
	}
	top["sections"], _ = json.Marshal(secs)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
