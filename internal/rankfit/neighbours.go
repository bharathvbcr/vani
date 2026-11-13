package rankfit

import (
	"cmp"
	"slices"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// YesNeighbourPool is the yes-neighbour audit cell's pool size (the third Fable
// pass on the second review): the draw picks uniformly inside it, so the cell
// spends places on near neighbours without always showing the very nearest.
const YesNeighbourPool = 50

// Vectored is a posting with its pooled unit vector.
type Vectored struct {
	Key    posting.Key
	Vector []float32
}

// YesNeighbours fills the yes-neighbour cell's pool: up to k candidates, taken
// round-robin over the anchors (the owner's yes postings, in key order), each
// anchor taking its nearest candidate by cosine that no anchor has taken yet.
// Round-robin keeps one anchor with many near copies from filling the pool
// alone. An anchor or candidate without a vector, or with one of another
// width, is never near anything; a key listed twice counts once. Ties break on
// the candidate key, so the pool is the same for the same inputs.
func YesNeighbours(anchors, cands []Vectored, k int) []posting.Key {
	if k <= 0 {
		return nil
	}
	seen := map[posting.Key]bool{}
	pool := make([]Vectored, 0, len(cands))
	for _, c := range cands {
		if c.Key == "" || len(c.Vector) == 0 || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		pool = append(pool, c)
	}
	as := slices.Clone(anchors)
	slices.SortFunc(as, func(a, b Vectored) int { return cmp.Compare(a.Key, b.Key) })
	as = slices.CompactFunc(as, func(a, b Vectored) bool { return a.Key == b.Key })
	type near struct {
		i   int
		cos float64
	}
	var orders [][]near
	for _, a := range as {
		if len(a.Vector) == 0 {
			continue
		}
		o := make([]near, 0, len(pool))
		for i, c := range pool {
			if len(c.Vector) == len(a.Vector) {
				o = append(o, near{i, cosine(a.Vector, c.Vector)})
			}
		}
		slices.SortFunc(o, func(x, y near) int {
			if c := cmp.Compare(y.cos, x.cos); c != 0 {
				return c
			}
			return cmp.Compare(pool[x.i].Key, pool[y.i].Key)
		})
		orders = append(orders, o)
	}
	taken := make([]bool, len(pool))
	next := make([]int, len(orders))
	var out []posting.Key
	for len(out) < k {
		moved := false
		for a := range orders {
			for next[a] < len(orders[a]) && taken[orders[a][next[a]].i] {
				next[a]++
			}
			if next[a] == len(orders[a]) {
				continue
			}
			i := orders[a][next[a]].i
			taken[i], moved = true, true
			out = append(out, pool[i].Key)
			if len(out) == k {
				break
			}
		}
		if !moved {
			break
		}
	}
	return out
}
