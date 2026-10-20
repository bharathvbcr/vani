package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type insightSeed struct {
	key, company, title, desc, fit, workMode, missing, note string
	status                                                  posting.Status
	posted                                                  time.Time
	seenAgo                                                 time.Duration
}

func seedInsight(t *testing.T, stateDir string, seeds ...insightSeed) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, s := range seeds {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: s.company, Title: s.title,
			Description: s.desc, PostedAt: s.posted}
		ann := map[string]string{}
		for k, v := range map[string]string{"triage.role_fit": s.fit, "triage.work_mode": s.workMode,
			ledger.AnnMissing: s.missing, ledger.AnnExplanation: s.note} {
			if v != "" {
				ann[k] = v
			}
		}
		if s.fit != "" {
			ann["triage.role_fit.reading"] = "answered"
		}
		e := ledger.Event{V: 1, At: jobsT0.Add(-s.seenAgo), Key: p.Key, Status: s.status, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

// fixedNow pins the clock the scores are measured against.
func fixedNow(t *testing.T) {
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
}

func writeTarget(t *testing.T, cfgPath, summary string) {
	t.Helper()
	cfg := config.Default()
	cfg.TargetSummary = summary
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

var insightSeeds = []insightSeed{
	{key: "gh:a:infra", company: "Acme", title: "Inference Engineer", fit: "strong_fit", workMode: "remote",
		desc:    "We serve models in Go and Rust on Kubernetes. Python helps.",
		missing: "Kubernetes (weak)", status: posting.Digested,
		note:   "Fits: Inference serving and runtimes (88%). Gap: Kubernetes depth (40%). Check: Visa policy.",
		posted: jobsT0.Add(-2 * 24 * time.Hour), seenAgo: time.Hour},
	{key: "gh:b:sales", company: "Beta", title: "Account Executive", fit: "mismatch_domain", workMode: "onsite",
		desc: "Sell to enterprises. Excel and Salesforce.", status: posting.Explained, seenAgo: 40 * 24 * time.Hour},
	{key: "gh:c:unread", company: "Cobalt", title: "Platform Engineer", desc: "Terraform and AWS.", status: posting.Unsure,
		posted: jobsT0.Add(-9 * 24 * time.Hour), seenAgo: 8 * 24 * time.Hour},
	{key: "gh:a:gone", company: "Acme", title: "Old role", status: posting.Dead},
	// Beta's board was first crawled 90 days ago, so gh:b:sales (40 days) was
	// put up at most a run before it was seen: its age is known.
	{key: "gh:b:old", company: "Beta", title: "Old role", status: posting.Dead, seenAgo: 90 * 24 * time.Hour},
}

func findJob(t *testing.T, l jobsListJSON, key string) jobJSON {
	t.Helper()
	for _, j := range l.Jobs {
		if string(j.Key) == key {
			return j
		}
	}
	t.Fatalf("no %s in %q", key, keys(l))
	return jobJSON{}
}

func TestJobsInsightFields(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference engineering in Go and Rust, with Kubernetes.")
	seedInsight(t, state, insightSeeds...)
	l := listJSON(t, "--view", "all")
	if !l.Insight.Target || l.Insight.TargetTerms != 4 || l.Insight.Warning != "" || l.Sort != "default" {
		t.Fatalf("insight %+v sort %q", l.Insight, l.Sort)
	}
	a := findJob(t, l, "gh:a:infra")
	if a.FreshSrc != "posted" || a.AgeDays == nil || *a.AgeDays != 2 || !a.FreshAt.Equal(jobsT0.Add(-48*time.Hour)) {
		t.Errorf("posted freshness: %s %v %v", a.FreshSrc, a.AgeDays, a.FreshAt)
	}
	kw := map[string]jobinsight.Keyword{}
	for _, k := range a.Keywords {
		kw[k.Term] = k
	}
	if !kw["Go"].Target || !kw["Rust"].Target || !kw["Kubernetes"].Target || !kw["Kubernetes"].Gap || kw["Python"].Target || kw["Python"].Term == "" {
		t.Errorf("keywords %+v", a.Keywords)
	}
	if a.Keywords[len(a.Keywords)-1].Term != "Python" {
		t.Errorf("target terms come first: %+v", a.Keywords)
	}
	if len(a.Reasons) != 3 || a.Reasons[0].Kind != "fits" || a.Reasons[0].Text != "Inference serving and runtimes" ||
		a.Reasons[0].Pct == nil || *a.Reasons[0].Pct != 88 || a.Reasons[2].Kind != "check" || a.Reasons[2].Pct != nil {
		t.Errorf("reasons %+v", a.Reasons)
	}
	b := findJob(t, l, "gh:b:sales")
	if b.FreshSrc != "seen" || b.AgeDays == nil || *b.AgeDays != 40 || len(b.Keywords) != 0 {
		t.Errorf("first-seen freshness: %s %v, keywords %+v", b.FreshSrc, b.AgeDays, b.Keywords)
	}
	if !(a.Relevance > b.Relevance && a.Priority > b.Priority) || a.RelevanceBasis != jobinsight.BasisFit {
		t.Errorf("relevance %v/%v priority %v/%v basis %s", a.Relevance, b.Relevance, a.Priority, b.Priority, a.RelevanceBasis)
	}
	if c := findJob(t, l, "gh:c:unread"); c.RelevanceBasis != jobinsight.BasisNone {
		t.Errorf("no fit reading: basis %s", c.RelevanceBasis)
	}

	for sortBy, want := range map[string]string{
		"priority":  "gh:a:infra gh:c:unread gh:b:sales",
		"relevance": "gh:a:infra gh:c:unread gh:b:sales",
		"fresh":     "gh:a:infra gh:c:unread gh:b:sales",
		"company":   "gh:a:infra gh:b:sales gh:c:unread",
	} {
		if got := keys(listJSON(t, "--sort", sortBy)); got != want {
			t.Errorf("--sort %s: %q, want %q", sortBy, got, want)
		}
	}
	// The default order is priority: the fresh strong fit, the unread job, then the 40-day-old mismatch.
	if got := keys(listJSON(t)); got != "gh:a:infra gh:c:unread gh:b:sales" {
		t.Errorf("default order %q", got)
	}
}

func TestJobsInsightFilters(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedInsight(t, state, insightSeeds...)
	mustCode(t, 0, "jobs", "mark", "gh:a:gone", "saved")
	for args, want := range map[string]string{
		"--fit strong_fit":                  "gh:a:infra",
		"--fit strong_fit,mismatch_domain":  "gh:a:infra gh:b:sales",
		"--fit none":                        "gh:c:unread",
		"--work-mode onsite":                "gh:b:sales",
		"--work-mode none --fit none":       "gh:c:unread",
		"--max-age 10":                      "gh:a:infra gh:c:unread",
		"--max-age 1":                       "",
		"--view saved":                      "gh:a:gone",
		"--view saved --open":               "",
		"--view all --sponsorship none":     "gh:a:infra gh:a:gone gh:c:unread gh:b:sales", // by priority: a fresh unread job leads an old mismatch
		"--view all --open --sort priority": "gh:a:infra gh:c:unread gh:b:sales",
	} {
		if got := keys(listJSON(t, strings.Fields(args)...)); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
	// Counts per view ignore the filters.
	if l := listJSON(t, "--fit", "strong_fit"); l.Counts["new"] != 3 || l.Total != 1 {
		t.Errorf("counts %+v total %d", l.Counts, l.Total)
	}
	for _, args := range [][]string{
		{"jobs", "list", "--sort", "bogus"},
		{"jobs", "list", "--fit", "great"},
		{"jobs", "list", "--fit", "strong_fit,"},
		{"jobs", "list", "--work-mode", "strong_fit"},
		{"jobs", "list", "--max-age", "-1"},
		{"jobs", "list", "--max-age", "99999"},
		{"jobs", "list", "--half-life", "0"},
		{"jobs", "list", "--half-life", "NaN"},
		{"jobs", "list", "--half-life", "1000"},
		{"jobs", "stats", "--view", "starred"},
		{"jobs", "stats", "--sort", "priority"}, // stats has no order
		{"jobs", "stats", "extra"},
	} {
		mustCode(t, 2, args...)
	}
}

func TestJobsStats(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference in Go and Rust.") // a sentence-initial "Go" would not count
	seedInsight(t, state, insightSeeds...)
	o, _ := mustCode(t, 0, "jobs", "stats", "--json", "--view", "all")
	var s jobsStatsJSON
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatalf("stats %q: %v", o, err)
	}
	if s.Total != 3 || s.Fit["strong_fit"] != 1 || s.Fit["mismatch_domain"] != 1 || s.Fit["none"] != 1 ||
		s.WorkMode["remote"] != 1 || s.Basis["fit"] != 2 || s.Basis["none"] != 1 || s.Records != 5 {
		t.Fatalf("stats %+v", s)
	}
	ages, posted := 0, 0
	for _, b := range s.Age {
		ages += b.Posted + b.Seen
		posted += b.Posted
	}
	if ages != 3 || posted != 2 || s.FreshSource["posted"] != 2 || s.FreshSource["seen"] != 1 || s.Age[0].Label != "≤1d" {
		t.Errorf("age %+v source %+v", s.Age, s.FreshSource)
	}
	sum := 0
	for _, n := range s.Relevance {
		sum += n
	}
	if len(s.Relevance) != 10 || sum != 3 || len(s.Priority) != 10 {
		t.Errorf("relevance buckets %v", s.Relevance)
	}
	if len(s.Companies) != 3 || s.Companies[0].Company != "Acme" || s.Companies[0].Strong != 1 {
		t.Errorf("companies %+v", s.Companies)
	}
	var goTerm *keywordStatJSON
	for i, k := range s.Keywords {
		if k.Term == "Go" {
			goTerm = &s.Keywords[i]
		}
	}
	if goTerm == nil || goTerm.N != 1 || !goTerm.Target {
		t.Errorf("keywords %+v", s.Keywords)
	}
	// A filtered stats call aggregates only the filtered jobs.
	o, _ = mustCode(t, 0, "jobs", "stats", "--json", "--fit", "strong_fit")
	if err := json.Unmarshal([]byte(o), &s); err != nil || s.Total != 1 {
		t.Errorf("filtered stats %q", o)
	}
	o, _ = mustCode(t, 0, "jobs", "stats")
	if !strings.Contains(o, "new: 3 jobs") || !strings.Contains(o, "Go 1") {
		t.Errorf("text stats %q", o)
	}
}

// Without a target summary the keywords are listed but unmarked, and the reply
// says why, so "nothing matched" is never what an unread config looks like.
func TestJobsInsightWithoutTarget(t *testing.T) {
	_, state := setup(t) // no config file at all
	fixedNow(t)
	seedInsight(t, state, insightSeeds...)
	l := listJSON(t)
	if l.Insight.Target || l.Insight.Warning == "" {
		t.Fatalf("insight %+v", l.Insight)
	}
	for _, k := range findJob(t, l, "gh:a:infra").Keywords {
		if k.Target {
			t.Errorf("%s marked without a target", k.Term)
		}
	}
	if o, _ := mustCode(t, 0, "jobs", "list"); !strings.Contains(o, "note: keywords are not marked") {
		t.Errorf("text list hides the warning: %q", o)
	}
}

// A term every one of a company's postings repeats is its blurb, not an ask.
func TestJobsKeywordsSkipCompanyBlurb(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	var seeds []insightSeed
	for i := range jobinsight.BoilerplateMin {
		desc := "We build analytics and agents."
		if i == 0 {
			desc += " This role writes Rust."
		}
		seeds = append(seeds, insightSeed{key: fmt.Sprintf("gh:blurb:%d", i), company: "Blurb", title: "Engineer",
			desc: desc, fit: "partial_fit", status: posting.Digested})
	}
	seedInsight(t, state, seeds...)
	l := listJSON(t, "--view", "all")
	if l.Insight.Boilerplate != 1 {
		t.Errorf("boilerplate %d", l.Insight.Boilerplate)
	}
	j := findJob(t, l, "gh:blurb:0")
	if len(j.Keywords) != 1 || j.Keywords[0].Term != "Rust" {
		t.Errorf("keywords %+v", j.Keywords)
	}
	o, _ := mustCode(t, 0, "jobs", "show", "--json", "gh:blurb:0")
	var shown jobJSON
	if err := json.Unmarshal([]byte(o), &shown); err != nil || len(shown.Keywords) != 1 {
		t.Errorf("show keywords %q", o)
	}
}

func TestParseReasons(t *testing.T) {
	pct := func(p int) *int { return &p }
	cases := []struct {
		in   string
		want []reasonJSON
	}{
		{"", nil},
		{"Fits: A (91%).", []reasonJSON{{"fits", "A", pct(91)}}},
		{"Fits: A (91%). Gap: B. Check: C (5%).", []reasonJSON{{"fits", "A", pct(91)}, {"gap", "B", nil}, {"check", "C", pct(5)}}},
		{"Gap: Go. Rust. (40%).", []reasonJSON{{"gap", "Go. Rust.", pct(40)}}},
		{"Fits: odd (250%).", []reasonJSON{{"fits", "odd (250%)", nil}}},
		{"No listed reason applies.", []reasonJSON{{"other", "No listed reason applies.", nil}}},
		{"Fits: A. trailing words", []reasonJSON{{"fits", "A. trailing words", nil}}},
		{"Gap: B. Fits: A.", []reasonJSON{{"gap", "B", nil}, {"fits", "A", nil}}},
		{"Fits: ", []reasonJSON{{"other", "Fits:", nil}}},
		{"Fits: (91%).", []reasonJSON{{"other", "Fits: (91%).", nil}}},
	}
	for _, c := range cases {
		got := parseReasons(c.in)
		if fmt.Sprint(reasonStrings(got)) != fmt.Sprint(reasonStrings(c.want)) {
			t.Errorf("parseReasons(%q) = %v, want %v", c.in, reasonStrings(got), reasonStrings(c.want))
		}
	}
}

func reasonStrings(rs []reasonJSON) []string {
	var out []string
	for _, r := range rs {
		p := "-"
		if r.Pct != nil {
			p = fmt.Sprint(*r.Pct)
		}
		out = append(out, r.Kind+"|"+r.Text+"|"+p)
	}
	return out
}
