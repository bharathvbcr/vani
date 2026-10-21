package eval

import (
	"strconv"
	"strings"
	"testing"
)

// With nothing asked (or paired) no split size is cleared, and the advice must
// not say one is: Split{Size: 3}.PromotionFloor() is 1, which 0 does not meet.
// With n >= 1 the advised size is exactly the largest one n clears.
func TestZeroAskedAdviceIsTrue(t *testing.T) {
	h := HeldOut{Split: Split{Name: "x", Size: 30}}
	for name, p := range map[string]Promotion{
		"0 asked":  {},
		"0 paired": {Asked: 30, PrimaryAnswered: 5, ShadowAnswered: 5},
	} {
		why := h.Insufficiency(p)
		if why == "" {
			t.Fatalf("%s: no advice", name)
		}
		if m := advisedSize.FindStringSubmatch(why); m != nil {
			t.Errorf("%s: advises a split size (%s) that 0 cannot clear: %q", name, m[1], why)
		}
		if !strings.Contains(why, "no split size would be cleared by the 0") {
			t.Errorf("%s: advice does not say nothing is cleared by 0: %q", name, why)
		}
	}
	for n := 1; n < 27; n++ {
		why := h.Insufficiency(Promotion{Asked: n})
		m := advisedSize.FindStringSubmatch(why)
		if m == nil {
			t.Fatalf("%d asked: no advised size in %q", n, why)
		}
		s, _ := strconv.Atoi(m[1])
		if (Split{Size: s}).PromotionFloor() > n || (Split{Size: s + 1}).PromotionFloor() <= n {
			t.Errorf("%d asked: advised size %d is not the largest cleared: %q", n, s, why)
		}
	}
}
