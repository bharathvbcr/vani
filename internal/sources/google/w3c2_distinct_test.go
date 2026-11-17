package google

import (
	"context"
	"errors"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A total below the distinct jobs still fails the board when ids repeat: the
// rows (4) exceed the total (2) and so do the distinct ids (3).
func TestW3c2TotalBelowDistinctFailsTheBoard(t *testing.T) {
	s, _ := newSource(map[string]string{searchURL("x", nil): page(2, 0, 0, 1, 2)})
	got, err := s.List(context.Background(), board("x"))
	if err == nil {
		t.Fatalf("listed %d postings of a term that counts 2 and served 3 distinct", len(got))
	}
	if !errors.Is(err, srcutil.ErrTotalBelowRows) || !errors.Is(err, ErrShape) {
		t.Errorf("err %v, want ErrShape wrapping ErrTotalBelowRows", err)
	}
}

// Repeated ids under a total of the distinct jobs are read in one request.
func TestW3c2RepeatedIDsReadOnce(t *testing.T) {
	s, d := newSource(map[string]string{searchURL("x", nil): page(3, 0, 0, 1, 1, 2)})
	if _, err := s.List(context.Background(), board("x")); err != nil || len(d.URLs()) != 1 {
		t.Fatalf("err %v, %d requests", err, len(d.URLs()))
	}
}

// A row that failed to decode has no id and is not counted as a distinct job:
// a page of one good row and one unreadable row under total 2 is still a whole
// page for readTerm, with the bad row left to the bad-row handling.
func TestW3c2UndecodableRowIsNotDistinct(t *testing.T) {
	body := wrap(`[[` + one("1000", "A") + `, 5], null, 2, 20]`)
	s, d := newSource(map[string]string{searchURL("x", nil): body})
	jobs, err := s.readTerm(context.Background(), "x", nil)
	if err != nil || len(jobs) != 2 || len(d.URLs()) != 1 {
		t.Fatalf("got %d rows, err %v, %d requests", len(jobs), err, len(d.URLs()))
	}
	if jobs[0].err != nil || jobs[1].err == nil {
		t.Errorf("row errors %v, %v: want the second row marked unreadable", jobs[0].err, jobs[1].err)
	}
}
