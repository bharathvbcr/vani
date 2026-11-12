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
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/textsim"
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

// Key identifies what c resolves: two Companies with one Key identify every
// posting alike, so Groups built under one serve the other.
func (c *Companies) Key() string {
	if c == nil {
		return ""
	}
	var lines []string
	for p, d := range c.byPrefix {
		lines = append(lines, "b\x00"+p+"\x00"+d)
	}
	for n, d := range c.byName {
		lines = append(lines, "n\x00"+n+"\x00"+d)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
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
func Name(s string) string { return pipeline.LooseCompany(textsim.Fold(s)) }
