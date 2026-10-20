// Package indexutil holds the small pieces the checksummed CSV-derived indexes
// (internal/lca, internal/employer) share: header canonicalisation, the
// checksum of an index body and the deterministic order of merged sources.
package indexutil

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// CanonHeader upper-cases a CSV header cell and joins its words with "_", so
// "Case Status", "case-status" and " CASE_STATUS " all read CASE_STATUS.
func CanonHeader(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '-' }), "_")
}

// SumHex is the lowercase hex sha256 of b: the checksum an index file records
// for its encoded sources.
func SumHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SortedBySHA returns the values of m ordered by the sha256 sha reports, the
// order an index saves and lists its merged sources in.
func SortedBySHA[P any](m map[string]P, sha func(P) string) []P {
	out := make([]P, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return sha(out[i]) < sha(out[j]) })
	return out
}
