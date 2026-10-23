package lca

import (
	"fmt"
	"sort"
	"strings"
)

// Match confidences.
const (
	ConfExact        = "exact"
	ConfUniquePrefix = "unique_prefix"
	ConfNone         = "none"
)

// badgeWindow is how many of the most recent covered fiscal years a badge sums.
const badgeWindow = 3

// Badge is the sponsorship-history hint shown beside a posting. It is never a
// filter.
type Badge struct {
	MatchedName string      // normalised employer key matched, "" when none
	Confidence  string      // ConfExact, ConfUniquePrefix or ConfNone
	Certified   map[int]int // certified H-1B LCAs per fiscal year, window FYs only
	Total       int         // sum of Certified over FYs
	FYs         []int       // the last (up to) 3 covered fiscal years, ascending
}

// Looker is what a pipeline needs of the LCA data: a badge per company. Both
// *Index and *View satisfy it and give identical answers for the same data.
type Looker interface {
	Lookup(company string) Badge
}

// table is the sorted employer keys with their certified H-1B counts: what
// lookup reads, whether the keys sit in an Index's maps or in a View's blob.
// A ref names one key.
type table interface {
	exact(norm string) (ref int, ok bool)
	prefix(p string) (ref int, ok bool) // the only key starting with p, if exactly one does
	key(ref int) string
	h1b(ref, fy int) int
}

// lookup is the one matching rule. It tries an exact normalised match, then a
// whole-token prefix that selects exactly one employer (and needs at least two
// tokens). It never chooses among several candidates. window is the covered
// fiscal years a badge sums, ascending.
func lookup(t table, window []int, company string) Badge {
	b := Badge{Confidence: ConfNone, FYs: append([]int(nil), window...)}
	norm := Normalize(company)
	if norm == "" {
		return b
	}
	ref, ok := t.exact(norm)
	if ok {
		b.Confidence = ConfExact
	} else if len(strings.Fields(norm)) >= 2 {
		if ref, ok = t.prefix(norm + " "); ok {
			b.Confidence = ConfUniquePrefix
		}
	}
	if !ok {
		return b
	}
	b.MatchedName = t.key(ref)
	b.Certified = map[int]int{}
	for _, fy := range window {
		n := t.h1b(ref, fy)
		b.Certified[fy] = n
		b.Total += n
	}
	return b
}

// lookupWindow is the last badgeWindow of the covered fiscal years.
func lookupWindow(coverage []int) []int {
	if len(coverage) > badgeWindow {
		return coverage[len(coverage)-badgeWindow:]
	}
	return coverage
}

// Lookup matches company against the index; see lookup.
func (x *Index) Lookup(company string) Badge {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return lookup(indexTable{x}, lookupWindow(x.coverage), company)
}

// indexTable reads the merged maps; a ref is a position in x.keys.
type indexTable struct{ x *Index }

func (t indexTable) exact(norm string) (int, bool) {
	if _, ok := t.x.merged[norm]; !ok {
		return 0, false
	}
	return sort.SearchStrings(t.x.keys, norm), true
}

func (t indexTable) prefix(p string) (int, bool) {
	keys := t.x.keys
	i := sort.SearchStrings(keys, p)
	if i >= len(keys) || !strings.HasPrefix(keys[i], p) {
		return 0, false
	}
	if i+1 < len(keys) && strings.HasPrefix(keys[i+1], p) {
		return 0, false
	}
	return i, true
}

func (t indexTable) key(ref int) string { return t.x.keys[ref] }

func (t indexTable) h1b(ref, fy int) int { return t.x.merged[t.x.keys[ref]][fy].H1B }

// String renders the badge for the digest.
func (b Badge) String() string {
	if b.Confidence == ConfNone || b.MatchedName == "" {
		return "no LCA match"
	}
	how := "exact match"
	if b.Confidence == ConfUniquePrefix {
		how = "unique prefix match: " + b.MatchedName
	}
	return fmt.Sprintf("%d certified H-1B LCAs %s (%s)", b.Total, fyRange(b.FYs), how)
}

// fyRange renders FY2023–FY2025 for contiguous years, else a comma list.
func fyRange(fys []int) string {
	switch len(fys) {
	case 0:
		return "(no fiscal years loaded)"
	case 1:
		return fmt.Sprintf("FY%d", fys[0])
	}
	if fys[len(fys)-1]-fys[0] == len(fys)-1 {
		return fmt.Sprintf("FY%d–FY%d", fys[0], fys[len(fys)-1])
	}
	parts := make([]string, len(fys))
	for i, fy := range fys {
		parts[i] = fmt.Sprintf("FY%d", fy)
	}
	return strings.Join(parts, ", ")
}
