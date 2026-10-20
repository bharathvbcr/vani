package textsim

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWords(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		max  int
		want []string
	}{
		{"empty", "", 100, nil},
		{"only separators", " \t\n--,,", 100, []string{}},
		{"case and punctuation", "Senior C++ Engineer, (Remote)!", 100, []string{"senior", "c", "engineer", "remote"}},
		{"digits", "Go 1.26 and R-12345", 100, []string{"go", "1", "26", "and", "r", "12345"}},
		{"unicode", "Ünïcode Straße ΑΒΓ 東京都 São", 100, []string{"ünïcode", "straße", "αβγ", "東京都", "são"}},
		{"invalid utf-8 separates", "ab\xffcd", 100, []string{"ab", "cd"}},
		{"no bytes allowed", "abc", 0, nil},
		{"negative bound", "abc", -5, nil},
		{"cut at a rune boundary", "abc 東京", 6, []string{"abc"}}, // byte 6 is inside 東
		{"cut keeps whole runes", "abc 東京", 7, []string{"abc", "東"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Words(tc.in, tc.max)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Words(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestWordsBoundsOversizedInput(t *testing.T) {
	huge := strings.Repeat("lorem ipsum dolor ", 1<<20) // ~18 MiB
	start := time.Now()
	got := Words(huge, DefaultMaxBytes)
	if n := len(strings.Join(got, " ")); n > DefaultMaxBytes {
		t.Fatalf("read %d bytes, bound is %d", n, DefaultMaxBytes)
	}
	if n := len(strings.Join(Words(huge, 1<<40), " ")); n > HardMaxBytes || n < HardMaxBytes-64 {
		t.Fatal("a caller's bound went past HardMaxBytes")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("oversized input took %v", d)
	}
}

func TestShingles(t *testing.T) {
	w := Words("a b c d e f", 100)
	if got := len(Shingles(w, 3)); got != 4 {
		t.Fatalf("6 words in 3-shingles: %d, want 4", got)
	}
	if got := len(Shingles(w, 6)); got != 1 {
		t.Fatalf("6 words in 6-shingles: %d, want 1", got)
	}
	for _, k := range []int{7, 0, -1} {
		if got := Shingles(w, k); len(got) != 0 {
			t.Fatalf("k=%d gave %d shingles", k, len(got))
		}
	}
	if len(Shingles(nil, 5)) != 0 {
		t.Fatal("no words gave shingles")
	}
	// Word boundaries matter: "ab c" is not "a bc".
	if Jaccard(Shingles([]string{"ab", "c"}, 2), Shingles([]string{"a", "bc"}, 2)) != 0 {
		t.Fatal("shingles ran words together")
	}
}

func TestJaccard(t *testing.T) {
	a := Shingles(Words("one two three four five six seven eight", 100), 2) // 7
	b := Shingles(Words("one two three four five six seven nine", 100), 2)  // 6 shared, 8 total... see below
	if got := Jaccard(a, a); got != 1 {
		t.Fatalf("self similarity %v", got)
	}
	// a and b share 6 of 7 shingles each; the union is 8.
	if got, want := Jaccard(a, b), 6.0/8.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("Jaccard = %v, want %v", got, want)
	}
	if Jaccard(a, b) != Jaccard(b, a) {
		t.Fatal("Jaccard is not symmetric")
	}
	disjoint := Shingles(Words("x y z w v u t s", 100), 2)
	if got := Jaccard(a, disjoint); got != 0 {
		t.Fatalf("disjoint = %v", got)
	}
}

func TestJaccardOfEmptyIsNeverOne(t *testing.T) {
	var nilSet Set
	full := TokenSet([]string{"a", "b"})
	for name, pair := range map[string][2]Set{
		"nil nil":     {nilSet, nilSet},
		"empty empty": {{}, {}},
		"nil full":    {nilSet, full},
		"full empty":  {full, {}},
	} {
		if got := Jaccard(pair[0], pair[1]); got != 0 {
			t.Errorf("%s: %v, want 0", name, got)
		}
	}
}

func TestTokenSetIgnoresOrderAndRepeats(t *testing.T) {
	a := TokenSet(Words("Senior Software Engineer", 100))
	b := TokenSet(Words("engineer SOFTWARE senior senior", 100))
	if Jaccard(a, b) != 1 {
		t.Fatal("a token set depends on order or repeats")
	}
}
