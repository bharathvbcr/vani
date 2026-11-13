package track

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// uTok matches <U+XXXX> or <U+XXXXX> in a test string.
var uTok = regexp.MustCompile(`<U\+([0-9A-Fa-f]{4,6})>`)

// U expands <U+XXXX> tokens to the code point, so tests can name invisible and
// look-alike characters without putting them in the source file.
func U(s string) string {
	return uTok.ReplaceAllStringFunc(s, func(m string) string {
		n, err := strconv.ParseInt(uTok.FindStringSubmatch(m)[1], 16, 32)
		if err != nil {
			panic(err)
		}
		return string(rune(n))
	})
}

// The owner's list in these tests is synthetic. Nothing here is a real term.
var ownerTerms = []string{"kernel", "decode-latency", "Zephyrnet", "Quillbridge Labs", U("Jos<U+00E9>"), "MIT", "UF"}

// leaks are spellings of a listed term that the whole-word matcher let through
// (F1: plurals, suffixes, camelCase, joins; F2: Unicode tricks). Every one must
// be caught by Found and removed by Redact.
var leaks = map[string]string{
	// F1: inflection, suffix, camelCase, joins and spacing of listed terms.
	"plural":            "tuned kernels for Metal",
	"suffix":            "a kernelized path",
	"hyphen listed":     "cut decode-latency in half",
	"space for hyphen":  "cut decode latency in half",
	"double space":      "cut decode  latency in half",
	"nbsp":              U("cut decode<U+00A0>latency in half"),
	"newline":           "cut decode\nlatency in half",
	"tab":               "cut decode\tlatency in half",
	"no separator":      "cut decodelatency in half",
	"camel join":        "cut DecodeLatency in half",
	"underscore":        "cut decode_latency in half",
	"camel suffix":      "ported ZephyrnetApp today",
	"plural name":       "ported Zephyrnets today",
	"camel prefix":      "ported myZephyrnet today",
	"hyphen inside":     "ported Zephyr-net today",
	"underscore inside": "ported Zephyr_net today",
	"short caps join":   "at the MITLab today",
	"short caps join2":  "at UFHealth today",
	"phrase joined":     "at QuillbridgeLabs now",
	"phrase hyphen":     "at Quillbridge-Labs now",
	"phrase double":     "at Quillbridge  Labs now",
	"phrase newline":    "at Quillbridge\nLabs now",
	// F2: Unicode variants.
	"nfd accent":     U("met Jose<U+0301> today"),
	"nfc accent":     U("met Jos<U+00E9> today"),
	"plain accent":   "met Jose today",
	"zero-width":     U("ported Zeph<U+200B>yrnet today"),
	"zwj":            U("ported Zeph<U+200D>yrnet today"),
	"soft hyphen":    U("ported Zephyrn<U+00AD>et today"),
	"bom":            U("ported <U+FEFF>Zephyrnet today"),
	"cyrillic e":     U("ported Z<U+0435>phyrnet today"),
	"cyrillic k e":   U("tuned k<U+0435>rnel for it"),
	"fullwidth":      U("ported <U+FF3A><U+FF45>phyrnet today"),
	"greek omicron":  U("met J<U+03BF>se today"),
	"math bold":      U("ported <U+1D433>ephyrnet today"),
	"circled":        U("ported <U+24CF>ephyrnet today"),
	"combining on z": U("ported Z<U+0301>ephyrnet today"),
}

func TestOwnerTermsAreCaughtInEverySpellingAndRemoved(t *testing.T) {
	w := wordsOf(t, ownerTerms...)
	for name, in := range leaks {
		t.Run(name, func(t *testing.T) {
			if f := w.Found(in); len(f) == 0 {
				t.Errorf("Found misses %+q", in)
			}
			out, n := w.Redact(in)
			if n == 0 || !strings.Contains(out, Redacted) {
				t.Errorf("Redact leaves %+q as %+q", in, out)
			}
			if f := w.Found(out); len(f) > 0 {
				t.Errorf("Found still finds %v in Redact(%+q) = %+q", f, in, out)
			}
		})
	}
}

// Check and Redact are one matcher: whatever Found reports, Redact removes
// something, and whatever Redact removes, Found reports (F3).
func TestFoundAndRedactAgree(t *testing.T) {
	w := wordsOf(t, ownerTerms...)
	odd := []string{
		U("V<U+0130>SA holders"), U("<U+017F>ponsor needed"), "VISA", U("<U+FF36><U+FF29><U+FF33><U+FF21>"),
		U("visa<U+200B>"), U("vi<U+00AD>sa"), "opt in", "optimize", "OPT", U("o<U+0440>t"), U("cit<U+0456>zen"),
		U("git<U+04BB>ub"), "GITHUB", U("h<U+2212>1b"), "H-1B", "h1b", U("mail me<U+FF20>example.com"), "a@b",
		"green  card", U("green<U+00A0>card"), "green\ncard", "work authorization", "nothing here", "", "   ",
	}
	for _, in := range append(odd, mapValues(leaks)...) {
		found := len(w.Found(in)) > 0
		out, n := w.Redact(in)
		if found != (n > 0) {
			t.Errorf("%+q: Found says %v, Redact removed %d", in, found, n)
		}
		if f := w.Found(out); len(f) > 0 {
			t.Errorf("%+q: Redact left %v in %+q", in, f, out)
		}
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// Built-in terms stay whole words, so ordinary words are not eaten: "opt" is
// not "optimize" and "visa" is not "visas". Folding still applies to them.
func TestBuiltinTermsStayWholeWords(t *testing.T) {
	w := BuiltinWords()
	for _, in := range []string{"optimize the plan", "an optional flag", "visas", "adopted", "sponsored", "capital", "ceadmin"} {
		if out, n := w.Redact(in); out != in || n != 0 {
			t.Errorf("Redact(%q) = %q (%d), want it unchanged", in, out, n)
		}
		if f := w.Found(in); len(f) > 0 {
			t.Errorf("Found(%q) = %v", in, f)
		}
	}
	for _, in := range []string{"OPT", "on opt.", "(visa)", U("V<U+0130>SA"), U("<U+017F>ponsor"), U("<U+FF36><U+FF29><U+FF33><U+FF21>"), U("git<U+200B>hub"), "Green  Card"} {
		if f := w.Found(in); len(f) == 0 {
			t.Errorf("Found misses %+q", in)
		}
		if out, n := w.Redact(in); n == 0 || len(w.Found(out)) > 0 {
			t.Errorf("Redact(%+q) = %+q (%d)", in, out, n)
		}
	}
}

// Owner terms do not eat unrelated words: a prefix of a term, a different word
// that shares a stem with one token, and short terms that are common word starts.
func TestOwnerTermsLeaveUnrelatedWordsAlone(t *testing.T) {
	w := wordsOf(t, "kernel", "decode-latency", "Zephyrnet", "mit", "metal")
	for _, in := range []string{
		"Zephyr and net", "Zephyr net", "decoder latency", "decode the latency", "the colonel", "mitigate risk", "committed", "limit it",
		// A space inside a word of a term is not a join: "metal" is not "met al".
		"met all the goals", "met al",
	} {
		if out, n := w.Redact(in); out != in || n != 0 {
			t.Errorf("Redact(%q) = %q (%d), want it unchanged", in, out, n)
		}
	}
	// A short term is still a whole word, with a plural.
	for _, in := range []string{"at MIT", "MIT's lab", "two mits"} {
		if f := w.Found(in); len(f) == 0 {
			t.Errorf("Found misses %q", in)
		}
	}
}

func TestRedactKeepsTextBetweenMatchesAndMapsBackToTheOriginal(t *testing.T) {
	w := wordsOf(t, "Zephyrnet", U("Jos<U+00E9>"))
	in := U("Zeph<U+200B>yrnet - and Jose<U+0301>, caf<U+00E9> naive")
	out, n := w.Redact(in)
	if want := U("[redacted] - and [redacted], caf<U+00E9> naive"); out != want || n != 2 {
		t.Fatalf("got %+q (%d), want %+q", out, n, want)
	}
}

func TestLoadWordsRefusesATermWithNothingVisible(t *testing.T) {
	dir := writeWords(t, "good term", U("<U+200B><U+200D>"))
	_, err := LoadWords(dir)
	if err == nil {
		t.Fatal("a term of invisible characters was accepted")
	}
	if strings.Contains(err.Error(), U("<U+200B>")) || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error %+q should name the line only", err.Error())
	}
}

// An over-long line is reported by number and length, never by content: the
// error reaches --json output and the ranking lookup's reason (F5).
func TestLoadWordsErrorNeverQuotesTheFile(t *testing.T) {
	long := "Zephyrnetlongterm" + strings.Repeat("x", 200)
	dir := writeWords(t, "fine", "# note", long)
	_, err := LoadWords(dir)
	if err == nil {
		t.Fatal("an over-long line was accepted")
	}
	if strings.Contains(strings.ToLower(err.Error()), "zephyrnet") || strings.Contains(err.Error(), "xxxx") {
		t.Fatalf("the error quotes the file: %v", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("the error does not name the line: %v", err)
	}
}

func TestRedactIsLinearOnSeparatorRuns(t *testing.T) {
	w := wordsOf(t, "Quillbridge Labs", "decode-latency")
	in := strings.Repeat("decode"+strings.Repeat("-", 200)+"x ", 300) + "Quillbridge" + strings.Repeat(" ", 5000) + "Labs"
	out, n := w.Redact(in)
	if n == 0 || len(w.Found(out)) > 0 {
		t.Fatalf("%d redactions, still found %v", n, w.Found(out))
	}
}

// writeWords writes a words file of lines and returns its directory.
func writeWords(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, WordsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
