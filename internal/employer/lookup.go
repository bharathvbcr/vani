package employer

import (
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/lca"
)

// Match confidences: the LCA badge's ladder, plus ConfExactOneWord.
const (
	ConfExact        = lca.ConfExact
	ConfUniquePrefix = lca.ConfUniquePrefix
	ConfNone         = lca.ConfNone
	// ConfExactOneWord is an exact match on a one-word name found only on the
	// IRS list. The list holds about two million organisations, so one word
	// collides with companies: on the 2026-10 file "Ramp", "Apex" and "Atlas"
	// all match small non-profits. Shown, never used for rank.
	ConfExactOneWord = "exact_one_word"
)

// Badge is the employer-attribute hint shown beside a posting. It is never a
// filter and never a legal determination.
type Badge struct {
	MatchedName string   // normalised key matched, "" when none
	Confidence  string   // ConfExact, ConfUniquePrefix or ConfNone
	Attributes  []string // in Attributes order
	Kinds       []string // source kinds that listed the name, sorted
}

// Confident reports whether the match is strong enough to act on (rank).
// Only an exact normalised match is; a unique-prefix or one-word IRS match
// is shown but never boosts.
func (b Badge) Confident() bool { return b.Confidence == ConfExact && len(b.Attributes) > 0 }

// Lookup matches company against the index: an exact normalised match, then
// a whole-token prefix of at least two tokens that selects exactly one name.
// It never chooses among several candidates.
func (x *Index) Lookup(company string) Badge {
	x.mu.RLock()
	v := x.currentView()
	x.mu.RUnlock()
	return v.Lookup(company)
}

// Lookup is Index.Lookup over the merged view.
func (v *View) Lookup(company string) Badge {
	b := Badge{Confidence: ConfNone}
	norm := lca.Normalize(company)
	if norm == "" {
		return b
	}
	i := -1
	if j := v.search(norm); j < v.n && v.key(j) == norm {
		i, b.Confidence = j, ConfExact
	} else if len(strings.Fields(norm)) >= 2 {
		if j, ok := v.uniquePrefix(norm); ok {
			i, b.Confidence = j, ConfUniquePrefix
		}
	}
	if i < 0 {
		return b
	}
	key := v.key(i)
	m := v.body[v.meta+i]
	b.MatchedName = strings.Clone(key) // never pin the view's body
	b.Attributes = attr(m & attrMask).names()
	b.Kinds = kindsOf(m >> kindsShift)
	if b.Confidence == ConfExact && !strings.Contains(key, " ") && !slices.Contains(b.Kinds, KindIPEDS) {
		b.Confidence = ConfExactOneWord
	}
	return b
}

func (v *View) uniquePrefix(norm string) (int, bool) {
	p := norm + " "
	i := v.search(p)
	if i >= v.n || !strings.HasPrefix(v.key(i), p) {
		return 0, false
	}
	if i+1 < v.n && strings.HasPrefix(v.key(i+1), p) {
		return 0, false
	}
	return i, true
}

// String renders the badge. Every attribute reads "likely": a name on a
// public list is evidence, not a determination.
func (b Badge) String() string {
	if b.Confidence == ConfNone || b.MatchedName == "" {
		return "no employer-list match"
	}
	var parts []string
	for _, a := range b.Attributes {
		switch a {
		case AttrCapExemptLikely:
			parts = append(parts, "likely H-1B cap-exempt (degree-granting public or non-profit institution, IPEDS)")
		case AttrNonprofit:
			if slices.Contains(b.Kinds, KindIRSBMF) {
				parts = append(parts, "likely non-profit (IRS exempt organizations list)")
			} else {
				parts = append(parts, "likely non-profit (private non-profit institution, IPEDS)")
			}
		}
	}
	how := "exact name match"
	switch b.Confidence {
	case ConfUniquePrefix:
		how = "unique prefix match: " + b.MatchedName + "; not used for rank"
	case ConfExactOneWord:
		how = "exact one-word name match, which often collides; not used for rank"
	}
	return strings.Join(parts, "; ") + " (" + how + ")"
}
