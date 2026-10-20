package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestJobsCategoryFilter(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	// Postings with different categories
	p1 := posting.Posting{Key: "gh:a:1", Company: "AI Corp", Title: "LLM Engineer"}
	p2 := posting.Posting{Key: "gh:a:2", Company: "Agent Inc", Title: "Agent Platform Dev"}
	p3 := posting.Posting{Key: "gh:a:3", Company: "Classic Corp", Title: "Software Engineer"} // no category

	appendRecord(ctx, t, l, p1, posting.Triaged, map[string]string{
		ledger.AnnCategory:               jobcategory.AIEngineer,
		ledger.AnnTriagePrefix + "category": jobcategory.AIEngineer,
	}, now)
	appendRecord(ctx, t, l, p2, posting.Triaged, map[string]string{
		ledger.AnnCategory:               jobcategory.AgentSystems,
		ledger.AnnTriagePrefix + "category": jobcategory.AgentSystems,
	}, now)
	appendRecord(ctx, t, l, p3, posting.Triaged, map[string]string{}, now)

	var stdout, stderr bytes.Buffer
	e := &env{stdout: &stdout, stderr: &stderr, stateDir: dir}

	// 1. Filter single category
	stdout.Reset()
	if err := e.cmdJobs([]string{"list", "--json", "--category", jobcategory.AIEngineer}); err != nil {
		t.Fatalf("jobs list error: %v", err)
	}
	var res jobsListJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, stdout.String())
	}
	if len(res.Jobs) != 1 || res.Jobs[0].Key != p1.Key || res.Jobs[0].Category != jobcategory.AIEngineer {
		t.Fatalf("got jobs: %+v, want only p1", res.Jobs)
	}

	// 2. Filter multiple categories
	stdout.Reset()
	if err := e.cmdJobs([]string{"list", "--json", "--category", jobcategory.AIEngineer + "," + jobcategory.AgentSystems}); err != nil {
		t.Fatalf("jobs list error: %v", err)
	}
	stdoutStr := stdout.String()
	var resMulti jobsListJSON
	if err := json.Unmarshal([]byte(stdoutStr), &resMulti); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if len(resMulti.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(resMulti.Jobs))
	}

	// 3. Filter uncategorized ("none")
	stdout.Reset()
	if err := e.cmdJobs([]string{"list", "--json", "--category", "none"}); err != nil {
		t.Fatalf("jobs list error: %v", err)
	}
	var resNone jobsListJSON
	if err := json.Unmarshal(stdout.Bytes(), &resNone); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if len(resNone.Jobs) != 1 || resNone.Jobs[0].Key != p3.Key {
		t.Fatalf("got jobs: %+v, want only p3", resNone.Jobs)
	}

	// 4. Test --folder alias
	stdout.Reset()
	if err := e.cmdJobs([]string{"list", "--json", "--folder", jobcategory.AgentSystems}); err != nil {
		t.Fatalf("jobs list --folder error: %v", err)
	}
	var resFolder jobsListJSON
	if err := json.Unmarshal(stdout.Bytes(), &resFolder); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if len(resFolder.Jobs) != 1 || resFolder.Jobs[0].Key != p2.Key {
		t.Fatalf("got jobs: %+v, want only p2", resFolder.Jobs)
	}

	// 5. Refuses invalid category
	stdout.Reset()
	stderr.Reset()
	if err := e.cmdJobs([]string{"list", "--category", "invalid_cat"}); err == nil {
		t.Fatal("expected error for invalid category")
	}
}

func TestJobsFoldersCommand(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	p1 := posting.Posting{Key: "gh:a:1", Company: "AI Corp", Title: "LLM Engineer"}
	p2 := posting.Posting{Key: "gh:a:2", Company: "AI Corp 2", Title: "AI Dev"}
	p3 := posting.Posting{Key: "gh:a:3", Company: "Agent Corp", Title: "Agent Dev"}
	p4 := posting.Posting{Key: "gh:a:4", Company: "Other Corp", Title: "Designer"}

	appendRecord(ctx, t, l, p1, posting.Triaged, map[string]string{ledger.AnnCategory: jobcategory.AIEngineer}, now)
	appendRecord(ctx, t, l, p2, posting.Triaged, map[string]string{ledger.AnnCategory: jobcategory.AIEngineer}, now)
	appendRecord(ctx, t, l, p3, posting.Triaged, map[string]string{ledger.AnnCategory: jobcategory.AgentSystems}, now)
	appendRecord(ctx, t, l, p4, posting.Triaged, map[string]string{}, now) // uncategorized

	var stdout, stderr bytes.Buffer
	e := &env{stdout: &stdout, stderr: &stderr, stateDir: dir}

	// JSON mode
	stdout.Reset()
	if err := e.cmdJobs([]string{"folders", "--json"}); err != nil {
		t.Fatalf("jobs folders error: %v", err)
	}
	var res jobsFoldersJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, stdout.String())
	}
	if res.Total != 4 {
		t.Errorf("total = %d, want 4", res.Total)
	}
	folderCounts := map[string]int{}
	for _, f := range res.Folders {
		folderCounts[f.Category] = f.Count
	}
	if folderCounts[jobcategory.AIEngineer] != 2 {
		t.Errorf("ai-engineer count = %d, want 2", folderCounts[jobcategory.AIEngineer])
	}
	if folderCounts[jobcategory.AgentSystems] != 1 {
		t.Errorf("agent-systems count = %d, want 1", folderCounts[jobcategory.AgentSystems])
	}
	if folderCounts["none"] != 1 {
		t.Errorf("uncategorized (none) count = %d, want 1", folderCounts["none"])
	}

	// Text mode
	stdout.Reset()
	if err := e.cmdJobs([]string{"categories"}); err != nil {
		t.Fatalf("jobs categories error: %v", err)
	}
	text := stdout.String()
	if !strings.Contains(text, "ai-engineer") || !strings.Contains(text, "AI Engineer") {
		t.Errorf("text output missing ai-engineer: %s", text)
	}
}

func TestAdvanceSingleFlowDecisionWithCategory(t *testing.T) {
	dir := t.TempDir()
	l, err := ledger.Open(filepath.Join(dir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	p := posting.Posting{Key: "gh:test:1", Company: "Anthropic", Title: "AI Engineer", Description: "Build production LLMs"}

	if err := l.Append(ctx, ledger.Event{
		V: ledger.SchemaVersion, At: now, Key: p.Key, Status: posting.Live, Posting: &p,
	}); err != nil {
		t.Fatal(err)
	}

	// Fake decider that answers all 5 slots in one single flow
	answeredSlots := map[string]decide.SlotReading{}
	var askedQuestion decide.Question
	fakeDecider := decideFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		askedQuestion = q
		ans := decide.Answer{
			Backend: "d1-ultra",
			Slots: map[string]decide.SlotReading{
				"role_fit":    {Reading: decide.Answered, Value: "strong_fit", Jev: &decide.JevCalib{Probability: 0.95}},
				"sponsorship": {Reading: decide.Answered, Value: "sponsors_visa", Jev: &decide.JevCalib{Probability: 0.98}},
				"work_mode":   {Reading: decide.Answered, Value: "remote", Jev: &decide.JevCalib{Probability: 0.99}},
				"seniority":   {Reading: decide.Answered, Value: "mid", Jev: &decide.JevCalib{Probability: 0.92}},
				"category":    {Reading: decide.Answered, Value: jobcategory.AIEngineer, Jev: &decide.JevCalib{Probability: 0.96}},
			},
		}
		for k, v := range ans.Slots {
			answeredSlots[k] = v
		}
		return ans, nil
	})

	triager := &jdtriage.Triager{
		D:             fakeDecider,
		TargetSummary: "AI Engineering",
		ExtraSlots:    []decide.Slot{jobcategory.DecideSlot()},
	}

	runner := &pipeline.Runner{
		Ledger:      l,
		Triager:     triager,
		SkipScoring: true,
		SkipExplain: true,
		Now:         func() time.Time { return now },
	}

	if err := runner.Advance(ctx, p.Key); err != nil {
		t.Fatalf("Advance failed: %v", err)
	}

	// Verify all 5 slots were asked in ONE question
	if len(askedQuestion.Slots) != 5 {
		t.Fatalf("asked slots = %d, want 5 (one flow)", len(askedQuestion.Slots))
	}
	hasCategorySlot := false
	for _, s := range askedQuestion.Slots {
		if s.Name == "category" {
			hasCategorySlot = true
			break
		}
	}
	if !hasCategorySlot {
		t.Error("category slot missing from unified question")
	}

	// Verify ledger record
	rec, ok := l.Get(p.Key)
	if !ok {
		t.Fatal("posting missing from ledger")
	}
	if rec.Status != posting.Scored && rec.Status != posting.Explained {
		t.Errorf("status = %s, want scored/explained", rec.Status)
	}
	if rec.Annotations[ledger.AnnCategory] != jobcategory.AIEngineer {
		t.Errorf("AnnCategory = %q, want %q", rec.Annotations[ledger.AnnCategory], jobcategory.AIEngineer)
	}
	if rec.Annotations[ledger.AnnTriagePrefix+"category"] != jobcategory.AIEngineer {
		t.Errorf("triage.category = %q, want %q", rec.Annotations[ledger.AnnTriagePrefix+"category"], jobcategory.AIEngineer)
	}
	if rec.Annotations[ledger.AnnTriagePrefix+"category.p"] != "0.9600" {
		t.Errorf("triage.category.p = %q, want 0.9600", rec.Annotations[ledger.AnnTriagePrefix+"category.p"])
	}
}

type decideFunc func(ctx context.Context, q decide.Question) (decide.Answer, error)

func (f decideFunc) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	return f(ctx, q)
}

func appendRecord(ctx context.Context, t *testing.T, l ledger.Ledger, p posting.Posting, st posting.Status, ann map[string]string, at time.Time) {
	t.Helper()
	if err := l.Append(ctx, ledger.Event{
		V:           ledger.SchemaVersion,
		At:          at,
		Key:         p.Key,
		Status:      st,
		Posting:     &p,
		Annotations: ann,
	}); err != nil {
		t.Fatal(err)
	}
}
