package eval

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Default weekly audit quotas (owner-approved: 15 postings, 5 scored and 10
// dropped). AuditClosed adds two postings the engine closed (dead or
// duplicate), which no other stratum ever draws.
const (
	AuditScored  = 5  // by score decile across the scored pool
	AuditDropped = 10 // across drop classes
	AuditClosed  = 2  // across closed statuses (dead, duplicate)
)

// AuditQuotas are the places of one draw per group. A group's places are
// never lent to another (that would change the inclusion probabilities).
type AuditQuotas struct {
	Scored, Dropped, Closed int
}

// DefaultAuditQuotas are the weekly draw's quotas.
var DefaultAuditQuotas = AuditQuotas{Scored: AuditScored, Dropped: AuditDropped, Closed: AuditClosed}

// AuditCandidate is a posting the weekly audit may draw: a kept one with its
// priority score, a dropped one with its DropClass, or a closed one (Closed,
// with Class its status: "dead" or "duplicate").
type AuditCandidate struct {
	Key     posting.Key
	Score   float64
	Dropped bool
	Closed  bool
	Class   string
}

// Audit cell and stratum prefixes. A kept posting's stratum is its score
// decile ("decile:N"); for the recall estimate all kept postings are one cell.
const (
	CellKept       = "kept"
	DroppedPrefix  = "dropped:"
	ClosedPrefix   = "closed:"
	unknownDropped = DroppedPrefix + "unknown:other"
)

// droppedSource is the stratum of a dropped posting of class c.
func droppedSource(c string) string {
	if src := DroppedPrefix + c; c != "" && validSource(src) {
		return src
	}
	return unknownDropped
}

// closedSource is the stratum of a closed posting whose status is c.
func closedSource(c string) string {
	if t := classToken(c); t != "" {
		return ClosedPrefix + t
	}
	return ClosedPrefix + "other"
}

// AuditCell is the cell c stands in for when labels are weighted up to the
// pool: CellKept, its dropped stratum, or its closed stratum. Unlike the draw's
// strata, kept postings are one cell: deciles are re-cut every week, so a
// label's decile is not where its posting sits now.
func AuditCell(c AuditCandidate) string {
	switch {
	case c.Closed:
		return closedSource(c.Class)
	case c.Dropped:
		return droppedSource(c.Class)
	}
	return CellKept
}

// AuditPick is a drawn posting, the stratum it stands for, and its inclusion
// probability in this draw (see AuditSample).
type AuditPick struct {
	Key     posting.Key
	Source  string
	PSelect float64
}

// AuditStratum is one non-empty stratum of the draw: how many unlabelled
// postings it held and how many were drawn.
type AuditStratum struct {
	Source string
	Pool   int
	Taken  int
}

// AuditPlan is the draw: the picks, and every non-empty stratum with its counts,
// so a short draw is visible as one.
type AuditPlan struct {
	Picks  []AuditPick
	Strata []AuditStratum
}

// AuditSample draws the week's audit. nScored postings are spread over the ten
// score deciles of the scored pool ("decile:10" is the highest) and nDropped over
// the drop classes ("dropped:<class>"). Postings that already have a label (any
// entry in labeled) are never drawn, a key listed twice counts once, and a
// quota is never lent from one group to the other (that would change the
// inclusion probabilities below). A group with fewer unlabelled postings than
// its quota is drawn short, not padded.
//
// Within a group, places go round-robin to the non-empty strata; when the
// places run out inside a round, the strata served are the first in a hash of
// seed and stratum name. Within a stratum the draw is uniform: the first members
// in a hash of seed and key. Everything is a function of (candidates, labels,
// seed), so the same week draws the same postings in any input order.
//
// PSelect is the posting's probability of being drawn over the seed: the
// expected places its stratum gets, over the stratum's unlabelled size. It is
// relative to the unlabelled pool at the time of the draw. Deciles are cut over
// the whole scored pool, labelled or not, so a stratum keeps its meaning as
// labels accumulate.
func AuditSample(cands []AuditCandidate, labeled map[posting.Key]Entry, seed string, nScored, nDropped int) AuditPlan {
	return AuditDraw(cands, labeled, seed, AuditQuotas{Scored: nScored, Dropped: nDropped})
}

// AuditDraw is AuditSample with a third group: q.Closed places over the closed
// statuses ("closed:dead", "closed:duplicate"), drawn as the drop classes are.
// The scored and dropped groups draw exactly what AuditSample draws for the
// same seed, whatever q.Closed is: each group spends only its own places.
func AuditDraw(cands []AuditCandidate, labeled map[posting.Key]Entry, seed string, q AuditQuotas) AuditPlan {
	deciles, classes, shut := auditBuckets(cands, labeled)
	var plan AuditPlan
	drawGroup(&plan, deciles, q.Scored, seed)
	drawGroup(&plan, classes, q.Dropped, seed)
	if q.Closed > 0 {
		drawGroup(&plan, shut, q.Closed, seed)
	}
	return plan
}

// AuditStrata is every stratum the audit draws from, with its members: the
// score deciles ("decile:N", cut over the whole scored pool), the drop classes
// ("dropped:<class>") and the closed statuses ("closed:<status>"), as AuditDraw
// buckets them, for a caller that pairs postings within a stratum. A posting
// with an entry in labeled is left out, so pass nil to keep the labelled ones.
// Members are in no particular order; a key listed twice counts once.
func AuditStrata(cands []AuditCandidate, labeled map[posting.Key]Entry) map[string][]posting.Key {
	deciles, classes, shut := auditBuckets(cands, labeled)
	out := make(map[string][]posting.Key, len(deciles)+len(classes)+len(shut))
	for _, m := range []map[string][]posting.Key{deciles, classes, shut} {
		for name, ks := range m {
			out[name] = ks
		}
	}
	return out
}

// AuditStratumOf is AuditStrata turned around: the stratum each candidate is
// filed under, labelled or not (deciles are cut over the whole scored pool).
// It draws nothing; the strata comparison (internal/clusterstudy) reads the
// audit's current cells from it.
func AuditStratumOf(cands []AuditCandidate) map[posting.Key]string {
	out := make(map[posting.Key]string, len(cands))
	for name, ks := range AuditStrata(cands, nil) {
		for _, k := range ks {
			out[k] = name
		}
	}
	return out
}

// auditBuckets sorts the unlabelled candidates into the audit's three groups of
// strata: score deciles, drop classes and closed statuses.
func auditBuckets(cands []AuditCandidate, labeled map[posting.Key]Entry) (deciles, classes, shut map[string][]posting.Key) {
	var scored, dropped, closed []AuditCandidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if c.Key == "" || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		switch {
		case c.Closed:
			closed = append(closed, c)
		case c.Dropped:
			dropped = append(dropped, c)
		default:
			scored = append(scored, c)
		}
	}

	// Deciles over the whole scored pool, ties broken by key.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score < scored[j].Score
		}
		return scored[i].Key < scored[j].Key
	})
	deciles = map[string][]posting.Key{}
	for i, c := range scored {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := decileSource(i, len(scored))
		deciles[src] = append(deciles[src], c.Key)
	}
	classes = map[string][]posting.Key{}
	for _, c := range dropped {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := droppedSource(c.Class)
		classes[src] = append(classes[src], c.Key)
	}
	shut = map[string][]posting.Key{}
	for _, c := range closed {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := closedSource(c.Class)
		shut[src] = append(shut[src], c.Key)
	}
	return deciles, classes, shut
}

// decileSource is the stratum of the i'th (from 0, in score order) of n scored
// postings: "decile:1" is the lowest tenth, "decile:10" the highest.
func decileSource(i, n int) string { return fmt.Sprintf("decile:%d", i*10/n+1) }

// WeekSeed is the ISO week of t ("2026-W43"): a draw stays the same all week.
func WeekSeed(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// Interleaved is the picks in a hash order of seed and key, so the owner does
// not meet every scored posting before every dropped one and cannot tell the
// stratum from the position.
func (p AuditPlan) Interleaved(seed string) []AuditPick {
	out := append([]AuditPick(nil), p.Picks...)
	sort.Slice(out, func(i, j int) bool {
		return lessHash(hashOf(seed, "order", string(out[i].Key)), hashOf(seed, "order", string(out[j].Key)))
	})
	return out
}

func hashOf(seed, purpose, name string) [sha256.Size]byte {
	return sha256.Sum256([]byte(seed + "\x00" + purpose + "\x00" + name))
}

func lessHash(a, b [sha256.Size]byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// drawGroup spends total places over the non-empty strata of one group and
// appends the picks and strata to plan, in stratum-name order.
func drawGroup(plan *AuditPlan, strata map[string][]posting.Key, total int, seed string) {
	names := make([]string, 0, len(strata))
	for n := range strata {
		names = append(names, n)
	}
	sort.Strings(names)
	// Stratum order for partial rounds, and member order within each stratum.
	order := append([]string(nil), names...)
	sort.Slice(order, func(i, j int) bool {
		return lessHash(hashOf(seed, "stratum", order[i]), hashOf(seed, "stratum", order[j]))
	})
	for _, n := range names {
		OrderMembers(seed, strata[n])
	}

	taken := map[string]int{}
	expected := map[string]float64{} // expected places over the seed
	for left := total; left > 0; {
		var active []string
		for _, n := range order {
			if taken[n] < len(strata[n]) {
				active = append(active, n)
			}
		}
		if len(active) == 0 {
			break
		}
		if len(active) <= left {
			for _, n := range active {
				taken[n]++
				expected[n]++
			}
			left -= len(active)
			continue
		}
		// A partial round: the first `left` active strata in hash order are
		// served; over the seed each active stratum is served left/len of the time.
		for i, n := range active {
			expected[n] += float64(left) / float64(len(active))
			if i < left {
				taken[n]++
			}
		}
		left = 0
	}

	for _, n := range names {
		ks := strata[n]
		plan.Strata = append(plan.Strata, AuditStratum{Source: n, Pool: len(ks), Taken: taken[n]})
		p := min(expected[n]/float64(len(ks)), 1)
		for _, k := range ks[:taken[n]] {
			plan.Picks = append(plan.Picks, AuditPick{Key: k, Source: n, PSelect: p})
		}
	}
}
