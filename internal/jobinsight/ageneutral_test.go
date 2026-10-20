package jobinsight

import (
	"math"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// Some employers cap applications rather than close a posting by age (Google
// stops taking them at a count, not a date). For those, how long ago Vani first
// saw an undated posting says nothing about whether it is still worth applying
// to, so the guess is not aged. A date the board gives is still used.

func neutralInputs(seenDays int) Inputs {
	return Inputs{
		RoleFit: sl("strong_fit", 0.8), Semantic: 0.8, HasSemantic: true,
		Target: true, TitleHits: 1, BodyHits: 3,
		SeenKnown: true, SeenDays: seenDays, AgeNeutral: true,
	}
}

func factorOf(t *testing.T, r Result, name string) Factor {
	t.Helper()
	for _, f := range r.Factors {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %q factor in %+v", name, r.Factors)
	return Factor{}
}

func TestAgeNeutralUndatedIsNotAged(t *testing.T) {
	w := DefaultWeights()
	for _, seen := range []int{0, 3, 40, 400} {
		r := Score(neutralInputs(seen))
		if math.Abs(r.Fresh-w.NeutralFresh) > 1e-9 {
			t.Errorf("seen %d days ago: fresh %.3f, want the flat %.3f", seen, r.Fresh, w.NeutralFresh)
		}
		if f := factorOf(t, r, "fresh"); f.Known || f.Note != NoteAgeNeutral {
			t.Errorf("seen %d days ago: fresh factor %+v, want unknown and noted %q", seen, f, NoteAgeNeutral)
		}
	}
	// Without the policy the same posting still ages from when it was first seen.
	aged := neutralInputs(40)
	aged.AgeNeutral = false
	if Score(aged).Fresh >= Score(neutralInputs(40)).Fresh {
		t.Errorf("an ordinary undated posting seen 40 days ago must score below the neutral one")
	}
}

func TestAgeNeutralNeverBeatsAKnownFreshPosting(t *testing.T) {
	// "No penalty" is not "brand new": a posting dated today still leads.
	today := neutralInputs(0)
	today.AgeKnown, today.AgeDays = true, 0
	if n, k := Score(neutralInputs(5)), Score(today); !(n.Score < k.Score) {
		t.Errorf("neutral undated %.1f must rank below the same posting dated today %.1f", n.Score, k.Score)
	}
}

func TestAgeNeutralStillUsesAGivenDate(t *testing.T) {
	old := neutralInputs(0)
	old.AgeKnown, old.AgeDays = true, 120
	plain := old
	plain.AgeNeutral = false
	if Score(old).Fresh != Score(plain).Fresh {
		t.Errorf("a date the board gives counts whatever the policy: %.3f vs %.3f", Score(old).Fresh, Score(plain).Fresh)
	}
	if f := factorOf(t, Score(old), "fresh"); !f.Known || f.Note != "" {
		t.Errorf("a given date is a known age with no note, got %+v", f)
	}
}

func TestAgeNeutralWithNothingSeenKeepsTheFloorRule(t *testing.T) {
	// No first sighting at all: nothing to be neutral about; Score keeps the
	// floor it gave any posting whose age it cannot bound (the unknown-age
	// invariant), so the policy never turns missing input into a bonus.
	in := neutralInputs(0)
	in.SeenKnown = false
	if got, want := Score(in).Fresh, DefaultWeights().FreshFloor; got != want {
		t.Errorf("fresh %.3f, want the floor %.3f", got, want)
	}
}

func TestNeutralFreshHostileWeights(t *testing.T) {
	def := DefaultWeights().NeutralFresh
	for name, v := range map[string]float64{
		"zero": 0, "negative": -1, "nan": math.NaN(), "inf": math.Inf(1), "over one": 7,
	} {
		w := DefaultWeights()
		w.NeutralFresh = v
		r := ScoreWith(neutralInputs(30), w)
		if !(r.Fresh >= w.FreshFloor && r.Fresh <= 1) || math.IsNaN(r.Fresh) {
			t.Errorf("%s: fresh %v out of [floor,1]", name, r.Fresh)
		}
		if v == 0 || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > 1 {
			if r.Fresh != def {
				t.Errorf("%s: fresh %.3f, want the default %.3f for an unusable weight", name, r.Fresh, def)
			}
		}
	}
}

func TestInputsForMarksAgeNeutralCompanies(t *testing.T) {
	now := inT0
	recs := []ledger.Record{
		rec("gg:google:1", "Google", 40*24*time.Hour, nil),
		rec("gg:google:2", "  GOOGLE ", 40*24*time.Hour, nil),
		rec("gh:acme:1", "Acme", 40*24*time.Hour, nil),
	}
	c := NewRankContext(recs, nil, now)
	c.AgeNeutral = map[string]bool{"google": true}
	for _, r := range recs {
		got := InputsFor(r, c)
		if want := companyKey(r.Posting.Company) == "google"; got.AgeNeutral != want {
			t.Errorf("%s (%q): AgeNeutral %v, want %v", r.Key, r.Posting.Company, got.AgeNeutral, want)
		}
	}
	// A nil set (the default) marks nothing.
	c.AgeNeutral = nil
	if InputsFor(recs[0], c).AgeNeutral {
		t.Errorf("no policy configured, yet the posting is neutral")
	}
}

func TestAgeNeutralKeysAreNormalised(t *testing.T) {
	got := AgeNeutralSet([]string{"Google", "  alphabet  ", "", "   ", "GOOGLE"})
	if len(got) != 2 || !got["google"] || !got["alphabet"] {
		t.Errorf("AgeNeutralSet = %v, want {google, alphabet}", got)
	}
	if AgeNeutralSet(nil) != nil {
		t.Errorf("an empty list must give a nil set")
	}
}
