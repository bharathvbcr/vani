// Package samecompany answers two display-only questions that need to know
// which postings belong to one company: "have I already applied to this company
// lately?" (RepeatApply) and "is this posting the same role relisted under a
// new key?" (Repost). Both are annotations for a person to read (CLAUDE.md
// Rule 7): nothing here drops, blocks or writes anything, and no engine rule
// reads the answers.
//
// Vani has no company registry. A posting carries a company name, and the
// watchlist can give a board a Domain, which the logo cache already treats as
// "boards that share a domain are one company" (cmd/vani logos.go). Company
// identity here is the same two facts: a board with a Domain is that domain,
// a name that exactly one watchlist domain claims is that domain, and every
// other name is its folded text. Two boards of one company (a Greenhouse and
// an Ashby board) are therefore one company when the owner gave them one
// Domain or the boards report one name; with neither, they are not known to be
// one, and the check stays quiet rather than guess.
package samecompany

import (
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Companies resolves a posting to a company identity from the watchlist's
// domains. A nil *Companies knows no domains: every posting is identified by its
// name alone.
type Companies struct {
	byPrefix map[string]string // board key prefix -> domain
	byName   map[string]string // folded company name -> domain; "" when two domains claim it
}

// NewCompanies reads the boards' domains. Boards without a Domain add nothing.
func NewCompanies(boards []sources.Board) *Companies {
	c := &Companies{byPrefix: map[string]string{}, byName: map[string]string{}}
	for _, b := range boards {
		dom := strings.ToLower(strings.TrimSpace(b.Domain))
		if dom == "" {
			continue
		}
		c.byPrefix[b.KeyPrefix()] = dom
		name := Name(b.Company)
		if name == "" {
			continue
		}
		if d, seen := c.byName[name]; seen && d != dom {
			c.byName[name] = ""
		} else {
			c.byName[name] = dom
		}
	}
	return c
}

func (c *Companies) domainOfBoard(k posting.Key) string {
	if c == nil {
		return ""
	}
	return c.byPrefix[jobinsight.BoardOf(string(k))+":"]
}

func (c *Companies) domainOfName(name string) string {
	if c == nil {
		return ""
	}
	return c.byName[name]
}

// Name is a company name folded for comparison: case, punctuation, legal
// suffixes ("Inc.", "LLC", "Corporation"; pipeline.LooseCompany), accents and
// full-width letters. It is "" for a name with no letter or digit in it, which
// matches nothing, not even another empty name.
func Name(s string) string { return pipeline.LooseCompany(foldRunes(s)) }

// accentGroups lists the Latin letters whose accented forms fold to them. The
// standard library has no Unicode normalisation, and textsim.Words (which
// LooseCompany reads through) would cut a word at a combining mark, so a
// decomposed "Zoe" + U+0308 and a composed "Zoë" would otherwise be two
// companies. The table covers Latin-1 Supplement and Latin Extended-A; a
// letter outside it compares as itself.
var accentGroups = map[rune]string{
	'a': "àáâãäåāăą", 'c': "çćĉċč", 'd': "ďđ", 'e': "èéêëēĕėęě", 'g': "ĝğġģ", 'h': "ĥħ",
	'i': "ìíîïĩīĭįı", 'j': "ĵ", 'k': "ķ", 'l': "ĺļľŀł", 'n': "ñńņňŉ", 'o': "òóôõöøōŏő",
	'r': "ŕŗř", 's': "śŝşš", 't': "ţťŧ", 'u': "ùúûüũūŭůűų", 'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
}

var accentBase = func() map[rune]rune {
	m := map[rune]rune{}
	for base, forms := range accentGroups {
		for _, f := range forms {
			m[f] = base
		}
	}
	return m
}()

// foldRunes lowercases s, maps full-width ASCII to ASCII, drops combining
// marks and folds the accents in accentGroups.
func foldRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E: // full-width ! .. ~
			r -= 0xFEE0
			b.WriteRune(unicode.ToLower(r))
		case unicode.Is(unicode.Mn, r):
		default:
			if base, ok := accentBase[r]; ok {
				r = base
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
