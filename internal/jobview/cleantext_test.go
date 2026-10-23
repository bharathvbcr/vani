package jobview

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanText(t *testing.T) {
	cases := []struct {
		in        string
		max       int
		keepLines bool
		want      string
	}{
		{"", 10, false, ""},
		{"  a \t b  ", 10, false, "a b"},
		{"a\x00b\x7fc", 10, false, "a b c"},
		{"bad\xffutf8", 20, false, "bad�utf8"},
		{strings.Repeat("é", 10), 4, false, "éééé…"},
		{"a\n\n b\n", 10, true, "a\nb"},
		{"a\nb", 10, false, "a b"},
		// A separator at the cut counts toward maxRunes: never maxRunes+1 before the ellipsis.
		{"ab cd", 3, false, "ab…"},
		{"ab\ncd", 3, true, "ab…"},
		{"ab cd", 4, false, "ab c…"},
		{"ab c", 4, false, "ab c"},
	}
	for _, c := range cases {
		if got := CleanText(c.in, c.max, c.keepLines); got != c.want {
			t.Errorf("CleanText(%q, %d, %v) = %q, want %q", c.in, c.max, c.keepLines, got, c.want)
		}
	}
	// The bound holds for any spacing: at most maxRunes, plus the ellipsis.
	for _, in := range []string{strings.Repeat("a b ", 200), strings.Repeat("é\n\n", 200), strings.Repeat(" x", 200)} {
		for _, keep := range []bool{false, true} {
			for max := 1; max < 20; max++ {
				got := strings.TrimSuffix(CleanText(in, max, keep), "…")
				if n := utf8.RuneCountInString(got); n > max {
					t.Errorf("CleanText(%q…, %d, %v) kept %d runes", in[:8], max, keep, n)
				}
			}
		}
	}
}
