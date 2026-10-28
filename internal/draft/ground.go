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
// the draft (a number, spelled or in digits, a percentage, a multiple such as
// 3.1x, a version) must occur in something the model was shown: the evidence, the
// posting excerpt, the question, the company and title, the target summary and,
// for a motivation question, the seeker's notes (years of experience in the seeker's
// own text only; see below). A figure that occurs in none of them is a
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
//     hyphen, past a "+" ("5+ years"), lowercased, with a plural folded ("years" is
//     "year", "ies" is "y") and "yr" read as "year". It
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
//   - Numbers written as words are the same figures as their digits ("five years"
//     is "5 years", in the draft and in a source alike), read by spelledNumbers
//     before anything else (ground_numwords.go lists what is and is not read).
//
// Currency symbols are not part of a figure: "$10 million" is the number 10 with
// the word "million".
//
// Two more kinds of claim are held to the same rule, in ground_claims.go: a
// credential (a degree, a licence, a certification, from a closed list) and an
// acronym (2 to 6 capital letters, less a short generic allowlist). Each is
// grounded only by the same credential or the same acronym in a source, and the
// refusal says which kind it was.
//
// Whose text it is matters for the claims that are about the seeker. The sources
// are two lists (newGrounding): what the seeker owns (the evidence, the target
// summary and, for a motivation question, their notes) and what the model was only
// shown (the posting excerpt, the question, the company, the title). A credential,
// and a figure whose word is "year" (years of experience, the dominant claim a
// draft makes about the seeker), are grounded only by the seeker's own text: a
// posting that says "PhD preferred" or "5 years of experience" describes the job,
// and a question "Do you hold a PhD?" asks. Every other figure and every acronym is
// grounded by both lists, since a draft is asked to echo the posting's own counts
// and tools. Negation is not parsed, so "I do not have a PhD" is refused like "I
// have a PhD" when the seeker's own text has none. The target summary is the
// seeker's, though it names roles they want: "PhD-level roles" in it grounds a
// draft's PhD, a residual that is kept.

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
	// after spaces or one hyphen, past a "+" ("5+ years"). A newline never joins a
	// number to the next line.
	wordAfterRE = regexp.MustCompile(`^\+?(?:[ \x{a0}\x{202f}]+|-)?(\p{L}+)`)
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
	w := foldPlural(m[1])
	if w == "yr" {
		return yearWord
	}
	return w
}

// yearWord is the word of "5 years", "5 yrs" and "5-year": the count that says how
// long the seeker has done something, which only the seeker's own text can ground.
const yearWord = "year"

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
	s = spelledNumbers(joinGroups(foldRE.Replace(strings.ToLower(s))))
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

// grounding is the set of claims the model was shown, by whose text they came from.
type grounding struct {
	tagged map[string]bool // tag + "\x00" + number: "k\x008"
	marked map[string]bool // "42%", "3.1x"
	words  map[string]bool // number + "\x00" + word: "7\x00year"; a year count only from the seeker
	bare   map[string]bool // every untagged number, marked or not
	creds  map[string]bool // credential keys (ground_claims.go): "phd", "cert:aws"; only the seeker's
	toks   map[string]bool // every lowercased word, for acronyms
}

// newGrounding indexes the text behind a draft. seeker is text the seeker owns:
// the evidence, their target summary and, for a motivation question, their notes.
// shown is text the model saw that describes the job, not the seeker: the posting
// excerpt, the question, the company and the title. A claim about the seeker (a
// credential, a count of years) is grounded only by seeker text, because "PhD
// preferred" and "5 years of experience" in a posting say what the employer wants.
// Every other figure and every acronym is grounded by both, because a draft may
// echo the posting's own numbers and tools ("a team of 40", "24/7", "SOC2", "AWS").
func newGrounding(seeker, shown []string) grounding {
	g := grounding{tagged: map[string]bool{}, marked: map[string]bool{}, words: map[string]bool{}, bare: map[string]bool{},
		creds: map[string]bool{}, toks: map[string]bool{}}
	for _, s := range seeker {
		g.add(s, true)
	}
	for _, s := range shown {
		g.add(s, false)
	}
	return g
}

// add indexes one source; own says the seeker wrote it.
func (g grounding) add(s string, own bool) {
	if own {
		for _, c := range credentialsIn(s) {
			g.creds[c.key] = true
		}
	}
	for _, w := range wordsIn(s) {
		g.toks[w] = true
	}
	for _, f := range figures(s) {
		switch {
		case f.tag != "":
			g.tagged[f.tag+"\x00"+f.num] = true
			continue
		case f.mark != "":
			g.marked[f.String()] = true
		case f.word != "":
			if own || f.word != yearWord {
				g.words[f.num+"\x00"+f.word] = true
			}
		}
		g.bare[f.num] = true
	}
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

// claim is one thing a draft says that no source backs: a figure, a credential or
// an acronym. text is what the refusal quotes; what is its kind with an article;
// seekerOnly marks a claim about the seeker, which only their own text could back.
type claim struct {
	text, what string
	seekerOnly bool
}

func (c claim) String() string { return c.text }

const (
	whatFigure     = "a figure"
	whatCredential = "a credential"
	whatAcronym    = "an acronym"
)

// ungrounded returns the first claim of text that no source holds, and how many
// distinct ones there are. Figures come first, then credentials, then acronyms,
// each in the order the draft says them.
func (g grounding) ungrounded(text string) (first claim, n int) {
	note := func(c claim) {
		if n == 0 {
			first = c
		}
		n++
	}
	seen := map[figure]bool{}
	for _, f := range figures(text) {
		if !seen[f] && !g.has(f) {
			seen[f] = true
			if f.word == yearWord {
				quoted := f.num + " " + yearWord
				if f.num != "1" {
					quoted += "s"
				}
				note(claim{quoted, whatFigure, true})
			} else {
				note(claim{f.String(), whatFigure, false})
			}
		}
	}
	for _, c := range credentialsIn(text) {
		if !g.creds[c.key] {
			note(claim{c.surface, whatCredential, true})
		}
	}
	for _, a := range acronymsIn(text) {
		if !g.toks[strings.ToLower(a)] {
			note(claim{a, whatAcronym, false})
		}
	}
	return first, n
}
