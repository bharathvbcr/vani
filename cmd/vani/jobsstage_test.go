package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func markJSON(t *testing.T, code int, key, word string) jobview.Job {
	t.Helper()
	o, e := mustCode(t, code, "jobs", "mark", "--json", key, word)
	var j jobview.Job
	if err := json.Unmarshal([]byte(o), &j); err != nil {
		t.Fatalf("reply %q (stderr %q): %v", o, e, err)
	}
	return j
}

// `jobs mark` sets a stage on an applied job, in the one command that sets the
// marks. The mark stays applied in every reply and every view; the stage is
// beside it.
func TestJobsMarkSetsStagesAndListFiltersByThem(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:1", title: "Inference Engineer", fit: "strong_fit", status: posting.Digested},
		seedJob{key: "gh:a:2", title: "Platform Engineer", fit: "partial_fit", status: posting.Digested},
		seedJob{key: "gh:a:3", title: "Data Engineer", fit: "adjacent", status: posting.Digested},
	)
	// A stage needs an applied job: saved, hidden and unmarked are refused.
	mustCode(t, 0, "jobs", "mark", "gh:a:1", "saved")
	if j := markJSON(t, 1, "gh:a:1", "interview"); j.Mark != "" {
		t.Fatalf("a refusal is the error reply, got %+v", j)
	}
	mustCode(t, 1, "jobs", "mark", "gh:a:3", "interview")

	mustCode(t, 0, "jobs", "mark", "gh:a:1", "applied")
	mustCode(t, 0, "jobs", "mark", "gh:a:2", "applied")
	j := markJSON(t, 0, "gh:a:1", "screening")
	appliedAt := j.MarkedAt
	if j.Mark != marks.Applied || j.Stage != marks.StageScreening || j.StageAt.IsZero() || appliedAt.IsZero() {
		t.Fatalf("%+v", j)
	}
	j = markJSON(t, 0, "gh:a:1", "interview")
	j = markJSON(t, 0, "gh:a:1", "rejected")
	if j.Mark != marks.Applied || j.Stage != marks.StageRejected || j.FurthestStage != marks.StageInterview || !j.MarkedAt.Equal(appliedAt) {
		t.Fatalf("a rejection after an interview: %+v", j)
	}
	// The extension's "mark applied" after submit cannot undo it.
	j = markJSON(t, 0, "gh:a:1", "applied")
	if j.Mark != marks.Applied || j.Stage != marks.StageRejected || !j.MarkedAt.Equal(appliedAt) {
		t.Fatalf("applied again changed the stage: %+v", j)
	}
	// Leaving applied needs the stage set back first; unstage does it.
	mustCode(t, 1, "jobs", "mark", "gh:a:1", "hidden")
	j = markJSON(t, 0, "gh:a:1", "unstage")
	if j.Stage != marks.StageNone || j.FurthestStage != marks.StageInterview || j.Mark != marks.Applied {
		t.Fatalf("unstage: %+v", j)
	}
	mustCode(t, 2, "jobs", "mark", "gh:a:2", "ghosted")   // not a word
	mustCode(t, 2, "jobs", "mark", "gh:a:2", "Interview") // the words are lower case
	markJSON(t, 0, "gh:a:1", "offer")

	// The views see applied for both; the stage filter narrows them.
	if l := listJSON(t, "--view", "applied"); keys(l) != "gh:a:1 gh:a:2" && keys(l) != "gh:a:2 gh:a:1" {
		t.Fatalf("applied view: %q", keys(l))
	}
	if l := listJSON(t, "--view", "applied", "--stage", "offer"); keys(l) != "gh:a:1" || l.Jobs[0].Stage != marks.StageOffer {
		t.Fatalf("--stage offer: %q", keys(l))
	}
	if l := listJSON(t, "--view", "applied", "--stage", "applied"); keys(l) != "gh:a:2" {
		t.Fatalf("--stage applied is applied with no stage yet: %q", keys(l))
	}
	if l := listJSON(t, "--view", "all", "--stage", "offer,applied"); len(l.Jobs) != 2 {
		t.Fatalf("a list of stages: %q", keys(l))
	}
	if l := listJSON(t, "--view", "applied", "--stage", "withdrawn"); len(l.Jobs) != 0 || l.Jobs == nil {
		t.Fatalf("no match is an empty array: %+v", l)
	}
	mustCode(t, 2, "jobs", "list", "--stage", "offer")                        // new view: no applied jobs in it
	mustCode(t, 2, "jobs", "list", "--view", "applied", "--stage", "ghosted") // unknown stage
	if o, _ := mustCode(t, 0, "jobs", "list", "--view", "applied"); !strings.Contains(o, "offer") {
		t.Fatalf("the text list does not show the stage:\n%s", o)
	}
}
