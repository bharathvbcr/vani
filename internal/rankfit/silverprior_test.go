package rankfit

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

func noneJudged(string) bool { return false }

// silverPairsFrom asks a noiseless auditor about n random pairs of items: it
// picks the higher grade, and says nothing on a tie.
func silverPairsFrom(items []Item, n int, seed uint64) []SilverPair {
	rng := rand.New(rand.NewPCG(seed, 7))
	var out []SilverPair
	for len(out) < n {
		a, b := &items[rng.IntN(len(items))], &items[rng.IntN(len(items))]
		switch {
		case a.Grade > b.Grade:
			out = append(out, SilverPair{a.Key, b.Key})
		case b.Grade > a.Grade:
			out = append(out, SilverPair{b.Key, a.Key})
		}
	}
	return out
}

// A prior equal to the hand shares is no prior: Fit's answer is identical.
func TestPriorEqualToHandIsTheGoldOnlyFit(t *testing.T) {
	items := world(11, 900, 400)
	base := jobinsight.DefaultWeights()
	want, err := Fit(items, base, Options{Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	hand := SharesOf(base)
	got, err := Fit(items, base, Options{Seed: 3, Prior: &hand})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a hand-equal prior changed the fit:\n got %+v\nwant %+v", got, want)
	}
}

// Silver answers drawn from the planted taste move the prior toward it, and
// the gold fit pulled toward that prior is still graded on gold.
func TestSilverPriorLearnsThePlantedTaste(t *testing.T) {
	silver := world(21, 1500, 0) // no owner judgments among these
	base := jobinsight.DefaultWeights()
	p, err := FitSilverPrior(silver, silverPairsFrom(silver, 3000, 1), noneJudged, base, 1)
	if err != nil {
		t.Fatal(err)
	}
	hand := SharesOf(base)
	if share(p.Shares, PartCoverage) <= share(hand, PartCoverage) {
		t.Errorf("the prior did not move toward the planted coverage share: prior %.3f, hand %.3f", share(p.Shares, PartCoverage), share(hand, PartCoverage))
	}
	gold := world(22, 900, 300)
	res, err := Fit(gold, base, Options{Seed: 5, Prior: &p.Shares})
	if err != nil {
		t.Fatal(err)
	}
	if res.HeldOutPairs == 0 || res.Hand != hand {
		t.Errorf("the gold evaluation changed: %+v", res)
	}
}

// A wrong prior held hard fails the gold gate: nothing it says is written.
func TestWrongPriorFailsTheGoldGate(t *testing.T) {
	items := world(31, 1200, 600)
	base := jobinsight.DefaultWeights()
	hand := SharesOf(base)
	wrong := hand
	wrong.Fit, wrong.Coverage, wrong.Title = hand.Fit*1e-4, hand.Coverage*1e-4, hand.Title*1e-4
	wrong.Employer = hand.Employer * 1e4
	res, err := Fit(items, base, Options{Seed: 2, Prior: &wrong, Lambda: 1e5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Beats() || res.FitAccuracy >= res.HandAccuracy {
		t.Errorf("a wrong prior passed: fit %.4f hand %.4f gain [%+.4f, %+.4f]", res.FitAccuracy, res.HandAccuracy, res.GainLo, res.GainHi)
	}
}

// Silver pairs about owner-judged postings are the gold answers in disguise:
// they are dropped, and a prior built only from them refuses.
func TestSilverPriorDropsPairsAboutJudgedPostings(t *testing.T) {
	items := world(41, 600, 600) // every item judged
	judged := map[string]bool{}
	for _, it := range items {
		judged[it.Key] = it.Judged
	}
	leak := silverPairsFrom(items, 500, 3)
	p, err := FitSilverPrior(items, leak, func(k string) bool { return judged[k] }, jobinsight.DefaultWeights(), 1)
	var nr *NotRunError
	if !errors.As(err, &nr) || p.Excluded != len(leak) || p.Pairs != 0 {
		t.Fatalf("leaked pairs: excluded %d of %d, %d used, err %v", p.Excluded, len(leak), p.Pairs, err)
	}
	if _, err := FitSilverPrior(items, leak, nil, jobinsight.DefaultWeights(), 1); err == nil {
		t.Error("no owner-judged test accepted")
	}
}

// Judgments named train-only are never held out.
func TestTrainOnlyIsNeverHeldOut(t *testing.T) {
	items := world(51, 900, 400)
	base := jobinsight.DefaultWeights()
	all, err := Fit(items, base, Options{Seed: 4})
	if err != nil {
		t.Fatal(err)
	}
	half := func(k string) bool { return k < "gh:w:00200" }
	part, err := Fit(items, base, Options{Seed: 4, TrainOnly: half})
	if err != nil {
		t.Fatal(err)
	}
	heldEligible := 0
	for _, it := range items {
		if it.Judged && !half(it.Key) && heldOut(it.Key, 4, DefaultHoldoutPct) {
			heldEligible++
		}
	}
	if part.HeldOutItems != heldEligible || part.HeldOutItems >= all.HeldOutItems {
		t.Errorf("held out %d with train-only, want %d (all: %d)", part.HeldOutItems, heldEligible, all.HeldOutItems)
	}
	if _, err := Fit(items, base, Options{Seed: 4, TrainOnly: func(string) bool { return true }}); err == nil {
		t.Error("every judgment train-only left a held-out set")
	}
}

func TestSilverPriorRefusals(t *testing.T) {
	items := world(61, 300, 0)
	base := jobinsight.DefaultWeights()
	var nr *NotRunError
	if _, err := FitSilverPrior(items, silverPairsFrom(items, MinSilverPairs-1, 1), noneJudged, base, 1); !errors.As(err, &nr) {
		t.Errorf("too few pairs: %v", err)
	}
	pairs := silverPairsFrom(items, MinSilverPairs, 1)
	if _, err := FitSilverPrior(items, append(pairs, SilverPair{"gh:nope", items[0].Key}), noneJudged, base, 1); err == nil || !strings.Contains(err.Error(), "no item") {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := FitSilverPrior(items, append(pairs, SilverPair{items[0].Key, items[0].Key}), noneJudged, base, 1); err == nil {
		t.Error("self pair accepted")
	}
	if _, err := FitSilverPrior(append(items, items[0]), pairs, noneJudged, base, 1); err == nil {
		t.Error("duplicate item accepted")
	}
	hand := SharesOf(base)
	off := hand
	off.KBMatch = 0 // switches a hand part off
	if _, err := Fit(world(62, 900, 400), base, Options{Prior: &off}); err == nil || !strings.Contains(err.Error(), "prior") {
		t.Errorf("a prior that switches a part off: %v", err)
	}
}
