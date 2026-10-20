package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type scoreSeed struct {
	key, company, title, fit, cov string
	posted                        time.Duration // posted this long before jobsT0; 0 = no posted date
	seenAgo                       time.Duration
	status                        posting.Status
	extra                         map[string]string
}

func seedScores(t *testing.T, stateDir string, seeds ...scoreSeed) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, s := range seeds {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: s.company, Title: s.title}
		if s.posted > 0 {
			p.PostedAt = jobsT0.Add(-s.posted)
		}
		ann := map[string]string{}
		if s.fit != "" {
			ann["triage.role_fit"], ann["triage.role_fit.reading"], ann["triage.role_fit.p"] = s.fit, "answered", "0.9"
		}
		if s.cov != "" {
			ann[ledger.AnnCoverage], ann[ledger.AnnRank] = s.cov, s.cov
		}
		for k, v := range s.extra {
			ann[k] = v
		}
		st := s.status
		if st == "" {
			st = posting.Explained
		}
		e := ledger.Event{V: 1, At: jobsT0.Add(-s.seenAgo), Key: p.Key, Status: st, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

const day = 24 * time.Hour

// The default order is the one ranker's: a fresh strong fit beats a stale,
// high-coverage domain mismatch, which the old rank-first order listed first.
func TestJobsDefaultOrderIsScore(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedScores(t, state,
		scoreSeed{key: "gh:a:first", company: "Acme", title: "Old", status: posting.Dead, seenAgo: 90 * day},
		scoreSeed{key: "gh:a:stale", company: "Acme", title: "Account Executive", fit: "mismatch_domain", cov: "95", posted: 60 * day, seenAgo: 50 * day},
		scoreSeed{key: "gh:a:fresh", company: "Acme", title: "Inference Engineer", fit: "strong_fit", cov: "60", posted: day, seenAgo: day},
	)
	if got := keys(listJSON(t)); got != "gh:a:fresh gh:a:stale" {
		t.Fatalf("default order %q", got)
	}
	if got := keys(listJSON(t, "--sort", "priority")); got != "gh:a:fresh gh:a:stale" {
		t.Fatalf("priority order %q", got)
	}
}

// A posting listed in its board's first crawl with no posted date may be
// arbitrarily old: it does not outrank an equally good, dated, fresh one, and
// the fresh sort puts it after every posting of known age.
func TestJobsFirstCrawlUndatedDoesNotOutrankFreshDated(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedScores(t, state,
		// Board gh:u has one crawl: both postings were listed in it. The dated
		// one is known fresh; the undated one is of unknown age.
		scoreSeed{key: "gh:u:undated", company: "Umbra", title: "Engineer", fit: "strong_fit", cov: "80", seenAgo: day},
		scoreSeed{key: "gh:u:dated", company: "Umbra", title: "Engineer", fit: "strong_fit", cov: "80", posted: 2 * day, seenAgo: day},
	)
	l := listJSON(t)
	if got := keys(l); got != "gh:u:dated gh:u:undated" {
		t.Fatalf("default order %q", got)
	}
	if u := findJob(t, l, "gh:u:undated"); u.AgeDays != nil || !u.FreshAt.IsZero() || u.FreshSrc != "" {
		t.Errorf("an undated first-crawl posting has no known age: %+v %v", u.AgeDays, u.FreshAt)
	}
	if d := findJob(t, l, "gh:u:dated"); d.AgeDays == nil || *d.AgeDays != 2 || d.FreshSrc != "posted" {
		t.Errorf("dated: %v %q", d.AgeDays, d.FreshSrc)
	}
	if got := keys(listJSON(t, "--sort", "fresh")); got != "gh:u:dated gh:u:undated" {
		t.Errorf("fresh order %q", got)
	}
}

// The JSON keeps the keys the Mac app decodes by name and adds score_factors.
func TestJobsJSONKeepsScoreKeysAndAddsFactors(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedScores(t, state, scoreSeed{key: "gh:a:one", company: "Acme", title: "Engineer", fit: "strong_fit", cov: "70", posted: day, seenAgo: day})
	o, _ := mustCode(t, 0, "jobs", "list", "--json")
	var raw struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(o), &raw); err != nil || len(raw.Jobs) != 1 {
		t.Fatalf("reply %q: %v", o, err)
	}
	for _, k := range []string{"relevance", "priority", "relevance_basis", "score_factors"} {
		if _, ok := raw.Jobs[0][k]; !ok {
			t.Errorf("job has no %q: %v", k, raw.Jobs[0])
		}
	}
	j := findJob(t, listJSON(t), "gh:a:one")
	if j.Priority <= 0 || j.Priority > 100 || j.Relevance < j.Priority || j.RelevanceBasis == "" || len(j.ScoreFactors) != 9 {
		t.Errorf("priority %v relevance %v basis %q factors %d", j.Priority, j.Relevance, j.RelevanceBasis, len(j.ScoreFactors))
	}
}

// The configured title signals and drop rules reach the score: an excluded
// title and a kept-by-threshold dropped value each rank below an otherwise
// identical posting.
func TestJobsScoreUsesConfiguredSignalsAndDropRules(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	cfg := config.Default()
	cfg.TitleSignals = config.TitleSignals{Include: []string{"engineer"}, Exclude: []string{"recruiter"}}
	cfg.DropRules = []config.DropRule{{Slot: "sponsorship", Values: []string{"no_sponsorship"}}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	spons := map[string]string{"triage.sponsorship": "no_sponsorship", "triage.sponsorship.reading": "answered", "triage.sponsorship.p": "0.9"}
	seedScores(t, state,
		scoreSeed{key: "gh:a:first", company: "Acme", title: "Old", status: posting.Dead, seenAgo: 90 * day},
		scoreSeed{key: "gh:a:plain", company: "Acme", title: "Software Engineer", fit: "adjacent", cov: "60", posted: day, seenAgo: day},
		scoreSeed{key: "gh:a:excluded", company: "Acme", title: "Recruiter Engineer", fit: "adjacent", cov: "60", posted: day, seenAgo: day},
		scoreSeed{key: "gh:a:kept", company: "Acme", title: "Software Engineer", fit: "adjacent", cov: "60", posted: day, seenAgo: day, extra: spons},
	)
	l := listJSON(t)
	plain := findJob(t, l, "gh:a:plain")
	for _, k := range []string{"gh:a:excluded", "gh:a:kept"} {
		if j := findJob(t, l, k); !(j.Priority < plain.Priority) {
			t.Errorf("%s priority %v is not below the plain twin's %v", k, j.Priority, plain.Priority)
		}
	}
	if keys(l) != "gh:a:plain gh:a:kept gh:a:excluded" && keys(l) != "gh:a:plain gh:a:excluded gh:a:kept" {
		t.Errorf("order %q", keys(l))
	}
}
