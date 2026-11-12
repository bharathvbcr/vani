package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// emptyBoard is a watched board that used to list and now lists none: it read
// fine (no error, so board health sees a good read), listed no posting, and the
// ledger holds postings of its that died or are held after an absence. The
// ledger is the witness, not board health: deleting health.json forgets
// nothing here. A board that never listed anything has no such postings and is
// not named (an employer with no openings is not news).
type emptyBoard struct {
	ATS     string `json:"ats"`
	Token   string `json:"token"`
	Company string `json:"company,omitempty"`
	// Closed counts the board's postings the ledger holds as dead; Held its
	// postings still open but marked by a mass absence (waiting out the mass
	// hold).
	Closed int `json:"closed,omitempty"`
	Held   int `json:"held,omitempty"`
}

// emptyBoardsOf names the boards among tried that read fine and listed none
// while recs holds postings of theirs that died or are mass held. At most
// maxStaleListed are returned, in watchlist order, with the exact count. Every
// record is looked at once, however many boards listed none.
func emptyBoardsOf(recs []ledger.Record, tried []boardReport) ([]emptyBoard, int) {
	want := map[string]*emptyBoard{}
	var order []string
	for _, e := range tried {
		if e.endedRun || e.Outcome != boardRead || e.Listed == nil || *e.Listed != 0 {
			continue
		}
		prefix := sources.Board{ATS: posting.ATS(e.ATS), Token: e.Token}.KeyPrefix()
		if _, dup := want[prefix]; dup {
			continue
		}
		want[prefix] = &emptyBoard{ATS: e.ATS, Token: e.Token, Company: e.Company}
		order = append(order, prefix)
	}
	if len(want) == 0 {
		return nil, 0
	}
	for _, r := range recs {
		k := string(r.Key)
		i := strings.LastIndexByte(k, ':')
		if i < 0 {
			continue
		}
		b, ok := want[k[:i+1]]
		if !ok {
			continue
		}
		switch {
		case r.Status == posting.Dead:
			b.Closed++
		case r.MissedRuns > 0 && r.Annotations[pipeline.MassMissedSinceKey] != "":
			b.Held++
		}
	}
	var out []emptyBoard
	for _, prefix := range order {
		if b := want[prefix]; b.Closed+b.Held > 0 {
			out = append(out, *b)
		}
	}
	n := len(out)
	if len(out) > maxStaleListed {
		out = out[:maxStaleListed]
	}
	return out, n
}

// emptyBoardLine is one such board for a person.
func emptyBoardLine(b emptyBoard) string {
	name := b.ATS + ":" + b.Token
	if b.Company != "" {
		name = b.Company + " (" + name + ")"
	}
	var parts []string
	if b.Closed > 0 {
		parts = append(parts, fmt.Sprintf("%d closed", b.Closed))
	}
	if b.Held > 0 {
		parts = append(parts, fmt.Sprintf("%d held after a mass absence", b.Held))
	}
	return fmt.Sprintf("%s: lists no postings now but listed some before (%s); remove it from the watchlist if it closed for good",
		name, strings.Join(parts, ", "))
}

// emptyBoardLines are the lines for a person, bounded by what was kept; count is
// the exact number.
func emptyBoardLines(boards []emptyBoard) []string {
	sort.SliceStable(boards, func(i, j int) bool { return boards[i].ATS+boards[i].Token < boards[j].ATS+boards[j].Token })
	out := make([]string, 0, len(boards))
	for _, b := range boards {
		out = append(out, emptyBoardLine(b))
	}
	return out
}
