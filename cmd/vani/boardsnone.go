package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

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

// massHeldBoard is a board whose postings wait out a mass hold: a mass absence
// marked them and they are still absent. Until is when the first of them can
// die, Last when the last can (each also needs a read that ages the board).
type massHeldBoard struct {
	ATS     string    `json:"ats"`
	Token   string    `json:"token"`
	Company string    `json:"company,omitempty"`
	Held    int       `json:"held"`
	Until   time.Time `json:"until"`
	Last    time.Time `json:"last,omitzero"`
}

// massHeldOf names the boards whose postings recs holds in a mass hold, named
// from watched where it has them (a board off the watchlist is named by its key
// prefix: nothing reads it, so its holds do not end). At most maxStaleListed,
// earliest end first, with the exact count of boards and of postings.
func massHeldOf(recs []ledger.Record, watched []sources.Board, lv pipeline.Liveness, now time.Time) (out []massHeldBoard, boards, postings int) {
	names := map[string]sources.Board{}
	for _, b := range watched {
		names[b.KeyPrefix()] = b
	}
	for _, h := range pipeline.MassHeld(recs, lv, now) {
		mb := massHeldBoard{Held: h.Held, Until: h.Until.UTC(), Last: h.Last.UTC()}
		if b, ok := names[h.Prefix]; ok {
			mb.ATS, mb.Token, mb.Company = string(b.ATS), b.Token, b.Company
		} else {
			ats, token, _ := strings.Cut(strings.TrimSuffix(h.Prefix, ":"), ":")
			mb.ATS, mb.Token = ats, token
		}
		if mb.Last.Equal(mb.Until) {
			mb.Last = time.Time{}
		}
		out = append(out, mb)
		postings += h.Held
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	boards = len(out)
	if len(out) > maxStaleListed {
		out = out[:maxStaleListed]
	}
	return out, boards, postings
}

// massHeldLine is the attention line a run carries while any posting is in a
// mass hold: not a failure, but the owner should know which boards are being
// carried and until when.
func massHeldLine(held []massHeldBoard, boards, postings int) string {
	if boards == 0 {
		return ""
	}
	parts := make([]string, 0, len(held)+1)
	for _, b := range held {
		name := b.ATS + ":" + b.Token
		if b.Company != "" {
			name = b.Company + " (" + name + ")"
		}
		when := b.Until.Format("2006-01-02")
		if !b.Last.IsZero() && b.Last.Format("2006-01-02") != when {
			when += ".." + b.Last.Format("2006-01-02")
		}
		parts = append(parts, fmt.Sprintf("%s %d until %s", name, b.Held, when))
	}
	if hidden := boards - len(held); hidden > 0 {
		parts = append(parts, fmt.Sprintf("%d more board(s)", hidden))
	}
	return fmt.Sprintf("Mass hold (attention, not a failure): %d posting(s) on %d board(s) are kept though their boards stopped listing them, and die at the end of the hold if still absent: %s",
		postings, boards, strings.Join(parts, "; "))
}
