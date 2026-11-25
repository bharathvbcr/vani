package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// foldNotes is one run's reconcile of b after the adapter reported notes: a
// fresh Reconciler and fresh fetch counts, as a run makes. It returns the
// summary and what the run log said.
func foldNotes(t *testing.T, w *wired, b sources.Board, listed []posting.Posting, at time.Time, notes ...boardFetch) (pipeline.Summary, string) {
	t.Helper()
	warn := &bytes.Buffer{}
	w.warn = warn
	w.mu.Lock()
	w.fetches = nil
	w.mu.Unlock()
	for _, f := range notes {
		w.noteBoardFetch(b, f)
	}
	rec, finish, err := w.runReconciler(nil, at)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	sum, err := w.reconcileBoard(context.Background(), rec, b, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	return sum, warn.String()
}

// reconcileBoard picks the entry point from the board's fetch counts: a
// windowed listing is folded by ReconcileWindow (warned about only for rows it
// could not read), rows it could not read win over everything else, a
// short one by ReconcileShort with one, anything else by Reconcile. Which one
// ran shows in the cause the absent posting carries.
func TestReconcileBoardPicksTheWindowedPath(t *testing.T) {
	for name, tc := range map[string]struct {
		notes    []boardFetch
		cause    string // MassShortKey on the absent posting
		mass     bool   // Summary.Mass
		warnWant string // "" means the run log must not name the board
	}{
		"complete":                  {cause: ""},
		"short":                     {notes: []boardFetch{{Short: 3}}, cause: "short", mass: true, warnWant: "counted 3 job(s) it did not list"},
		"windowed":                  {notes: []boardFetch{{Windowed: true, Short: 870}}, cause: "short"},
		"windowed, nothing counted": {notes: []boardFetch{{Windowed: true}}, cause: "short"},
		// A window's unreadable rows are warned about, but the board is still
		// folded as a window: held as short, never an unreadable mass mark.
		"windowed with unreadable rows": {notes: []boardFetch{{Windowed: true, Short: 870}, {Short: 2, BadRows: 2}}, cause: "short", warnWant: "the board's listing is a window"},
		"unreadable rows then windowed": {notes: []boardFetch{{Short: 2, BadRows: 2}, {Windowed: true, Short: 870}}, cause: "short", warnWant: "the board's listing is a window"},
		"unreadable rows, not windowed": {notes: []boardFetch{{Short: 2, BadRows: 2}}, cause: "unreadable", mass: true, warnWant: "2 row(s) could not be read"},
	} {
		t.Run(name, func(t *testing.T) {
			w, b, all, t0 := shortBoardSetup(t)
			s, log := foldNotes(t, w, b, all[:9], t0.Add(24*time.Hour), tc.notes...)
			r, _ := w.led.Get(all[9].Key)
			if r.Status != posting.Live || r.MissedRuns != 1 || r.Annotations[pipeline.MassShortKey] != tc.cause {
				t.Fatalf("absent posting: %s, %d missed, cause %q; want %q", r.Status, r.MissedRuns, r.Annotations[pipeline.MassShortKey], tc.cause)
			}
			if s.Mass != tc.mass || s.Missed != 1 || s.Dead != 0 {
				t.Fatalf("summary %+v", s)
			}
			if tc.warnWant == "" {
				if strings.Contains(log, "acme/External") {
					t.Fatalf("the run log named the board: %q", log)
				}
			} else if !strings.Contains(log, tc.warnWant) {
				t.Fatalf("run log %q, want %q", log, tc.warnWant)
			}
		})
	}
}

// An empty window over a board with open postings is no failure: nothing is
// mass marked or killed, and when the hold ends the postings die as expected
// closures (ShortDead), not mass deaths. The same empty listing from a short
// board is a true mass absence (the contrast).
func TestEmptyWindowThroughReconcileBoardIsNoMassAbsence(t *testing.T) {
	w, b, _, t0 := shortBoardSetup(t)
	var total pipeline.Summary
	for d := 1; d <= 15; d++ {
		s, log := foldNotes(t, w, b, nil, t0.Add(time.Duration(d)*24*time.Hour), boardFetch{Windowed: true})
		if s.Mass || s.MassMarked != 0 || s.MassDead != 0 || log != "" {
			t.Fatalf("day %d: %+v, log %q", d, s, log)
		}
		total.Dead += s.Dead
		total.ShortDead += s.ShortDead
	}
	if total.Dead != 10 || total.ShortDead != 10 {
		t.Fatalf("deaths %+v", total)
	}

	w, b, _, t0 = shortBoardSetup(t)
	if s, _ := foldNotes(t, w, b, nil, t0.Add(24*time.Hour), boardFetch{Short: 10}); !s.Mass || s.MassMarked != 10 {
		t.Fatalf("contrast: an empty short listing is a mass absence: %+v", s)
	}
}

// A windowed board's unlisted rows are not a shortfall: no short streak opens
// (and one open before closes), its record item carries no Short, and the
// attention line never names it, however long it has been read windowed. Rows
// it could not read are still counted.
func TestWindowedBoardIsNotFlaggedShort(t *testing.T) {
	b := wdBoard("zr/search")
	open := bhNow.Add(-10 * 24 * time.Hour)
	if r := trackBacklog(boardHealthRec{}, wdRead(b), boardFetch{Windowed: true, Short: 870}, bhNow); !r.ShortSince.IsZero() {
		t.Fatalf("a windowed read opened a short streak: %v", r.ShortSince)
	}
	if r := trackBacklog(boardHealthRec{ShortSince: open}, wdRead(b), boardFetch{Windowed: true, Short: 870}, bhNow); !r.ShortSince.IsZero() {
		t.Fatalf("a windowed read kept a short streak open: %v", r.ShortSince)
	}
	if r := trackBacklog(boardHealthRec{ShortSince: open}, wdRead(b), boardFetch{Short: 4}, bhNow); !r.ShortSince.Equal(open) {
		t.Fatalf("a short read lost its streak: %v", r.ShortSince)
	}

	// Even an item that says it is short with an old streak (a record written
	// by hand or by another build) is not named when it is windowed.
	win := wdRead(b)
	win.Windowed, win.Short, win.ShortSince = true, 2, &open
	plain := wdRead(wdBoard("slow/Site"))
	plain.Short, plain.ShortSince = 5, &open
	got := boardAttentionLine(&boardsReport{Read: 2, Short: 7, Items: []boardReport{win, plain}}, bhNow, true)
	if strings.Contains(got, "zr/search") || !strings.Contains(got, "1 listing short for over") || !strings.Contains(got, "slow/Site") {
		t.Fatalf("attention line %q", got)
	}
	if got := boardAttentionLine(&boardsReport{Read: 1, Items: []boardReport{win}}, bhNow, true); got != "" {
		t.Fatalf("a windowed board alone raised an attention line: %q", got)
	}
}

// The board fetch carries Windowed onto the run record's item and total: the
// window's unlisted rows stay out of Short (rows it could not read stay in),
// a board that failed is not counted windowed, a board the record does not
// keep is counted, and the fields survive the record's encoding. A record from
// before the field reads as not windowed.
func TestWindowedReachesTheRunRecord(t *testing.T) {
	w := &wired{warn: &bytes.Buffer{}}
	zr := sources.Board{ATS: posting.ZipRecruiter, Token: "golang-remote"}
	w.noteBoardFetch(zr, boardFetch{Windowed: true, Short: 870})
	dc := sources.Board{ATS: posting.Dice, Token: "go"}
	w.noteBoardFetch(dc, boardFetch{Windowed: true, Short: 3, BadRows: 3})
	w.noteBoardFetch(sources.Board{ATS: posting.Adzuna, Token: "broke"}, boardFetch{Windowed: true, Short: 50})
	w.noteBoardFetch(sources.Board{ATS: posting.Adzuna, Token: "cut"}, boardFetch{Windowed: true})
	if log := w.warn.(*bytes.Buffer).String(); strings.Contains(log, "golang-remote") || strings.Contains(log, "cut") || !strings.Contains(log, "dc/go: listed 3 job(s) fewer") {
		t.Fatalf("run log %q", log)
	}
	rep := &boardsReport{Items: []boardReport{
		{ATS: string(zr.ATS), Token: zr.Token, Outcome: boardRead},
		{ATS: string(dc.ATS), Token: dc.Token, Outcome: boardRead},
		{ATS: string(posting.Adzuna), Token: "broke", Outcome: boardFailed, Error: "500"},
	}}
	w.mu.Lock()
	w.applyFetchesLocked(rep)
	w.mu.Unlock()
	if it := rep.Items[0]; !it.Windowed || it.Short != 0 {
		t.Fatalf("windowed item %+v", it)
	}
	if it := rep.Items[1]; !it.Windowed || it.Short != 3 {
		t.Fatalf("windowed item with unreadable rows %+v", it)
	}
	if it := rep.Items[2]; it.Windowed || it.Short != 0 {
		t.Fatalf("failed item %+v", it)
	}
	if rep.Windowed != 3 || rep.Short != 3 {
		t.Fatalf("totals: windowed %d short %d, want 3 and 3", rep.Windowed, rep.Short)
	}

	rec := histRec(1, outcomeOK)
	rec.Boards = rep
	raw, err := marshalRunRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"windowed": true`)) || !bytes.Contains(raw, []byte(`"windowed": 3`)) {
		t.Fatalf("encoded record lacks the windowed fields:\n%s", raw)
	}
	var back runRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Boards.Windowed != 3 || !back.Boards.Items[0].Windowed || back.Boards.Items[2].Windowed {
		t.Fatalf("decoded %+v", back.Boards)
	}
	// A reader that does not know the key (an older build) ignores it.
	var old struct {
		Boards struct {
			Items []struct {
				ATS   string `json:"ats"`
				Short int    `json:"short"`
			} `json:"items"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || len(old.Boards.Items) != 3 {
		t.Fatalf("an older reader: %+v %v", old, err)
	}
	// A record from before the field.
	var pre boardReport
	if err := json.Unmarshal([]byte(`{"ats":"zr","token":"x","outcome":"read","short":4}`), &pre); err != nil || pre.Windowed || pre.Short != 4 {
		t.Fatalf("pre-field item %+v %v", pre, err)
	}
}

// runs show says a board was read through a window, and does not present the
// window's unlisted rows as a shortfall or the run as failed.
func TestRunsShowNamesWindowedSearch(t *testing.T) {
	rec := histRec(1, outcomeOK)
	thirty := 30
	rec.Boards = &boardsReport{Configured: 1, Read: 1, Listed: 30, Windowed: 1, Items: []boardReport{
		{ATS: "zr", Token: "golang-remote", Outcome: boardRead, Listed: &thirty, Windowed: true},
	}}
	if runHasFailures(rec, histT0) {
		t.Fatal("a windowed board made the run a failure")
	}
	text := (&env{stateDir: t.TempDir(), stderr: &bytes.Buffer{}}).runReportOf(runRef{ID: runID(rec), Rec: rec}, histT0, true)
	if !strings.Contains(text.Text, "1 windowed search(es) (listed in part by design") {
		t.Errorf("report does not name the windowed search\n%s", text.Text)
	}
	for _, bad := range []string{"counted but not listed", "Listed short", "Boards not being read"} {
		if strings.Contains(text.Text, bad) {
			t.Errorf("report carries %q for a windowed board\n%s", bad, text.Text)
		}
	}
	if text.HasFailures {
		t.Fatal("report marked failures")
	}
}

// The windowed flag is a plain field of the fetch: every adapter that does not
// report it leaves it false, and its own Stats function is where a windowed
// adapter sets it (TestAdapterStatsReachTheBoardFetch).
func TestBoardFetchShortfall(t *testing.T) {
	for _, tc := range []struct {
		f    boardFetch
		want int
	}{
		{boardFetch{}, 0},
		{boardFetch{Short: 5}, 5},
		{boardFetch{Short: 5, BadRows: 2}, 5},
		{boardFetch{Windowed: true, Short: 870}, 0},
		{boardFetch{Windowed: true, Short: 870, BadRows: 2}, 2},
		{boardFetch{Windowed: true, BadRows: 2}, 0}, // malformed: bad rows not in Short
	} {
		if got := tc.f.shortfall(); got != tc.want {
			t.Errorf("%+v: shortfall %d, want %d", tc.f, got, tc.want)
		}
	}
}
