package jobview

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// tieFixture is two postings of equal priority and different relevance, found
// by search over Score itself, so the test does not pin Score's constants.
func tieFixture(t *testing.T) (hi, lo jobinsight.Result) {
	t.Helper()
	type cand struct {
		res jobinsight.Result
	}
	var all []jobinsight.Result
	for _, f := range []string{"strong_fit", "partial_fit", "adjacent"} {
		for age := 0; age < 40; age++ {
			all = append(all, jobinsight.Score(jobinsight.Inputs{
				RoleFit:  jobinsight.Slot{Value: f, Answered: true, P: 0.9, HasP: true},
				Coverage: 60, HasCoverage: true, AgeKnown: true, AgeDays: age,
			}))
		}
	}
	for _, a := range all {
		for _, b := range all {
			if a.Score == b.Score && a.Relevance > b.Relevance {
				return a, b
			}
		}
	}
	t.Fatal("no fixture with equal priority and different relevance")
	return
}

func orderBy(by string, jobs []Job) []string {
	sel := make([]Selected, len(jobs))
	for i, j := range jobs {
		sel[i] = Selected{Job: j}
	}
	Sort(sel, "new", by)
	var out []string
	for _, s := range sel {
		out = append(out, string(s.Job.Key))
	}
	return out
}

// The default and priority sorts break an equal priority exactly as the digest
// does (jobinsight.ComparePriority): by relevance, then key. Rank, which counts
// coverage a second time, is not a tie-break.
func TestSortJobsTieBreakIsTheDigests(t *testing.T) {
	hi, lo := tieFixture(t)
	ranked := func(v float64) *float64 { return &v }
	// Keys and ranks both oppose the relevance order.
	jobs := []Job{
		{Key: "gh:a:a", Priority: lo.Score, Relevance: lo.Relevance, Rank: ranked(99)},
		{Key: "gh:a:z", Priority: hi.Score, Relevance: hi.Relevance, Rank: ranked(1)},
	}
	want := []string{"gh:a:z", "gh:a:a"}
	for _, by := range []string{"default", "priority"} {
		if got := orderBy(by, jobs); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v (relevance breaks the tie)", by, got, want)
		}
	}
	// The same fixture through the digest's comparator.
	cmp := jobinsight.ComparePriority(hi, lo, "gh:a:z", "gh:a:a")
	if cmp >= 0 {
		t.Errorf("ComparePriority puts the lower relevance first: %d", cmp)
	}

	// A full tie falls to the key, whatever the rank, the fit or when it was seen.
	tie := []Job{
		{Key: "gh:a:b", Priority: 50, Relevance: 60, Rank: ranked(10)},
		{Key: "gh:a:a", Priority: 50, Relevance: 60, Rank: ranked(1)},
		{Key: "gh:a:c", Priority: 50, Relevance: 60},
	}
	for _, by := range []string{"default", "priority"} {
		if got := orderBy(by, tie); !slices.Equal(got, []string{"gh:a:a", "gh:a:b", "gh:a:c"}) {
			t.Errorf("%s: full tie order %v, want by key", by, got)
		}
	}
	// The relevance sort is the mirror: relevance first, then priority.
	rel := []Job{
		{Key: "gh:a:a", Priority: 10, Relevance: 80},
		{Key: "gh:a:b", Priority: 90, Relevance: 80},
	}
	if got := orderBy("relevance", rel); !slices.Equal(got, []string{"gh:a:b", "gh:a:a"}) {
		t.Errorf("relevance sort: %v", got)
	}
}

func TestSortJobsNaNAndEmpty(t *testing.T) {
	if got := orderBy("default", nil); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
	nan := Job{Key: "gh:a:b", Priority: nanF(), Relevance: nanF()}
	ok := Job{Key: "gh:a:a", Priority: nanF(), Relevance: nanF()}
	if got := orderBy("priority", []Job{nan, ok}); !slices.Equal(got, []string{"gh:a:a", "gh:a:b"}) {
		t.Errorf("NaN scores must order by key: %v", got)
	}
}

func nanF() float64 { var z float64; return z / z }
