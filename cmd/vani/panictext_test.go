package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A panic value can carry untrusted JD text: it is bounded and stripped of
// control characters before it reaches a run record or a log line.
func TestPanicTextIsBoundedAndClean(t *testing.T) {
	got := panicText("line1\nline2\x1b[31m" + strings.Repeat("é", 5000))
	if n := utf8.RuneCountInString(got); n > maxPanicText+1 { // the cut adds a one-rune ellipsis
		t.Errorf("%d runes, want at most %d plus the ellipsis", n, maxPanicText)
	}
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("control characters survive: %q", got)
	}
	if got := panicText(nil); got == "" {
		t.Error("a nil panic value renders as nothing")
	}
}
