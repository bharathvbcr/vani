package main

import (
	"strings"
	"testing"
)

// The unit is read from a bounded tail, so a unit still reads after a number
// at any offset, a multi-byte rune at the cut never corrupts the string, and
// padding longer than the window reads as no unit, not as a crash.
func TestW2AFiguresInBoundedTail(t *testing.T) {
	long := strings.Repeat("é", 100) + " "
	for _, tc := range []struct {
		in, class string
		mult      float64
	}{
		{long + "5 µs", "time", 1},
		{"5 percentage points" + long, "pp", 1},
		{"5 thousand percent" + long, "pct", 1e3},
		{"5" + strings.Repeat("é", 60), "", 1},
		{"5" + strings.Repeat(" ", 200) + "million", "", 1},
	} {
		fs := figuresIn(tc.in)
		if len(fs) != 1 || fs[0].Class != tc.class || fs[0].Mult != tc.mult {
			t.Errorf("figuresIn(%.30q...) = %+v, want one %q figure with multiplier %v", tc.in, fs, tc.class, tc.mult)
		}
	}
}

// BenchmarkW2AFiguresIn reads 80 KB of numbers (a pasted quote or a verifier's
// found_value at the size the adversarial test uses). It was quadratic: every
// number re-scanned the rest of the string for its unit.
func BenchmarkW2AFiguresIn(b *testing.B) {
	s := strings.Repeat("1 ", 40000)
	b.SetBytes(int64(len(s)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if n := len(figuresIn(s)); n != 40000 {
			b.Fatalf("figuresIn found %d numbers, want 40000", n)
		}
	}
}
