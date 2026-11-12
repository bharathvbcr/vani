package ashby

import (
	"context"
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// A posting the engine dropped never stores the form read for it, so without
// KeepsBody it is read on every run (the live crusoe and 1x boards re-read 55
// and 36 forms a run). With KeepsBody an unchanged one is not asked; a changed
// one is, as its record then takes the listing.
func TestKeptPostingFormIsNotReadWhileUnchanged(t *testing.T) {
	fd := &formDoer{body: apiBody("a", "b", "c"), forms: map[string]srctest.Reply{
		"a": formReply("a", sponsorForm), "b": formReply("b", sponsorForm), "c": formReply("c", sponsorForm),
	}}
	// a: kept, text unchanged, no form stored. b: kept, text changed. c: live, no form.
	stored := map[posting.Key]posting.Posting{
		"ab:ashby:a": {Description: "text of a"},
		"ab:ashby:b": {Description: "older text"},
		"ab:ashby:c": {Description: "text of c"},
	}
	kept := map[posting.Key]bool{"ab:ashby:a": true, "ab:ashby:b": true}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{
		Known:     func(k posting.Key) (posting.Posting, bool) { p, ok := stored[k]; return p, ok },
		KeepsBody: func(k posting.Key) bool { return kept[k] },
		Report:    func(_ sources.Board, st FormStats) { stats = st },
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if fmt.Sprint(fd.posts) != "[b c]" {
		t.Fatalf("asked %v: want the changed kept posting and the live one, not the unchanged kept one", fd.posts)
	}
	if got[0].Form != nil || got[1].Form == nil || got[2].Form == nil {
		t.Fatalf("forms %+v %+v %+v", got[0].Form, got[1].Form, got[2].Form)
	}
	if stats.Kept != 1 || stats.Fetched != 2 || stats.Reused != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

// A stored usable form is still reused for a kept posting: Reused, not Kept.
func TestKeptPostingWithAStoredFormIsReused(t *testing.T) {
	fd := &formDoer{body: apiBody("a")}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{
		Known: func(posting.Key) (posting.Posting, bool) {
			return posting.Posting{Description: "text of a", Form: &posting.ApplyForm{V: 1, Questions: 3}}, true
		},
		KeepsBody: func(posting.Key) bool { return true },
		Report:    func(_ sources.Board, st FormStats) { stats = st },
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form == nil || len(fd.posts) != 0 || stats.Reused != 1 || stats.Kept != 0 {
		t.Fatalf("%v %v asked %v stats %+v", got, err, fd.posts, stats)
	}
}
