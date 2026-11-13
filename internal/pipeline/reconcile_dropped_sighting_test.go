package pipeline

// A full listing that shows an exclusion drop again judges it again on the
// listed text. The board added Austin to a London role and left the description
// alone: the stored fields said London, so the drop stood, and no listing ever
// moved it (a dropped posting took no field refresh). The exclusion is judged
// on the board's current text at the sighting instead, the way a pending reopen
// is.

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// excludeLocation excludes a posting whose location names word.
func excludeLocation(word string) func(posting.Posting) (string, bool) {
	return func(p posting.Posting) (string, bool) {
		if strings.Contains(p.Location, word) {
			return "location matches " + word, true
		}
		return "", false
	}
}

// onlyLocation excludes a posting whose location does not name word (the shape
// of exclude.only_locations).
func onlyLocation(word string) func(posting.Posting) (string, bool) {
	return func(p posting.Posting) (string, bool) {
		if !strings.Contains(p.Location, word) {
			return "location is not " + word, true
		}
		return "", false
	}
}

// excludedDrop stores a posting of the test board at loc and drops it on ex at
// triage, with no backend asked.
func excludedDrop(t *testing.T, l ledger.Ledger, id, loc string, ex func(posting.Posting) (string, bool)) posting.Key {
	t.Helper()
	p := ghPost(t, id, "jd text "+id)
	p.Location = loc
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"role_fit": jev("strong_fit", 0.95)}}}
	r := &Runner{Ledger: l, Triager: ft, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "n"},
		Policy: DropPolicy{OpenRouter: "d1-ultra", Exclude: ex}, Now: func() time.Time { return t0.Add(time.Minute) }}
	if err := r.Advance(ctx, p.Key); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, p.Key); got.Status != posting.Dropped || !Excluded(got) || ft.calls != 0 {
		t.Fatalf("setup: %s %v (%d backend calls)", got.Status, got.Annotations, ft.calls)
	}
	return p.Key
}

// moved is k's stored posting with its location changed.
func moved(t *testing.T, l ledger.Ledger, k posting.Key, loc string) posting.Posting {
	t.Helper()
	p := listedAgain(t, l, k)
	p.Location = loc
	return p
}

func exPolicy(ex func(posting.Posting) (string, bool)) DropPolicy {
	return DropPolicy{OpenRouter: "d1-ultra", Exclude: ex}
}

// The finding: London, dropped on location; the board now lists London and
// Austin with the same description. The exclusion no longer matches the listed
// text, so the role comes back at this listing, with the board's fields.
func TestSightingReopensAnExclusionDropWhoseLocationMoved(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	sum, so := sighting(t, l, NewPendingReopens(), exPolicy(onlyLocation("Austin")), t0.Add(time.Hour), true, moved(t, l, k, "Austin"))
	after := rec(t, l, k)
	if sum.Reopened != 1 || so.Applied != 1 || after.Status != posting.Live || after.Posting.Location != "Austin" || Excluded(after) {
		t.Fatalf("%+v %+v: %s %q excluded=%v", sum, so, after.Status, after.Posting.Location, Excluded(after))
	}
	if after.Annotations["triage.backend"] != "" || after.Annotations[DescHashKey] == "" {
		t.Errorf("annotations %v: the drop's triage must be forgotten and the listing's hash taken", after.Annotations)
	}
}

// What still matches stays dropped but takes the board's fields, so the
// exclusion pass judges the board's text and not the stale fields: a moved
// location to another non-US place, and the same place under another spelling.
// A listing that moved nothing writes nothing.
func TestSightingKeepsAnExclusionDropTheListedTextStillMatches(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	s := NewPendingReopens()
	for i, loc := range []string{"Paris", "London, UK"} {
		ex := excludeLocation("London")
		if loc == "Paris" {
			ex = onlyLocation("Austin")
		}
		sum, so := sighting(t, l, s, exPolicy(ex), t0.Add(time.Duration(i+1)*time.Hour), true, moved(t, l, k, loc))
		after := rec(t, l, k)
		if sum.Reopened != 0 || sum.Refreshed != 1 || so.Applied != 0 || after.Status != posting.Dropped || !Excluded(after) || after.Posting.Location != loc {
			t.Fatalf("%s: %+v %+v: %s, location %q", loc, sum, so, after.Status, after.Posting.Location)
		}
	}
	// The same text again: nothing moved, nothing written.
	before := rec(t, l, k)
	sum, _ := sighting(t, l, s, exPolicy(excludeLocation("London")), t0.Add(3*time.Hour), true, listedAgain(t, l, k))
	if sum.Refreshed != 0 || sum.Unchanged != 1 || !rec(t, l, k).LastEvent.Equal(before.LastEvent) {
		t.Fatalf("unmoved relisting wrote: %+v", sum)
	}
}

// Only a full listing is the board's word.
func TestSightingPartialListingNeverReopensAnExclusionDrop(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	sum, so := sighting(t, l, NewPendingReopens(), exPolicy(onlyLocation("Austin")), t0.Add(time.Hour), false, moved(t, l, k, "Austin"))
	if sum.Reopened != 0 || so.Applied != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("a partial listing reopened it: %+v %+v", sum, so)
	}
}

// A model drop is the model's reading, not an exclusion: a moved location
// does not reopen it here (RetriagePolicyDrops owns it).
func TestSightingMovedLocationDoesNotReopenAModelDrop(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	sum, so := sighting(t, l, NewPendingReopens(), exPolicy(onlyLocation("Austin")), t0.Add(time.Hour), true, moved(t, l, k, "Austin"))
	if sum.Reopened != 0 || so.Applied != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("a model drop was reopened by a moved location: %+v %+v", sum, so)
	}
}

// Dropped by an exclusion over a model's reading (ApplyExclusions keeps the
// reading as evidence): the exclusion is what is judged, and the reopen
// forgets the model's reading with the rest of the triage.
func TestSightingReopensAnExclusionDropThatKeepsAModelReading(t *testing.T) {
	l := openLedger(t)
	_, _, k := modelDrop(t, l, "1", staffAns(0.95), staffRules)
	// The exclusion pass later dropped it too: here as one event, the exclusion's
	// annotation beside the model's reading. (A record the model dropped is not
	// excludable, so this is the shape of a model-kept record excluded later.)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: k, Status: posting.Dropped,
		Annotations: map[string]string{AnnExcluded: "excluded: location matches London"}, Reason: "excluded"}); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, k); !Excluded(r) || r.Annotations["triage.seniority"] != "staff_plus" {
		t.Fatalf("setup: %v", r.Annotations)
	}
	sum, _ := sighting(t, l, NewPendingReopens(), exPolicy(onlyLocation("Austin")), t0.Add(2*time.Hour), true, moved(t, l, k, "Austin"))
	after := rec(t, l, k)
	if sum.Reopened != 1 || after.Status != posting.Live || after.Annotations["triage.seniority"] != "" {
		t.Fatalf("%+v: %s %v", sum, after.Status, after.Annotations)
	}
}

// Duplicate keys in one listing: the first is the board's word for the key.
func TestSightingDuplicateKeysFirstListingWins(t *testing.T) {
	for name, order := range map[string]struct {
		first, second string
		reopened      int
	}{
		"first still matches": {"London", "Austin", 0},
		"first moved":         {"Austin", "London", 1},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
			sum, _ := sighting(t, l, NewPendingReopens(), exPolicy(excludeLocation("London")), t0.Add(time.Hour), true,
				moved(t, l, k, order.first), moved(t, l, k, order.second))
			if sum.Reopened != order.reopened {
				t.Fatalf("%+v, want %d reopened", sum, order.reopened)
			}
		})
	}
}

// With no policy to judge it by, a moved drop is left dropped: a check that
// could not run is not a pass.
func TestSightingWithoutAPolicyLeavesAMovedExclusionDropDropped(t *testing.T) {
	l := openLedger(t)
	k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
	sum, err := NewReconciler(l).Reconcile(ctx, board, []posting.Posting{moved(t, l, k, "Austin")}, t0.Add(time.Hour))
	if err != nil || sum.Reopened != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("%+v (%v): %s", sum, err, rec(t, l, k).Status)
	}
}

// The liveness rule: a drop that was missed when dropped and is sighted moved
// ends its absence episode, whether it comes back or stays dropped.
func TestSightingOfAMovedExclusionDropEndsItsAbsenceEpisode(t *testing.T) {
	for name, tc := range map[string]struct {
		ex       func(posting.Posting) (string, bool)
		reopened int
	}{
		"reopened":      {onlyLocation("Austin"), 1},
		"stays dropped": {excludeLocation("London"), 0},
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			k := excludedDrop(t, l, "1", "London", excludeLocation("London"))
			heldWhenDropped(t, l, k, 1, t0.Add(10*time.Minute), false)
			listed := moved(t, l, k, "Austin")
			if name == "stays dropped" {
				listed.Location = "London, UK"
			}
			sum, _ := sighting(t, l, NewPendingReopens(), exPolicy(tc.ex), t0.Add(2*time.Hour), true, listed)
			if sum.Reopened != tc.reopened {
				t.Fatalf("%+v", sum)
			}
			noMissMarks(t, l, k)
		})
	}
}
