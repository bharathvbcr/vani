package jobinsight

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

//go:embed vocab.txt
var vocabText string

// Bounds on what one Extract call reads. Posting descriptions are already cut
// at 64 KiB by the sources (srcutil.MaxDescription); these hold even for a
// caller that passes something larger.
const (
	MaxScanBytes = 128 << 10
	MaxTokens    = 40_000
	maxSeqLen    = 4 // longest term, in tokens
	maxTermBytes = 64
	maxVocab     = 2000
)

// Vocab is a parsed term list (see vocab.txt for the format).
type Vocab struct {
	terms   []term
	byFirst map[string][]pattern // first token (lowercase) -> patterns, longest first
}

type term struct {
	name, category string
}

type pattern struct {
	toks  []string // lowercase tokens
	orig  []string // as written, for case-sensitive patterns
	exact bool     // case-sensitive, never sentence-initial
	term  int
}

// Hit is one vocabulary term a text mentions and how often.
type Hit struct {
	Term     string
	Category string
	Count    int
}

// Keyword is a hit as `vani jobs` shows it. Target means the target-roles
// summary names the term too; Gap means DevPrism listed it as missing or weak
// in the knowledge base.
type Keyword struct {
	Term     string `json:"term"`
	Category string `json:"cat"`
	Count    int    `json:"n"`
	Target   bool   `json:"target,omitempty"`
	Gap      bool   `json:"gap,omitempty"`
}

var defaultVocab = sync.OnceValues(func() (*Vocab, error) { return ParseVocab(vocabText) })

// Default is the embedded vocabulary. Its parse is covered by a test, so an
// error here means a broken build.
func Default() (*Vocab, error) { return defaultVocab() }

// ParseVocab reads the vocab.txt format. Duplicate spellings across terms are
// refused: a token sequence must name one term.
func ParseVocab(text string) (*Vocab, error) {
	v := &Vocab{byFirst: map[string][]pattern{}}
	seen := map[string]string{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cat, rest, ok := strings.Cut(line, " ")
		if !ok || cat == "" || strings.TrimSpace(rest) == "" {
			return nil, fmt.Errorf("jobinsight: vocab line %d: want <category> <term>[|alias...]", n+1)
		}
		if len(v.terms) >= maxVocab {
			return nil, fmt.Errorf("jobinsight: vocab has more than %d terms", maxVocab)
		}
		spellings := strings.Split(strings.TrimSpace(rest), "|")
		name := strings.TrimSpace(spellings[0])
		exact := strings.HasPrefix(name, "=")
		name = strings.TrimPrefix(name, "=")
		idx := len(v.terms)
		v.terms = append(v.terms, term{name: name, category: cat})
		for i, s := range spellings {
			s = strings.TrimSpace(strings.TrimPrefix(s, "="))
			if s == "" || len(s) > maxTermBytes {
				return nil, fmt.Errorf("jobinsight: vocab line %d: empty or over-long spelling", n+1)
			}
			toks := tokenize(s, MaxScanBytes, MaxTokens)
			if len(toks) == 0 || len(toks) > maxSeqLen {
				return nil, fmt.Errorf("jobinsight: vocab line %d: %q is not 1-%d tokens", n+1, s, maxSeqLen)
			}
			p := pattern{exact: exact && i == 0, term: idx}
			for _, t := range toks {
				p.toks = append(p.toks, t.lower)
				p.orig = append(p.orig, t.orig)
			}
			k := strings.Join(p.toks, " ")
			if p.exact {
				k = "=" + strings.Join(p.orig, " ")
			}
			if prev, dup := seen[k]; dup {
				return nil, fmt.Errorf("jobinsight: vocab line %d: %q already spells %q", n+1, s, prev)
			}
			seen[k] = name
			v.byFirst[p.toks[0]] = append(v.byFirst[p.toks[0]], p)
		}
	}
	if len(v.terms) == 0 {
		return nil, errors.New("jobinsight: vocab is empty")
	}
	for k, ps := range v.byFirst {
		sort.SliceStable(ps, func(i, j int) bool { return len(ps[i].toks) > len(ps[j].toks) })
		v.byFirst[k] = ps
	}
	return v, nil
}

// stopCategory marks stop phrases in vocab.txt.
const stopCategory = "!"

// Len is how many terms the vocabulary has, stop phrases aside.
func (v *Vocab) Len() int {
	n := 0
	for _, t := range v.terms {
		if t.category != stopCategory {
			n++
		}
	}
	return n
}

// Extract returns the terms text mentions, most mentioned first (ties by
// term). It reads at most MaxScanBytes and MaxTokens of text. The longest
// spelling wins at each position, and a match consumes its tokens; a stop
// phrase consumes them and counts nothing.
func (v *Vocab) Extract(text string) []Hit {
	toks := tokenize(text, MaxScanBytes, MaxTokens)
	counts := map[int]int{}
	for i := 0; i < len(toks); {
		n, idx := v.matchAt(toks, i)
		if n == 0 {
			i++
			continue
		}
		if v.terms[idx].category != stopCategory {
			counts[idx]++
		}
		i += n
	}
	hits := make([]Hit, 0, len(counts))
	for idx, c := range counts {
		hits = append(hits, Hit{Term: v.terms[idx].name, Category: v.terms[idx].category, Count: c})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Count != hits[j].Count {
			return hits[i].Count > hits[j].Count
		}
		return hits[i].Term < hits[j].Term
	})
	return hits
}

// Terms is the set of term names text mentions.
func (v *Vocab) Terms(text string) map[string]bool {
	out := map[string]bool{}
	for _, h := range v.Extract(text) {
		out[h.Term] = true
	}
	return out
}

func (v *Vocab) matchAt(toks []token, i int) (int, int) {
next:
	for _, p := range v.byFirst[toks[i].lower] {
		if i+len(p.toks) > len(toks) {
			continue
		}
		if p.exact && toks[i].sentenceStart {
			continue
		}
		for k := range p.toks {
			if p.exact {
				if toks[i+k].orig != p.orig[k] {
					continue next
				}
			} else if toks[i+k].lower != p.toks[k] {
				continue next
			}
		}
		return len(p.toks), p.term
	}
	return 0, 0
}

// Annotate turns hits into keywords, marking those the target summary names
// and those DevPrism found missing. Target terms come first, then gaps, then
// the rest, each most mentioned first; at most limit are kept.
func Annotate(hits []Hit, target, gap map[string]bool, limit int) []Keyword {
	out := make([]Keyword, 0, min(len(hits), max(limit, 0)))
	for _, h := range hits {
		out = append(out, Keyword{Term: h.Term, Category: h.Category, Count: h.Count, Target: target[h.Term], Gap: gap[h.Term]})
	}
	rank := func(k Keyword) int {
		switch {
		case k.Target:
			return 0
		case k.Gap:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return false // Extract's order: count, then term
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Boilerplate thresholds: a term in at least BoilerplateShare of a company's
// postings, for a company with at least BoilerplateMin postings, is part of
// that company's standard blurb ("… analytics and agents. Headquartered …"),
// not something one posting asks for.
const (
	BoilerplateShare = 0.9
	BoilerplateMin   = 8
)

// Boilerplate returns, per company, the terms that are its blurb. company[i]
// names the company of the posting whose hits are hits[i]; companies are
// compared case-insensitively.
func Boilerplate(company []string, hits [][]Hit) map[string]map[string]bool {
	postings := map[string]int{}
	df := map[string]map[string]int{}
	for i := range min(len(company), len(hits)) {
		c := strings.ToLower(strings.TrimSpace(company[i]))
		postings[c]++
		if df[c] == nil {
			df[c] = map[string]int{}
		}
		for _, h := range hits[i] {
			df[c][h.Term]++
		}
	}
	out := map[string]map[string]bool{}
	for c, n := range postings {
		if n < BoilerplateMin {
			continue
		}
		for t, k := range df[c] {
			if float64(k) >= BoilerplateShare*float64(n) {
				if out[c] == nil {
					out[c] = map[string]bool{}
				}
				out[c][t] = true
			}
		}
	}
	return out
}

// Without returns hits minus the terms in drop. It never modifies hits.
func Without(hits []Hit, drop map[string]bool) []Hit {
	if len(drop) == 0 {
		return hits
	}
	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		if !drop[h.Term] {
			out = append(out, h)
		}
	}
	return out
}

// TargetHits counts the hits the target summary names.
func TargetHits(hits []Hit, target map[string]bool) int {
	n := 0
	for _, h := range hits {
		if target[h.Term] {
			n++
		}
	}
	return n
}

type token struct {
	lower, orig   string
	sentenceStart bool
}

// tokenize splits text into word tokens: runs of letters and digits that may
// carry + # . - & _ inside (C++, C#, node.js, scikit-learn, R&D). Trailing dots
// and dashes are sentence punctuation and are dropped; a single leading dot is
// kept after a space (".NET"). sentenceStart marks a token that opens the text,
// a line or a sentence.
func tokenize(text string, maxBytes, maxTokens int) []token {
	if len(text) > maxBytes {
		// Cutting may split the last rune; dropping invalid bytes removes it.
		text = strings.ToValidUTF8(text[:maxBytes], "")
	}
	var out []token
	var cur strings.Builder
	start := true      // next token opens a sentence
	afterSpace := true // the rune before the current token was a space (or the text start)
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		s := cur.String()
		cur.Reset()
		lead := strings.HasPrefix(s, ".") && !strings.HasPrefix(s, "..")
		s = strings.TrimRight(strings.TrimLeft(s, ".-&_+#"), ".-&_")
		if s == "" {
			return
		}
		if lead && afterSpace && isWordy(s) {
			s = "." + s
		}
		out = append(out, token{lower: strings.ToLower(s), orig: s, sentenceStart: start})
		start = false
	}
	prevSpace := true
	for _, r := range text {
		if len(out) >= maxTokens {
			return out
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("+#.-&_", r):
			if cur.Len() == 0 {
				afterSpace = prevSpace
			}
			cur.WriteRune(r)
			prevSpace = false
			continue
		}
		// A token ending in sentence punctuation (handled before the flush
		// trims it) opens the next sentence.
		ends := strings.HasSuffix(cur.String(), ".")
		flush()
		if ends || strings.ContainsRune("\n!?:;•·", r) {
			start = true
		}
		prevSpace = unicode.IsSpace(r) || r == '(' || r == '"' || r == '“' || r == '/'
	}
	if len(out) < maxTokens {
		flush()
	}
	return out
}

func isWordy(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
