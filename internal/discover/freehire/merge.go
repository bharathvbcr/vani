package freehire

import (
	"cmp"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources"
)

// MaxSampleTitles bounds the titles a merged board carries as examples.
const MaxSampleTitles = 3

// MinMirrorShared is the fewest postings a board must share with another for
// Merge to call it a mirror of that board. One shared title and place is too
// little to say two boards are one employer's; JPMorgan's Oracle sites and
// Stone's two Greenhouse boards (live, 2026-11-11) shared three each.
const MinMirrorShared = 2

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
	// found it or one of its Mirrors, at most sources.MaxSearches, in Hit order.
	Board sources.Board
	// Postings is the distinct results on the board across every search: a
	// result two searches both found counts once. A mirror's are not added.
	Postings int
	// Searches is the searches that found a result on the board or on one of
	// its Mirrors, in Hit order.
	Searches []string
	// Titles is up to MaxSampleTitles distinct result titles, first seen first.
	Titles []string
	// Evidence is the most Merge's evidence function gave any result on the
	// board or its Mirrors, under the search that found it; zero without one.
	Evidence int
	// Mirrors is the boards folded into this one: each lists only postings
	// this board lists too (the same title and location, case and spacing
	// aside), at least MinMirrorShared of them, as one employer's second
	// career site does. Most searches first, then most postings, then ATS
	// and token.
	Mirrors []MirrorBoard
}

// MirrorBoard is a board Merge folded into another because every posting on
// it is on the other too.
type MirrorBoard struct {
	// Board is the mirror as `watchlist add` would take it, with its own
	// company name and the searches that found it.
	Board sources.Board
	// Postings is its distinct results, as MergedBoard.Postings counts them.
	Postings int
	// Shared is its distinct postings by title and location, every one of
	// which the board it is folded into lists.
	Shared int
	// Searches is the searches that found it, in Hit order.
	Searches []string
}

// Merged is what several searches found together. Every distinct result lands
// in exactly one board, one mirror or one Unreadable group, so the board and
// mirror postings and the unreadable counts add up to Distinct.
type Merged struct {
	// Boards is ranked: most searches first, then most Evidence, then most
	// postings, then ATS and token, so a board many of the owner's searches
	// reach comes before a big employer one search happened to fill.
	Boards     []MergedBoard
	Unreadable []Unreadable
	// Distinct is the results after removing ones two searches both found;
	// Repeated is how many were removed.
	Distinct, Repeated int
	// Mirrored is how many boards were folded into another (MergedBoard.Mirrors).
	Mirrored int
}

// mergeAgg is one board's results while Merge reads them.
type mergeAgg struct {
	key      string // ATS ":" lower-case token
	job      Job    // the first result read, for the board
	postings int
	searches []string
	names    map[string]int
	order    []string
	titles   []string
	evidence int
	prints   map[string]bool // postingPrint of each titled distinct result
}

// postingPrint names a posting by what a reader sees of it, so one employer's
// copies on two boards (each with its own id and URL) match: the title and
// the location, case-folded with their spacing collapsed. An untitled result
// has none.
func postingPrint(j Job) string {
	title := strings.ToLower(strings.Join(strings.Fields(j.Title), " "))
	if title == "" {
		return ""
	}
	return title + "\x00" + strings.ToLower(strings.Join(strings.Fields(j.Location), " "))
}

// Merge turns several searches' results into distinct candidate boards. A
// result is identified as Search does (source, external id and URL), so the
// same posting found by two searches counts once on its board and both
// searches are credited. evidence, when not nil, scores a result under the
// search that found it (how much more than the search word itself says it is
// on-profile); a board's Evidence is its best. A board whose postings another
// board all lists is folded into it (MergedBoard.Mirrors), so one employer's
// several career sites are one candidate.
func Merge(hits []Hit, evidence func(search string, j Job) int) Merged {
	boards := map[string]*mergeAgg{}
	var keys []string
	unreadable := map[[2]string]int{}
	seen := map[string]bool{}
	searchAt := map[string]int{}
	var m Merged
	for i, h := range hits {
		if _, ok := searchAt[h.Search]; !ok {
			searchAt[h.Search] = i
		}
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
				a = &mergeAgg{key: key, job: j, names: map[string]int{}, prints: map[string]bool{}}
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
			if p := postingPrint(j); p != "" {
				a.prints[p] = true
			}
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

	folded := foldMirrors(boards, keys)
	mirrors := map[string][]MirrorBoard{}
	for _, k := range keys {
		into, ok := folded[k]
		if !ok {
			continue
		}
		a, keep := boards[k], boards[into]
		mirrors[into] = append(mirrors[into], MirrorBoard{Board: a.board(a.searches), Postings: a.postings, Shared: len(a.prints),
			Searches: slices.Clone(a.searches)})
		for _, s := range a.searches {
			if !slices.Contains(keep.searches, s) {
				keep.searches = append(keep.searches, s)
			}
		}
		keep.evidence = max(keep.evidence, a.evidence)
		m.Mirrored++
	}
	for _, k := range keys {
		if _, ok := folded[k]; ok {
			continue
		}
		a := boards[k]
		slices.SortStableFunc(a.searches, func(x, y string) int { return cmp.Compare(searchAt[x], searchAt[y]) })
		ms := mirrors[k]
		slices.SortFunc(ms, func(x, y MirrorBoard) int {
			return cmp.Or(cmp.Compare(len(y.Searches), len(x.Searches)), cmp.Compare(y.Postings, x.Postings), boardOrder(x.Board, y.Board))
		})
		m.Boards = append(m.Boards, MergedBoard{Board: a.board(a.searches), Postings: a.postings, Searches: a.searches, Titles: a.titles,
			Evidence: a.evidence, Mirrors: ms})
	}
	slices.SortFunc(m.Boards, func(x, y MergedBoard) int {
		return cmp.Or(
			cmp.Compare(len(y.Searches), len(x.Searches)),
			cmp.Compare(y.Evidence, x.Evidence),
			cmp.Compare(y.Postings, x.Postings),
			boardOrder(x.Board, y.Board),
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

// foldMirrors maps each board that is a mirror to the board it folds into.
// Boards are tried largest first (most distinct postings, then as Merge ranks
// them), and a board folds into the first earlier board not itself folded
// that lists every one of its postings, when it has at least MinMirrorShared.
// So the keeper lists the most, equal boards fold the same way whatever order
// they were read in, and a chain folds into one keeper.
func foldMirrors(boards map[string]*mergeAgg, keys []string) map[string]string {
	order := slices.Clone(keys)
	slices.SortFunc(order, func(x, y string) int {
		a, b := boards[x], boards[y]
		return cmp.Or(
			cmp.Compare(len(b.prints), len(a.prints)),
			cmp.Compare(len(b.searches), len(a.searches)),
			cmp.Compare(b.evidence, a.evidence),
			cmp.Compare(b.postings, a.postings),
			strings.Compare(x, y),
		)
	})
	folded := map[string]string{}
	for i, k := range order {
		a := boards[k]
		if len(a.prints) < MinMirrorShared {
			continue
		}
		for _, kk := range order[:i] {
			if _, ok := folded[kk]; ok {
				continue
			}
			if keep := boards[kk]; containsAll(keep.prints, a.prints) {
				folded[k] = kk
				break
			}
		}
	}
	return folded
}

func containsAll(set, sub map[string]bool) bool {
	if len(sub) > len(set) {
		return false
	}
	for p := range sub {
		if !set[p] {
			return false
		}
	}
	return true
}

// board is the aggregate's board with searches attached (readBoard), named
// by the company most of its results gave.
func (a *mergeAgg) board(searches []string) sources.Board {
	b, reason := readBoard(a.job, searches)
	if reason != "" {
		// Only the attached searches can make a board readBoard took without
		// them invalid; the board stands without them.
		b, _ = readBoard(a.job, nil)
	}
	b.Company = b.Token
	best := 0
	for _, n := range a.order {
		if a.names[n] > best {
			best, b.Company = a.names[n], n
		}
	}
	return b
}

// boardOrder is the last tie-break: ATS, then token case-folded, then as
// written.
func boardOrder(x, y sources.Board) int {
	return cmp.Or(
		cmp.Compare(x.ATS, y.ATS),
		strings.Compare(strings.ToLower(x.Token), strings.ToLower(y.Token)),
		strings.Compare(x.Token, y.Token),
	)
}
