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

// Lookup matches company against the index. It tries an exact normalised
// match, then a whole-token prefix that selects exactly one employer (and
// needs at least two tokens). It never chooses among several candidates.
func (x *Index) Lookup(company string) Badge {
	x.mu.RLock()
	defer x.mu.RUnlock()

	window := x.coverage
	if len(window) > badgeWindow {
		window = window[len(window)-badgeWindow:]
	}
	b := Badge{Confidence: ConfNone, FYs: append([]int(nil), window...)}

	norm := Normalize(company)
	if norm == "" {
		return b
	}
	key := ""
	if _, ok := x.merged[norm]; ok {
		key, b.Confidence = norm, ConfExact
	} else if len(strings.Fields(norm)) >= 2 {
		if k, ok := x.uniquePrefix(norm); ok {
			key, b.Confidence = k, ConfUniquePrefix
		}
	}
	if key == "" {
		return b
	}
	b.MatchedName = key
	b.Certified = map[int]int{}
	for _, fy := range window {
		n := x.merged[key][fy].H1B
		b.Certified[fy] = n
		b.Total += n
	}
	return b
}

// uniquePrefix returns the single employer whose key starts with norm at a
// token boundary, or false when there are none or several.
func (x *Index) uniquePrefix(norm string) (string, bool) {
	p := norm + " "
	i := sort.SearchStrings(x.keys, p)
	if i >= len(x.keys) || !strings.HasPrefix(x.keys[i], p) {
		return "", false
	}
	if i+1 < len(x.keys) && strings.HasPrefix(x.keys[i+1], p) {
		return "", false
	}
	return x.keys[i], true
}

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
