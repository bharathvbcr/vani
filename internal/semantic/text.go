package semantic

import (
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Chunk is a piece of text with the weight it carries in an aggregate. For a
// profile chunk Label is a role title or "skills"; for a posting chunk it is
// "title" or "line".
type Chunk struct {
	Label  string
	Text   string
	Weight float64
}

var (
	scriptRe  = regexp.MustCompile(`(?is)<script\b.*?</script\s*>`)
	styleRe   = regexp.MustCompile(`(?is)<style\b.*?</style\s*>`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	blockRe   = regexp.MustCompile(`(?i)</?(?:p|div|br|li|ul|ol|h[1-6]|tr|td|table|section|article|header|footer|blockquote|hr)\b[^>]*>`)
	tagRe     = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	lineMark  = regexp.MustCompile(`^\s*(?:[-*+•·▪◦–—]|\d{1,2}[.)]|#{1,6})\s+`)
	sentRe    = regexp.MustCompile(`[.!?]\s+`)
	urlRe     = regexp.MustCompile(`https?://\S+|www\.\S+`)
	emailRe   = regexp.MustCompile(`\S+@\S+\.\S+`)
	claimRe   = regexp.MustCompile(`\{\{[^}]*\}\}`)
)

// truncBytes cuts s to at most n bytes on a rune boundary.
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// truncRunes cuts s to at most n runes.
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// tidy makes s valid UTF-8 on one line: control characters become spaces and
// runs of white space collapse.
func tidy(s string) string {
	s = strings.ToValidUTF8(s, " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// stripHTML turns an HTML (or HTML-escaped HTML, as Greenhouse serves it)
// description into text with one block per line. Every pass is linear.
func stripHTML(s string) string {
	if strings.Contains(s, "&lt;") || strings.Contains(s, "&#60;") {
		s = html.UnescapeString(s)
	}
	s = commentRe.ReplaceAllString(s, " ")
	s = scriptRe.ReplaceAllString(s, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = blockRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, " ")
	return html.UnescapeString(s)
}

// Section tiers: lower is preferred.
const (
	tierPreferred = iota
	tierNeutral
	tierLow
)

var (
	preferredWords = []string{"requirement", "qualification", "responsibilit", "you will", "you'll", "what you", "you have", "you bring", "about you", "must have", "looking for", "skills", "experience", "the role", "your role", "day to day", "what we need", "nice to have"}
	lowWords       = []string{"benefit", "perks", "compensation", "salary", "equal opportunity", "eeo", "about us", "who we are", "our company", "why join", "how to apply", "privacy", "accommodat"}
)

func sectionTier(heading string) int {
	h := strings.ToLower(heading)
	for _, w := range lowWords {
		if strings.Contains(h, w) {
			return tierLow
		}
	}
	for _, w := range preferredWords {
		if strings.Contains(h, w) {
			return tierPreferred
		}
	}
	return tierNeutral
}

func knownHeading(h string) bool {
	h = strings.ToLower(h)
	for _, w := range preferredWords {
		if strings.Contains(h, w) {
			return true
		}
	}
	for _, w := range lowWords {
		if strings.Contains(h, w) {
			return true
		}
	}
	return false
}

func wordCount(s string) int { return len(strings.Fields(s)) }

type unit struct {
	text string
	tier int
	pos  int
}

// ChunkPosting cuts a posting into at most MaxPostingChunks chunks of at most
// MaxPostingChunkBytes: the title first (weight 2), then sentence and bullet
// lines of four or more words. Lines under requirements, qualifications or
// responsibilities headings are taken before the rest, and lines under
// benefits, EEO or company-blurb headings last; chosen lines keep document
// order. Lines that ask the same of any candidate (a degree, years of
// experience, communication skills: genericLine) are left out. HTML and
// markdown are stripped. The result is deterministic and depends only on the
// two arguments.
func ChunkPosting(title, description string) []Chunk {
	var out []Chunk
	seen := map[string]bool{}
	if t := tidy(kbsync.PlainText(stripHTML(title))); t != "" {
		t = truncBytes(t, MaxPostingChunkBytes)
		out = append(out, Chunk{Label: "title", Text: t, Weight: titleWeight})
		seen[strings.ToLower(t)] = true
	}

	desc := truncBytes(strings.ToValidUTF8(description, " "), MaxDescriptionBytes)
	desc = strings.ReplaceAll(strings.ReplaceAll(stripHTML(desc), "\r\n", "\n"), "\r", "\n")

	const maxUnits = 4000
	var units []unit
	tier := tierNeutral
	for _, raw := range strings.Split(desc, "\n") {
		if len(units) >= maxUnits {
			break
		}
		md := strings.HasPrefix(strings.TrimSpace(raw), "#")
		line := tidy(kbsync.PlainText(lineMark.ReplaceAllString(strings.TrimSpace(raw), "")))
		if line == "" {
			continue
		}
		words := wordCount(line)
		if md || (words <= 8 && strings.HasSuffix(line, ":")) || (words <= 3 && knownHeading(line) && !strings.ContainsAny(line, ".,;")) {
			tier = sectionTier(line)
			continue
		}
		// "Requirements: 5 years of Go": a heading with its first line attached.
		if i := strings.IndexByte(line, ':'); i > 0 && wordCount(line[:i]) <= 4 && knownHeading(line[:i]) {
			tier = sectionTier(line[:i])
			line = strings.TrimSpace(line[i+1:])
		}
		for _, s := range splitSentences(line) {
			if len(units) >= maxUnits {
				break
			}
			if wordCount(s) < 4 || genericLine(s) {
				continue
			}
			t := tier
			if low := strings.ToLower(s); strings.Contains(low, "equal opportunity") || strings.Contains(low, "does not discriminate") {
				t = tierLow
			}
			units = append(units, unit{text: truncBytes(s, MaxPostingChunkBytes), tier: t, pos: len(units)})
		}
	}

	// Drop duplicates, keeping the first, then take the best tiers.
	uniq := units[:0:0]
	for _, u := range units {
		k := strings.ToLower(u.text)
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, u)
	}
	room := MaxPostingChunks - len(out)
	if len(uniq) > room {
		sort.SliceStable(uniq, func(i, j int) bool { return uniq[i].tier < uniq[j].tier })
		uniq = uniq[:room]
		sort.SliceStable(uniq, func(i, j int) bool { return uniq[i].pos < uniq[j].pos })
	}
	for _, u := range uniq {
		out = append(out, Chunk{Label: "line", Text: u.text, Weight: 1})
	}
	return out
}

// splitSentences cuts a line after sentence-ending punctuation that is
// followed by white space ("Node.js" stays whole).
func splitSentences(line string) []string {
	var out []string
	last := 0
	for _, loc := range sentRe.FindAllStringIndex(line, -1) {
		out = append(out, strings.TrimSpace(line[last:loc[0]+1]))
		last = loc[1]
	}
	return append(out, strings.TrimSpace(line[last:]))
}

// phoneMaxSep is how many separator characters may sit in a row between digit
// groups of one phone-shaped run. A fourth ends the run, so a space, four
// dashes and a space ("555 ---- 4567") is not one number.
const phoneMaxSep = 3

// isPhoneSep reports a character that may join digit groups: a space, a dot,
// a hyphen, an en dash, or a parenthesis.
func isPhoneSep(r rune) bool {
	switch r {
	case '.', '-', '\u2013', '(', ')':
		return true
	default:
		return unicode.Is(unicode.Zs, r)
	}
}

// asciiYear reports four ASCII digits that read as a year from 1900 through 2099.
func asciiYear(rs []rune) bool {
	if len(rs) != 4 {
		return false
	}
	n := 0
	for _, r := range rs {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
	}
	return n >= 1900 && n <= 2099
}

// phoneYearSpan is the one run that is not a phone: exactly two groups, joined
// only by "-" or "–" with no spaces, both four-digit years from 1900 through 2099.
func phoneYearSpan(groups [][]rune, seps []string) bool {
	if len(groups) != 2 || len(seps) != 1 {
		return false
	}
	if seps[0] != "-" && seps[0] != "\u2013" {
		return false
	}
	return asciiYear(groups[0]) && asciiYear(groups[1])
}

// stripPhoneRuns replaces a phone-shaped run with a space. A run is seven or
// more Unicode decimal digits in one group, or in groups joined by at most
// phoneMaxSep separators. Circled digits and superscripts are not decimal
// digits. The year span phoneYearSpan lets through is left as written.
func stripPhoneRuns(s string) string {
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); {
		if !unicode.IsDigit(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		start := i
		var groups [][]rune
		var seps []string
		digits := 0
		for {
			j := i
			for j < len(rs) && unicode.IsDigit(rs[j]) {
				j++
			}
			groups = append(groups, rs[i:j])
			digits += j - i
			i = j
			k := i
			for k < len(rs) && k-i < phoneMaxSep && isPhoneSep(rs[k]) {
				k++
			}
			if k == i || k >= len(rs) || !unicode.IsDigit(rs[k]) {
				break
			}
			seps = append(seps, string(rs[i:k]))
			i = k
		}
		if phoneYearSpan(groups, seps) || digits < 7 {
			b.WriteString(string(rs[start:i]))
			continue
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// cleanBulletText makes bank text safe and meaningful to embed: markdown
// removed, claim tokens ({{clmX}}, whose values live in rkb and are numbers
// that carry no meaning to an embedder) dropped, URLs, e-mail addresses,
// phone-shaped runs and work-authorization phrases removed, one line. It does
// not scrub names, so an organisation named in the bullet stays.
func cleanBulletText(s string) string {
	s = claimRe.ReplaceAllString(s, " ")
	s = kbsync.PlainText(s)
	s = urlRe.ReplaceAllString(s, " ")
	s = emailRe.ReplaceAllString(s, " ")
	s = stripPhoneRuns(s)
	s = authorizationRe.ReplaceAllString(s, " ")
	return tidy(s)
}
