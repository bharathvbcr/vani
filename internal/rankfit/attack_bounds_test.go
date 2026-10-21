package rankfit

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

func flatItems(n int, grade func(i int) int) []Item {
	items := make([]Item, n)
	for i := range items {
		it := Item{Key: fmt.Sprintf("gh:f:%06d", i), Gate: 0.8, Fresh: 0.8, Grade: grade(i), Judged: true}
		for k := range NumParts {
			it.Present[k], it.Known[k], it.Conf[k], it.Value[k] = true, true, 1, float64(i%7)/7
		}
		items[i] = it
	}
	return items
}

// Attack 8: a held-out set above MaxHeldOutPairs is refused before any fit or
// resample runs, with the lever named.
func TestAttackMaxHeldOutPairsRefused(t *testing.T) {
	items := flatItems(3300, func(i int) int { return i % 3 })
	_, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 1, HoldoutPct: 90})
	if err == nil || !strings.Contains(err.Error(), "held-out pairs, max") {
		t.Fatalf("got %v", err)
	}
}

// Attack 2: all held-out items share one label (or the held-out set is a single
// item) is a NotRunError, not a result.
func TestAttackDegenerateHeldOut(t *testing.T) {
	// Every judged item is a yes; marks supply the other grades to train on.
	items := flatItems(300, func(i int) int { return GradeYes })
	for i := 200; i < 300; i++ {
		items[i].Judged = false
		items[i].Grade = GradeNo
	}
	_, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 1})
	var nr *NotRunError
	if !errors.As(err, &nr) {
		t.Fatalf("all-yes held-out: %v", err)
	}
	// One held-out item at most: 150 judged, holdout 1% leaves 0 or 1 held out.
	items = flatItems(150, func(i int) int { return i % 3 })
	res, err := Fit(items, jobinsight.DefaultWeights(), Options{Seed: 1, HoldoutPct: 1})
	if err == nil && res.Beats() {
		t.Fatalf("a 1%% held-out set beat the hand weights: %+v", res)
	}
}
