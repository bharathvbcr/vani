package textsim

// Jaccard is |a ∩ b| / |a ∪ b|, from 0 (nothing shared) to 1 (the same set).
// It is symmetric. If either set is empty it is 0: two empty texts say nothing
// about each other, so they are never reported as identical.
func Jaccard(a, b Set) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	inter := 0
	for h := range small {
		if _, ok := large[h]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
