package apple

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// Report sees the counts even when the board fails, and none for a page that
// could not be read.
func TestReportCounts(t *testing.T) {
	var got Stats
	rep := func(_ sources.Board, st Stats) { got = st }
	s := goneSource(t, 12, map[int]srctest.Reply{1: notFound()})
	s.opt = &Options{Report: rep}
	if ps, err := s.List(context.Background(), board("t")); err != nil || len(ps) != 11 || got.Listed != 12 || got.Gone != 1 {
		t.Errorf("one gone: %d postings, err %v, report %+v", len(ps), err, got)
	}
	s = goneSource(t, 12, map[int]srctest.Reply{1: notFound(), 2: notFound()})
	s.opt = &Options{Report: rep}
	if _, err := s.List(context.Background(), board("t")); err == nil || got.Listed != 12 || got.Gone != 2 {
		t.Errorf("two gone: err %v, report %+v", err, got)
	}
	s = goneSource(t, 12, map[int]srctest.Reply{1: {Status: 500, Body: []byte("x")}})
	s.opt = &Options{Report: rep}
	got = Stats{}
	if _, err := s.List(context.Background(), board("t")); err == nil || got.Gone != 0 {
		t.Errorf("500: err %v, report %+v", err, got)
	}
}
