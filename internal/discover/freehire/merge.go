package freehire

import (
	"cmp"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources"
)

// MaxSampleTitles bounds the titles a merged board carries as examples.
const MaxSampleTitles = 3

// Hit is one search's results: the search text and the results it found that
// the caller kept.
type Hit struct {
	Search string
	Jobs   []Job
}

// MergedBoard is a board found by one or more searches.
type MergedBoard struct {
	// Board is the board as `watchlist add` would take it. On the ATSs whose
	// board takes a search (Workday, Oracle HCM) it carries every search that
	// found it, at most sources.MaxSearches, in Hit order.
	Board sources.Board
	// Postings is the distinct results on the board across every search: a
	// result two searches both found counts once.
	Postings int
	// Searches is the searches that found a result on the board, in Hit order.
	Searches []string
	// Titles is up to MaxSampleTitles distinct result titles, first seen first.
	Titles []string
	// Evidence is the most Merge's evidence function gave any result on the
	// board, under the search that found it; zero without one.
	Evidence int
}

// Merged is what several searches found together. Every distinct result lands
// in exactly one board or one Unreadable group, so the board postings and the
// unreadable counts add up to Distinct.
type Merged struct {
	// Boards is ranked: most searches first, then most Evidence, then most
	// postings, then ATS and token, so a board many of the owner's searches
	// reach comes before a big employer one search happened to fill.
	Boards     []MergedBoard
	Unreadable []Unreadable
	// Distinct is the results after removing ones two searches both found;
	// Repeated is how many were removed.
	Distinct, Repeated int
}

// Merge turns several searches' results into distinct candidate boards. A
// result is identified as Search does (source, external id and URL), so the
// same posting found by two searches counts once on its board and both
// searches are credited. evidence, when not nil, scores a result under the
// search that found it (how much more than the search word itself says it is
// on-profile); a board's Evidence is its best.
func Merge(hits []Hit, evidence func(search string, j Job) int) Merged {
	type agg struct {
		job      Job // the first result read, for the board
		postings int
		searches []string
		names    map[string]int
		order    []string
		titles   []string
		evidence int
	}
	boards := map[string]*agg{}
	var keys []string
	unreadable := map[[2]string]int{}
	seen := map[string]bool{}
	var m Merged
	for _, h := range hits {
		for _, j := range h.Jobs {
			id := j.Source + "\x00" + j.ExternalID + "\x00" + j.URL
			b, reason := readBoard(j, nil)
			if reason != "" {
				if !seen[id] {
					seen[id] = true
					m.Distinct++
					unreadable[[2]string{j.Source, reason}]++
				} else {
					m.Repeated++
				}
				continue
			}
			key := string(b.ATS) + ":" + strings.ToLower(b.Token)
			a := boards[key]
			if a == nil {
				a = &agg{job: j, names: map[string]int{}}
				boards[key] = a
				keys = append(keys, key)
			}
			if !slices.Contains(a.searches, h.Search) {
				a.searches = append(a.searches, h.Search)
			}
			if evidence != nil {
				a.evidence = max(a.evidence, evidence(h.Search, j))
			}
			if seen[id] {
				m.Repeated++
				continue
			}
			seen[id] = true
			m.Distinct++
			a.postings++
			if j.Company != "" {
				if a.names[j.Company] == 0 {
					a.order = append(a.order, j.Company)
				}
				a.names[j.Company]++
			}
			if j.Title != "" && len(a.titles) < MaxSampleTitles && !slices.Contains(a.titles, j.Title) {
				a.titles = append(a.titles, j.Title)
			}
		}
	}
	for _, k := range keys {
		a := boards[k]
		b, reason := readBoard(a.job, a.searches)
		if reason != "" {
			// Only the attached searches can make a board readBoard took
			// without them invalid; the board stands without them.
			b, _ = readBoard(a.job, nil)
		}
		b.Company = b.Token
		best := 0
		for _, n := range a.order {
			if a.names[n] > best {
				best, b.Company = a.names[n], n
			}
		}
		m.Boards = append(m.Boards, MergedBoard{Board: b, Postings: a.postings, Searches: a.searches, Titles: a.titles, Evidence: a.evidence})
	}
	slices.SortFunc(m.Boards, func(x, y MergedBoard) int {
		return cmp.Or(
			cmp.Compare(len(y.Searches), len(x.Searches)),
			cmp.Compare(y.Evidence, x.Evidence),
			cmp.Compare(y.Postings, x.Postings),
			cmp.Compare(x.Board.ATS, y.Board.ATS),
			strings.Compare(strings.ToLower(x.Board.Token), strings.ToLower(y.Board.Token)),
			strings.Compare(x.Board.Token, y.Board.Token),
		)
	})
	for k, n := range unreadable {
		m.Unreadable = append(m.Unreadable, Unreadable{Source: k[0], Reason: k[1], Count: n})
	}
	slices.SortFunc(m.Unreadable, func(x, y Unreadable) int {
		return cmp.Or(cmp.Compare(y.Count, x.Count), strings.Compare(x.Source, y.Source), strings.Compare(x.Reason, y.Reason))
	})
	return m
}
