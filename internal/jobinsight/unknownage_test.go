package jobinsight

import (
	"math"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
)

// googleShaped is a strong, well-matched posting whose board gave no date and
// that was listed in the board's first crawl: age unknown, first seen seenDays
// ago. (Google's careers board, 2026-10-19: priority 19 for relevance 75.9.)
func googleShaped(seenDays int) Inputs {
	return Inputs{
		RoleFit: sl("strong_fit", 0.85), Semantic: 1, HasSemantic: true, Target: true, TitleHits: 1, BodyHits: 4,
		TitleSignals: true, TitleRank: 1, AgeDays: -1, SeenKnown: true, SeenDays: seenDays,
	}
}

// A strong posting of unknown age that Vani saw yesterday must not sit at the
// floor reserved for very old postings: it outranks a mediocre dated posting
// two weeks old. Before the fix it scored 0.25 x match and lost.
func TestUnknownAgeRecentlySeenIsNotBuried(t *testing.T) {
	g := Score(googleShaped(1))
	mediocre := baseIn()
	mediocre.RoleFit, mediocre.Coverage, mediocre.Semantic = sl("adjacent", 0.7), 35, 0.35
	mediocre.AgeDays = 14
	m := Score(mediocre)
	if g.Fresh <= DefaultWeights().FreshFloor+0.2 {
		t.Errorf("unknown age seen yesterday: fresh %.3f, barely above the floor %.2f", g.Fresh, DefaultWeights().FreshFloor)
	}
	if g.Score <= m.Score {
		t.Errorf("strong unknown-age posting %.1f does not beat a mediocre 14-day-old one %.1f", g.Score, m.Score)
	}
}

// An unknown age is a lower bound plus a prior: it ages with the days since
// first seen, so a stale pile of undated postings cannot sit above fresh dated
// ones for ever, and a dated posting of the same quality beats it while it is
// newer than the assumption.
func TestUnknownAgeKeepsAgeingFromFirstSeen(t *testing.T) {
	w := DefaultWeights()
	prev := math.Inf(1)
	for _, seen := range []int{0, 1, 5, 20, 60, 400} {
		f := ScoreWith(googleShaped(seen), w).Fresh
		if f >= prev {
			t.Errorf("seen %d days ago: fresh %.4f did not fall below %.4f", seen, f, prev)
		}
		if f < w.FreshFloor {
			t.Errorf("seen %d days ago: fresh %.4f below the floor %.2f", seen, f, w.FreshFloor)
		}
		prev = f
	}
	for _, age := range []int{0, 1, 3, 7} {
		k, u := googleShaped(0), googleShaped(0)
		k.AgeKnown, k.AgeDays, k.SeenKnown = true, age, false
		if !(ScoreWith(k, w).Score > ScoreWith(u, w).Score) {
			t.Errorf("a dated posting %d days old does not beat the same posting of unknown age", age)
		}
	}
	// Older than the assumption: the unknown one now wins, which is the point.
	k, u := googleShaped(0), googleShaped(0)
	k.AgeKnown, k.AgeDays, k.SeenKnown = true, 60, false
	if !(ScoreWith(u, w).Score > ScoreWith(k, w).Score) {
		t.Error("an unknown age seen today does not beat a dated posting 60 days old")
	}
}

// Nothing known at all (no first-seen date either) keeps the floor, and the
// zero value of Inputs is still not fresher than an unknown age.
func TestUnknownAgeWithoutFirstSeenKeepsTheFloor(t *testing.T) {
	w := DefaultWeights()
	r := ScoreWith(Inputs{AgeDays: -1}, w)
	if r.Fresh != w.FreshFloor {
		t.Errorf("no dates: fresh %.3f, want the floor %.2f", r.Fresh, w.FreshFloor)
	}
	if z := Score(Inputs{}); z.Fresh > Score(googleShaped(0)).Fresh {
		t.Errorf("zero Inputs fresh %.3f above an unknown age seen today %.3f", z.Fresh, Score(googleShaped(0)).Fresh)
	}
}

// UnknownAgeDays follows the half-life when unset, takes an explicit value,
// and survives hostile ones.
func TestUnknownAgeDaysWeight(t *testing.T) {
	in := googleShaped(0)
	in.HalfLifeDays = 30
	w := DefaultWeights()
	want := w.FreshFloor + (1-w.FreshFloor)*math.Exp2(-unknownAgeHalfLives) // 1.5 resolved half-lives
	if got := ScoreWith(in, w).Fresh; math.Abs(got-want) > 1e-9 {
		t.Errorf("unset: fresh %.4f, want %.4f (1.5 half-lives of 30 days)", got, want)
	}
	w.UnknownAgeDays = 60
	if got, want := ScoreWith(in, w).Fresh, w.FreshFloor+(1-w.FreshFloor)*0.25; math.Abs(got-want) > 1e-9 {
		t.Errorf("60 days at half-life 30: fresh %.4f, want %.4f", got, want)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -5, 1e12} {
		w.UnknownAgeDays = bad
		r := ScoreWith(in, w)
		inRange(t, "hostile UnknownAgeDays", r)
		if r.Fresh < w.FreshFloor || r.Fresh > 1 {
			t.Errorf("UnknownAgeDays %v: fresh %.4f outside [%.2f, 1]", bad, r.Fresh, w.FreshFloor)
		}
	}
	for _, seen := range []int{-3, math.MaxInt32, math.MaxInt} {
		inRange(t, "hostile SeenDays", ScoreWith(googleShaped(seen), DefaultWeights()))
	}
}

// The fresh factor says when its age was a guess.
func TestFreshFactorIsNotKnownWhenTheAgeIsAGuess(t *testing.T) {
	fresh := func(in Inputs) Factor {
		for _, f := range Score(in).Factors {
			if f.Name == "fresh" {
				return f
			}
		}
		t.Fatal("no fresh factor")
		return Factor{}
	}
	if f := fresh(googleShaped(1)); f.Known {
		t.Errorf("unknown age: fresh factor %+v claims to be known", f)
	}
	if f := fresh(baseIn()); !f.Known {
		t.Errorf("dated posting: fresh factor %+v is not known", f)
	}
}

// InputsFor carries the first sighting for an undated first-crawl posting, and
// only then.
func TestInputsForUnknownAgeCarriesSeenDays(t *testing.T) {
	first := rec("gh:a:1", "Acme", 3*24*time.Hour, nil)
	later := rec("gh:a:2", "Acme", 1*24*time.Hour, nil)
	dated := rec("gh:a:3", "Acme", 3*24*time.Hour, nil)
	dated.Posting.PostedAt = inT0.Add(-9 * 24 * time.Hour)
	zero := rec("gh:z:1", "Zed", 0, nil)
	zero.FirstSeen = time.Time{}
	c := NewRankContext([]ledger.Record{first, later, dated, zero}, nil, inT0)

	if in := InputsFor(first, c); in.AgeKnown || !in.SeenKnown || in.SeenDays != 3 {
		t.Errorf("first-crawl undated: %+v", in)
	}
	if in := InputsFor(later, c); !in.AgeKnown || in.SeenKnown {
		t.Errorf("seen after the first crawl is known, not a guess: %+v", in)
	}
	if in := InputsFor(dated, c); !in.AgeKnown || in.SeenKnown {
		t.Errorf("dated: %+v", in)
	}
	if in := InputsFor(zero, c); in.AgeKnown || in.SeenKnown {
		t.Errorf("no dates at all must carry no first sighting: %+v", in)
	}
}
