package workday

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// w1dadvNoText is cmd/vani's Options.Unusable: a stored posting with no text.
func w1dadvNoText(p posting.Posting) bool { return strings.TrimSpace(p.Description) == "" }

// w1dadvUnreadableStored is every job of fb stored, the unreadable one with the
// empty Description cmd/vani's workdayStored gives a posting whose text did not
// read back.
func w1dadvUnreadableStored(fb *fakeBoard, unreadable int) []posting.Posting {
	var stored []posting.Posting
	for _, i := range rangeIdx(0, len(fb.jobs)) {
		sp := storedJobs(fb, i)
		if i == unreadable {
			sp[0].Description = ""
		}
		stored = append(stored, sp...)
	}
	return stored
}

// The adapter's own guards (CanDefer, repostOf, LeftOut) assume every stored
// posting is in Stored: "a row with no stored path holds no stored posting". A
// posting whose text cannot be read back is therefore kept in Stored with an
// empty Description, and the adapter reads its row again. When that row then
// misses (a timed-out detail, tolerated up to the unanswered bound) the posting
// must not be silently omitted and aged as absent by the ledger: the board fails
// or the posting stays listed.
func TestW1dadvAStoredPostingLeftOutOfStoredIsNotSilentlyOmittedOnAMiss(t *testing.T) {
	fb := missBoard(30)
	const unreadable = 5
	stored := w1dadvUnreadableStored(fb, unreadable)
	s := missSource(failDetails(fb, missTimeout, unreadable), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored }, Unusable: w1dadvNoText,
	})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		return // failing the board is the safe answer
	}
	if !hasReq(got, fb.jobs[unreadable].req) {
		t.Fatalf("the listing silently omits job %d, whose posting the ledger holds (but left out of Stored): %d postings, no error; the ledger will age it as absent", unreadable, len(got))
	}
}

// A stored posting with no text is read again and listed with the text read,
// never reused as an empty description, and a title rule that drops new rows
// unread does not drop it (it is a stored posting).
func TestW1dadvUnusableStoredPostingIsReadAgainAndNeverDropped(t *testing.T) {
	fb := missBoard(30)
	const unreadable = 5
	var st Stats
	s := missSource(fb.handle, &Options{
		Stored: func(sources.Board) []posting.Posting { return w1dadvUnreadableStored(fb, unreadable) }, Unusable: w1dadvNoText,
		DropListed: func(posting.Posting) bool { return true },
		Report:     func(_ sources.Board, x Stats) { st = x },
	})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range got {
		if strings.HasSuffix(string(p.Key), strings.ToLower(fmt.Sprintf("JR%d", fb.jobs[unreadable].req))) {
			found = true
			if strings.TrimSpace(p.Description) == "" || p.Description == "stored text" {
				t.Fatalf("posting %s listed with text %q, want the text its detail read", p.Key, p.Description)
			}
		}
	}
	if !found || st.Fetched != 1 || st.Prefiltered != 0 {
		t.Fatalf("found=%v stats %+v: want the unusable posting fetched once and not prefiltered", found, st)
	}
}

// The row of an unusable stored posting that misses is never left out: the
// board fails rather than listing 29 of 30.
func TestW1dadvUnusableStoredPostingMissFailsTheBoard(t *testing.T) {
	fb := missBoard(30)
	const unreadable = 7
	s := missSource(failDetails(fb, missTimeout, unreadable), &Options{
		Stored: func(sources.Board) []posting.Posting { return w1dadvUnreadableStored(fb, unreadable) }, Unusable: w1dadvNoText,
	})
	if got, err := s.List(missNoRetry(), board); err == nil {
		t.Fatalf("a missed unusable stored posting was left out silently: %d postings, no error", len(got))
	}
}
