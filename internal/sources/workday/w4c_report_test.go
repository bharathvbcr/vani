package workday

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// w4c125: the board's counts are reported when it fails on a detail too, once.
func TestW4cWorkdayReportsAFailedBoardToo(t *testing.T) {
	fb := missBoard(5)
	h := failDetails(fb, srctest.Reply{Status: 400, Body: []byte(`{}`)}, 2)
	var reports []Stats
	s := missSource(h, &Options{
		Stored: func(sources.Board) []posting.Posting { return nil },
		Report: func(_ sources.Board, x Stats) { reports = append(reports, x) },
	})
	if _, err := s.List(missNoRetry(), board); err == nil {
		t.Fatal("a detail answering 400 must fail the board")
	}
	if len(reports) != 1 {
		t.Fatalf("%d reports for a failed board, want exactly 1", len(reports))
	}
	if reports[0].Listed != 5 || reports[0].Fetched != 2 {
		t.Fatalf("report %+v, want Listed 5 and the 2 details fetched before the failure", reports[0])
	}
}
