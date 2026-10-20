package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// `jobs mark` reports the priority `jobs list` shows for that job: its insight
// is built from the marks as written, so the company factor (the company's
// marked postings, this one included) is the same.
func TestJobsMarkReportsTheListsPriority(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:1", title: "One", fit: "strong_fit", status: posting.Explained, age: time.Hour},
		seedJob{key: "gh:a:2", title: "Two", fit: "strong_fit", status: posting.Explained, age: time.Hour},
		seedJob{key: "gh:a:3", title: "Three", fit: "strong_fit", status: posting.Explained, age: time.Hour})
	mustCode(t, 0, "jobs", "mark", "gh:a:2", "saved")
	mustCode(t, 0, "jobs", "mark", "gh:a:3", "saved")
	o, _ := mustCode(t, 0, "jobs", "mark", "--json", "gh:a:1", "saved")
	var marked jobJSON
	if err := json.Unmarshal([]byte(o), &marked); err != nil {
		t.Fatalf("mark reply %q: %v", o, err)
	}
	listed := findJob(t, listJSON(t, "--view", "all"), "gh:a:1")
	if listed.Priority <= 0 {
		t.Fatalf("list priority %v", listed.Priority)
	}
	if marked.Priority != listed.Priority || marked.Relevance != listed.Relevance {
		t.Errorf("mark reports priority %v relevance %v, list shows %v / %v",
			marked.Priority, marked.Relevance, listed.Priority, listed.Relevance)
	}
	if !strings.Contains(o, `"relevance_known"`) {
		t.Errorf("mark reply lacks relevance_known: %s", o)
	}
}
