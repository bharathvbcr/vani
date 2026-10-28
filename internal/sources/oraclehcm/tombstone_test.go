package oraclehcm

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	tomb1 = "210260943_300015306973757_ORA_DELETED"
	tomb2 = "210260948_300015306781955_ORA_DELETED"
)

// jpmc/CX_1001's "software engineer" search ended with two deleted markers
// among its rows (2026-10-27): a listing that could not read one failed the
// whole board.
func TestDeletedRequisitionMarkersAreNotPostings(t *testing.T) {
	f := &fake{ids: append(ids(4), tomb1, tomb2)}
	var st Stats
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { st = s })
	got, err := list(t, s, jpmc())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || f.details != 4 {
		t.Fatalf("%d postings, %d details; want 4 and 4 (no detail for a marker)", len(got), f.details)
	}
	for _, p := range got {
		if strings.Contains(string(p.Key), "deleted") || strings.Contains(p.URL, "DELETED") {
			t.Errorf("a marker became a posting: %q %q", p.Key, p.URL)
		}
	}
	if st.Listed != 4 || st.Deleted != 2 || st.Fetched != 4 {
		t.Errorf("stats %+v, want listed 4, deleted 2, fetched 4", st)
	}
	for _, u := range f.urlsAsked {
		if strings.Contains(u, "DELETED") {
			t.Errorf("a detail was asked for a marker: %s", u)
		}
	}
}

// The search's total counts its markers, so they are accounted rows: a board
// with several must not read as a listing that lost rows.
func TestMarkersCountTowardTheTotalSoTheyAreNotAShortfall(t *testing.T) {
	var rows []string
	rows = append(rows, ids(95)...)
	for i := 0; i < 5; i++ {
		rows = append(rows, fmt.Sprintf("2102609%02d_30001530697375%d_ORA_DELETED", i, i))
	}
	f := &fake{ids: rows}
	got, err := list(t, withStored(t, f.doer(), storedAll(rows[:95]), nil), jpmc())
	if err != nil || len(got) != 95 {
		t.Fatalf("%d postings, err %v; want 95, nil (100 counted, 5 of them markers)", len(got), err)
	}
	// the same count with the markers missing from the pages is a real loss
	f2 := &fake{ids: rows}
	f2.page = func(_ string, _ int, r []string) []string { return r[:95] }
	if _, err := list(t, withStored(t, f2.doer(), storedAll(rows[:95]), nil), jpmc()); !errors.Is(err, errIncomplete) {
		t.Fatalf("err %v, want errIncomplete when the markers the total counts never arrive", err)
	}
}

// The cap is checked against the search's own count, markers included, before
// any paging; across terms it is on the postings listed, which a marker is not.
func TestCapIsCheckedAgainstTheCountAndTheUnionOfPostings(t *testing.T) {
	rows := append(ids(3), tomb1, tomb2)
	b := jpmc()
	b.MaxPostings = 4
	f := &fake{ids: rows}
	if _, err := list(t, New(f.doer()), b); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("a count of 5 under max_postings 4: err %v, want ErrTooManyPostings", err)
	}
	b.MaxPostings = 5
	f = &fake{ids: rows}
	if got, err := list(t, New(f.doer()), b); err != nil || len(got) != 3 {
		t.Fatalf("a count of 5 under max_postings 5: %d postings, err %v", len(got), err)
	}
	// two terms of 2 postings and one shared marker each: 4 postings, under 4
	f = &fake{byTerm: map[string][]string{"a": {"210000001", "210000002", tomb1}, "b": {"210000003", "210000004", tomb1}}}
	b = jpmcWith("a", "b")
	b.MaxPostings = 4
	if got, err := list(t, New(f.doer()), b); err != nil || len(got) != 4 {
		t.Fatalf("%d postings, err %v; want 4, nil: a marker is no posting", len(got), err)
	}
}

func TestMarkerRepeatedAcrossTermsIsCountedOnce(t *testing.T) {
	f := &fake{byTerm: map[string][]string{"a": {"210000001", tomb1}, "b": {"210000002", tomb1}}}
	var st Stats
	s := withStored(t, f.doer(), func(sources.Board) []posting.Posting { return nil }, func(_ sources.Board, s Stats) { st = s })
	got, err := list(t, s, jpmcWith("a", "b"))
	if err != nil || len(got) != 2 || st.Deleted != 1 {
		t.Fatalf("%d postings, deleted %d, err %v; want 2, 1, nil", len(got), st.Deleted, err)
	}
}

func TestMalformedMarkerIdsAreRefused(t *testing.T) {
	for _, id := range []string{
		"210260943_ORA_DELETED",           // no second number
		"abc_300015306973757_ORA_DELETED", // not a number
		"210260943_300015306973757_ora_deleted",
		"210260943_300015306973757_ORA_DELETED_X",
		"210260943_300015306973757_ORA_DELETED/../x",
		"_300015306973757_ORA_DELETED",
		"210260943_300015306973757_ORA_DELETED\n",
		"210260943_300015306973757",
	} {
		f := &fake{ids: []string{id}}
		got, err := list(t, New(f.doer()), jpmc())
		if err == nil {
			t.Errorf("id %q accepted: %d postings", id, len(got))
		}
		if f.details != 0 {
			t.Errorf("id %q: %d details asked", id, f.details)
		}
	}
}
