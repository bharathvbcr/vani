package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// stubHandler serves total rows over pages of 20 (total on the first page
// only, like the live service), where the rows at the indexes in stub are
// replaced by row(i).
func stubHandler(total int, replace map[int]string) func(srctest.Request) (srctest.Reply, bool) {
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			var req struct{ Limit, Offset int }
			if err := json.Unmarshal(r.Body, &req); err != nil {
				return srctest.Reply{}, false
			}
			var rows []string
			for i := req.Offset; i < total && i < req.Offset+req.Limit; i++ {
				if row, hit := replace[i]; hit {
					rows = append(rows, row)
					continue
				}
				rows = append(rows, fmt.Sprintf(`{"title":"T%d","externalPath":"/job/X/T%d_JR%d","locationsText":"L","postedOn":"Posted 3 Days Ago","bulletFields":["JR%d"]}`, i, i, i, i))
			}
			reported := 0
			if req.Offset == 0 {
				reported = total
			}
			return ok(fmt.Sprintf(`{"total":%d,"jobPostings":[%s]}`, reported, strings.Join(rows, ","))), true
		}
		if r.Method == "GET" && strings.HasPrefix(r.URL, siteURL+"/job/X/T") {
			id := r.URL[strings.LastIndex(r.URL, "_JR")+3:]
			return ok(fmt.Sprintf(detailTmpl, id, id)), true
		}
		return srctest.Reply{}, false
	}
}

const mastercardStub = `{"bulletFields":["R-290227"]}`

// Mastercard's listing answered a bare {"bulletFields":[...]} row at offset
// 1000 (2026-10-28), and the whole 1,147-posting board failed on it.
func TestOneStubRowIsLeftOutAndCounted(t *testing.T) {
	var st Stats
	s, d := newSource(stubHandler(45, map[int]string{21: mastercardStub}))
	s.opt = &Options{
		Stored: func(sources.Board) []posting.Posting { return nil },
		Report: func(_ sources.Board, got Stats) { st = got },
	}
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 44 || st.Listed != 44 || st.Stubs != 1 {
		t.Fatalf("%d postings, stats %+v; want 44 listed and 1 stub", len(got), st)
	}
	for _, u := range d.URLs() {
		if strings.HasSuffix(u, siteURL) || strings.HasSuffix(u, siteURL+"/") {
			t.Fatalf("requested a detail for the stub: %s", u)
		}
	}
}

func TestStubsPastTheShortfallFailTheBoard(t *testing.T) {
	// 45 rows: a hundredth is 0, so the shortfall is 1 and two stubs are over it.
	s, _ := newSource(stubHandler(45, map[int]string{3: mastercardStub, 30: `{"title":"","bulletFields":["R-1"]}`}))
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, errIncomplete) || got != nil {
		t.Fatalf("got %d postings, err %v; want errIncomplete", len(got), err)
	}
}

// One stub in each search is that search's allowance. Summing the three and
// judging them against the union is what failed CrowdStrike on 2026-11-02
// (3 of 57, three searches).
func TestOneStubPerSearchStaysWithinThatSearch(t *testing.T) {
	var st Stats
	s, _ := newSource(stubHandler(45, map[int]string{21: mastercardStub}))
	s.opt = &Options{
		Stored: func(sources.Board) []posting.Posting { return nil },
		Report: func(_ sources.Board, got Stats) { st = got },
	}
	b := board
	b.Searches = []string{"machine learning", "LLM", "inference"}
	got, err := s.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 44 || st.Listed != 44 || st.Stubs != 3 || st.Queries != 3 {
		t.Fatalf("%d postings, stats %+v; want 44 listed, one stub in each of 3 searches", len(got), st)
	}
}

// A stub that is a real row on the next read of the same page is listed, and
// the page is not read again once it has no stub.
func TestAStubThatFillsInOnRereadIsListed(t *testing.T) {
	var reads []int
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method != "POST" || r.URL != jobsURL {
			return stubHandler(2, nil)(r)
		}
		var req struct{ Offset int }
		if err := json.Unmarshal(r.Body, &req); err != nil {
			return srctest.Reply{}, false
		}
		reads = append(reads, req.Offset)
		if req.Offset == 0 && len(reads) == 1 {
			return stubHandler(2, map[int]string{0: mastercardStub})(r)
		}
		return stubHandler(2, nil)(r)
	})
	var st Stats
	s.opt = &Options{
		Stored: func(sources.Board) []posting.Posting { return nil },
		Report: func(_ sources.Board, got Stats) { st = got },
	}
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || st.Stubs != 0 {
		t.Fatalf("%d postings, stubs %d; want both rows and no stub", len(got), st.Stubs)
	}
	zeros := 0
	for _, o := range reads {
		if o == 0 {
			zeros++
		}
	}
	if zeros != 2 {
		t.Errorf("offset 0 was read %d times (%v), want the stub page and one reread", zeros, reads)
	}
}

// A stub that is still a stub after the rereads is left out, and the page is
// not read past the bound.
func TestAStableStubIsRereadThenLeftOut(t *testing.T) {
	var offset0 int
	inner := stubHandler(45, map[int]string{0: mastercardStub})
	s, _ := newSource(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method == "POST" && r.URL == jobsURL {
			var req struct{ Offset int }
			if json.Unmarshal(r.Body, &req) == nil && req.Offset == 0 {
				offset0++
			}
		}
		return inner(r)
	})
	var st Stats
	s.opt = &Options{
		Stored: func(sources.Board) []posting.Posting { return nil },
		Report: func(_ sources.Board, got Stats) { st = got },
	}
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 44 || st.Stubs != 1 {
		t.Fatalf("%d postings, stubs %d; want the stub left out", len(got), st.Stubs)
	}
	if offset0 != 1+stubRefetch {
		t.Errorf("offset 0 was read %d times, want %d", offset0, 1+stubRefetch)
	}
}

func TestATitledRowWithoutAPathIsStillAnError(t *testing.T) {
	s, _ := newSource(stubHandler(45, map[int]string{5: `{"title":"Engineer","bulletFields":["R-9"]}`}))
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !strings.Contains(err.Error(), "is not a job path") {
		t.Fatalf("got %d postings, err %v; want the job-path error", len(got), err)
	}
}
