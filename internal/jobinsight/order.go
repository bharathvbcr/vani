package jobinsight

import (
	"cmp"
	"strings"
	"unicode"
)

// The one tie-break for every list Vani orders by Score: the digest, the
// `vani jobs` default and priority sorts, and the digest's Top open. A list
// led by priority breaks ties on relevance, then on the key; a list led by
// relevance breaks ties on priority, then on the key. Rank (DevPrism coverage)
// is not a tie-break: coverage is already a part of both numbers. Each
// function returns a negative number when a goes first.

// ComparePriority orders by Score (priority) desc, Relevance desc, key asc.
func ComparePriority(a, b Result, ka, kb string) int {
	if c := desc(a.Score, b.Score); c != 0 {
		return c
	}
	if c := desc(a.Relevance, b.Relevance); c != 0 {
		return c
	}
	return strings.Compare(ka, kb)
}

// CompareRelevance orders by Relevance desc, Score (priority) desc, key asc.
func CompareRelevance(a, b Result, ka, kb string) int {
	if c := desc(a.Relevance, b.Relevance); c != 0 {
		return c
	}
	if c := desc(a.Score, b.Score); c != 0 {
		return c
	}
	return strings.Compare(ka, kb)
}

// desc compares x and y larger-first. A NaN counts as equal to everything, so
// it falls through to the next key instead of making the order inconsistent.
func desc(x, y float64) int {
	if x != x || y != y {
		return 0
	}
	return cmp.Compare(y, x)
}

// DiverseRow is one row of a diversified list: the input index it stands for
// and the input indexes of the postings collapsed into it (same company and
// normalised title), in input order.
type DiverseRow struct {
	Index   int
	Similar []int
}

// Diversification bounds for the lists a person reads from the top.
const (
	DiversityWindow     = 50
	DiversityPerCompany = 3
)

// Diversify shapes an already-ordered list of n postings for reading. Exact
// company + normalised-title duplicates collapse into the first one, which
// carries the others as Similar. Then at most perCompany rows of one company
// stay in the first window rows: the surplus is moved to just below the
// window, in order, never dropped. A perCompany or window below 1 means the
// defaults. It is pure: company and title read the postings by input index.
func Diversify(n int, company, title func(i int) string, perCompany, window int) []DiverseRow {
	if n <= 0 {
		return nil
	}
	if perCompany < 1 {
		perCompany = DiversityPerCompany
	}
	if window < 1 {
		window = DiversityWindow
	}
	type dupKey struct{ company, title string }
	rows := make([]DiverseRow, 0, n)
	at := make(map[dupKey]int, n)
	for i := 0; i < n; i++ {
		k := dupKey{companyKey(company(i)), normTitle(title(i))}
		if j, ok := at[k]; ok {
			rows[j].Similar = append(rows[j].Similar, i)
			continue
		}
		at[k] = len(rows)
		rows = append(rows, DiverseRow{Index: i})
	}
	out := make([]DiverseRow, 0, len(rows))
	var deferred []DiverseRow
	perCo := map[string]int{}
	next := 0
	for ; next < len(rows) && len(out) < window; next++ {
		c := companyKey(company(rows[next].Index))
		if perCo[c] >= perCompany {
			deferred = append(deferred, rows[next])
			continue
		}
		perCo[c]++
		out = append(out, rows[next])
	}
	out = append(out, deferred...)
	return append(out, rows[next:]...)
}

// normTitle lower-cases s and collapses every run of non-alphanumerics into one
// space, so "ML  Engineer!" and "ml engineer" are one title. It reads at most
// maxNormTitle runes: a longer title is told apart by its start.
func normTitle(s string) string {
	const maxNormTitle = 200
	var b strings.Builder
	space, n := false, 0
	for _, r := range s {
		if n >= maxNormTitle {
			break
		}
		n++
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(unicode.ToLower(r))
		} else {
			space = true
		}
	}
	return b.String()
}
