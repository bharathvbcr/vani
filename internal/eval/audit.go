package eval

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Default weekly audit quotas (owner-approved: 15 postings).
const (
	AuditScored  = 5  // by score decile across the scored pool
	AuditDropped = 10 // across drop classes
)

// AuditCandidate is a posting the weekly audit may draw: a kept one with its
// priority score, or a dropped one with its DropClass.
type AuditCandidate struct {
	Key     posting.Key
	Score   float64
	Dropped bool
	Class   string
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
	var scored, dropped []AuditCandidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if c.Key == "" || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		if c.Dropped {
			dropped = append(dropped, c)
		} else {
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
	deciles := map[string][]posting.Key{}
	for i, c := range scored {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := fmt.Sprintf("decile:%d", i*10/len(scored)+1)
		deciles[src] = append(deciles[src], c.Key)
	}
	classes := map[string][]posting.Key{}
	for _, c := range dropped {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := "dropped:" + c.Class
		if c.Class == "" || !validSource(src) {
			src = "dropped:unknown:other"
		}
		classes[src] = append(classes[src], c.Key)
	}

	var plan AuditPlan
	drawGroup(&plan, deciles, nScored, seed)
	drawGroup(&plan, classes, nDropped, seed)
	return plan
}

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
		ks := strata[n]
		sort.Slice(ks, func(i, j int) bool {
			return lessHash(hashOf(seed, "member", string(ks[i])), hashOf(seed, "member", string(ks[j])))
		})
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
