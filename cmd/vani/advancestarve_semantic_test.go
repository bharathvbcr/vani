package main

import (
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The pre-advance semantic warm embeds in Score order (semanticwire.go
// warmOrder) and its reading only orders the advance phase (Rule 7): nothing
// else in advance reads it. A starved posting is moved to the front by age
// (advancestarve.go promoteStarved), after that order, so it does not matter
// whether the warm reached it. A starved posting the warm never embedded (no
// reading) is promoted the same way, in the same place, as one it did.
func TestStarvedPostingsAreAdvancedWhetherOrNotTheWarmReadThem(t *testing.T) {
	pinStarveClock(t)
	build := func() []ledger.Record {
		var recs []ledger.Record
		for i := 0; i < 10; i++ {
			recs = append(recs, freshRec(i))
		}
		for i, d := range []time.Duration{4 * 24 * time.Hour, 9 * 24 * time.Hour, 6 * 24 * time.Hour, 5 * 24 * time.Hour} {
			recs = append(recs, starvedRec(i, d))
		}
		return recs
	}
	// Fresh postings were warmed and read well; the starved ones' warm status varies.
	fresh := func(r ledger.Record) bool {
		return r.Posting.Company == "Acme" && r.Key[len("gh:acme:"):][:5] == "fresh"
	}
	variants := map[string]func(ledger.Record) (float64, bool){
		"starved unread":      func(r ledger.Record) (float64, bool) { return 0.9, fresh(r) },
		"starved read, lower": func(r ledger.Record) (float64, bool) { return map[bool]float64{true: 0.9, false: 0.2}[fresh(r)], true },
	}
	for name, sem := range variants {
		recs := build()
		orderAdvance(recs, recs, nil, withSemantic(sem), withSlots(8))
		got := starveKeys(recs)
		// A cap of 8 gives a quarter, two slots, to the two oldest starved postings.
		if !slices.Equal(got[:2], []posting.Key{"gh:acme:old001", "gh:acme:old002"}) {
			t.Errorf("%s: front of the run = %v, want the two oldest starved postings", name, got[:2])
		}
	}
}
