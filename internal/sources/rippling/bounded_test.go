package rippling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// boardDoer answers the list with ids and every detail with a good job, except
// the ids in bad.
func boardDoer(ids []string, bad map[string]srctest.Reply) *srctest.Doer {
	list := listBody(ids...)
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == listURL {
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		}
		id, ok := strings.CutPrefix(r.URL, listURL+"/")
		if !ok {
			return srctest.Reply{}, false
		}
		if rp, hit := bad[id]; hit {
			return rp, true
		}
		return srctest.Reply{Status: 200, Body: []byte(detailBody(id, ""))}, true
	})
}

func bounded(t *testing.T, d *srctest.Doer, o Options) (*source, *[]Stats) {
	t.Helper()
	var reps []Stats
	o.Report = func(_ sources.Board, st Stats) { reps = append(reps, st) }
	s, err := NewWith(d, o)
	if err != nil {
		t.Fatal(err)
	}
	src := s.(*source)
	src.now = func() time.Time { return fixedNow }
	return src, &reps
}

// idsDue returns n ids whose refresh turn is (due) or is not fixedNow's day.
func idsDue(t *testing.T, n int, due bool) []string {
	t.Helper()
	day := srcutil.Day(fixedNow)
	var out []string
	for i := 0; len(out) < n && i < 100000; i++ {
		id := fmt.Sprintf("s%d", i)
		if srcutil.RefreshTurn(id, day) == due {
			out = append(out, id)
		}
	}
	if len(out) < n {
		t.Fatalf("found %d of %d ids", len(out), n)
	}
	return out
}

func storedPosting(t *testing.T, id string) posting.Posting {
	t.Helper()
	key, err := posting.RipplingKey(board.Token, id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: key, ATS: posting.Rippling, Company: "Acme Robotics", Title: "Stored " + id,
		URL: "https://jobs.example/" + id, Description: "stored text", SeenAt: fixedNow.Add(-24 * time.Hour)}
}

func detailRequests(d *srctest.Doer) int { return len(d.URLs()) - 1 }

// Before the bound, every listed job cost a detail request every run.
func TestNewFailsABoardOverMaxDetailsBeforeAnyDetail(t *testing.T) {
	s, d := newSource(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(listN(MaxDetails + 1))}})
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("%d %v urls %d", len(got), err, len(d.URLs()))
	}
}

func TestStoredJobsAreReusedWithoutARequest(t *testing.T) {
	old := idsDue(t, 3, false)
	ids := append(append([]string{}, old...), "new1")
	d := boardDoer(ids, nil)
	stored := []posting.Posting{storedPosting(t, old[0]), storedPosting(t, old[1]), storedPosting(t, old[2])}
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if n := detailRequests(d); n != 1 || d.URLs()[1] != detailURL("new1") {
		t.Fatalf("want one detail request, for new1: %v", d.URLs())
	}
	if len(got) != 4 {
		t.Fatalf("got %d postings", len(got))
	}
	for i, p := range got[:3] {
		if p.Title != "Stored "+old[i] || p.Description != "stored text" || !p.SeenAt.Equal(fixedNow) {
			t.Fatalf("posting %d not reused as stored with SeenAt now: %+v", i, p)
		}
	}
	if got[3].Key != "rp:acme:new1" || got[3].Description != "Build & ship" {
		t.Fatalf("new posting %+v", got[3])
	}
	if want := (Stats{Listed: 4, Reused: 3, Fetched: 1}); len(*reps) != 1 || (*reps)[0] != want {
		t.Fatalf("reports %+v, want %+v", *reps, want)
	}
}

func TestStoredJobOnItsTurnIsReread(t *testing.T) {
	due := idsDue(t, 1, true)[0]
	d := boardDoer([]string{due}, nil)
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting {
		return []posting.Posting{storedPosting(t, due)}
	}})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Description != "Build & ship" || detailRequests(d) != 1 {
		t.Fatalf("%v %+v urls %v", err, got, d.URLs())
	}
	if want := (Stats{Listed: 1, Refreshed: 1}); (*reps)[0] != want {
		t.Fatalf("reports %+v", *reps)
	}
}

func TestNewJobsPastTheCapAreDeferred(t *testing.T) {
	ids := make([]string, MaxDetails+5)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i)
	}
	d := boardDoer(ids, nil)
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return nil }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if n := detailRequests(d); n != MaxDetails || len(got) != MaxDetails {
		t.Fatalf("%d detail requests, %d postings; want %d of each", n, len(got), MaxDetails)
	}
	if got[len(got)-1].Key != posting.Key("rp:acme:"+ids[MaxDetails-1]) {
		t.Fatalf("the first jobs in list order are fetched; last is %s", got[len(got)-1].Key)
	}
	if want := (Stats{Listed: MaxDetails + 5, Fetched: MaxDetails, Deferred: 5}); (*reps)[0] != want {
		t.Fatalf("reports %+v", *reps)
	}
}

func TestDropListedSkipsNewJobsButNotStoredOnes(t *testing.T) {
	old := idsDue(t, 1, false)[0]
	d := boardDoer([]string{"keep", "drop", old}, nil)
	var judged []posting.Posting
	s, reps := bounded(t, d, Options{
		Stored: func(sources.Board) []posting.Posting { return []posting.Posting{storedPosting(t, old)} },
		DropListed: func(p posting.Posting) bool {
			judged = append(judged, p)
			return strings.Contains(p.Title, "drop")
		},
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 2 || detailRequests(d) != 1 || d.URLs()[1] != detailURL("keep") {
		t.Fatalf("%v %d urls %v", err, len(got), d.URLs())
	}
	// Only new rows are judged, from the list row alone.
	want := posting.Posting{ATS: posting.Rippling, Company: board.Company, Title: "List drop", Location: "List place"}
	if len(judged) != 2 || judged[1] != want {
		t.Fatalf("judged %+v", judged)
	}
	if st := (*reps)[0]; st.Prefiltered != 1 || st.Reused != 1 || st.Fetched != 1 {
		t.Fatalf("reports %+v", *reps)
	}
}

func TestBoundedDetailFailureStillFailsTheBoardAndReports(t *testing.T) {
	d := boardDoer([]string{"a", "b"}, map[string]srctest.Reply{"b": {Status: 404, Body: []byte(`{}`)}})
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return nil }})
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if len(*reps) != 1 || (*reps)[0].Listed != 2 {
		t.Fatalf("a failed board still reports once: %+v", *reps)
	}
}

func TestUnlistedIsCountedNotReturned(t *testing.T) {
	d := boardDoer([]string{"u"}, map[string]srctest.Reply{"u": {Status: 200, Body: []byte(`{"uuid":"u","unlistedFromSearch":true}`)}})
	s, reps := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return nil }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 0 || (*reps)[0] != (Stats{Listed: 1, Unlisted: 1}) {
		t.Fatalf("%v %+v %+v", err, got, *reps)
	}
}

// A stored posting under another board's key never stands in for this
// board's job.
func TestAnotherBoardsStoredPostingIsNotReused(t *testing.T) {
	old := idsDue(t, 1, false)[0]
	other, err := posting.RipplingKey("other", old)
	if err != nil {
		t.Fatal(err)
	}
	d := boardDoer([]string{old}, nil)
	s, _ := bounded(t, d, Options{Stored: func(sources.Board) []posting.Posting {
		return []posting.Posting{{Key: other, Title: "Other board"}}
	}})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Title == "Other board" || detailRequests(d) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestNewWithNeedsStored(t *testing.T) {
	if _, err := NewWith(srctest.New(nil), Options{}); err == nil {
		t.Fatal("NewWith without Stored must fail")
	}
}
