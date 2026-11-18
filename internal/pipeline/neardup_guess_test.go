package pipeline

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// swapWords replaces the words of base at the given indexes with unique ones.
func swapWords(base string, tag string, at ...int) string {
	ws := strings.Fields(base)
	for n, i := range at {
		ws[i] = fmt.Sprintf("%s%d", tag, n)
	}
	return strings.Join(ws, " ") + " "
}

// The newest posting is never written from a guessed candidate status. B is a
// duplicate of A in a full run; C (newest) is about 0.90 like B and about 0.81
// like A, so a full run keeps C live as a candidate of A. A spent deadline
// cuts B's status: C must then be left undecided, not written a Duplicate of
// B (Duplicate is terminal, so a guess would stay wrong for good). Card
// ft-ae057be0ac19ae51ab9d56474ff7ee7c.
func TestNearDupSpentDeadlineNeverWritesTheNewestFromAGuessedCandidate(t *testing.T) {
	jd := words("duty", 100)
	jdB := swapWords(jd, "bb", 95, 96, 97, 98, 99)
	jdC := swapWords(jdB, "cc", 50)
	build := func() (*eventLedger, [3]posting.Key) {
		recs := []ledger.Record{
			atkRecord(t, "1", "Acme", "Engineer", jd, t0),
			atkRecord(t, "2", "Acme", "Engineer", jdB, t0.Add(time.Minute)),
			atkRecord(t, "3", "Acme", "Engineer", jdC, t0.Add(2*time.Minute)),
		}
		return &eventLedger{recs: recs}, [3]posting.Key{recs[0].Key, recs[1].Key, recs[2].Key}
	}
	full, keys := build()
	fr := atkRun(t, full, NearDupOptions{})
	if fr.Partial || fr.Duplicates != 1 || fr.Candidates != 1 {
		t.Fatalf("full run %+v: the fixture no longer makes B a duplicate and C a candidate", fr)
	}
	cut, _ := build()
	cr := atkRun(t, cut, NearDupOptions{Deadline: time.Nanosecond})
	if !cr.Partial {
		t.Fatalf("spent-deadline report %+v, want Partial", cr)
	}
	for _, e := range cut.evs {
		if e.Key == keys[2] {
			t.Fatalf("the newest posting was written from a guess: %+v", e)
		}
	}
	checkSubset(t, cut.evs, full.evs)
}

// checkSubset fails when a spent-deadline run wrote an event a full run does
// not write for the same key.
func checkSubset(t *testing.T, got, full []ledger.Event) {
	t.Helper()
	byKey := map[posting.Key]ledger.Event{}
	for _, e := range full {
		byKey[e.Key] = e
	}
	for _, e := range got {
		want, ok := byKey[e.Key]
		if !ok || !reflect.DeepEqual(want, e) {
			t.Fatalf("a spent-deadline run wrote %+v; a full run writes %+v (written: %v)", e, want, ok)
		}
	}
}

// A spent-deadline run may write less than a full run, never something
// different: seeded small ledgers of near-identical texts with random edits.
func TestNearDupSpentDeadlineWritesNothingAFullRunWouldNot(t *testing.T) {
	rng := rand.New(rand.NewSource(20261118))
	base := words("duty", 100)
	titles := []string{"Engineer", "Engineer (Remote)", "Senior Engineer"}
	partial, wrote := 0, 0
	for iter := range 400 {
		n := 3 + rng.Intn(6)
		recs := make([]ledger.Record, n)
		texts := make([]string, n)
		for i := range recs {
			// Each text derives from the base or an earlier one with a few edits,
			// so chains of near-duplicates form: A~B~C with A and C further apart.
			parent := base
			if i > 0 && rng.Intn(4) > 0 {
				parent = texts[rng.Intn(i)]
			}
			seen := map[int]bool{}
			var uniq []int
			for range rng.Intn(9) {
				if x := rng.Intn(100); !seen[x] {
					seen[x] = true
					uniq = append(uniq, x)
				}
			}
			texts[i] = swapWords(parent, fmt.Sprintf("e%d_", i), uniq...)
			recs[i] = atkRecord(t, fmt.Sprintf("p%d", i), "Acme", titles[rng.Intn(len(titles))],
				texts[i], t0.Add(time.Duration(i)*time.Minute))
		}
		full := &eventLedger{recs: recs}
		cut := &eventLedger{recs: recs}
		atkRun(t, full, NearDupOptions{})
		cr := atkRun(t, cut, NearDupOptions{Deadline: time.Nanosecond})
		if cr.Partial {
			partial++
		}
		wrote += len(cut.evs)
		t.Run(fmt.Sprint(iter), func(t *testing.T) { checkSubset(t, cut.evs, full.evs) })
	}
	if partial == 0 || wrote == 0 {
		t.Fatalf("the property was not exercised: %d partial runs, %d events written", partial, wrote)
	}
}
