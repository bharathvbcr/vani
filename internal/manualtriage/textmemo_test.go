package manualtriage

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func memoPosting() posting.Posting {
	return posting.Posting{Key: "gh:acme:1", Title: "Engineer", Location: "Austin, TX", Description: "Build inference."}
}

// The memo answers what TextSHA256 answers, for the posting as it is now: a
// changed title, location or description under the same key is hashed again,
// never served the old answer.
func TestTextMemoAnswersTheCurrentText(t *testing.T) {
	var memo TextMemo
	p := memoPosting()
	if got, want := memo.TextSHA256(p), TextSHA256(p); got != want {
		t.Fatalf("first ask %s, want %s", got, want)
	}
	changes := []func(*posting.Posting){
		func(q *posting.Posting) { q.Title = "Staff Engineer" },
		func(q *posting.Posting) { q.Location = "Remote" },
		func(q *posting.Posting) { q.Description = "Build training." },
		func(q *posting.Posting) { q.Description = "" },
		func(q *posting.Posting) { q.Title, q.Location, q.Description = "", "", "" },
	}
	for i, change := range changes {
		q := memoPosting()
		change(&q)
		if got, want := memo.TextSHA256(q), TextSHA256(q); got != want {
			t.Errorf("change %d: memo %s, want %s", i, got, want)
		}
		// And back: the original text is answered for again.
		if got, want := memo.TextSHA256(p), TextSHA256(p); got != want {
			t.Errorf("change %d, then back: memo %s, want %s", i, got, want)
		}
	}
	// A posting with no key is still answered by its own text.
	a, b := posting.Posting{Title: "A"}, posting.Posting{Title: "B"}
	if memo.TextSHA256(a) != TextSHA256(a) || memo.TextSHA256(b) != TextSHA256(b) {
		t.Error("two keyless postings shared an answer")
	}
	var none *TextMemo
	if got, want := none.TextSHA256(p), TextSHA256(p); got != want {
		t.Errorf("nil memo %s, want %s", got, want)
	}
}

func TestStateWithMatchesStateOf(t *testing.T) {
	p := memoPosting()
	changed := p
	changed.Description = "Something else."
	r := Reading{Key: p.Key, Text: TextSHA256(p)}
	var memo TextMemo
	for i, c := range []struct {
		p    posting.Posting
		gone bool
	}{{p, false}, {changed, false}, {p, true}, {changed, true}, {p, false}} {
		if got, want := r.StateWith(c.p, c.gone, &memo), r.StateOf(c.p, c.gone); got != want {
			t.Errorf("case %d: StateWith %s, StateOf %s", i, got, want)
		}
	}
}

// Asking again for an unchanged posting does not render and hash its text
// again: the Jobs views ask once per triage slot and once for the row, for
// every listed posting that has a reading.
func TestTextMemoDoesNotRehashAnUnchangedPosting(t *testing.T) {
	p := memoPosting()
	p.Description = strings.Repeat("Serve models at low latency. ", 3_000) // ~87 KB
	var memo TextMemo
	memo.TextSHA256(p)
	var before, after runtime.MemStats
	const n = 50
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range n {
		memo.TextSHA256(p)
	}
	runtime.ReadMemStats(&after)
	if per := (after.TotalAlloc - before.TotalAlloc) / n; per > 1<<10 {
		t.Errorf("a repeated ask allocated %d bytes; the text is %d bytes, so it was rendered again", per, len(p.Description))
	}
}

func TestTextMemoConcurrentUse(t *testing.T) {
	var memo TextMemo
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				p := posting.Posting{Key: posting.Key(fmt.Sprintf("gh:acme:%d", i%20)), Title: "T", Description: fmt.Sprintf("d%d", (i+g)%3)}
				if got, want := memo.TextSHA256(p), TextSHA256(p); got != want {
					t.Errorf("goroutine %d ask %d: %s, want %s", g, i, got, want)
					return
				}
			}
		})
	}
	wg.Wait()
}
