package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A board that has never listed anything. A mistyped or retired board token
// answers a clean, empty listing: no error, so board health counts a good read,
// and the ledger holds nothing of its, so "lists none" (boardsnone.go) has no
// postings to be the witness. Left alone it reads as an employer with no
// openings for ever.
//
// Board health keeps the minimum that tells it from one: the streak of good
// reads that each listed 0 postings (ZeroSince, ZeroReads). A watched board is
// named when that streak has at least minZeroReads reads and its last read is
// at least zeroListingSpan after its first, and the ledger holds no posting at
// all of the board's (a board that ever listed one is a board that exists, and
// boardsnone.go owns what it lists after). One read that listed some, or whose
// count the adapter did not give, ends the streak; a failed attempt neither
// counts nor ends it. Runs in one day add reads, not span, so a day of manual
// runs is not a week.
//
// It is an attention item, never a failure: only held postings dying fails a
// run.
const (
	minZeroReads    = 3
	zeroListingSpan = 7 * 24 * time.Hour
)

// zeroBoard is a watched board whose good reads keep listing nothing.
type zeroBoard struct {
	ATS     string `json:"ats"`
	Token   string `json:"token"`
	Company string `json:"company,omitempty"`
	// Reads is how many good reads in a row listed 0; Since is the first.
	Reads int       `json:"reads"`
	Since time.Time `json:"since"`
}

// trackListing folds one attempt into r's zero-listing streak: a good read that
// listed 0 extends it (starting it at at when none is open), a good read that
// listed some, or said nothing of how many, ends it, and an attempt that was not
// a good read leaves it as it is.
func trackListing(r boardHealthRec, e boardReport, at time.Time) boardHealthRec {
	if !goodRead(e) {
		return r
	}
	if e.Listed == nil || *e.Listed != 0 {
		r.ZeroSince, r.ZeroReads = time.Time{}, 0
		return r
	}
	if r.ZeroSince.IsZero() {
		r.ZeroSince, r.ZeroReads = at, 0
	}
	r.ZeroReads = min(r.ZeroReads+1, maxBoardFailCount)
	return r
}

// listsNothing reports whether r's good reads have listed 0 long enough to name
// the board (the ledger is checked apart). A streak stamped far ahead of at is
// unreadable (sane), not old.
func (r boardHealthRec) listsNothing(at time.Time) (reads int, since time.Time, ok bool) {
	r = r.sane(at)
	if r.ZeroSince.IsZero() || r.ZeroReads < minZeroReads || r.LastOK.IsZero() {
		return 0, time.Time{}, false
	}
	if r.LastOK.Sub(r.ZeroSince) < zeroListingSpan {
		return 0, time.Time{}, false
	}
	return r.ZeroReads, r.ZeroSince, true
}

// zeroCandidatesOf names the boards among boards whose health entry says they
// list nothing, in ATS and token order, each once. The ledger is not asked.
func zeroCandidatesOf(doc boardHealthDoc, boards []sources.Board, at time.Time) []zeroBoard {
	var out []zeroBoard
	seen := map[string]bool{}
	for _, b := range boards {
		k := boardHealthKey(string(b.ATS), b.Token)
		if seen[k] {
			continue
		}
		seen[k] = true
		r, ok := doc.Boards[k]
		if !ok {
			continue
		}
		if reads, since, ok := r.listsNothing(at); ok {
			out = append(out, zeroBoard{ATS: string(b.ATS), Token: oneLine(b.Token, maxBoardNameRunes),
				Company: oneLine(b.Company, maxBoardNameRunes), Reads: reads, Since: since.UTC()})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ATS+":"+out[i].Token < out[j].ATS+":"+out[j].Token })
	return out
}

// zeroBoardsOf keeps the candidates the ledger holds no posting for, at most
// maxStaleListed with the exact count. The ledger is read (lean, writing
// nothing) only when there is a candidate. A ledger that cannot be read leaves
// the question open: it names nothing and note says the check did not run, so
// "no board lists nothing" never covers a ledger nobody looked in. No ledger at
// all holds nothing.
func (e *env) zeroBoardsOf(cands []zeroBoard) (out []zeroBoard, n int, note string) {
	if len(cands) == 0 {
		return nil, 0, ""
	}
	snap, err := ledger.ReadSnapshotScoped(filepath.Join(e.stateDir, "ledger"), nil)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, 0, oneLine("boards that list nothing not checked: the ledger could not be read: "+err.Error(), 300)
	}
	held := map[string]bool{}
	want := map[string]bool{}
	for _, c := range cands {
		want[sources.Board{ATS: posting.ATS(c.ATS), Token: c.Token}.KeyPrefix()] = true
	}
	for _, r := range snap.Records {
		k := string(r.Key)
		if i := strings.LastIndexByte(k, ':'); i >= 0 && want[k[:i+1]] {
			held[k[:i+1]] = true
		}
	}
	for _, c := range cands {
		if !held[sources.Board{ATS: posting.ATS(c.ATS), Token: c.Token}.KeyPrefix()] {
			out = append(out, c)
		}
	}
	n = len(out)
	if len(out) > maxStaleListed {
		out = out[:maxStaleListed]
	}
	return out, n, ""
}

// zeroBoardLine is one such board for a person.
func zeroBoardLine(b zeroBoard) string {
	name := b.ATS + ":" + b.Token
	if b.Company != "" {
		name = b.Company + " (" + name + ")"
	}
	return fmt.Sprintf("%s: lists nothing (check the board token): its last %d good reads since %s each listed 0 postings, and the ledger holds none of its",
		name, b.Reads, b.Since.UTC().Format("2006-01-02"))
}

// zeroBoardLines are the attention lines for boards that list nothing: a
// header with the exact count, each board, and what was cut. note, when the
// check could not run, is said alone.
func zeroBoardLines(boards []zeroBoard, n int, note string) []string {
	if note != "" {
		return []string{note}
	}
	if n == 0 {
		return nil
	}
	out := []string{fmt.Sprintf("boards that have never listed anything (%d; attention, not a failure):", n)}
	for _, b := range boards {
		out = append(out, "  "+oneLine(zeroBoardLine(b), 400))
	}
	if hidden := n - len(boards); hidden > 0 {
		out = append(out, fmt.Sprintf("  (%d more)", hidden))
	}
	return out
}

// runZeroLines are the lines a run's report carries for the boards it read that
// board health says list nothing, as they stand now: the streak is board
// health's, which only the latest run's report may speak for.
func (e *env) runZeroLines(rep *boardsReport, at time.Time) []string {
	if rep == nil {
		return nil
	}
	doc, ok, _ := loadBoardHealth(boardHealthPath(e.stateDir))
	if !ok {
		return nil
	}
	var read []sources.Board
	for _, it := range rep.Items {
		if it.Outcome == boardRead {
			read = append(read, sources.Board{ATS: posting.ATS(it.ATS), Token: it.Token, Company: it.Company})
		}
	}
	boards, n, note := e.zeroBoardsOf(zeroCandidatesOf(doc, read, at))
	return zeroBoardLines(boards, n, note)
}
