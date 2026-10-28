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

func TestATitledRowWithoutAPathIsStillAnError(t *testing.T) {
	s, _ := newSource(stubHandler(45, map[int]string{5: `{"title":"Engineer","bulletFields":["R-9"]}`}))
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !strings.Contains(err.Error(), "is not a job path") {
		t.Fatalf("got %d postings, err %v; want the job-path error", len(got), err)
	}
}
