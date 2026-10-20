package eval

import (
	"math/rand"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// DropRule is the part of a drop rule a class name needs: its slot and the
// values it drops on. The values are matched, never printed.
type DropRule struct {
	Slot   string
	Values []string
}

// DropClass names why a dropped record was dropped, from its triage
// annotations (the folded record keeps no event reason): "title" or "model"
// for what read it, and the slot of the first rule its readings match, as
// "title:role_fit" or "model:seniority". A dropped record no rule explains is
// "<who>:other"; "" is returned for a record that is not dropped. It never
// names a rule's value: values can reveal the owner's work authorization, and a
// class reaches the label file.
func DropClass(r ledger.Record, rules []DropRule) string {
	if r.Status != posting.Dropped {
		return ""
	}
	backend := r.Annotations[ledger.AnnTriagePrefix+"backend"]
	for _, rule := range rules {
		v := strings.TrimSpace(r.Annotations[ledger.AnnTriagePrefix+rule.Slot])
		if v == "" {
			continue
		}
		for _, want := range rule.Values {
			if v == want {
				if b := r.Annotations[ledger.AnnTriagePrefix+rule.Slot+ledger.AnnTriageSuffixBackend]; b != "" {
					backend = b
				}
				return dropWho(backend) + ":" + classToken(rule.Slot)
			}
		}
	}
	return dropWho(backend) + ":other"
}

func dropWho(backend string) string {
	switch strings.TrimSpace(backend) {
	case "":
		return "unknown"
	case "title":
		return "title"
	}
	return "model"
}

// classToken keeps the characters of s that are safe in a class name (a-z, 0-9,
// _ and -), at most 40.
func classToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if b.Len() >= 40 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Queue picks up to n unlabeled postings to label, most informative first:
//
//  1. postings with any "triage.<slot>.disagree" annotation (primary and shadow
//     disagreed);
//  2. then unsure ones (status Unsure or a non-empty "triage.unsure");
//  3. then a random sample of the remaining postings that carry triage
//     annotations.
//
// Tiers 2 and 3 are taken first among postings the shadow was asked about
// (any "triage.<slot>.shadow_reading"), then among the rest: a label counts
// toward a shadow's promotion verdict only where the shadow was asked.
//
// Dropped postings are a separate stratum, so a drop rule's false drops can be
// measured: they are not pooled into the tiers above but drawn round-robin
// across their DropClass (rules name the slots; nil rules class every drop as
// "<who>:other"), up to a third of n. If the tiers run out, the stratum fills
// the rest; if it runs out, the tiers do.
//
// Dead and Duplicate postings are never returned. Each tier is put in key order
// and then shuffled with rng, so the result is deterministic for a given rng;
// a nil rng keeps key order. Labels are read from s; if they cannot be read
// Queue returns the error and no postings, as `label` does, because a queue
// built without them could repeat labelled work. A nil s means no label check.
func Queue(recs []ledger.Record, s *Store, n int, rng *rand.Rand, rules []DropRule) ([]ledger.Record, error) {
	if n <= 0 {
		return nil, nil
	}
	labeled := map[posting.Key]Entry{}
	if s != nil {
		l, _, err := s.Labels()
		if err != nil {
			return nil, err
		}
		labeled = l
	}
	var disagree, unsure, rest, oldUnsure, oldRest, drops []ledger.Record
	for _, r := range recs {
		if r.Status == posting.Dead || r.Status == posting.Duplicate {
			continue
		}
		if _, done := labeled[r.Key]; done || Inherited(r) {
			continue // a copied reading is not worth a label: it is not counted
		}
		if r.Status == posting.Dropped {
			drops = append(drops, r)
			continue
		}
		seen := hasShadowReading(r)
		switch {
		case hasDisagree(r):
			disagree = append(disagree, r)
		case r.Status == posting.Unsure || r.Annotations[ledger.AnnTriageUnsure] != "":
			if seen {
				unsure = append(unsure, r)
			} else {
				oldUnsure = append(oldUnsure, r)
			}
		case hasTriage(r):
			if seen {
				rest = append(rest, r)
			} else {
				oldRest = append(oldRest, r)
			}
		}
	}
	shuffle := func(tier []ledger.Record) {
		sort.Slice(tier, func(i, j int) bool { return tier[i].Key < tier[j].Key })
		if rng != nil {
			rng.Shuffle(len(tier), func(i, j int) { tier[i], tier[j] = tier[j], tier[i] })
		}
	}
	// The dropped stratum reserves a third of n; unused reserve goes back to
	// the tiers, and a short tier list lends its places to the stratum.
	nDrop := min(len(drops), n/3)
	var out []ledger.Record
	for _, tier := range [][]ledger.Record{disagree, unsure, rest, oldUnsure, oldRest} {
		shuffle(tier)
		out = append(out, tier...)
		if len(out) >= n-nDrop {
			break
		}
	}
	if len(out) > n-nDrop {
		out = out[:n-nDrop]
	}
	nDrop = min(len(drops), n-len(out))
	out = append(out, byClass(drops, nDrop, rules, shuffle)...)
	return out, nil
}

// byClass takes n of drops round-robin across drop classes (classes in name
// order, each class in shuffle order).
func byClass(drops []ledger.Record, n int, rules []DropRule, shuffle func([]ledger.Record)) []ledger.Record {
	classes := map[string][]ledger.Record{}
	var names []string
	for _, r := range drops {
		c := DropClass(r, rules)
		if _, ok := classes[c]; !ok {
			names = append(names, c)
		}
		classes[c] = append(classes[c], r)
	}
	sort.Strings(names)
	for _, c := range names {
		shuffle(classes[c])
	}
	var out []ledger.Record
	for len(out) < n {
		for _, c := range names {
			if len(out) == n {
				break
			}
			if len(classes[c]) > 0 {
				out = append(out, classes[c][0])
				classes[c] = classes[c][1:]
			}
		}
	}
	return out
}

// Inherited reports whether r's triage reading was copied from another
// posting rather than made for it.
func Inherited(r ledger.Record) bool {
	return r.Annotations[ledger.AnnTriageInheritedFrom] != ""
}

func hasDisagree(r ledger.Record) bool {
	for k := range r.Annotations {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) && strings.HasSuffix(k, ledger.AnnTriageSuffixDisagree) {
			return true
		}
	}
	return false
}

func hasShadowReading(r ledger.Record) bool {
	for k := range r.Annotations {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) && strings.HasSuffix(k, ledger.AnnTriageSuffixShadowReading) {
			return true
		}
	}
	return false
}

func hasTriage(r ledger.Record) bool {
	for k := range r.Annotations {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) {
			return true
		}
	}
	return false
}
