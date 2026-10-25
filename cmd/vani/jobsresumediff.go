package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// `jobs resume diff` says what differs between two stored versions of a job's
// resume, and `jobs resume current` makes a stored version the one the Studio
// shows (a restore). Both read the store alone: no rkb, no model, nothing spent.

// resumeDiffSide names one side of a diff: which version, how it was made and, for
// a tailored one, the preference it was asked with (as recorded, scrubbed) and
// what its redraft kept: the scope and the locked section names.
type resumeDiffSide struct {
	Version     int       `json:"version"`
	Source      string    `json:"source"`
	Parent      int       `json:"parent,omitempty"`
	Instruction string    `json:"instruction,omitempty"`
	Scope       string    `json:"scope,omitempty"`
	Lock        []string  `json:"lock,omitempty"`
	Created     time.Time `json:"created"`
}

// bulletPlace is where a bullet sits on a page: its section, its entry (none in a
// summary section) and its place in that entry's list (0 is first).
type bulletPlace struct {
	Section string `json:"section"`
	Entry   string `json:"entry,omitempty"`
	Index   int    `json:"index"`
}

type bulletAt struct {
	ID string `json:"id"`
	bulletPlace
}

// bulletMoved is a bullet that is on both pages in a different place: another
// section or entry, or the same entry with its order against the other bullets
// the two pages share changed.
type bulletMoved struct {
	ID   string      `json:"id"`
	From bulletPlace `json:"from"`
	To   bulletPlace `json:"to"`
}

type bulletDiff struct {
	Added   []bulletAt    `json:"added"`
	Removed []bulletAt    `json:"removed"`
	Moved   []bulletMoved `json:"moved"`
}

// overrideChange is one field of one entry's owner override that differs ("" is no
// override: the entry's own value shows).
type overrideChange struct {
	Entry string `json:"entry"`
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// sectionOrder is the order of the sections both pages have, when it differs.
type sectionOrder struct {
	From []string `json:"from"`
	To   []string `json:"to"`
}

type keywordDiff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// resumeDiffed is the reply of `jobs resume diff`: every list is present (empty
// when nothing differs). Same says the choices are identical in bullets, places,
// overrides, section order and keywords; the sides can still differ in how they
// were made.
type resumeDiffed struct {
	Key          string           `json:"key"`
	From         resumeDiffSide   `json:"from"`
	To           resumeDiffSide   `json:"to"`
	Same         bool             `json:"same"`
	Bullets      bulletDiff       `json:"bullets"`
	Overrides    []overrideChange `json:"overrides"`
	SectionOrder *sectionOrder    `json:"section_order,omitempty"`
	Keywords     keywordDiff      `json:"keywords"`
}

// storedChoice is the part of a stored choice a diff reads (the owner-edit shape).
type storedChoice struct {
	Sections []struct {
		Title   string   `json:"title"`
		Bullets []string `json:"bullets"`
		Entries []struct {
			Entry   string   `json:"entry"`
			Bullets []string `json:"bullets"`
			Title   string   `json:"title"`
			Stack   string   `json:"stack"`
			Lead    string   `json:"lead"`
			Org     string   `json:"org"`
			Dates   string   `json:"dates"`
		} `json:"entries"`
	} `json:"sections"`
	Keywords []string `json:"jd_keywords"`
}

// pageOf is a choice's bullets in page order with their places, its overrides by
// entry and field, and its section titles in order.
type pageOf struct {
	bullets   []bulletAt
	where     map[string]bulletPlace
	overrides map[string]map[string]string // entry -> field -> value
	sections  []string
	keywords  []string
}

func readChoice(raw json.RawMessage) (pageOf, error) {
	var c storedChoice
	if err := json.Unmarshal(raw, &c); err != nil {
		return pageOf{}, err
	}
	p := pageOf{where: map[string]bulletPlace{}, overrides: map[string]map[string]string{}, keywords: c.Keywords}
	for _, s := range c.Sections {
		p.sections = append(p.sections, s.Title)
		add := func(entry string, ids []string) {
			for i, id := range ids {
				at := bulletAt{ID: id, bulletPlace: bulletPlace{Section: s.Title, Entry: entry, Index: i}}
				if _, dup := p.where[id]; dup {
					continue // a stored choice uses an id once; the first wins, never a panic
				}
				p.bullets = append(p.bullets, at)
				p.where[id] = at.bulletPlace
			}
		}
		add("", s.Bullets)
		for _, e := range s.Entries {
			add(e.Entry, e.Bullets)
			for field, v := range map[string]string{"title": e.Title, "stack": e.Stack, "lead": e.Lead, "org": e.Org, "dates": e.Dates} {
				if v == "" {
					continue
				}
				if p.overrides[e.Entry] == nil {
					p.overrides[e.Entry] = map[string]string{}
				}
				p.overrides[e.Entry][field] = v
			}
		}
	}
	return p, nil
}

// diffChoices is what differs between two stored choices.
func diffChoices(a, b pageOf) (bulletDiff, []overrideChange, *sectionOrder, keywordDiff) {
	bd := bulletDiff{Added: []bulletAt{}, Removed: []bulletAt{}, Moved: []bulletMoved{}}
	for _, x := range a.bullets {
		if _, ok := b.where[x.ID]; !ok {
			bd.Removed = append(bd.Removed, x)
		}
	}
	for _, x := range b.bullets {
		if _, ok := a.where[x.ID]; !ok {
			bd.Added = append(bd.Added, x)
		}
	}
	// A bullet in a different section or entry moved. Within an entry both pages
	// share, one that is out of order against the others moved: the smallest set of
	// bullets that, left in place, keeps the rest in order (the longest run both
	// pages agree on stays), so removing one bullet does not make its neighbours move.
	moved := map[string]bool{}
	type box struct{ section, entry string }
	byBox := map[box][]string{}
	for _, x := range a.bullets {
		bp, ok := b.where[x.ID]
		if !ok {
			continue
		}
		if bp.Section != x.Section || bp.Entry != x.Entry {
			moved[x.ID] = true
			continue
		}
		k := box{x.Section, x.Entry}
		byBox[k] = append(byBox[k], x.ID)
	}
	for _, ids := range byBox {
		inB := slices.Clone(ids)
		slices.SortFunc(inB, func(p, q string) int { return b.where[p].Index - b.where[q].Index })
		for _, id := range outOfOrder(ids, inB) {
			moved[id] = true
		}
	}
	for _, x := range a.bullets {
		if moved[x.ID] {
			bd.Moved = append(bd.Moved, bulletMoved{ID: x.ID, From: x.bulletPlace, To: b.where[x.ID]})
		}
	}

	ov := []overrideChange{}
	entries := map[string]bool{}
	for e := range a.overrides {
		entries[e] = true
	}
	for e := range b.overrides {
		entries[e] = true
	}
	ids := make([]string, 0, len(entries))
	for e := range entries {
		ids = append(ids, e)
	}
	slices.Sort(ids)
	for _, e := range ids {
		for _, f := range []string{"title", "org", "stack", "lead", "dates"} {
			if from, to := a.overrides[e][f], b.overrides[e][f]; from != to {
				ov = append(ov, overrideChange{Entry: e, Field: f, From: from, To: to})
			}
		}
	}

	var so *sectionOrder
	common := func(p, q []string) []string {
		var out []string
		for _, s := range p {
			if slices.Contains(q, s) {
				out = append(out, s)
			}
		}
		return out
	}
	if ca, cb := common(a.sections, b.sections), common(b.sections, a.sections); !slices.Equal(ca, cb) {
		so = &sectionOrder{From: ca, To: cb}
	}

	kd := keywordDiff{Added: []string{}, Removed: []string{}}
	for _, k := range b.keywords {
		if !slices.Contains(a.keywords, k) && !slices.Contains(kd.Added, k) {
			kd.Added = append(kd.Added, k)
		}
	}
	for _, k := range a.keywords {
		if !slices.Contains(b.keywords, k) && !slices.Contains(kd.Removed, k) {
			kd.Removed = append(kd.Removed, k)
		}
	}
	return bd, ov, so, kd
}

// outOfOrder names the ids of a (the order on one page) that are not in the longest
// subsequence it shares with b (the same ids in the other page's order): the
// bullets that moved. The pick is deterministic.
func outOfOrder(a, b []string) []string {
	pos := make(map[string]int, len(b))
	for i, id := range b {
		pos[id] = i
	}
	// seq[i] is b's position of a[i]; the longest increasing run of it stays put.
	n := len(a)
	best := make([]int, n)
	prev := make([]int, n)
	top := -1
	for i := range a {
		best[i], prev[i] = 1, -1
		for j := 0; j < i; j++ {
			if pos[a[j]] < pos[a[i]] && best[j]+1 > best[i] {
				best[i], prev[i] = best[j]+1, j
			}
		}
		if top < 0 || best[i] > best[top] {
			top = i
		}
	}
	stay := map[int]bool{}
	for i := top; i >= 0; i = prev[i] {
		stay[i] = true
	}
	var out []string
	for i, id := range a {
		if !stay[i] {
			out = append(out, id)
		}
	}
	return out
}

func (e *env) jobsResumeDiff(args []string) error {
	fs := e.newFlags("jobs resume diff")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	if err := parseResumeArgs(fs, args, "diff", 3); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, resumeShowStoredDeadline)
	defer dcancel()
	out, err := e.resumeDiff(ctx, fs.Arg(0), fs.Arg(1), fs.Arg(2))
	return e.resumeEmit(*asJSON, out, err, func() {
		if out.Same {
			fmt.Fprintf(e.stdout, "%s: v%d and v%d choose the same page\n", out.Key, out.From.Version, out.To.Version)
			return
		}
		fmt.Fprintf(e.stdout, "%s: v%d -> v%d: %d bullets added, %d removed, %d moved, %d override changes, keywords +%d -%d",
			out.Key, out.From.Version, out.To.Version, len(out.Bullets.Added), len(out.Bullets.Removed), len(out.Bullets.Moved),
			len(out.Overrides), len(out.Keywords.Added), len(out.Keywords.Removed))
		if out.SectionOrder != nil {
			fmt.Fprint(e.stdout, ", sections reordered")
		}
		fmt.Fprintln(e.stdout)
	})
}

// resumeDiff reads two stored versions and compares their choices. A version that
// does not read is the store's own refusal (not_found, damaged, unsupported).
func (e *env) resumeDiff(ctx context.Context, rawKey, rawA, rawB string) (resumeDiffed, error) {
	na, perr := parseVersion(rawA)
	if perr != nil {
		return resumeDiffed{}, perr
	}
	nb, perr := parseVersion(rawB)
	if perr != nil {
		return resumeDiffed{}, perr
	}
	job, jerr := e.resumeJobFor(rawKey, false)
	if jerr != nil {
		return resumeDiffed{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeDiffed{}, serr
	}
	sides := [2]resumestore.Meta{}
	for i, n := range []int{na, nb} {
		m, err := withContext(ctx, func() (resumestore.Meta, error) { m, _, err := st.Get(string(job.key), n); return m, err })
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return resumeDiffed{}, err
			}
			return resumeDiffed{}, storeError(err)
		}
		sides[i] = m
	}
	pa, err := readChoice(sides[0].Selection)
	if err != nil {
		return resumeDiffed{}, jrErr(jrDamaged, "jobs resume: v%d's choice cannot be read: %s", na, oneLine(err.Error(), 200))
	}
	pb, err := readChoice(sides[1].Selection)
	if err != nil {
		return resumeDiffed{}, jrErr(jrDamaged, "jobs resume: v%d's choice cannot be read: %s", nb, oneLine(err.Error(), 200))
	}
	bd, ov, so, kd := diffChoices(pa, pb)
	side := func(m resumestore.Meta) resumeDiffSide {
		return resumeDiffSide{Version: m.Version, Source: m.Source, Parent: m.Parent, Instruction: m.Instruction, Scope: m.Scope, Lock: m.Lock, Created: m.Created}
	}
	same := len(bd.Added)+len(bd.Removed)+len(bd.Moved)+len(ov)+len(kd.Added)+len(kd.Removed) == 0 && so == nil
	return resumeDiffed{Key: string(job.key), From: side(sides[0]), To: side(sides[1]), Same: same,
		Bullets: bd, Overrides: ov, SectionOrder: so, Keywords: kd}, nil
}

// resumeCurrentSet is the reply of `jobs resume current`: Previous is the version
// that was current (0 for none); Changed is false when vN already was.
type resumeCurrentSet struct {
	Key      string `json:"key"`
	Version  int    `json:"version"`
	Previous int    `json:"previous,omitempty"`
	Changed  bool   `json:"changed"`
}

func (e *env) jobsResumeCurrent(args []string) error {
	fs := e.newFlags("jobs resume current")
	asJSON := fs.Bool("json", false, "one JSON object on stdout ({error,code} on failure)")
	if err := parseResumeArgs(fs, args, "current", 2); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, dcancel := context.WithTimeout(ctx, resumeUseDeadline)
	defer dcancel()
	out, err := e.resumeCurrent(ctx, fs.Arg(0), fs.Arg(1))
	return e.resumeEmit(*asJSON, out, err, func() {
		if !out.Changed {
			fmt.Fprintf(e.stdout, "%s: v%d already is the current version\n", out.Key, out.Version)
			return
		}
		was := "none"
		if out.Previous != 0 {
			was = fmt.Sprintf("v%d", out.Previous)
		}
		fmt.Fprintf(e.stdout, "%s: v%d is now the current version (was %s)\n", out.Key, out.Version, was)
	})
}

// resumeCurrent makes version vN the job's current one: the version the Studio
// shows and the next redraft starts from. It restores an older version over a
// newer one without deleting either. The version must read; the pointer of a job
// that has none, or a damaged one, is replaced (SetCurrent repairs it). Which
// version is *used* for applying is `use`'s alone, and not touched.
func (e *env) resumeCurrent(ctx context.Context, rawKey, rawVersion string) (resumeCurrentSet, error) {
	n, perr := parseVersion(rawVersion)
	if perr != nil {
		return resumeCurrentSet{}, perr
	}
	job, jerr := e.resumeJobFor(rawKey, false)
	if jerr != nil {
		return resumeCurrentSet{}, jerr
	}
	st, serr := e.resumeStore()
	if serr != nil {
		return resumeCurrentSet{}, serr
	}
	prev := 0
	if m, err := st.Current(string(job.key)); err == nil {
		prev = m.Version
	}
	if prev == n {
		return resumeCurrentSet{Key: string(job.key), Version: n, Previous: prev}, nil
	}
	if err := st.SetCurrent(ctx, string(job.key), n); err != nil {
		return resumeCurrentSet{}, storeError(err)
	}
	return resumeCurrentSet{Key: string(job.key), Version: n, Previous: prev, Changed: true}, nil
}
