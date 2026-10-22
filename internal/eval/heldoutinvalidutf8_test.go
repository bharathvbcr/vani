package eval

import (
	"bytes"
	"testing"
)

// Invalid UTF-8 is dropped, not replaced: Lappi's pipeline decodes with
// errors="ignore", so every invalid byte vanishes. NormalizeText must hash the
// same text Python does for each shape of invalid input.
func TestPythonInvalidUTF8Parity(t *testing.T) {
	cap := MaxNormalizedBytes
	raws := map[string][]byte{
		"lone-ff":         []byte("abc\xffdef"),
		"two-bad-bytes":   []byte("abc\xff\xfedef"),
		"truncated-seq":   []byte("abc\xe2\x82 def"),
		"bad-at-end":      []byte("abc def\xff"),
		"truncated-end":   []byte("abc def\xe2\x82"),
		"lone-cont":       []byte("abc\x80\xbfdef"),
		"overlong":        []byte("abc\xc0\xafdef"),
		"surrogate":       []byte("abc\xed\xa0\x80def"),
		"above-10ffff":    []byte("abc\xf4\x90\x80\x80def"),
		"bad-between-ws":  []byte("abc \xff def"),
		"cap-splits-rune": append(bytes.Repeat([]byte("a"), cap-1), []byte("€tail")...),
		"cap-exact":       append(bytes.Repeat([]byte("a"), cap-3), []byte("€tail")...),
		// Invalid bytes before the cap are gone before it is applied.
		"bad-before-cap": append(bytes.Repeat([]byte("a\xff"), 1000), bytes.Repeat([]byte("b"), cap)...),
	}
	for name, raw := range raws {
		want := pyHashes(t, []pyCase{{Name: name, raw: raw, Mode: "ignore"}})[0]
		got, err := NormalizedTextSHA256(string(raw))
		if err != nil || got != want {
			t.Errorf("%s: Go %s (err %v), Python %s", name, got, err, want)
		}
	}
}

// Dropping is observable without Python: the bytes are gone, not replaced.
func TestNormalizeTextDropsInvalidBytes(t *testing.T) {
	for in, want := range map[string]string{
		"abc\xffdef":      "abcdef",
		"abc\xff\xfedef":  "abcdef",
		"abc\xe2\x82 def": "abc def",
		"abc def\xe2\x82": "abc def",
		"\x80 a \xbf":     "a",
		"a \xff b":        "a b",
		"real � kept":     "real � kept",
	} {
		got, err := NormalizeText(in)
		if err != nil || got != want {
			t.Errorf("NormalizeText(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeText("\xff\xfe \x80"); err != ErrEmptyText {
		t.Errorf("only invalid bytes: err = %v, want ErrEmptyText", err)
	}
}
