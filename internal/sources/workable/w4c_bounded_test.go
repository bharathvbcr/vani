package workable

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w4cBounded is a source with the bounded detail step over d.
func w4cBounded(d *srctest.Doer, o Options) *source {
	return &source{d: d, now: func() time.Time { return fixedNow }, opt: &o}
}

// w4cStore is the stored posting of shortcode id, as an earlier run left it.
func w4cStore(t *testing.T, id string) posting.Posting {
	t.Helper()
	key, err := posting.WorkableKey("hf", id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: key, ATS: posting.Workable, Company: "Hugging Face", Title: "stored " + id, Description: "stored text", URL: "https://apply.workable.com/j/" + id}
}

// w4cOffTurn and w4cOnTurn split ids by whether fixedNow is their re-read day.
func w4cTurns(ids []string) (on, off []string) {
	day := srcutil.Day(fixedNow)
	for _, id := range ids {
		if srcutil.RefreshTurn(strings.ToLower(id), day) {
			on = append(on, id)
		} else {
			off = append(off, id)
		}
	}
	return on, off
}

func w4cDetailRequests(d *srctest.Doer) int {
	n := 0
	for _, u := range d.URLs() {
		if u != listURL {
			n++
		}
	}
	return n
}

func w4cNone(sources.Board) []posting.Posting { return nil }

// The per-run detail cap: a board of MaxDetails+7 new jobs spends exactly
// MaxDetails detail requests and defers the rest; a source from New, which cannot
// tell a deferred row from a stored one, fails the board before any detail.
func TestW4cDetailRequestsAreCappedPerRun(t *testing.T) {
	ids := w4cIDs(MaxDetails + 7)
	var st Stats
	reports := 0
	_, d := newSource(replies(ids...))
	s := w4cBounded(d, Options{Stored: w4cNone, Report: func(_ sources.Board, x Stats) { st = x; reports++ }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDetails || w4cDetailRequests(d) != MaxDetails || st.Deferred != 7 || st.Fetched != MaxDetails || reports != 1 {
		t.Fatalf("%d postings, %d detail requests, stats %+v, %d reports; want %d fetched, 7 deferred, 1 report", len(got), w4cDetailRequests(d), st, reports, MaxDetails)
	}

	s, d = newSource(replies(ids...))
	got, err = s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || w4cDetailRequests(d) != 0 {
		t.Fatalf("New-shaped source over the cap: err %v, %d detail requests", err, w4cDetailRequests(d))
	}

	// Exactly at the cap is fine for either shape.
	s, d = newSource(replies(w4cIDs(MaxDetails)...))
	if got, err = s.List(context.Background(), board); err != nil || len(got) != MaxDetails {
		t.Fatalf("exactly MaxDetails jobs: %d postings, err %v", len(got), err)
	}
}

func TestW4cStoredRowsAreReusedAndDueOnesReRead(t *testing.T) {
	ids := w4cIDs(30)
	on, off := w4cTurns(ids)
	if len(on) == 0 || len(off) < 2 {
		t.Fatalf("ids on turn %v; the test needs both kinds", on)
	}
	stored := []posting.Posting{w4cStore(t, off[0]), w4cStore(t, off[1]), w4cStore(t, on[0])}
	var st Stats
	_, d := newSource(replies(ids...))
	got, err := w4cBounded(d, Options{Stored: func(sources.Board) []posting.Posting { return stored }, Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), board)
	if err != nil || len(got) != 30 {
		t.Fatalf("%d postings, err %v", len(got), err)
	}
	if w4cDetailRequests(d) != 28 || st.Reused != 2 || st.Refreshed != 1 || st.Fetched != 27 {
		t.Fatalf("%d detail requests, stats %+v; want 28 requests, 2 reused, 1 refreshed, 27 fetched", w4cDetailRequests(d), st)
	}
	for _, p := range got {
		if p.Key == stored[0].Key && (p.Description != "stored text" || !p.SeenAt.Equal(fixedNow)) {
			t.Fatalf("a reused posting keeps its stored text and is seen now: %+v", p)
		}
		if p.Key == stored[2].Key && p.Description == "stored text" {
			t.Fatalf("the posting due its re-read kept its stored text: %+v", p)
		}
	}
}

// A stored row marked unusable (its text could not be read back) is read again
// even when it is off its turn, and is never returned as stored.
func TestW4cUnusableStoredRowIsReadAgain(t *testing.T) {
	ids := w4cIDs(20)
	_, off := w4cTurns(ids)
	sp := w4cStore(t, off[0])
	sp.Description = ""
	_, d := newSource(replies(ids...))
	got, err := w4cBounded(d, Options{
		Stored:   func(sources.Board) []posting.Posting { return []posting.Posting{sp} },
		Unusable: func(k posting.Key) bool { return k == sp.Key },
	}).List(context.Background(), board)
	if err != nil || len(got) != 20 {
		t.Fatalf("%d postings, err %v", len(got), err)
	}
	for _, p := range got {
		if p.Key == sp.Key && (p.Description == "" || strings.HasPrefix(p.Title, "stored ")) {
			t.Fatalf("the unusable row was listed as stored: %+v", p)
		}
	}
	if w4cDetailRequests(d) != 20 {
		t.Fatalf("%d detail requests, want one per row (the unusable one included)", w4cDetailRequests(d))
	}
}

func TestW4cDropListedNewRowsAreNeverFetched(t *testing.T) {
	ids := w4cIDs(12)
	var st Stats
	var judged []posting.Posting
	_, d := newSource(replies(ids...))
	got, err := w4cBounded(d, Options{
		Stored: w4cNone,
		DropListed: func(p posting.Posting) bool {
			judged = append(judged, p)
			return strings.HasSuffix(p.Title, "J004") || strings.HasSuffix(p.Title, "J005")
		},
		Report: func(_ sources.Board, x Stats) { st = x },
	}).List(context.Background(), board)
	if err != nil || len(got) != 10 || w4cDetailRequests(d) != 10 || st.Prefiltered != 2 {
		t.Fatalf("%d postings, %d detail requests, stats %+v, err %v; want 10, 10, 2 prefiltered", len(got), w4cDetailRequests(d), st, err)
	}
	if len(judged) != 12 || judged[0].Company != "Hugging Face" || judged[0].Title != "List J000" || judged[0].Location != "List city, France" {
		t.Fatalf("DropListed saw %+v", judged)
	}
}

// A timed-out detail under NewWith is left for a later run (srcutil.DetailMisses),
// as the other bounded-detail adapters do; without Options it fails the board.
func TestW4cATimedOutDetailIsLeftForALaterRun(t *testing.T) {
	ids := w4cIDs(20)
	r := replies(ids...)
	r[detailURL("J009")] = srctest.Reply{Err: os.ErrDeadlineExceeded}
	var st Stats
	_, d := newSource(r)
	ctx := srcutil.WithRetryBudget(context.Background(), 0)
	got, err := w4cBounded(d, Options{Stored: w4cNone, Report: func(_ sources.Board, x Stats) { st = x }}).List(ctx, board)
	if err != nil || len(got) != 19 || st.Unanswered != 1 {
		t.Fatalf("%d postings, stats %+v, err %v; want 19 listed and 1 unanswered", len(got), st, err)
	}
	s, _ := newSource(r)
	if got, err = s.List(ctx, board); err == nil || got != nil {
		t.Fatalf("a timed-out detail without Options listed %d postings, err %v", len(got), err)
	}
}

// The report carries the counts of a board that failed on a detail too.
func TestW4cReportsAFailedBoard(t *testing.T) {
	ids := w4cIDs(6)
	r := replies(ids...)
	r[detailURL("J002")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"OTHER","title":"T"}`)}
	var reports []Stats
	_, d := newSource(r)
	if _, err := w4cBounded(d, Options{Stored: w4cNone, Report: func(_ sources.Board, x Stats) { reports = append(reports, x) }}).List(context.Background(), board); err == nil {
		t.Fatal("a mismatched detail must fail the board")
	}
	if len(reports) != 1 || reports[0].Listed != 6 || reports[0].Fetched != 2 {
		t.Fatalf("reports %+v, want one with Listed 6 and the 2 details fetched before the failure", reports)
	}
}

func TestW4cNewWithNeedsStored(t *testing.T) {
	if _, err := NewWith(nil, Options{}); err == nil {
		t.Fatal("NewWith without Stored must be refused")
	}
	s, err := NewWith(nil, Options{Stored: w4cNone})
	if err != nil || s.ATS() != posting.Workable {
		t.Fatalf("%v %v", s, err)
	}
}

// The detail read uses the shared detail cap.
func TestW4cDetailBodyUsesTheSharedCap(t *testing.T) {
	if maxDetailBody != srcutil.MaxDetailBody {
		t.Fatalf("detail cap %d, want srcutil.MaxDetailBody %d", maxDetailBody, srcutil.MaxDetailBody)
	}
}
