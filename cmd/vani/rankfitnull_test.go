package main

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedNullJudgments writes n would_apply answers whose verdicts follow the HAND
// weights' own priority plus logistic noise: a world where the fitted shares
// have nothing real to find. Every judgment is from one scored decile with the
// same inclusion probability, so the audit's strata do not matter here.
func seedNullJudgments(t *testing.T, stateDir string, n int, seed uint64) {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(stateDir, "eval", "labels"), filepath.Join(stateDir, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(seed, 7))
	fits := []string{"strong_fit", "partial_fit", "adjacent"}
	type row struct {
		in  jobinsight.Inputs
		u   float64
		key posting.Key
	}
	rows := make([]row, n)
	for i := range rows {
		in := jobinsight.Inputs{
			RoleFit:  jobinsight.Slot{Value: fits[rng.IntN(3)], Answered: true, HasP: true, P: 0.5 + 0.5*rng.Float64()},
			Coverage: float64(rng.IntN(101)), HasCoverage: true,
			Semantic: rng.Float64(), HasSemantic: rng.IntN(4) > 0,
			AgeKnown: true, AgeDays: rng.IntN(30),
		}
		rows[i] = row{in: in, key: posting.Key(fmt.Sprintf("gh:n:%04d", i))}
		rows[i].u = jobinsight.Score(in).Score + 6*math.Log(1/rng.Float64()-1)
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		switch {
		case rows[a].u < rows[b].u:
			return -1
		case rows[a].u > rows[b].u:
			return 1
		}
		return 0
	})
	imps := make([]feedback.Impression, 0, n)
	for rank, i := range order {
		v := "no"
		switch {
		case rank >= 2*n/3:
			v = "yes"
		case rank >= n/3:
			v = "maybe"
		}
		r := rows[i]
		if err := s.Put(eval.Entry{Key: r.key, Labels: map[string]string{eval.WouldApply: v}, Source: "decile:5", PSelect: 0.5}); err != nil {
			t.Fatal(err)
		}
		imps = append(imps, feedback.Impression{At: jobsT0, SlateID: "audit-null", Surface: feedback.SurfaceAudit, Key: r.key, Position: rank + 1,
			Source: "decile:5", PSelect: 0.5, Scores: feedback.ScoresOf(jobinsight.Score(r.in))})
	}
	for len(imps) > 0 {
		m := min(len(imps), feedback.MaxAppend)
		if _, err := feedback.AppendImpressions(context.Background(), stateDir, imps[:m]); err != nil {
			t.Fatal(err)
		}
		imps = imps[m:]
	}
}

// A null world through the whole command: for each of 20 seeds the run must
// refuse with errRankfitNoGain and write no file (A3-1). A bare accuracy
// comparison wrote a weights file for a good share of them. The seeds share no
// state (each builds its own env, so no t.Setenv), so they run as parallel
// subtests; the clock is pinned once, before they start.
func TestRankfitNullWorldWritesNothing(t *testing.T) {
	const seeds = 20
	fixedNow(t)
	for seed := uint64(1); seed <= seeds; seed++ {
		t.Run(fmt.Sprintf("seed%02d", seed), func(t *testing.T) {
			t.Parallel()
			x := newIsoEnv(t)
			x.mustCode(t, 0, "init")
			seedNullJudgments(t, x.stateDir, 400, 500+seed)
			o, e := x.mustCode(t, 1, "rankfit", "--part", "weights", "--seed", fmt.Sprint(seed))
			if !strings.Contains(e, errRankfitNoGain.Error()) {
				t.Errorf("seed %d: stderr lacks the no-gain refusal:\n%s\n%s", seed, o, e)
			}
			if !strings.Contains(o, "held-out pairs") || !strings.Contains(o, "gain interval") {
				t.Errorf("seed %d: output does not print n and the interval:\n%s", seed, o)
			}
			if _, err := os.Stat(weightsPath(x.stateDir)); !os.IsNotExist(err) {
				t.Errorf("seed %d: a refused run left a weights file: %v", seed, err)
			}
		})
	}
}
