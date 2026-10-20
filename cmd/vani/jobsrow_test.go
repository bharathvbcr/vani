package main

import (
	"encoding/json"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The row is the versioned contract every surface reads: `jobs list --json`
// and `jobs find --json` carry it, built by one function from the same job.
func TestJobsListCarriesTheVersionedRow(t *testing.T) {
	_, state := setup(t)
	seedScores(t, state,
		scoreSeed{key: "gh:a:1", company: "Acme", title: "Backend Engineer", fit: "strong_fit", cov: "80", posted: 3 * day, status: posting.Scored},
		scoreSeed{key: "gh:a:2", company: "Acme", title: "Other", status: posting.Triaged, extra: map[string]string{"dup.sibling_of": "gh:a:1"}},
	)
	o, _ := mustCode(t, 0, "jobs", "list", "--json", "--sort", "priority")
	var out struct {
		Jobs []struct {
			Key       string       `json:"key"`
			Relevance float64      `json:"relevance"`
			Priority  float64      `json:"priority"`
			Row       *jobview.Row `json:"row"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 2 {
		t.Fatalf("jobs %d: %s", len(out.Jobs), o)
	}
	for _, j := range out.Jobs {
		if j.Row == nil {
			t.Fatalf("%s has no row: %s", j.Key, o)
		}
		if err := jobview.Check(j.Row.V); err != nil {
			t.Fatal(err)
		}
		if j.Row.Key != j.Key || j.Row.Relevance != j.Relevance || j.Row.Priority != j.Priority {
			t.Errorf("%s: row %+v disagrees with the job's own fields", j.Key, j.Row)
		}
		if j.Row.StatusLabel == "" {
			t.Errorf("%s: no status label", j.Key)
		}
	}
	first := out.Jobs[0].Row
	if first.Key != "gh:a:1" || first.Fit != "strong_fit" || first.FitLabel != "Strong fit" || first.AgeDays == nil || first.AgeLabel != jobview.AgeLabel(first.AgeDays, first.FreshSrc) || first.AgeLabel == "" {
		t.Errorf("first row %+v", first)
	}
	if out.Jobs[1].Row.Similar != 1 {
		t.Errorf("sibling link not counted: %+v", out.Jobs[1].Row)
	}
}

func TestJobsFindCarriesTheRow(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state, findSeed{key: "gh:a:1", url: "https://boards.greenhouse.io/acme/jobs/1", status: posting.Scored})
	out := findJSON(t, "https://boards.greenhouse.io/acme/jobs/1")
	if out.Job == nil || out.Job.Row == nil {
		t.Fatalf("no row on the found job: %+v", out.Job)
	}
	if err := jobview.Check(out.Job.Row.V); err != nil || out.Job.Row.Key != "gh:a:1" || out.Job.Row.Title != out.Job.Title {
		t.Fatalf("row %+v err %v", out.Job.Row, err)
	}
}

// roleFitOrder (cmd/vani's sort order) and the contract's vocabulary are the
// same five options, so a label exists for every one the sort knows.
func TestRoleFitOrderMatchesTheContractVocabulary(t *testing.T) {
	if len(roleFitOrder) != len(jobview.FitOrder) {
		t.Fatalf("%v vs %v", roleFitOrder, jobview.FitOrder)
	}
	for i, v := range roleFitOrder {
		if v != jobview.FitOrder[i] || jobview.FitLabel(v) == "" {
			t.Errorf("option %d: %s vs %s", i, v, jobview.FitOrder[i])
		}
	}
}
