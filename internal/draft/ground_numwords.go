package draft

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Spelled numbers, for the figure reader (ground.go). figures rewrites them as
// digits before it reads anything, so "five years" is the figure "5 years" and
// grounds, and is grounded by, "5 years" with no second code path: the tag,
// mark, word and bare rules apply to a spelled number exactly as to a digit one.
//
// Read: one to nineteen, the tens (twenty to ninety), compounds with a space or
// one hyphen ("twenty-five", "twenty five"), "hundred" and "thousand" with what
// they multiply ("two hundred and fifty", "five thousand"), "a hundred" and "a
// thousand", "a dozen" (12), "half a dozen" (6) and "a couple of" (2).
//
// Not read, each on purpose:
//   - "dozens", "hundreds", "several", "a few", "half", "twice": vague, and they
//     name no number a source could hold. "two dozen" is "2 dozen", as "2
//     million" is already (the unit is the word).
//   - Word ordinals ("first", "second", "third-party"): the digit reader takes
//     "99th" as the bare figure 99, but "the first step", "at first" and "a second
//     language" outnumber every counting use of the words, and each would need a
//     bare "1" or "2" in a source to pass.
//   - "zero", "million", "billion": "zero downtime" is not a count, and a million
//     is read as the word after its number, as for digits.
//
// "one" is the one number word that is also a pronoun and a determiner ("one of
// the", "no one", "one another", "one-on-one", "at one point"). It is a figure
// only when a counted noun follows ("one year") and neither the word before nor
// the noun is one of the idioms below; a bare "one" is never read. Every other
// number word with no counted noun is a bare figure, as a bare digit is ("a team
// of five" needs a 5 in a source).

var spelledValues = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
	"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
	"seventeen": 17, "eighteen": 18, "nineteen": 19,
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
}

// oneBefore are the words that make a following "one" a pronoun ("no one",
// "which one"); oneNouns are the nouns that make "one" a figure of speech
// ("one thing", "one day", "in one go").
var (
	oneBefore = map[string]bool{}
	oneNouns  = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields(`no any every each some the this that which another what whose none`) {
		oneBefore[w] = true
	}
	for _, w := range strings.Fields(`thing things way ways day days time times example reason point moment instance
		case kind sort part place hand side go step more less another other such`) {
		oneNouns[w] = true
	}
}

// maxSpelledChain bounds the words one spelled number may take.
const maxSpelledChain = 12

var spelledRunRE = regexp.MustCompile(`\p{L}+`)

// spelledSep reports whether sep is exactly one space or one hyphen, the only
// joiners a spelled number reads across.
func spelledSep(sep string) bool {
	return sep == " " || sep == "-" || sep == " " || sep == " "
}

// spelledNumbers rewrites the spelled numbers of s (already lowercased) as ASCII
// digits and leaves the rest as it was. Full-width digits stay distinct
// characters, as everywhere in the figure reader.
func spelledNumbers(s string) string {
	runs := spelledRunRE.FindAllStringIndex(s, -1)
	var b strings.Builder
	last := 0
	for i := 0; i < len(runs); {
		to, val, next := scanSpelled(s, runs, i)
		if next == i {
			i++
			continue
		}
		b.WriteString(s[last:runs[i][0]])
		b.WriteString(strconv.Itoa(val))
		last, i = to, next
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// scanSpelled reads a spelled number that starts at run i. It returns the byte
// offset where it ends, its value and the index of the next unread run; next == i
// means nothing was read.
func scanSpelled(s string, runs [][]int, i int) (to, val, next int) {
	if !isolatedRun(s, runs[i]) {
		return 0, 0, i
	}
	words := chain(s, runs, i)
	n, v := 0, 0
	switch w := words[0]; {
	case w == "half" && len(words) >= 3 && words[1] == "a" && words[2] == "dozen":
		n, v = 3, 6
	case w == "a" && len(words) >= 2 && words[1] == "dozen":
		n, v = 2, 12
	case w == "a" && len(words) >= 2 && words[1] == "couple":
		n, v = 2, 2
		if len(words) >= 3 && words[2] == "of" {
			n = 3
		}
	case w == "a" && len(words) >= 2 && (words[1] == "hundred" || words[1] == "thousand"):
		n, v = parseSpelled(words[1:], true)
		n++
	default:
		n, v = parseSpelled(words, false)
	}
	if n == 0 || (n == 1 && words[0] == "one" && !oneIsFigure(s, runs, i)) {
		return 0, 0, i
	}
	return runs[i+n-1][1], v, i + n
}

// touches reports whether c, next to letters, makes them part of something else:
// a digit ("k8s", "five2") or an apostrophe or underscore ("one's", "five_x").
func touches(c rune) bool {
	return c == '\'' || c == '_' || c == '’' || unicode.IsDigit(c)
}

// isolatedRun reports whether the letters of run r stand alone.
func isolatedRun(s string, r []int) bool {
	if r[0] > 0 {
		if c, _ := utf8.DecodeLastRuneInString(s[:r[0]]); touches(c) {
			return false
		}
	}
	if r[1] < len(s) {
		if c, _ := utf8.DecodeRuneInString(s[r[1]:]); touches(c) {
			return false
		}
	}
	return true
}

// chain collects the words from run i on that one separator apiece joins, at
// most maxSpelledChain of them.
func chain(s string, runs [][]int, i int) []string {
	words := []string{s[runs[i][0]:runs[i][1]]}
	for j := i + 1; j < len(runs) && len(words) < maxSpelledChain; j++ {
		if !spelledSep(s[runs[j-1][1]:runs[j][0]]) || !isolatedRun(s, runs[j]) {
			break
		}
		words = append(words, s[runs[j][0]:runs[j][1]])
	}
	return words
}

// parseSpelled reads the longest spelled number at the head of words: how many
// words it took and its value, or 0, 0. bareMultiplier lets "hundred" and
// "thousand" stand without a number before them (after "a").
func parseSpelled(words []string, bareMultiplier bool) (n, val int) {
	const (
		fresh = iota // nothing in the current group
		tens         // a tens word, which a unit may follow
		unit         // a unit or a teen
	)
	total, cur, state := 0, 0, fresh
	hundredUsed, thousandUsed, started := false, false, false
	for i := 0; i < len(words); i++ {
		w := words[i]
		v, isNum := spelledValues[w]
		switch {
		case isNum && v < 10 && state != unit:
			cur += v
			state = unit
		case isNum && v >= 10 && v < 20 && state == fresh:
			cur += v
			state = unit
		case isNum && v >= 20 && state == fresh:
			cur += v
			state = tens
		case w == "hundred" && !hundredUsed && ((state == unit && cur >= 1 && cur <= 9) || (!started && bareMultiplier)):
			if cur == 0 {
				cur = 1
			}
			cur *= 100
			hundredUsed, state = true, fresh
		case w == "thousand" && !thousandUsed && ((started && cur > 0) || (!started && bareMultiplier)):
			if cur == 0 {
				cur = 1
			}
			total, cur = cur*1000, 0
			thousandUsed, hundredUsed, state = true, false, fresh
		case w == "and" && started && (hundredUsed || thousandUsed) && state == fresh && i+1 < len(words) && spelledValues[words[i+1]] > 0:
			// "and" is part of the number only when a number follows it.
			continue
		default:
			return n, total + cur
		}
		started = true
		n = i + 1
	}
	return n, total + cur
}

// oneIsFigure decides whether the "one" at run i counts something: a counted
// noun follows, and neither it nor the word before makes "one" an idiom.
func oneIsFigure(s string, runs [][]int, i int) bool {
	if i > 0 {
		sep, prev := s[runs[i-1][1]:runs[i][0]], s[runs[i-1][0]:runs[i-1][1]]
		// "one-on-one", "one-to-one": a function word and a hyphen before it.
		if (spelledSep(sep) && oneBefore[prev]) || (sep == "-" && functionWords[prev]) {
			return false
		}
	}
	m := wordAfterRE.FindStringSubmatch(s[runs[i][1]:])
	return m != nil && !functionWords[m[1]] && !oneNouns[m[1]]
}
