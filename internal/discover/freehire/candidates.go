package freehire

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// sourceATS maps freehire's source names to the Vani ATS that reads a board of
// that kind. It is the one table: a source with no row is reported as one Vani
// cannot read, and adding an ATS to Vani's discovery is a row here (with
// IDBoard when freehire's id names the board, which is enough on its own for a
// subdomain-per-tenant ATS), plus the ATS's host in sources.BoardFromURL when
// its URLs should be read directly.
//
// IDBoard marks a source whose result's external_id begins with the board's
// token ("nebius:4864833101"), for results whose URL is a company's own page or
// a shortlink that names no board (greenhouse's careers.nebius.com/?gh_jid=,
// workable's apply.workable.com/j/{id}). The URL stays the first reading; the
// id is the fallback, and only where the id's shape was seen to match the URL's
// board: Greenhouse's across the live fixture (TestFixtureExternalIDMatchesURLBoard,
// which includes the EU-hosted job-boards.eu.greenhouse.io boards that
// sources.BoardFromURL does not read), Workable's in one result.
//
// Source names are freehire's, lower case. Verified against live results on
// 2026-10-28: greenhouse, lever, ashby, workday, workable. From freehire's
// source tree (internal/ingest/atsboard), not seen live: gem, smartrecruiters,
// rippling, oracle.
type sourceRow struct {
	Source  string
	ATS     posting.ATS
	IDBoard bool
}

var sourceATS = []sourceRow{
	{"greenhouse", posting.Greenhouse, true},
	{"lever", posting.Lever, false},
	{"ashby", posting.Ashby, false},
	{"workday", posting.Workday, false},
	{"gem", posting.Gem, false},
	{"smartrecruiters", posting.SmartRecruiters, false},
	{"workable", posting.Workable, true},
	{"rippling", posting.Rippling, false},
	{"oracle", posting.OracleHCM, false},
	{"recruitee", posting.Recruitee, false},
	{"pinpoint", posting.Pinpoint, false},
	{"bamboohr", posting.BambooHR, false},
	{"personio", posting.Personio, false},
}

// searchesBoard is true for the ATSs whose board takes the search that found
// it: a Workday or Oracle HCM company board lists every role the company has
// unless told to narrow, and the whole company is rarely what a discovery
// search was after.
func searchesBoard(a posting.ATS) bool { return a == posting.Workday || a == posting.OracleHCM }

// Why a result's board could not be named. Each is a stable phrase the CLI
// prints and tests match.
const (
	ReasonNoAdapter  = "no Vani adapter reads this source"
	ReasonBadURL     = "the posting URL is not a usable https URL"
	ReasonNoBoard    = "the posting URL names no board"
	ReasonOtherATS   = "the posting URL is on a different ATS than its source"
	ReasonBadBoardID = "the external id names no usable board"
	reasonRefused    = "board refused: "
)

// Candidate is a board some results were found on, ready for `watchlist add`.
type Candidate struct {
	// Board is the board as `watchlist add` would take it: ATS, Token, Host,
	// BoardName, Region and, for Workday and Oracle HCM, the Search.
	Board sources.Board
	// Postings is how many results were on this board.
	Postings int
}

// Unreadable is a group of results Vani cannot turn into a board: the source
// name (freehire's) and why, with how many results.
type Unreadable struct {
	Source string
	Reason string
	Count  int
}

// Candidates is the boards a search found and what could not be named, in
// stable order: Candidates by Postings descending then ATS and token,
// Unreadable by Count descending then source and reason. Every result lands in
// exactly one of the two, so the counts add up to len(jobs).
type Candidates struct {
	Boards     []Candidate
	Unreadable []Unreadable
}

// tokenRE is the shape of a board token taken from an external id: a plain
// slug, never a path or a query.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Boards turns validated results into distinct candidate boards. search is the
// text the results were found with, attached to the boards that need one
// (searchesBoard). A board's company is the name most of its results carry
// (the first seen on a tie), or its token when none carries one.
func Boards(jobs []Job, search string) Candidates {
	type agg struct {
		board    sources.Board
		postings int
		names    map[string]int
		order    []string
	}
	boards := map[string]*agg{}
	var keys []string
	unreadable := map[[2]string]int{}
	for _, j := range jobs {
		b, reason := boardOf(j)
		if reason == "" {
			if searchesBoard(b.ATS) {
				b.Search = strings.TrimSpace(search)
			}
			if err := config.ValidBoard(b); err != nil {
				reason = reasonRefused + err.Error()
			} else if !printable(b.Token) || !printable(b.BoardName) || !printable(b.Host) {
				reason = ReasonBadBoardID
			}
		}
		if reason != "" {
			unreadable[[2]string{j.Source, reason}]++
			continue
		}
		key := string(b.ATS) + ":" + strings.ToLower(b.Token)
		a := boards[key]
		if a == nil {
			a = &agg{board: b, names: map[string]int{}}
			boards[key] = a
			keys = append(keys, key)
		}
		a.postings++
		if j.Company != "" {
			if a.names[j.Company] == 0 {
				a.order = append(a.order, j.Company)
			}
			a.names[j.Company]++
		}
	}
	var out Candidates
	for _, k := range keys {
		a := boards[k]
		a.board.Company = a.board.Token
		best := 0
		for _, n := range a.order {
			if a.names[n] > best {
				best, a.board.Company = a.names[n], n
			}
		}
		out.Boards = append(out.Boards, Candidate{Board: a.board, Postings: a.postings})
	}
	slices.SortFunc(out.Boards, func(x, y Candidate) int {
		return cmp.Or(
			cmp.Compare(y.Postings, x.Postings),
			cmp.Compare(x.Board.ATS, y.Board.ATS),
			strings.Compare(strings.ToLower(x.Board.Token), strings.ToLower(y.Board.Token)),
			strings.Compare(x.Board.Token, y.Board.Token),
		)
	})
	for k, n := range unreadable {
		out.Unreadable = append(out.Unreadable, Unreadable{Source: k[0], Reason: k[1], Count: n})
	}
	slices.SortFunc(out.Unreadable, func(x, y Unreadable) int {
		return cmp.Or(cmp.Compare(y.Count, x.Count), strings.Compare(x.Source, y.Source), strings.Compare(x.Reason, y.Reason))
	})
	return out
}

// boardOf names the board a result is on, or says why it cannot (reason).
func boardOf(j Job) (b sources.Board, reason string) {
	var row *sourceRow
	for i := range sourceATS {
		if sourceATS[i].Source == j.Source {
			row = &sourceATS[i]
			break
		}
	}
	if row == nil {
		return b, ReasonNoAdapter
	}
	u, err := url.Parse(j.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return b, ReasonBadURL
	}
	fromURL, known, err := sources.BoardFromURL(u)
	switch {
	case known && err == nil:
		if fromURL.ATS != row.ATS {
			return b, ReasonOtherATS
		}
		return fromURL, ""
	case !row.IDBoard:
		return b, ReasonNoBoard
	}
	token, _, _ := strings.Cut(j.ExternalID, ":")
	if !tokenRE.MatchString(token) {
		return b, ReasonBadBoardID
	}
	return sources.Board{ATS: row.ATS, Token: token}, ""
}

// printable is whether s holds only characters a terminal shows as themselves:
// a token read from a URL path is percent-decoded, so it can carry an escape
// sequence that ValidBoard, which only guards what a request needs, lets by.
func printable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}
