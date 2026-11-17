package workday

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// cmd/vani's storedPosting leaves a stored posting out of Options.Stored when
// its text cannot be read back, so the adapter fetches it again. The adapter's
// own guards (CanDefer, repostOf, LeftOut) assume every stored posting is in
// Stored: "a row with no stored path holds no stored posting". A left-out one
// whose row then misses (a timed-out detail, tolerated up to the unanswered
// bound) is dropped from the listing, and the ledger ages its stored posting as
// absent. The desired behaviour: the board fails or the posting stays listed,
// never a silent omission of a posting the ledger holds.
func TestW1dadvAStoredPostingLeftOutOfStoredIsNotSilentlyOmittedOnAMiss(t *testing.T) {
	fb := missBoard(30)
	const unreadable = 5
	var stored []posting.Posting
	for _, i := range rangeIdx(0, 30) {
		if i != unreadable { // storedPosting left this one out: its text did not read back
			stored = append(stored, storedJobs(fb, i)...)
		}
	}
	s := missSource(failDetails(fb, missTimeout, unreadable), &Options{
		Stored: func(sources.Board) []posting.Posting { return stored },
	})
	got, err := s.List(missNoRetry(), board)
	if err != nil {
		return // failing the board is the safe answer
	}
	if !hasReq(got, fb.jobs[unreadable].req) {
		t.Fatalf("the listing silently omits job %d, whose posting the ledger holds (but left out of Stored): %d postings, no error; the ledger will age it as absent", unreadable, len(got))
	}
}
