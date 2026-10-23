package rankeval

import (
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// headline runs the headline comparison for the ranker named "Score" against
// the legacy rankers and returns every failed assertion:
//   - sharp recency: Score's mean NDCG@10 beats every legacy ranker, in every
//     scenario;
//   - gentle recency: it is within 0.01 of the best legacy ranker or better.
//
// logf receives the metric table.
func headline(p *prepared, score ranker, logf func(string, ...any)) []string {
	var fails []string
	all := append([]ranker{score}, legacyRankers...)
	for ui, u := range utilities {
		for si, sc := range scenarios {
			logf("== %s recency | %s | %d seeds | REGRESSION-ONLY (synthetic worlds)", u.name, sc.name, len(p.worlds))
			res := map[string]result{}
			for _, r := range all {
				x := p.evaluate(r, si, ui)
				res[r.name] = x
				logf("  %-15s ndcg@10 mean %.3f min %.3f | ndcg@25 mean %.3f min %.3f | first grade-3 at %.1f",
					r.name, x.n10.mean, x.n10.min, x.n25.mean, x.n25.min, x.first3)
			}
			for _, r := range referenceRankers {
				x := p.evaluate(r, si, ui)
				logf("  %-15s ndcg@10 mean %.3f min %.3f | ndcg@25 mean %.3f min %.3f | first grade-3 at %.1f (reference)",
					r.name, x.n10.mean, x.n10.min, x.n25.mean, x.n25.min, x.first3)
			}
			best, bestName := -1.0, ""
			for _, lr := range legacyRankers {
				if v := res[lr.name].n10.mean; v > best {
					best, bestName = v, lr.name
				}
			}
			got := res[score.name].n10.mean
			if u.name == "sharp" {
				for _, lr := range legacyRankers {
					if v := res[lr.name].n10.mean; !(got > v) {
						fails = append(fails, fmt.Sprintf("sharp/%s: %s ndcg@10 %.3f does not beat %s %.3f", sc.name, score.name, got, lr.name, v))
					}
				}
			} else if !(got >= best-0.01) {
				fails = append(fails, fmt.Sprintf("gentle/%s: %s ndcg@10 %.3f is worse than best legacy %s %.3f by more than 0.01", sc.name, score.name, got, bestName, best))
			}
		}
	}
	return fails
}

// TestScoreBeatsLegacy is the headline comparison for the shipped ranker.
// REGRESSION-ONLY: it ranks synthetic worlds (see world_test.go), so it guards
// against Score regressing against the legacy rankers, and says nothing about
// relevance to the owner.
func TestScoreBeatsLegacy(t *testing.T) {
	p := prepare(evalSeeds)
	for _, f := range headline(p, scoreRanker("Score", jobinsight.DefaultWeights()), t.Logf) {
		t.Errorf("FAIL %s", f)
	}
}

// TestHeadlineRejectsStub proves the harness discriminates: a ranker that
// scores every posting zero (what the temporary Score stub did) fails every
// sharp and gentle assertion.
func TestHeadlineRejectsStub(t *testing.T) {
	p := prepare(evalSeeds)
	zero := ranker{name: "ZeroStub", order: func(os []obs) []int {
		return scoreRankerFunc(os, func(jobinsight.Inputs) float64 { return 0 })
	}}
	fails := headline(p, zero, func(string, ...any) {})
	want := len(scenarios)*len(legacyRankers) + len(scenarios) // sharp: one per legacy; gentle: one per scenario
	if len(fails) != want {
		t.Errorf("zero-score stub failed %d assertions, want all %d:\n%v", len(fails), want, fails)
	}
	t.Logf("zero-score stub fails %d/%d headline assertions (e.g. %s)", len(fails), want, fails[0])
}
