package eval

import (
	"slices"
	"sort"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// OrderMembers sorts ks in place by a hash of seed and key: the one uniform
// within-pool order every sampled draw (the weekly audit, the digest's
// Explore) takes its first members from.
func OrderMembers(seed string, ks []posting.Key) {
	sort.Slice(ks, func(i, j int) bool {
		return lessHash(hashOf(seed, "member", string(ks[i])), hashOf(seed, "member", string(ks[j])))
	})
}

// UniformSample draws up to n distinct non-empty keys from pool uniformly: the
// first n in OrderMembers' order, a function of (pool as a set, seed) only.
// pSelect is each pick's inclusion probability over the seed, n over the pool's
// distinct size, at most 1; it is 0 when nothing is drawn. pool is not changed.
func UniformSample(seed string, pool []posting.Key, n int) (picked []posting.Key, pSelect float64) {
	seen := make(map[posting.Key]bool, len(pool))
	ks := make([]posting.Key, 0, len(pool))
	for _, k := range pool {
		if k != "" && !seen[k] {
			seen[k] = true
			ks = append(ks, k)
		}
	}
	if n <= 0 || len(ks) == 0 {
		return nil, 0
	}
	OrderMembers(seed, ks)
	take := min(n, len(ks))
	return slices.Clone(ks[:take]), min(float64(n)/float64(len(ks)), 1)
}
