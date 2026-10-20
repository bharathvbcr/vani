package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func gapsJSON(t *testing.T, args ...string) jobsGapsJSON {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"jobs", "gaps", "--json"}, args...)...)
	var out jobsGapsJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatalf("gaps reply %q: %v", o, err)
	}
	return out
}

func gapTerms(r jobsGapsJSON) []string {
	var ts []string
	for _, g := range r.Gaps {
		ts = append(ts, g.Term)
	}
	return ts
}

// Two roles DevPrism says lack Kubernetes (one also lacks Terraform), one it
// found nothing missing in.
var gapSeeds = []insightSeed{
	{key: "gh:a:infra", company: "Acme", title: "Inference Engineer", fit: "strong_fit",
		desc: "Serve models in Go on Kubernetes.", missing: "Kubernetes (weak), Terraform",
		status: posting.Digested, posted: jobsT0.Add(-2 * 24 * time.Hour), seenAgo: time.Hour},
	{key: "gh:b:plat", company: "Beta", title: "Platform Engineer", fit: "partial_fit",
		desc: "Run Kubernetes clusters.", missing: "Kubernetes",
		status: posting.Explained, posted: jobsT0.Add(-3 * 24 * time.Hour), seenAgo: time.Hour},
	{key: "gh:c:clean", company: "Cobalt", title: "Go Developer", fit: "strong_fit",
		desc: "Write Go services.", status: posting.Explained,
		posted: jobsT0.Add(-1 * 24 * time.Hour), seenAgo: time.Hour},
}

func TestJobsGapsEmptyLedger(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedJobs(t, state) // an existing, empty ledger
	r := gapsJSON(t)
	if r.Roles != 0 || r.TotalGaps != 0 || r.Gaps == nil || len(r.Gaps) != 0 {
		t.Fatalf("empty ledger: %+v", r)
	}
	if !strings.Contains(string(mustJSON(t, r)), `"gaps":[]`) {
		t.Fatalf("gaps must encode as [], not null: %s", mustJSON(t, r))
	}
	text, _ := mustCode(t, 0, "jobs", "gaps")
	if strings.Contains(text, "no gaps could be judged") {
		t.Fatalf("no roles is not an unjudged set:\n%s", text)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJobsGapsRanksDevPrismGaps(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference in Go and Rust.") // a sentence-initial "Go" would not count
	seedInsight(t, state, gapSeeds...)
	r := gapsJSON(t, "--view", "all")
	if r.View != "all" || r.Roles != 3 || r.WithMissing != 2 || !r.TargetKnown {
		t.Fatalf("report %+v", r)
	}
	if len(r.Gaps) == 0 || r.Gaps[0].Term != "Kubernetes" || r.Gaps[0].Roles != 2 || r.Gaps[0].Missing != 2 || r.Gaps[0].Companies != 2 {
		t.Fatalf("Kubernetes should lead with two roles at two companies: %+v", r.Gaps)
	}
	for i := 1; i < len(r.Gaps); i++ {
		if r.Gaps[i].Weight > r.Gaps[i-1].Weight {
			t.Fatalf("gaps not ordered by weight: %+v", r.Gaps)
		}
	}
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all")
	for _, want := range []string{"all: 3 roles, 2 with a DevPrism gap analysis", "skills to build", " 1. Kubernetes [", "2 roles (2 found missing by DevPrism), 2 companies", "e.g. "} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "target summary was not read") {
		t.Errorf("target was read, but the note was printed:\n%s", text)
	}
}

func TestJobsGapsWithoutTargetSaysSo(t *testing.T) {
	_, state := setup(t) // no config file: no target summary
	fixedNow(t)
	seedInsight(t, state, gapSeeds...)
	r := gapsJSON(t, "--view", "all")
	if r.TargetKnown || r.Insight.Target || r.Insight.Warning == "" {
		t.Fatalf("target must read as unknown: %+v", r)
	}
	if len(r.Gaps) == 0 || r.Gaps[0].Term != "Kubernetes" {
		t.Fatalf("DevPrism gaps are still counted: %+v", r.Gaps)
	}
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all")
	if !strings.Contains(text, "only DevPrism gaps were counted because the target summary was not read") || !strings.Contains(text, "note: keywords are not marked") {
		t.Fatalf("missing the not-judged note:\n%s", text)
	}
}

// Roles with neither a gap analysis nor a target summary were not judged: that
// must never read as "no gaps".
func TestJobsGapsUnjudgedIsNotNoGaps(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedInsight(t, state, gapSeeds[2])
	r := gapsJSON(t, "--view", "all")
	if r.Roles != 1 || r.WithMissing != 0 || r.TargetKnown || len(r.Gaps) != 0 {
		t.Fatalf("report %+v", r)
	}
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all")
	if !strings.Contains(text, "no gaps could be judged") || strings.Contains(text, "no gaps found") {
		t.Fatalf("an unjudged set read as judged:\n%s", text)
	}
}

func TestJobsGapsJudgedWithNoneFound(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference in Go and Rust.")
	seedInsight(t, state, gapSeeds[2])
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all")
	if !strings.Contains(text, "no gaps found") || strings.Contains(text, "could be judged") {
		t.Fatalf("judged and clean:\n%s", text)
	}
}

// A gap analysis naming nothing the vocabulary knows, with no target summary,
// found no DevPrism gaps and judged no keyword gaps: it must not read as a
// clean "no gaps found".
func TestJobsGapsDevPrismOnlyCleanSaysKeywordsUnjudged(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	s := gapSeeds[2]
	s.missing = "a wholly unlisted requirement"
	seedInsight(t, state, s)
	r := gapsJSON(t, "--view", "all")
	if r.Roles != 1 || r.WithMissing != 1 || r.TargetKnown || len(r.Gaps) != 0 {
		t.Fatalf("report %+v", r)
	}
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all")
	if !strings.Contains(text, "no DevPrism gaps found (keyword gaps were not judged)") || strings.Contains(text, "no gaps found") {
		t.Fatalf("a DevPrism-only check read as a full one:\n%s", text)
	}
}

func TestJobsGapsFlagsRejected(t *testing.T) {
	setup(t)
	for _, args := range [][]string{
		{"jobs", "gaps", "-n", "0"},
		{"jobs", "gaps", "-n", "-1"},
		{"jobs", "gaps", "-n", "999"},
		{"jobs", "gaps", "--examples", "0"},
		{"jobs", "gaps", "--examples", "11"},
		{"jobs", "gaps", "--view", "starred"},
		{"jobs", "gaps", "--half-life", "0"},
		{"jobs", "gaps", "extra"},
		{"jobs", "gaps", "--json", "-n", "0"}, // a usage error stays one even with --json
	} {
		mustCode(t, 2, args...)
	}
}

func TestJobsGapsLimitReportsShowing(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference in Go and Rust.")
	seedInsight(t, state, gapSeeds...)
	all := gapsJSON(t, "--view", "all")
	if all.TotalGaps < 2 {
		t.Fatalf("seed should yield at least two gaps: %+v", all)
	}
	one := gapsJSON(t, "--view", "all", "-n", "1")
	if len(one.Gaps) != 1 || one.TotalGaps != all.TotalGaps {
		t.Fatalf("-n 1: %d gaps of %d (want 1 of %d)", len(one.Gaps), one.TotalGaps, all.TotalGaps)
	}
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all", "-n", "1")
	if !strings.Contains(text, "showing 1 of ") {
		t.Fatalf("truncation not reported:\n%s", text)
	}
}

func TestJobsGapsPostingTextCannotBreakLines(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference in Go and Rust.")
	seedInsight(t, state, insightSeed{key: "gh:e:evil", company: "Evil\nCo\x1b[31m", title: "Eng\n# injected\r\n\x07bell",
		desc: "Kubernetes.", missing: "Kubernetes", status: posting.Explained, fit: "strong_fit",
		posted: jobsT0.Add(-time.Hour), seenAgo: time.Hour})
	text, _ := mustCode(t, 0, "jobs", "gaps", "--view", "all")
	if !strings.Contains(text, "e.g. ") {
		t.Fatalf("no example line:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "# injected") || strings.Contains(line, "\x1b") || strings.ContainsAny(line, "\r\x07") {
			t.Fatalf("posting text escaped its line: %q", line)
		}
	}
}

func TestJobsGapsViewFilters(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference in Go and Rust.")
	seedInsight(t, state, gapSeeds...)
	mustCode(t, 0, "jobs", "mark", "gh:a:infra", "saved")
	if r := gapsJSON(t); r.View != "new" || r.Roles != 2 {
		t.Fatalf("new view: %+v", r)
	}
	r := gapsJSON(t, "--view", "saved")
	if r.View != "saved" || r.Roles != 1 || r.WithMissing != 1 {
		t.Fatalf("saved view: %+v", r)
	}
	if got := gapTerms(r); len(got) == 0 || got[0] != "Kubernetes" || r.Gaps[0].Roles != 1 {
		t.Fatalf("saved view gaps: %+v", r.Gaps)
	}
	if r := gapsJSON(t, "--view", "saved", "--q", "no such role"); r.Roles != 0 || r.Query != "no such role" {
		t.Fatalf("query: %+v", r)
	}
}
