package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/apple"
	"github.com/bharathvbcr/Vani/internal/sources/avature"
	"github.com/bharathvbcr/Vani/internal/sources/eightfold"
	"github.com/bharathvbcr/Vani/internal/sources/oraclehcm"
	"github.com/bharathvbcr/Vani/internal/sources/rippling"
	"github.com/bharathvbcr/Vani/internal/sources/smartrecruiters"
	"github.com/bharathvbcr/Vani/internal/sources/talentbrew"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
)

// Unanswered is detail requests that failed transiently and were left for a
// later run. All eight bounded-detail adapters report it; each one's Report
// must carry it into the board fetch, or a board with unanswered postings
// records as read with no sign of it.
func TestEveryDetailAdapterCarriesUnanswered(t *testing.T) {
	for name, got := range map[string]boardFetch{
		"workday":         workdayFetch(workday.Stats{Unanswered: 4}),
		"smartrecruiters": smartRecruitersFetch(smartrecruiters.Stats{Unanswered: 4}),
		"oraclehcm":       oracleFetch(oraclehcm.Stats{Unanswered: 4}),
		"rippling":        ripplingFetch(rippling.Stats{Unanswered: 4}),
		"talentbrew":      talentBrewFetch(talentbrew.Stats{Unanswered: 4}),
		"eightfold":       eightfoldFetch(eightfold.Stats{Unanswered: 4}),
		"avature":         avatureFetch(avature.Stats{Unanswered: 4}),
		"apple":           appleFetch(apple.Stats{Unanswered: 4}),
	} {
		if got.Unanswered != 4 {
			t.Errorf("%s: Unanswered %d, want 4 (%+v)", name, got.Unanswered, got)
		}
	}
}

// Unanswered reaches the board's record entry and the totals (also for a board
// the item list left out), reports for one board add, and zero is omitted from
// the JSON.
func TestUnansweredReachesTheRunRecord(t *testing.T) {
	w := &wired{warn: &bytes.Buffer{}}
	acme := sources.Board{ATS: posting.Workday, Token: "acme/External"}
	w.noteBoardFetch(acme, boardFetch{Unanswered: 2})
	w.noteBoardFetch(acme, boardFetch{Unanswered: 3})
	w.noteBoardFetch(sources.Board{ATS: posting.Workday, Token: "cut/Site"}, boardFetch{Unanswered: 1})
	w.noteBoardFetch(sources.Board{ATS: posting.Workday, Token: "fine/Site"}, boardFetch{Deferred: 1})

	rep := &boardsReport{Items: []boardReport{
		{ATS: string(posting.Workday), Token: "acme/External", Outcome: boardRead},
		{ATS: string(posting.Workday), Token: "fine/Site", Outcome: boardRead},
	}}
	w.mu.Lock()
	w.applyFetchesLocked(rep)
	w.mu.Unlock()
	if rep.Items[0].Unanswered != 5 || rep.Unanswered != 6 {
		t.Fatalf("item %d, total %d; want 5 and 6", rep.Items[0].Unanswered, rep.Unanswered)
	}
	if rep.Items[1].Unanswered != 0 {
		t.Errorf("a board with none reports %d", rep.Items[1].Unanswered)
	}

	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), `"unanswered"`); n != 2 { // the first item and the total; zero is omitted
		t.Errorf("unanswered written %d times in %s", n, raw)
	}
	var back boardsReport
	if err := json.Unmarshal(raw, &back); err != nil || back.Items[0].Unanswered != 5 || back.Unanswered != 6 {
		t.Errorf("round trip: %v %+v", err, back)
	}
}

// The run report line says how many details went unanswered, and says nothing
// when none did.
func TestRunsShowSaysUnanswered(t *testing.T) {
	if got := boardFetchSummary(&boardsReport{Unanswered: 7}); !strings.Contains(got, "7 unanswered") {
		t.Errorf("line %q", got)
	}
	if got := boardFetchSummary(&boardsReport{Deferred: 1}); strings.Contains(got, "unanswered") {
		t.Errorf("line %q mentions unanswered for zero", got)
	}
}

// Unanswered is a transient fault, not a cap backlog: it never opens the
// DeferredSince streak (that streak is an upper bound on the age of rows past
// the detail cap), and it does not close one that deferred rows hold open.
func TestUnansweredDoesNotFeedTheDeferredStreak(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	read := boardReport{Outcome: boardRead}
	if r := trackBacklog(boardHealthRec{}, read, boardFetch{Unanswered: 3}, at); !r.DeferredSince.IsZero() {
		t.Errorf("unanswered opened the deferred streak: %v", r.DeferredSince)
	}
	open := at.Add(-48 * time.Hour)
	if r := trackBacklog(boardHealthRec{DeferredSince: open}, read, boardFetch{Deferred: 1, Unanswered: 3}, at); !r.DeferredSince.Equal(open) {
		t.Errorf("streak moved: %v", r.DeferredSince)
	}
}
