package rkbresume

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// families groups bank entries that describe the same work: entries that list
// a bullet in common, or that name a common origin (rkb's importer records the
// entry a variant was derived from, rkb importtex.go), are one family, and the
// relation is closed transitively. The owner keeps several framings of one
// project for different resumes ("Custom LLM Inference Runtime" and "GPU
// Performance: gemma-metal Runtime" share six bullets); rkb's rule is to say
// each thing once, so a page shows at most one entry of a family. A family
// the base selection itself shows more than once is the owner's choice and is
// allowed (pairs).
type families struct {
	of    map[string]string // entry id -> family key (smallest member id)
	pairs map[string]bool   // family keys the base shows more than one entry of
}

func newFamilies(bank kbsync.Bank, base *baseSel) families {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if p, ok := parent[x]; ok && p != x {
			r := find(p)
			parent[x] = r
			return r
		}
		parent[x] = x
		return x
	}
	union := func(a, c string) {
		ra, rc := find(a), find(c)
		if ra == rc {
			return
		}
		if rc < ra {
			ra, rc = rc, ra
		}
		parent[rc] = ra
	}
	byBullet := map[string]string{}
	byOrigin := map[string]string{}
	ids := make([]string, 0, len(bank.Entries))
	for id := range bank.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic keys
	for _, id := range ids {
		find(id)
		e := bank.Entries[id]
		for _, bl := range e.Bullets {
			if o, ok := byBullet[bl]; ok {
				union(o, id)
			} else {
				byBullet[bl] = id
			}
		}
		for _, o := range e.Origin {
			if o == id {
				continue
			}
			if first, ok := byOrigin[o]; ok {
				union(first, id)
			} else {
				byOrigin[o] = id
			}
			if _, ok := bank.Entries[o]; ok {
				union(o, id)
			}
		}
	}
	f := families{of: map[string]string{}, pairs: map[string]bool{}}
	for _, id := range ids {
		f.of[id] = find(id)
	}
	if base != nil {
		seen := map[string]bool{}
		for _, s := range base.sections {
			for _, e := range s.entries {
				k := f.key(e.id)
				if seen[k] {
					f.pairs[k] = true
				}
				seen[k] = true
			}
		}
	}
	return f
}

func (f families) key(id string) string {
	if k, ok := f.of[id]; ok {
		return k
	}
	return id
}

// check refuses a page that shows two entries of one family the base does not
// itself pair. entries are in page order.
func (f families) check(entries []string) error {
	first := map[string]string{}
	for _, id := range entries {
		k := f.key(id)
		if o, ok := first[k]; ok && !f.pairs[k] {
			return fmt.Errorf("entries %q and %q describe the same work (they share bullets or an origin): use at most one of them", clip(o, 60), clip(id, 60))
		}
		first[k] = id
	}
	return nil
}

// groups lists, for the prompt, each family of two or more of the given
// entries that the base does not pair, members sorted.
func (f families) groups(entries []string) []string {
	by := map[string][]string{}
	for _, id := range entries {
		by[f.key(id)] = append(by[f.key(id)], id)
	}
	var out []string
	for k, ms := range by {
		if len(ms) < 2 || f.pairs[k] {
			continue
		}
		sort.Strings(ms)
		out = append(out, strings.Join(ms, ", "))
	}
	sort.Strings(out)
	return out
}
