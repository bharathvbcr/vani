package avature

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// w4c: a source from New has no Options (opt is nil). A board that lists
// exactly MaxDetails rows made the limit equal MaxDetails, which the plan loop
// read as "bounded" and then dereferenced the nil options.
func TestW4cNewWithExactlyMaxDetailsRowsDoesNotDereferenceNil(t *testing.T) {
	f := newFake(t, MaxDetails)
	s, _ := f.source()
	if s.opt != nil {
		t.Fatal("the fake source must be the New shape: no options")
	}
	got, err := s.List(context.Background(), board())
	if err != nil {
		t.Fatalf("New-shaped source, %d rows: %v", MaxDetails, err)
	}
	if len(got) != MaxDetails || f.detailCount() != MaxDetails {
		t.Fatalf("%d postings, %d details, want %d of each", len(got), f.detailCount(), MaxDetails)
	}
}

// Options without Stored is unbounded too: DropListed must not be asked, since
// a row dropped there cannot be told from a stored one that aged out.
func TestW4cOptionsWithoutStoredNeverDropAtMaxDetails(t *testing.T) {
	f := newFake(t, MaxDetails)
	asked := 0
	s, _ := f.source()
	s.opt = &Options{DropListed: func(posting.Posting) bool { asked++; return true }}
	got, err := s.List(context.Background(), board())
	if err != nil || len(got) != MaxDetails || asked != 0 {
		t.Fatalf("%d postings (want %d), DropListed asked %d times (want 0), err %v", len(got), MaxDetails, asked, err)
	}
}
