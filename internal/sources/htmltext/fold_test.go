package htmltext

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

// fragments are the pieces random documents are built from: mixed-case tags,
// unterminated constructs, entities, and non-ASCII letters whose Unicode case
// folding would match ASCII ("K" Kelvin sign, "ſ" long s) where ASCII folding
// must not.
var fragments = []string{
	"<", ">", "/", "!", "-", "--", "<!--", "-->", " ", "\n", "\t", "\x00",
	"<p>", "</P>", "<BR>", "<Li>", "<td>", "<TH>", "<div class=x>", "</DIV>",
	"<script>", "</script>", "<SCRIPT type=a>", "</ScRiPt>", "</script", "<style>", "</STYLE>",
	"<a href='/x'>", "</A>", "<b>", "</b>",
	"&lt;", "&amp;", "&gt;b&lt;", "&#65;", "&bogus",
	"text", "Word", "é", "\u212a", "ſ", "<\u212aBD>", "</ſcript>", "<ſcript>",
	"\xff", "<h1>", "</H1>", "<?xml?>", "<!DOCTYPE html>",
}

func randomDoc(rng *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(fragments[rng.Intn(len(fragments))])
	}
	return b.String()
}

// Text no longer lowers a copy of its input; on every document it must still
// produce exactly what the old version did.
func TestTextMatchesOldTextOnRandomDocuments(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := range 100000 {
		doc := randomDoc(rng, rng.Intn(40))
		max := 1 + rng.Intn(300)
		if got, want := Text(doc, max), oldText(doc, max); got != want {
			t.Fatalf("case %d, max %d, doc %q:\n got %q\nwant %q", i, max, doc, got, want)
		}
	}
}

func TestTextMatchesOldTextOnLargeDocuments(t *testing.T) {
	for _, doc := range []string{benchPage(2000), strings.Repeat("<SCRIPT>x", 3000), strings.Repeat("<!-- ", 4000), randomDoc(rand.New(rand.NewSource(2)), 50000)} {
		for _, max := range []int{1, 100, 4096, 64 << 10, 1 << 20} {
			if got, want := Text(doc, max), oldText(doc, max); got != want {
				t.Fatalf("max %d over a %d-byte document: outputs differ (%d vs %d bytes)", max, len(doc), len(got), len(want))
			}
		}
	}
}

// IndexFold is strings.Index over the ASCII-lowered string, without the copy.
func TestIndexFoldIsIndexOverTheLoweredString(t *testing.T) {
	subs := []string{"</script", "</style", "</a", "-->", "<!--", "script", "k", "s", "", "<", "</ſ"}
	rng := rand.New(rand.NewSource(3))
	for range 50000 {
		s := randomDoc(rng, rng.Intn(12))
		for _, sub := range subs {
			if got, want := IndexFold(s, sub), strings.Index(oldASCIILower(s), sub); got != want {
				t.Fatalf("IndexFold(%q, %q) = %d, want %d", s, sub, got, want)
			}
		}
	}
	for _, tc := range []struct {
		s, sub string
		want   int
	}{
		{"</\u212aBD", "</k", -1}, // Kelvin sign is not ASCII "k"
		{"</ſcript>", "</script", -1},
		{"x</SCRIPT>", "</script", 1},
		{"</scrip", "</script", -1}, // sub longer than what is left
		{"", "</a", -1},
		{"abc", "", 0},
	} {
		if got := IndexFold(tc.s, tc.sub); got != tc.want {
			t.Errorf("IndexFold(%q, %q) = %d, want %d", tc.s, tc.sub, got, tc.want)
		}
	}
}

func TestLowerASCII(t *testing.T) {
	for in, want := range map[string]string{"": "", "div": "div", "DIV": "div", "Data-ID": "data-id", "É\u212aX": "É\u212ax", "a1B2": "a1b2"} {
		if got := LowerASCII(in); got != want {
			t.Errorf("LowerASCII(%q) = %q, want %q", in, got, want)
		}
		if got := LowerASCII(in); got != oldASCIILower(in) {
			t.Errorf("LowerASCII(%q) differs from the old lowering", in)
		}
	}
	// Already lower case: no copy.
	if n := testing.AllocsPerRun(100, func() { _ = LowerASCII("application") }); n != 0 {
		t.Errorf("LowerASCII on a lower-case name allocated %v times", n)
	}
}

// The point of the change: a 16 MiB document read for 256 KiB no longer
// costs a lowered copy of all 16 MiB (two, counting the string conversion).
// What Text allocates depends on max, not on the document: doubling a
// document read for the same max must not double the allocation (the old
// version's two lowered copies were 2 x len(doc)).
func TestTextDoesNotCopyTheWholeDocument(t *testing.T) {
	alloc := func(doc string) uint64 {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		if Text(doc, 256<<10) == "" {
			t.Fatal("no text")
		}
		runtime.ReadMemStats(&m1)
		return m1.TotalAlloc - m0.TotalAlloc
	}
	small, large := benchPage(8<<20/200), benchPage(16<<20/200)
	a, b := alloc(small), alloc(large)
	if grew := int64(b) - int64(a); grew > int64(len(large)-len(small))/4 {
		t.Errorf("allocation grew by %d bytes when the document grew by %d (%d -> %d bytes allocated)", grew, len(large)-len(small), a, b)
	}
}
