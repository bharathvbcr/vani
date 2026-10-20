// Package textsim measures how much two texts overlap: word tokens, k-word
// shingles and the Jaccard index of their sets. It is stdlib only and bounds
// every input, because the text it reads is untrusted posting content.
//
// A similarity here is a measurement for an engine rule or an annotation to
// read. It never opens, follows or executes anything in the text.
package textsim

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultMaxBytes is how much of a description the pipeline compares.
	DefaultMaxBytes = 4096
	// HardMaxBytes caps any caller's bound, so one careless argument cannot
	// make a comparison read megabytes.
	HardMaxBytes = 1 << 20
)

// Set is a set of hashed tokens or shingles. The hash is 64-bit FNV-1a, so a
// false match needs a collision among the few hundred elements one bounded
// text holds.
type Set map[uint64]struct{}

// Words lowercases s and splits it into runs of letters and digits, reading at
// most maxBytes of s (cut back to a rune boundary; maxBytes over HardMaxBytes
// is HardMaxBytes). A maxBytes of zero or less reads nothing. Bytes that are
// not valid UTF-8 separate words.
func Words(s string, maxBytes int) []string {
	if maxBytes <= 0 || s == "" {
		return nil
	}
	maxBytes = min(maxBytes, HardMaxBytes)
	if len(s) > maxBytes {
		for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
			maxBytes--
		}
		s = s[:maxBytes]
	}
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

func hashWords(ws []string) uint64 {
	h := uint64(fnvOffset)
	for _, w := range ws {
		for i := 0; i < len(w); i++ {
			h = (h ^ uint64(w[i])) * fnvPrime
		}
		h = (h ^ 0xff) * fnvPrime // a byte no UTF-8 word holds, so ["ab","c"] and ["a","bc"] differ
	}
	return h
}

// TokenSet is the set of distinct words.
func TokenSet(words []string) Set {
	out := make(Set, len(words))
	for _, w := range words {
		out[hashWords([]string{w})] = struct{}{}
	}
	return out
}

// Shingles is the set of every run of k consecutive words. A text of fewer
// than k words, or a k below one, has none: there is nothing to compare, which
// is not the same as nothing in common.
func Shingles(words []string, k int) Set {
	if k < 1 || len(words) < k {
		return Set{}
	}
	out := make(Set, len(words)-k+1)
	for i := 0; i+k <= len(words); i++ {
		out[hashWords(words[i:i+k])] = struct{}{}
	}
	return out
}
