package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// textHashRef is TextHash as it was first written. The hash keys every stored
// vector and score (vectors.log, the score cache), so any other way of
// computing it must give these exact bytes, or every cached score is lost.
func textHashRef(title, description string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s\x00%d:%s", len(title), title, len(description), description)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func TestTextHashMatchesReference(t *testing.T) {
	big := strings.Repeat("Go, Rust and CUDA on Kubernetes. ", 20_000) // ~660 KB, past any buffer
	cases := []struct{ title, desc string }{
		{"", ""},
		{"Inference Engineer", ""},
		{"", "a description with no title"},
		{"Engineer", "We build things."},
		{"Ünïcödé — 工程师", "日本語のテキスト and emoji 🚀"},
		{"nul\x00in title", "nul\x00in\x00description"},
		{"invalid \xff\xfe utf8", "\xc3\x28 bad sequence"},
		{"1:2", "3:4\x005:6"},                // looks like the framing itself
		{"a", "\x00" + "1:b"},                // a boundary shift must not collide
		{"title", strings.Repeat("x", 4095)}, // around a 4 KiB chunk edge
		{"title", strings.Repeat("x", 4096)},
		{"title", strings.Repeat("x", 4097)},
		{strings.Repeat("T", 5000), big},
	}
	for i, c := range cases {
		if got, want := TextHash(c.title, c.desc), textHashRef(c.title, c.desc); got != want {
			t.Errorf("case %d: TextHash = %s, reference %s", i, got, want)
		}
	}
	// Framing keeps title and description apart: moving bytes across the
	// boundary changes the hash.
	if TextHash("ab", "c") == TextHash("a", "bc") {
		t.Error("TextHash(\"ab\",\"c\") equals TextHash(\"a\",\"bc\")")
	}
}

// The Jobs views hash every listed posting's text on every list (twice: the
// rank and the shown match), so hashing must not copy the description into a
// growing buffer. Bytes allocated per call stay bounded however long the text
// is (the first implementation allocated more than the text itself).
func TestTextHashAllocationIsBounded(t *testing.T) {
	desc := strings.Repeat("We serve models with low latency. ", 8_000) // ~270 KB
	TextHash("warm", desc)
	var before, after runtime.MemStats
	const n = 20
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range n {
		TextHash("Inference Engineer", desc)
	}
	runtime.ReadMemStats(&after)
	per := (after.TotalAlloc - before.TotalAlloc) / n
	if per > 16<<10 {
		t.Errorf("TextHash allocated %d bytes per call over a %d-byte description; want at most %d", per, len(desc), 16<<10)
	}
}

func BenchmarkTextHash(b *testing.B) {
	for _, n := range []int{6 << 10, 64 << 10, 256 << 10} {
		desc := strings.Repeat("x", n)
		b.Run(fmt.Sprintf("desc=%dKiB", n>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(n))
			for b.Loop() {
				TextHash("Inference Engineer", desc)
			}
		})
	}
}
