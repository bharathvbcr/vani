package eval

import (
	"math/rand"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

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
// Dead and Duplicate postings are never returned. Each tier is put in key order
// and then shuffled with rng, so the result is deterministic for a given rng;
// a nil rng keeps key order. Labels are read from s; if they cannot be read
// the queue treats nothing as labeled (the signature has no error), so a caller
// that must not repeat work should call s.Labels first and check its error.
func Queue(recs []ledger.Record, s *Store, n int, rng *rand.Rand) []ledger.Record {
	if n <= 0 {
		return nil
	}
	labeled := map[posting.Key]Entry{}
	if s != nil {
		if l, _, err := s.Labels(); err == nil {
			labeled = l
		}
	}
	var disagree, unsure, rest, oldUnsure, oldRest []ledger.Record
	for _, r := range recs {
		if r.Status == posting.Dead || r.Status == posting.Duplicate {
			continue
		}
		if _, done := labeled[r.Key]; done || inherited(r) {
			continue // a copied reading is not worth a label: it is not counted
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
	var out []ledger.Record
	for _, tier := range [][]ledger.Record{disagree, unsure, rest, oldUnsure, oldRest} {
		sort.Slice(tier, func(i, j int) bool { return tier[i].Key < tier[j].Key })
		if rng != nil {
			rng.Shuffle(len(tier), func(i, j int) { tier[i], tier[j] = tier[j], tier[i] })
		}
		out = append(out, tier...)
		if len(out) >= n {
			break
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// inherited reports whether r's triage reading was copied from another
// posting rather than made for it.
func inherited(r ledger.Record) bool {
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
