// Package lca turns US DOL OFLC LCA disclosure data (H-1B, H-1B1, E-3) into a
// compact per-employer, per-fiscal-year count index, and looks employers up
// to produce a sponsorship-history badge.
//
// The badge is a hint, never a filter: employer-name matching is fuzzy and the
// job-description triage remains the actual sponsorship signal.
package lca

import (
	"strings"
	"unicode"
)

// suffixes are trailing legal-form phrases dropped by Normalize. They are
// matched against the end of the token list, longest phrase first.
var suffixes = [][]string{
	{"P", "L", "L", "C"},
	{"L", "L", "C"},
	{"L", "L", "P"},
	{"L", "P"},
	{"P", "C"},
	{"INC"}, {"LLC"}, {"CORP"}, {"CORPORATION"}, {"CO"}, {"LTD"},
	{"LP"}, {"LLP"}, {"PLLC"}, {"PC"}, {"GMBH"}, {"PBC"},
}

// Normalize reduces an employer name to a comparison key: upper case,
// punctuation removed, spaces collapsed, a leading "THE" and trailing legal
// suffixes (INC, LLC, L L C, CORP, ...) dropped iteratively. It returns ""
// when nothing but suffixes remain.
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r == '.' || r == '\'' || r == '’':
			// dropped so that "L.L.C." becomes "LLC" and "MCDONALD'S" stays one token
		case r == '&':
			b.WriteString(" AND ")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	toks := strings.Fields(b.String())
	if len(toks) > 0 && toks[0] == "THE" {
		toks = toks[1:]
	}
	for len(toks) > 0 {
		n := trailingSuffix(toks)
		if n == 0 {
			break
		}
		toks = toks[:len(toks)-n]
	}
	return strings.Join(toks, " ")
}

// trailingSuffix returns the token count of the legal suffix ending toks, or 0.
func trailingSuffix(toks []string) int {
	for _, s := range suffixes {
		if len(s) > len(toks) {
			continue
		}
		tail := toks[len(toks)-len(s):]
		match := true
		for i := range s {
			if tail[i] != s[i] {
				match = false
				break
			}
		}
		if match {
			return len(s)
		}
	}
	return 0
}
