package draft

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The grounding guard: the system prompt tells the model never to invent a
// metric, but an instruction is not a check. After the reply, every figure in
// the draft (a number, a percentage, a multiple such as 3.1x, a version) must
// occur in something the model was shown: the evidence, the posting excerpt, the
// question, the company and title, the target summary and, for a motivation
// question, the seeker's notes. A figure that occurs in none of them is a
// fabrication or a derived number ("5 years" from two dates), and the draft is
// refused rather than handed to a person who may not check it.
//
// Matching is on figures, not strings, so formatting does not trip it: "3.1×" and
// "3.1x", "1,000" and "1000", "42 %", "42 percent" and "42%" are the same figure.
// A figure is a number plus what it counts, because the same digits count
// different things ("7 years" is not "24/7", "3 regions" is not "S3", "40 people"
// is not "40%"):
//
//   - A mark is % or x ("42%", "3.1x"): a marked figure needs the same number with
//     the same mark in a source.
//   - A tag is what a number is attached to, so it is not a plain count: letters
//     directly before it ("S3", "k8s", "H100", "p99": the tag is the letters) or a
//     slash joining it to another number ("24/7", "1/2", "2023/01": the tag is
//     "/"). A tagged figure is only grounded by the same tag and number; a plain
//     figure is never grounded by a tagged one, and the reverse. A "v" before a
//     version ("v2.1") is no tag: it is the same figure as "2.1".
//   - A word is the next word token of a plain, unmarked number ("7 years", "40ms",
//     "3-year", "5000 GPUs"): the letters that follow it, after a space or one
//     hyphen, lowercased, with a plural folded ("years" is "year", "ies" is "y"). It
//     is not read from a closed set of function words ("and", "in", "I", "more",
//     "x", the ordinal suffixes th st nd rd), which never say what is counted, nor
//     from a year (1900 to 2099), which is a date. A plain figure that has a word is
//     grounded only by the same number with the same word in a source ("7 years"
//     needs "7 years", not "7 days" and not "7x"); a plain figure with no word is
//     grounded by that number anywhere untagged, as before. So an honest rewording
//     of what is counted ("40ms" as "40 milliseconds", "team of 12" as "12
//     engineers") is refused, as it names a count no source states; a refusal can
//     be re-drawn, a fabrication cannot be un-sent.
//   - Vulgar fractions (½ ¼ ¾ ⅓ …) are figures, matched as the character.
//
// Numbers written as words ("three", "five years") are not read: the guard sees
// digits and fraction characters only. Currency symbols are not part of a figure:
// "$10 million" is the number 10 with the word "million".

// figure is a number with its mark, tag and word (see above): "42%", "3.1x",
// "2023", "1.2.3", "7 years", "k8s".
type figure struct{ num, mark, tag, word string }

func (f figure) String() string { return f.num + f.mark }

var (
	// numRE is one number: digits, with dotted groups for decimals and versions,
	// or one vulgar fraction character (the \p{No} that are fractions).
	numRE = regexp.MustCompile(`\p{Nd}+(?:\.\p{Nd}+)*|[\x{bc}-\x{be}\x{2150}-\x{215f}\x{2189}]`)
	// groupRE is a whole number written with thousands separators ("1,000,000"),
	// bounded by non-digits so "2020,2021" is two numbers. Only a comma or a
	// no-break or thin space groups: an ordinary space never does.
	groupRE = regexp.MustCompile(`(^|[^\p{Nd}])(\p{Nd}{1,3}(?:[,\x{a0}\x{2009}\x{202f}]\p{Nd}{3})+)([^\p{Nd}]|$)`)
	// percentRE is what makes the number before it a percentage.
	percentRE = regexp.MustCompile(`^[ \x{a0}\x{202f}]*(?:%|percent(?:age)?\b|per cent\b)`)
	// wordAfterRE is the word token after a number: attached letters, or letters
	// after spaces or one hyphen. A newline never joins a number to the next line.
	wordAfterRE = regexp.MustCompile(`^(?:[ \x{a0}\x{202f}]+|-)?(\p{L}+)`)
	// foldRE maps the look-alikes of % and x that are not worth distinguishing.
	foldRE = strings.NewReplacer("×", "x", "✕", "x", "✖", "x", "％", "%", "﹪", "%")
)

// maxTagRunes bounds the letters read before a number as its tag.
const maxTagRunes = 12

// functionWords never say what a number counts, so a number they follow has no
// word. A closed class of English: determiners, pronouns, prepositions,
// conjunctions, auxiliaries and the few adverbs that sit between a number and
// its noun ("5 more engineers"); "x" and "by" stand for "3 x 4" and "3 by 4",
// and the ordinal suffixes are the "th" of "99th".
var functionWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or but nor so yet for of in on at to from with without into onto
		over under about as than then that this these those it its i we you he she they me us them my our your his
		her their is are was were be been am do did does has have had will would can could should may might must
		shall not no if when while who whom which what where why how also just more most less x by plus both each
		all every other another such very only even still too up down out off again once th st nd rd`) {
		functionWords[w] = true
	}
}

// joinGroups writes "1,000" as "1000". Adjacent matches share a boundary rune
// ("1,000 2,000"), so it repeats until nothing changes; each pass strictly
// shortens s, so it ends.
func joinGroups(s string) string {
	for {
		var b strings.Builder
		last, changed := 0, false
		for _, m := range groupRE.FindAllStringSubmatchIndex(s, -1) {
			b.WriteString(s[last:m[4]])
			b.WriteString(strings.Map(func(r rune) rune {
				if r == ',' || r == '\u00a0' || r == '\u2009' || r == '\u202f' {
					return -1
				}
				return r
			}, s[m[4]:m[5]]))
			last, changed = m[5], true
		}
		if !changed {
			return s
		}
		b.WriteString(s[last:])
		s = b.String()
	}
}

// tagOf is what the number between before and rest is attached to: the letters
// right before it ("s" of "S3"), or "/" when a slash joins it to another number.
// A "v" before it is a version, not a tag, and version reports it.
func tagOf(before, rest string) (tag string, version bool) {
	if r, _ := utf8.DecodeLastRuneInString(before); unicode.IsLetter(r) {
		i := len(before)
		for n := 0; n < maxTagRunes && i > 0; n++ {
			r, sz := utf8.DecodeLastRuneInString(before[:i])
			if !unicode.IsLetter(r) {
				break
			}
			i -= sz
		}
		if before[i:] == "v" {
			return "", true
		}
		return before[i:], false
	}
	if strings.HasPrefix(rest, "/") && startsWithDigit(rest[1:]) {
		return "/", false
	}
	if strings.HasSuffix(before, "/") && endsWithDigit(before[:len(before)-1]) {
		return "/", false
	}
	return "", false
}

func startsWithDigit(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsDigit(r)
}

func endsWithDigit(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsDigit(r)
}

// isYear reports whether num is a four-digit year, which dates and does not count.
func isYear(num string) bool {
	n, err := strconv.Atoi(num)
	return err == nil && len(num) == 4 && n >= 1900 && n <= 2099
}

// wordOf is the word of a plain figure: the word token that follows num in rest.
func wordOf(num, rest string) string {
	if isYear(num) {
		return ""
	}
	m := wordAfterRE.FindStringSubmatch(rest)
	if m == nil || functionWords[m[1]] {
		return ""
	}
	return foldPlural(m[1])
}

// foldPlural writes a plural noun as its singular, so "years" and "year" match.
func foldPlural(w string) string {
	n := len(w)
	switch {
	case n > 4 && strings.HasSuffix(w, "ies"):
		return w[:n-3] + "y"
	case n > 4 && (strings.HasSuffix(w, "sses") || strings.HasSuffix(w, "xes") || strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")):
		return w[:n-2]
	case n >= 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:n-1]
	}
	return w
}

// figures reads every figure in s, in order.
func figures(s string) []figure {
	s = joinGroups(foldRE.Replace(strings.ToLower(s)))
	var out []figure
	for _, loc := range numRE.FindAllStringIndex(s, -1) {
		f := figure{num: s[loc[0]:loc[1]]}
		rest := s[loc[1]:]
		var version bool
		f.tag, version = tagOf(s[:loc[0]], rest)
		switch {
		case percentRE.MatchString(rest):
			f.mark = "%"
		case strings.HasPrefix(rest, "x"):
			// A multiple, not the start of a word or of a product like 2x4.
			if after := []rune(rest[1:]); len(after) == 0 || !(unicode.IsLetter(after[0]) || unicode.IsDigit(after[0])) {
				f.mark = "x"
			}
		}
		if f.mark == "" && f.tag == "" && !version {
			f.word = wordOf(f.num, rest)
		}
		out = append(out, f)
	}
	return out
}

// grounding is the set of figures the model was shown.
type grounding struct {
	tagged map[string]bool // tag + "\x00" + number: "k\x008"
	marked map[string]bool // "42%", "3.1x"
	words  map[string]bool // number + "\x00" + word: "7\x00year"
	bare   map[string]bool // every untagged number, marked or not
}

func newGrounding(sources ...string) grounding {
	g := grounding{tagged: map[string]bool{}, marked: map[string]bool{}, words: map[string]bool{}, bare: map[string]bool{}}
	for _, s := range sources {
		for _, f := range figures(s) {
			switch {
			case f.tag != "":
				g.tagged[f.tag+"\x00"+f.num] = true
				continue
			case f.mark != "":
				g.marked[f.String()] = true
			case f.word != "":
				g.words[f.num+"\x00"+f.word] = true
			}
			g.bare[f.num] = true
		}
	}
	return g
}

func (g grounding) has(f figure) bool {
	switch {
	case f.tag != "":
		return g.tagged[f.tag+"\x00"+f.num]
	case f.mark != "":
		return g.marked[f.String()]
	case f.word != "":
		return g.words[f.num+"\x00"+f.word]
	}
	return g.bare[f.num]
}

// maxFigureRunes bounds the figure quoted in the refusal message.
const maxFigureRunes = 40

// ungrounded returns the first figure of text that no source holds, and how many
// distinct ones there are.
func (g grounding) ungrounded(text string) (first figure, n int) {
	seen := map[figure]bool{}
	for _, f := range figures(text) {
		if seen[f] || g.has(f) {
			continue
		}
		seen[f] = true
		if n == 0 {
			first = f
		}
		n++
	}
	return first, n
}
