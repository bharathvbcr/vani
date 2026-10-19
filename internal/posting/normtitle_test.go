package posting

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

// Different languages are different roles: their fallback keys, and so the
// tuples reconcile dedupes on, must differ.
func TestFallbackKeySeparatesLanguageTitles(t *testing.T) {
	titles := []string{
		"Senior C Engineer", "Senior C++ Engineer", "Senior C# Engineer", "Senior F# Engineer",
		"Senior .NET Engineer", "Senior NET Engineer",
	}
	seen := map[Key]string{}
	for _, title := range titles {
		k, err := FallbackKey("Acme", title, "Remote")
		if err != nil {
			t.Fatal(err)
		}
		if prev, dup := seen[k]; dup {
			t.Errorf("%q and %q share key %s", prev, title, k)
		}
		seen[k] = title
	}
}

func TestNormTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Senior C++ Engineer":      "senior c++ engineer",
		"Senior C# Engineer":       "senior c# engineer",
		"C/C++ Developer":          "c c++ developer",
		"C#/.NET Developer":        "c# .net developer",
		"Software Engineer (.NET)": "software engineer .net",
		"F# Engineer":              "f# engineer",
		"Sr. Engineer":             "sr engineer",
		"ASP.NET Engineer":         "asp net engineer",
		"Node.js Engineer":         "node js engineer",
		"Engineer #2":              "engineer 2",
		"Engineer#2":               "engineer 2",
		"#1 Engineer":              "1 engineer",
		"Engineer + Lead":          "engineer lead",
		"Engineering Manager- C++": "engineering manager c++",
		"G++ Toolchain Engineer":   "g++ toolchain engineer",
		"C+++ Engineer":            "c++ engineer",
		// A single "+" is seniority or a joiner, not a language (the live
		// ledger's "Staff+" titles must keep their keys).
		"Staff+ Software Engineer":  "staff software engineer",
		"Senior+ Engineer":          "senior engineer",
		"Data+AI Practitioner":      "data ai practitioner",
		"A+ Engineer":               "a engineer",
		"Engineer.":                 "engineer",
		"Engineer, .":               "engineer",
		"  ++ ## .. ":               "",
		"":                          "",
		"Ingénieur C++ Senior":      "ingénieur c++ senior",
		"SENIOR   c++   ENGINEER  ": "senior c++ engineer",
	} {
		if got := normTitle(in); got != want {
			t.Errorf("normTitle(%q) = %q, want %q", in, got, want)
		}
	}
	// Spacing and case still never matter.
	a, _ := FallbackKey("Acme", "Senior C++ Engineer", "")
	b, _ := FallbackKey("acme", "  senior   c++ engineer ", "")
	if a != b {
		t.Errorf("%s != %s", a, b)
	}
}

// Company and location fold exactly as before: only titles keep language
// punctuation, so "Acme C# Inc" and "D.C." key as they always have.
func TestCompanyAndLocationFoldAsBefore(t *testing.T) {
	a, _ := FallbackKey("Acme C# Inc.", "Engineer", "Washington, D.C.")
	b, _ := FallbackKey("acme c inc", "engineer", "washington d c")
	if a != b {
		t.Errorf("company or location folding changed: %s != %s", a, b)
	}
}

// oldNorm is norm as it was before titles kept language punctuation.
func oldNorm(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}

// changesTitle reports whether s holds what normTitle keeps: "++" right after
// a letter or digit, "#" right after a one-character word, or a "." starting a
// word before a letter. It is written independently of fold.
func changesTitle(s string) bool {
	rs := []rune(strings.ToLower(s))
	alnum := func(j int) bool { return j >= 0 && j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) }
	for i, r := range rs {
		switch {
		case r == '+' && alnum(i-1) && i+1 < len(rs) && rs[i+1] == '+':
			return true
		case r == '#' && alnum(i-1) && !alnum(i-2):
			return true
		case r == '.' && !alnum(i-1) && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			return true
		}
	}
	return false
}

// Every title without that punctuation keeps its old fold, so the change
// re-keys nothing else. Random titles over a punctuation-heavy alphabet.
func TestNormTitleMatchesOldNormElsewhere(t *testing.T) {
	norm, oldN := norm, oldNorm
	alphabet := []rune("ac C1 .+#/-(),é\t")
	rng := rand.New(rand.NewSource(7))
	checked := 0
	for range 200000 {
		n := rng.Intn(12)
		rs := make([]rune, n)
		for i := range rs {
			rs[i] = alphabet[rng.Intn(len(alphabet))]
		}
		s := string(rs)
		if norm(s) != oldN(s) {
			t.Fatalf("norm(%q) changed: %q, was %q", s, norm(s), oldN(s))
		}
		if changesTitle(s) {
			continue
		}
		checked++
		if got := normTitle(s); got != oldN(s) {
			t.Fatalf("normTitle(%q) = %q, old fold %q", s, got, oldN(s))
		}
	}
	if checked < 50000 {
		t.Fatalf("only %d titles without kept punctuation were checked", checked)
	}
}
