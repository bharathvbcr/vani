package rkbresume

import (
	"strings"
	"testing"
)

func TestClipIsOneLineAndBounded(t *testing.T) {
	got := clip("a\n\n  b\tc"+strings.Repeat("é", 500), 20)
	if strings.ContainsAny(got, "\n\t") || len([]rune(got)) != 20 {
		t.Errorf("clip = %q (%d runes)", got, len([]rune(got)))
	}
	if clip("\xff\xfe ok", 10) != "? ok" {
		t.Errorf("clip of invalid UTF-8 = %q", clip("\xff\xfe ok", 10))
	}
}
