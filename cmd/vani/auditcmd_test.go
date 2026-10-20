package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type auditNextJSON struct {
	Week     string      `json:"week"`
	Items    []auditItem `json:"items"`
	Warnings []string    `json:"warnings"`
}

func auditFixture(t *testing.T) string {
	t.Helper()
	_, state := setup(t)
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	var jobs []seedJob
	for i := 0; i < 30; i++ {
		jobs = append(jobs, seedJob{key: fmt.Sprintf("gh:k:%02d", i), title: fmt.Sprintf("Engineer %d", i), fit: "strong_fit",
			rank: fmt.Sprint(i * 3), status: posting.Digested, url: "https://example.com/" + fmt.Sprint(i)})
	}
	for i := 0; i < 10; i++ {
		jobs = append(jobs, seedJob{key: fmt.Sprintf("gh:d:%02d", i), title: fmt.Sprintf("Dropped %d", i), fit: "mismatch_domain", status: posting.Dropped})
	}
	jobs = append(jobs,
		seedJob{key: "gh:h:1", title: "Hidden one", status: posting.Digested, fit: "strong_fit"},
		seedJob{key: "gh:x:1", title: "Dead one", status: posting.Dead},
		seedJob{key: "gh:esc:1", title: "Evil\x1b[2J\r\nTitle", status: posting.Digested, fit: "partial_fit"},
	)
	seedJobs(t, state, jobs...)
	if _, err := marks.SetMark(marks.Dir(state), "gh:h:1", marks.Hidden, jobsT0); err != nil {
		t.Fatal(err)
	}
	return state
}

func auditNext(t *testing.T, args ...string) auditNextJSON {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"audit", "next", "--json"}, args...)...)
	var out auditNextJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatalf("%v: %s", err, o)
	}
	return out
}

func TestAuditNextSamplesKeptAndDropped(t *testing.T) {
	auditFixture(t)
	b := auditNext(t)
	if b.Week != "2026-W42" || len(b.Items) != 20 {
		t.Fatalf("week %s, %d items", b.Week, len(b.Items))
	}
	var dropped int
	for _, it := range b.Items {
		if it.Key == "gh:h:1" || it.Key == "gh:x:1" {
			t.Fatalf("sampled %s (hidden or dead)", it.Key)
		}
		if it.Status == string(posting.Dropped) {
			dropped++
			if !strings.HasPrefix(it.Source, "dropped:") {
				t.Fatalf("dropped item source %q", it.Source)
			}
		}
		if strings.ContainsAny(it.Title, "\x1b\r\n") {
			t.Fatalf("board text not flattened: %q", it.Title)
		}
	}
	if dropped != 6 {
		t.Fatalf("%d dropped of 20, want 6", dropped)
	}
	// The same week gives the same batch.
	if again := auditNext(t); fmt.Sprint(again.Items) != fmt.Sprint(b.Items) {
		t.Fatal("the batch changed within a week")
	}
	// No config: it still samples, and says why drop classes are unknown.
	if len(b.Warnings) == 0 || !strings.Contains(strings.Join(b.Warnings, " "), "drop rules unknown") {
		t.Fatalf("warnings %v", b.Warnings)
	}
}

func TestAuditJudgeRecordsScoresAndSource(t *testing.T) {
	state := auditFixture(t)
	b := auditNext(t)
	first := b.Items[0]
	mustCode(t, 0, "audit", "judge", string(first.Key), "YES")
	mustCode(t, 0, "audit", "judge", "gh:k:29", "maybe")
	got, err := feedback.ReadJudgments(state)
	if err != nil || len(got.Rows) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	j := got.Rows[0]
	if j.Verdict != feedback.VerdictYes || j.Source != first.Source || j.Status != string(posting.Digested) && j.Status != string(posting.Dropped) {
		t.Fatalf("judgment %+v", j)
	}
	if len(j.Factors) == 0 {
		t.Fatal("judgment kept no score factors")
	}
	// A judged posting leaves the batch.
	for _, it := range auditNext(t).Items {
		if it.Key == first.Key {
			t.Fatal("a judged posting is still in the batch")
		}
	}
	for _, bad := range [][]string{
		{"audit", "judge", "gh:k:01"},
		{"audit", "judge", "gh:k:01", "apply"},
		{"audit", "judge", "", "yes"},
		{"audit", "judge", "gh:k:01", "yes", "extra"},
		{"audit", "next", "-n", "0"},
		{"audit", "next", "-n", "101"},
		{"audit", "bogus"},
		{"audit"},
	} {
		mustCode(t, 2, bad...)
	}
	mustCode(t, 1, "audit", "judge", "gh:nope:1", "yes")
}

func TestAuditRunReadsAnswersAndStopsCleanly(t *testing.T) {
	state := auditFixture(t)
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("y\n\nwhat\nn\nm\ns\nq\n")
	o, _ := mustCode(t, 0, "audit", "run", "-n", "10")
	if !strings.Contains(o, "judged 4 of 10") {
		t.Fatalf("stdout:\n%s", o)
	}
	got, _ := feedback.ReadJudgments(state)
	var vs []string
	for _, j := range got.Rows {
		vs = append(vs, j.Verdict)
	}
	if strings.Join(vs, ",") != "yes,no,maybe,skip" {
		t.Fatalf("verdicts %v", vs)
	}
	// End of input stops without an error and keeps what was answered.
	stdin = strings.NewReader("n\n")
	if o, _ := mustCode(t, 0, "audit", "run", "-n", "3"); !strings.Contains(o, "judged 1 of 3") {
		t.Fatalf("eof run:\n%s", o)
	}
	// An answer line past the bound is an error, not a silent stop.
	stdin = strings.NewReader(strings.Repeat("y", maxAuditAnswer+10) + "\n")
	mustCode(t, 1, "audit", "run", "-n", "1")
}

func TestAuditStatsSaysWhatCannotRunYet(t *testing.T) {
	state := auditFixture(t)
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		v := feedback.VerdictNo
		if i == 0 {
			v = feedback.VerdictYes
		}
		if err := feedback.AppendJudgment(ctx, state, feedback.Judgment{At: jobsT0, Key: posting.Key(fmt.Sprintf("gh:d:%02d", i)),
			Verdict: v, Source: "dropped:title:role_fit"}); err != nil {
			t.Fatal(err)
		}
	}
	o, _ := mustCode(t, 0, "audit", "stats")
	for _, want := range []string{"judged 6 (6 usable", "calibration: cannot run yet, 94 more", "weight fit: cannot run yet, 144 more",
		"title:role_fit: 17% (1 of 6)", "explore: unmeasured (0 judged, 5 needed)", "marks: saved 0, hidden 1"} {
		if !strings.Contains(o, want) {
			t.Errorf("stats missing %q:\n%s", want, o)
		}
	}
	js, _ := mustCode(t, 0, "audit", "stats", "--json")
	var s struct {
		Usable          int  `json:"usable"`
		CalibrationNeed int  `json:"calibration_need"`
		WeightFitReady  bool `json:"weight_fit_ready"`
	}
	if err := json.Unmarshal([]byte(js), &s); err != nil || s.Usable != 6 || s.CalibrationNeed != 94 || s.WeightFitReady {
		t.Fatalf("%+v %v", s, err)
	}
}

// The digest's Explore picks come first in the batch, as explore.
func TestAuditBatchLeadsWithExplorePicks(t *testing.T) {
	state := auditFixture(t)
	if err := feedback.AppendImpressions(context.Background(), state, []feedback.Impression{
		{At: jobsT0, Surface: feedback.SurfaceDigest, Section: feedback.SectionExplore, Key: "gh:d:07", Position: 1},
		{At: jobsT0, Surface: feedback.SurfaceDigest, Section: feedback.SectionExplore, Key: "gh:gone:1", Position: 2},
	}); err != nil {
		t.Fatal(err)
	}
	b := auditNext(t)
	if b.Items[0].Key != "gh:d:07" || b.Items[0].Source != feedback.SourceExplore {
		t.Fatalf("first item %+v", b.Items[0])
	}
	for _, it := range b.Items {
		if it.Key == "gh:gone:1" {
			t.Fatal("a pick no longer in the ledger was listed")
		}
	}
	mustCode(t, 0, "audit", "judge", "gh:d:07", "no")
	got, _ := feedback.ReadJudgments(state)
	if got.Rows[0].Source != feedback.SourceExplore {
		t.Fatalf("source %q", got.Rows[0].Source)
	}
}
