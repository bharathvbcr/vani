package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// enrichOnly hides insight's Rank and Finish, so BuildList takes the
// whole-Enrich path every list took before it finished only its page.
type enrichOnly struct{ in *insight }

func (e enrichOnly) Enrich(j *jobview.Job, r ledger.Record, kwLimit int) []jobinsight.Hit {
	return e.in.Enrich(j, r, kwLimit)
}

// A list that ranks every job and finishes only its page answers byte for
// byte what enriching every job whole answered, over every view and order and
// the filters that read a ranked field (pay, relevance, age, place).
func TestPagedListMatchesWholeEnrich(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference engineering in Go and Rust, with Kubernetes.")
	seeds := append([]insightSeed(nil), insightSeeds...)
	pays := []string{"$150,000 - $210,000 per year", "€90k–€120k", "USD 60/hour", "", "competitive"}
	places := []string{"Remote - US", "Austin, TX", "Berlin, Germany", "", "San Francisco, CA or Remote"}
	for i := range 40 {
		seeds = append(seeds, insightSeed{
			key: fmt.Sprintf("gh:co%d:%02d", i%6, i), company: fmt.Sprintf("Co %d", i%6),
			title:   []string{"Inference Engineer", "Platform Engineer", "Sales Engineer", "ML Engineer"}[i%4],
			desc:    fmt.Sprintf("Go and Rust on Kubernetes. Location: %s. Pay: %s. Python tooling, CUDA.", places[i%5], pays[i%5]),
			fit:     []string{"strong_fit", "partial_fit", "weak_fit", ""}[i%4],
			status:  []posting.Status{posting.Digested, posting.Scored, posting.Live, posting.Dead}[i%4],
			posted:  jobsT0.AddDate(0, 0, -i),
			seenAgo: 0,
		})
	}
	seedInsight(t, state, seeds...)
	mustCode(t, 0, "jobs", "mark", "gh:co1:01", "saved")
	mustCode(t, 0, "jobs", "mark", "gh:co2:02", "applied")
	mustCode(t, 0, "jobs", "mark", "gh:co3:03", "hidden")

	e := &env{stateDir: state, configPath: cfgPath, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
	snap, m, err := e.jobsState()
	if err != nil {
		t.Fatal(err)
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
	if err != nil {
		t.Fatal(err)
	}
	var opts []jobview.Opts
	for _, v := range jobview.Views {
		for _, s := range jobview.Sorts {
			opts = append(opts, jobview.Opts{View: v, Sort: s, HalfLife: jobinsight.DefaultScoreHalfLifeDays})
		}
	}
	opts = append(opts,
		jobview.Opts{View: "new", Sort: "default", More: jobview.Filter{PayListed: true, Currency: "USD"}},
		jobview.Opts{View: "all", Sort: "priority", More: jobview.Filter{MinPay: 100_000, Currency: "USD"}},
		jobview.Opts{View: "all", Sort: "salary", More: jobview.Filter{Currency: "EUR"}},
		jobview.Opts{View: "new", Sort: "relevance", More: jobview.Filter{MinRelevance: 10}},
		jobview.Opts{View: "all", Sort: "fresh", MaxAge: 10, Open: true},
		jobview.Opts{View: "new", Sort: "default", Remote: true},
		jobview.Opts{View: "new", Sort: "company", Query: "engineer"},
		jobview.Opts{View: "all", Sort: "default", Query: "CO 3"},
		jobview.Opts{View: "all", Sort: "fresh", Query: "inference"},
	)
	// Stats and Select (gaps, audit) agree with the whole-Enrich path too.
	for _, o := range opts {
		if o.View == jobview.ViewPending {
			continue
		}
		a, b := o, o
		got := jobview.StatsFor(snap, m, &a, in, in.info, in.target, "USD")
		sel, _ := jobview.Select(snap, m, &b, enrichOnly{in}, 0)
		want := jobview.StatsOf(sel, in.info, in.target)
		want.Salary = jobview.PayStatsOf(sel, "USD")
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Errorf("stats %s/%s (%+v): differs from whole Enrich\ngot:  %.600s\nwant: %.600s", o.View, o.Sort, o.More, gj, wj)
		}
		c := o
		paged, _ := jobview.Select(snap, m, &c, in, listKeywords)
		d := o
		whole, _ := jobview.Select(snap, m, &d, enrichOnly{in}, listKeywords)
		pj, _ := json.Marshal(paged)
		wj, _ = json.Marshal(whole)
		if string(pj) != string(wj) {
			t.Errorf("select %s/%s (%+v): differs from whole Enrich", o.View, o.Sort, o.More)
		}
	}
	for _, limit := range []int{1, 3, defaultJobsLimit} {
		for _, o := range opts {
			a, b := o, o
			paged := jobview.BuildList(snap, m, &a, in, in.info, listKeywords, limit)
			whole := jobview.BuildList(snap, m, &b, enrichOnly{in}, in.info, listKeywords, limit)
			pj, err := json.Marshal(paged)
			if err != nil {
				t.Fatal(err)
			}
			wj, err := json.Marshal(whole)
			if err != nil {
				t.Fatal(err)
			}
			if string(pj) != string(wj) {
				t.Errorf("view %s sort %s limit %d (%+v): paged list differs from whole Enrich\npaged: %.400s\nwhole: %.400s",
					o.View, o.Sort, limit, o.More, pj, wj)
			}
		}
	}
}
