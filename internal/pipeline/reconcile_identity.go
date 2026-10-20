package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/joburl"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Exact identity across sources. Reconcile keys a posting by its own ATS, so
// the same job reached through two routes (a company page that links to its
// Greenhouse posting, or one role listed on both Greenhouse and Lever) lands
// as two records. These rules fold the cases where the two records are
// provably one job, by the posting's own link or by an identical
// company|title|location tuple. Anything less than that stays two records:
// the same company and title in two cities is two requisitions.

// minIDDigits is how many digits a path segment needs to count as a job id. A
// segment such as "v2" or "web3" names a section of a site, not a posting.
const minIDDigits = 3

// identityURL reduces a posting link to the form identity is compared on: the
// joburl form (tracking parameters, fragment and default port gone, Greenhouse
// hosts folded) with http read as https. ok is false for a link that does not
// parse, and for one that names no posting: a careers homepage is shared by
// every job a company lists, so only a link whose path holds an id segment (at
// least minIDDigits digits) or that keeps a job-naming parameter counts.
func identityURL(raw string) (string, bool) {
	n, err := joburl.Normalize(raw)
	if err != nil {
		return "", false
	}
	n = "https://" + strings.TrimPrefix(strings.TrimPrefix(n, "https://"), "http://")
	if !carriesID(n) {
		return "", false
	}
	return n, true
}

// carriesID reports whether a normalised link names one posting.
func carriesID(n string) bool {
	rest, query, _ := strings.Cut(n, "?")
	if query != "" {
		return true // joburl keeps only job-naming parameters (gh_jid, req_id, ...)
	}
	_, afterScheme, _ := strings.Cut(rest, "://")
	_, path, _ := strings.Cut(afterScheme, "/")
	for seg := range strings.SplitSeq(path, "/") {
		digits := 0
		for _, r := range seg {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits >= minIDDigits {
			return true
		}
	}
	return false
}

// urlKeys are the identity links of a posting (its URL and its ApplyURL), each
// once, in a fixed order; nil when it has none.
func urlKeys(p posting.Posting) []string {
	var out []string
	for _, raw := range []string{p.URL, p.ApplyURL} {
		if u, ok := identityURL(raw); ok && (len(out) == 0 || out[0] != u) {
			out = append(out, u)
		}
	}
	return out
}

// twinOf says which existing record the new posting p repeats, and why. now is
// the time p will be first seen, which ranks it against records written in
// the same run. Rules, first match wins:
//
//  1. p shares an identity link (identityURL) with exactly one live ATS-keyed
//     record: a link two or more records claim identifies none of them.
//  2. p is ATS-keyed and its company|title|location tuple equals that of a
//     live record under a different ATS (the same ATS lists distinct
//     requisitions as distinct ids, so it is not a twin). The survivor is the
//     earliest first seen, ties by key; p is the twin only when it is not it.
//  3. p is weakly keyed (fallback or careers page) and an ATS record has its
//     tuple.
//
// ok is false when p repeats nothing. It decides p alone; markCrossTwins is
// its mirror for the record that arrives second but ranks first.
func twinOf(p posting.Posting, now time.Time, idx *ledgerIndex) (orig posting.Key, reason string, ok bool) {
	if k, found := urlTwin(p, idx); found {
		return k, "same url", true
	}
	if weakKey(p.Key) {
		if fk, err := posting.FallbackKey(p.Company, p.Title, p.Location); err == nil {
			if k, found := idx.atsFor(fk); found {
				return k, "", true
			}
		}
		return "", "", false
	}
	if survivor, found := crossSurvivor(p, now, idx); found && survivor != p.Key {
		return survivor, "same role on another ats", true
	}
	return "", "", false
}

// urlTwin is rule 1. Two of p's links naming different records name none.
func urlTwin(p posting.Posting, idx *ledgerIndex) (posting.Key, bool) {
	var found posting.Key
	for _, u := range urlKeys(p) {
		var claim posting.Key
		n := 0
		for _, k := range idx.byURL[u] {
			if k == p.Key || weakKey(k) || closed(idx.info[k].status) {
				continue
			}
			claim, n = k, n+1
		}
		switch {
		case n != 1:
			continue // nobody claims it, or several do
		case found == "":
			found = claim
		case found != claim:
			return "", false
		}
	}
	return found, found != ""
}

// crossCandidates are the live records under a different ATS with p's tuple.
func crossCandidates(p posting.Posting, idx *ledgerIndex) []posting.Key {
	fk, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	if err != nil {
		return nil
	}
	var out []posting.Key
	for _, k := range idx.ats[fk] {
		if k != p.Key && k.ATS() != p.Key.ATS() && !closed(idx.info[k].status) {
			out = append(out, k)
		}
	}
	return out
}

// crossSurvivor is rule 2's survivor among p and its candidates: the earliest
// first seen, ties by key. ok is false when p has no candidate.
func crossSurvivor(p posting.Posting, now time.Time, idx *ledgerIndex) (posting.Key, bool) {
	cands := crossCandidates(p, idx)
	if len(cands) == 0 {
		return "", false
	}
	best, bestAt := p.Key, now
	for _, k := range cands {
		at := idx.info[k].firstSeen
		if at.Before(bestAt) || at.Equal(bestAt) && k < best {
			best, bestAt = k, at
		}
	}
	return best, true
}

// markCrossTwins is rule 2 from the other side: p was written live and ranks
// before the same role under another ATS, which arrived first, so those
// records become p's duplicates, whatever order the boards ran in. A record
// that is terminal now or that protected names is left alone. rank is when p
// was first seen, which orders it against the candidates: now for a record
// written this run, its own first sighting for a revived one.
func markCrossTwins(ctx context.Context, l ledger.Ledger, p posting.Posting, rank, now time.Time, idx *ledgerIndex, protected func(posting.Key) bool) (int, error) {
	if weakKey(p.Key) {
		return 0, nil
	}
	if survivor, ok := crossSurvivor(p, rank, idx); !ok || survivor != p.Key {
		return 0, nil
	}
	var evs []ledger.Event
	for _, k := range crossCandidates(p, idx) {
		r, ok := l.Get(k)
		if !ok || r.Status.Terminal() || protected != nil && protected(k) {
			continue
		}
		evs = append(evs, ledger.Event{V: ledger.SchemaVersion, At: now, Key: k, Status: posting.Duplicate,
			Annotations: map[string]string{DuplicateOfKey: string(p.Key)}, Reason: "duplicate of " + string(p.Key) + " (same role on another ats)"})
	}
	return appendBatched(ctx, l, evs)
}
