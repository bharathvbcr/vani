package avature

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// bounded is f's source with the bounded detail step: stored as the earlier
// run's postings, drop as DropListed, and the last report kept.
func (f *fake) bounded(stored []posting.Posting, drop func(posting.Posting) bool) (*source, *Stats) {
	var st Stats
	s, _ := f.source()
	s.opt = &Options{
		Stored:     func(sources.Board) []posting.Posting { return stored },
		DropListed: drop,
		Report:     func(_ sources.Board, got Stats) { st = got },
	}
	return s, &st
}

func storedAs(t *testing.T, id string) posting.Posting {
	t.Helper()
	key, err := posting.AvatureKey("twosigma", id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: key, ATS: posting.Avature, Title: "stored " + id, SeenAt: fixedNow.Add(-24 * time.Hour)}
}

// idsByTurn splits the fake's ids by whether fixedNow is their re-read day.
func idsByTurn(f *fake) (due, notDue []string) {
	day := srcutil.Day(fixedNow)
	for _, id := range f.ids {
		if srcutil.RefreshTurn(id, day) {
			due = append(due, id)
		} else {
			notDue = append(notDue, id)
		}
	}
	return due, notDue
}

// Before the bound every posting cost a detail page every run: Bloomberg's
// 341 took 19 minutes a day.
func TestStoredPostingsAreReusedWithoutADetailRequest(t *testing.T) {
	f := newFake(t, 40)
	due, notDue := idsByTurn(f)
	if len(due) == 0 || len(notDue) < 3 {
		t.Fatalf("ids %v due on fixedNow; the test needs both kinds", due)
	}
	stored := []posting.Posting{storedAs(t, notDue[0]), storedAs(t, notDue[1]), storedAs(t, due[0])}
	s, st := f.bounded(stored, nil)
	got, err := s.List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 40 || f.detailCount() != 38 {
		t.Fatalf("%d postings, %d detail requests; want 40 and 38 (two reused, the due one re-read)", len(got), f.detailCount())
	}
	if *st != (Stats{Listed: 40, Reused: 2, Fetched: 37, Refreshed: 1}) {
		t.Fatalf("stats %+v", *st)
	}
	for _, p := range got {
		if p.Key == stored[0].Key && (p.Title != "stored "+notDue[0] || !p.SeenAt.Equal(fixedNow)) {
			t.Fatalf("a reused posting keeps its stored text and is seen now: %+v", p)
		}
		if p.Key == stored[2].Key && strings.HasPrefix(p.Title, "stored ") {
			t.Fatalf("the posting due its re-read kept its stored text: %+v", p)
		}
	}
}

func TestNewPostingsPastMaxDetailsAreDeferred(t *testing.T) {
	f := newFake(t, MaxDetails+7)
	s, st := f.bounded(nil, nil)
	got, err := s.List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDetails || f.detailCount() != MaxDetails || st.Deferred != 7 || st.Fetched != MaxDetails {
		t.Fatalf("%d postings, %d details, stats %+v; want %d fetched and 7 deferred", len(got), f.detailCount(), *st, MaxDetails)
	}
}

// Without stored postings (New, or NewWith without Stored) every row is still
// fetched, as before: a deferred row could not be told from a stored one.
func TestWithoutStoredEveryRowIsFetched(t *testing.T) {
	f := newFake(t, MaxDetails+7)
	got, err := f.list()
	if err != nil || len(got) != MaxDetails+7 || f.detailCount() != MaxDetails+7 {
		t.Fatalf("%d postings, %d details, err %v", len(got), f.detailCount(), err)
	}
}

func TestDropListedSkipsNewCardsOnly(t *testing.T) {
	f := newFake(t, 12)
	_, notDue := idsByTurn(f)
	keep := notDue[len(notDue)-1] // stored, not due, and never ids[0]
	if keep == f.ids[0] {
		t.Fatalf("only %v is off its turn on fixedNow", notDue)
	}
	var judged []posting.Posting
	drop := func(p posting.Posting) bool {
		judged = append(judged, p)
		return strings.HasSuffix(p.Title, f.ids[0]) || strings.HasSuffix(p.Title, keep)
	}
	s, st := f.bounded([]posting.Posting{storedAs(t, keep)}, drop)
	got, err := s.List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	// ids[0] dropped; keep is stored, so it is never judged and is reused.
	if len(got) != 11 || st.Prefiltered != 1 || st.Reused != 1 || len(judged) != 11 {
		t.Fatalf("%d postings, %d judged, stats %+v", len(got), len(judged), *st)
	}
	want := posting.Posting{ATS: posting.Avature, Company: "Two Sigma", Title: "Role " + f.ids[0], Location: "United States - NY New York"}
	if judged[0] != want {
		t.Fatalf("DropListed saw %+v, want the card's title, the board's company and the card's location", judged[0])
	}
}
