package eightfold

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// Positions two searches both find are one position: one detail, one count.
func TestDuplicateIDsCountOnce(t *testing.T) {
	s := goneSite(t, 12, map[string]srctest.Reply{id(1): notFound()})
	var got Stats
	s.opt = &Options{Report: func(_ sources.Board, st Stats) { got = st }}
	ps, err := s.List(context.Background(), board("t", "u"))
	if err != nil || len(ps) != 11 || got.Listed != 12 || got.Gone != 1 {
		t.Errorf("got %d postings, err %v, report %+v; want 11, nil, listed 12 gone 1", len(ps), err, got)
	}
}

// Report sees the counts even when the board fails, and none for a detail
// that could not be read.
func TestReportOnFailure(t *testing.T) {
	s := goneSite(t, 12, map[string]srctest.Reply{id(1): notFound(), id(2): notFound()})
	var got Stats
	s.opt = &Options{Report: func(_ sources.Board, st Stats) { got = st }}
	if _, err := s.List(context.Background(), board("t")); err == nil {
		t.Fatal("want an error")
	}
	if got.Listed != 12 || got.Gone != 2 {
		t.Errorf("report %+v, want listed 12 gone 2", got)
	}
	s = goneSite(t, 12, map[string]srctest.Reply{id(1): {Status: 500, Body: []byte("{}")}})
	got = Stats{}
	s.opt = &Options{Report: func(_ sources.Board, st Stats) { got = st }}
	if _, err := s.List(context.Background(), board("t")); err == nil {
		t.Fatal("want an error")
	}
	if got.Gone != 0 {
		t.Errorf("a 500 detail counted as gone: %+v", got)
	}
}
