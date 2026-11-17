package workable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w4cIDs is n distinct shortcodes.
func w4cIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("J%03d", i)
	}
	return out
}

// A posting that closed between the widget list and its detail answers 404. The
// board is real and listed it, so the row is left out, as every other detail
// adapter does (srcutil.DetailStep Gone): one closed posting among twenty must
// not fail the board.
func TestW4cOneGoneDetailIsLeftOut(t *testing.T) {
	ids := w4cIDs(20)
	r := replies(ids...)
	r[detailURL("J007")] = srctest.Reply{Status: 404, Body: []byte(`{}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatalf("one gone detail failed the board: %v", err)
	}
	if len(got) != 19 {
		t.Fatalf("%d postings, want the 19 whose details answered", len(got))
	}
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), "j007") || strings.HasSuffix(string(p.Key), "J007") {
			t.Fatalf("the gone posting was listed: %s", p.Key)
		}
	}
}

// Many 404s at once are the detail endpoint failing, not postings closing, and
// would empty a healthy board: more than srcutil.MaxGone fails it, and the error
// is not ErrBoardNotFound (the listing answered).
func TestW4cManyGoneDetailsFailTheBoard(t *testing.T) {
	ids := w4cIDs(20)
	r := replies(ids...)
	for _, id := range ids[:srcutil.MaxGone(len(ids))+1] {
		r[detailURL(id)] = srctest.Reply{Status: 404, Body: []byte(`{}`)}
	}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%d gone of 20: %v %v", srcutil.MaxGone(20)+1, got, err)
	}
}

// A real row error (here a wrong shortcode in the detail) still fails the board:
// workable is in the fail-the-board set (docs/FOLLOWUPS.md).
func TestW4cARealRowErrorStillFailsTheBoard(t *testing.T) {
	ids := w4cIDs(20)
	r := replies(ids...)
	r[detailURL("J003")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"OTHER","title":"T"}`)}
	s, _ := newSource(r)
	if got, err := s.List(context.Background(), board); err == nil || got != nil {
		t.Fatalf("a detail naming another posting listed %v, err %v", got, err)
	}
}
