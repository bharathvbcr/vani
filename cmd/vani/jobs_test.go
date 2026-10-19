package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var jobsT0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

type seedJob struct {
	key, title, fit, rank, url string
	status                     posting.Status
	age                        time.Duration
	company                    string // "" is Acme
}

// seedJobs writes a ledger and returns it still open, so every command under
// test runs while the ledger is locked, as it is during a run.
func seedJobs(t *testing.T, stateDir string, jobs ...seedJob) *ledger.File {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, j := range jobs {
		company := j.company
		if company == "" {
			company = "Acme"
		}
		p := posting.Posting{Key: posting.Key(j.key), ATS: "gh", Company: company, Title: j.title, URL: j.url,
			Description: "Build things.\n\nWith people.\x1b[31m"}
		ann := map[string]string{ledger.AnnExplanation: "Fits: Inference serving and runtimes (88%)."}
		if j.fit != "" {
			ann["triage.role_fit"] = j.fit
		}
		if j.rank != "" {
			ann[ledger.AnnRank] = j.rank
		}
		e := ledger.Event{V: 1, At: jobsT0.Add(-j.age), Key: p.Key, Status: j.status, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func listJSON(t *testing.T, args ...string) jobsListJSON {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"jobs", "list", "--json"}, args...)...)
	var out jobsListJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatalf("list reply %q: %v", o, err)
	}
	return out
}

func keys(l jobsListJSON) string {
	var ks []string
	for _, j := range l.Jobs {
		ks = append(ks, string(j.Key))
	}
	return strings.Join(ks, " ")
}

// Postings awaiting triage are not listed, but they are counted: a board whose
// 656 postings all waited for triage read as "no jobs" in the app.
func TestJobsReportPostingsAwaitingTriage(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:anthropic:1", title: "Performance Engineer, Inference", status: posting.Live, company: "Anthropic"},
		seedJob{key: "gh:anthropic:2", title: "Applied AI Engineer", status: posting.Live, company: "Anthropic"},
		seedJob{key: "gh:anthropic:3", title: "Research Engineer", status: posting.Dead, company: "Anthropic"},
		seedJob{key: "ab:cohere:1", title: "ML Engineer", status: posting.Live, company: "Cohere"},
		seedJob{key: "ab:cohere:2", title: "Inference Engineer", fit: "strong_fit", status: posting.Digested, company: "Cohere"},
	)
	l := listJSON(t, "--q", "  ANTHROPIC ")
	if l.Total != 0 || len(l.Jobs) != 0 {
		t.Fatalf("untriaged postings were listed: %s", keys(l))
	}
	want := pendingJSON{Total: 3, Matching: 2, Companies: []pendingCompanyJSON{{"Anthropic", 2}, {"Cohere", 1}}}
	if !reflect.DeepEqual(l.Pending, want) {
		t.Fatalf("pending %+v, want %+v", l.Pending, want)
	}
	if all := listJSON(t); all.Pending.Matching != 3 || all.Total != 1 {
		t.Fatalf("no query: pending %+v, total %d", all.Pending, all.Total)
	}

	o, _ := mustCode(t, 0, "jobs", "stats", "--json", "--q", "inference")
	var s jobsStatsJSON
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatal(err)
	}
	if s.Total != 1 || s.Pending.Total != 3 || s.Pending.Matching != 1 {
		t.Fatalf("stats total %d pending %+v", s.Total, s.Pending)
	}

	text, _ := mustCode(t, 0, "jobs", "list", "--q", "anthropic")
	if !strings.Contains(text, `awaiting triage (not listed until a run triages them): 2 matching "anthropic", 3 in all`) {
		t.Fatalf("text list does not say what waits:\n%s", text)
	}
}

// Nothing waiting: zero, an empty list (not null), and no text line.
func TestJobsPendingEmpty(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state, seedJob{key: "gh:a:1", title: "Engineer", fit: "strong_fit", status: posting.Digested})
	o, _ := mustCode(t, 0, "jobs", "list", "--json")
	if !strings.Contains(o, `"pending":{"total":0,"matching":0,"companies":[]}`) {
		t.Fatalf("reply %s", o)
	}
	if text, _ := mustCode(t, 0, "jobs", "list"); strings.Contains(text, "awaiting triage") {
		t.Fatalf("text names a backlog that is not there:\n%s", text)
	}
}

// The company list is bounded and the total still counts every posting.
func TestJobsPendingCompaniesAreBounded(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < maxPendingCompanies+5; i++ {
		for j := 0; j <= i; j++ {
			k := posting.Key(fmt.Sprintf("gh:c%02d:%d", i, j))
			recs = append(recs, ledger.Record{Key: k, Status: posting.Live, Posting: posting.Posting{Key: k, Company: fmt.Sprintf("C%02d", i)}})
		}
	}
	p := pendingOf(ledger.Snapshot{Records: recs}, nil, "")
	if len(p.Companies) != maxPendingCompanies || p.Total != len(recs) || p.Companies[0].Company != "C24" {
		t.Fatalf("%d companies, total %d of %d, first %+v", len(p.Companies), p.Total, len(recs), p.Companies[0])
	}
}

func TestJobsListOrdersAndFilters(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:dom", title: "Account Executive", fit: "mismatch_domain", status: posting.Digested},
		seedJob{key: "gh:a:strong", title: "Inference Engineer", fit: "strong_fit", status: posting.Digested, age: time.Hour},
		seedJob{key: "gh:a:ranked", title: "ML Systems", fit: "adjacent", rank: "71", status: posting.Explained},
		seedJob{key: "gh:a:partial", title: "Platform Engineer", fit: "partial_fit", status: posting.Scored},
		seedJob{key: "gh:a:dropped", title: "Recruiter", status: posting.Dropped},
		seedJob{key: "gh:a:dead", title: "Old role", status: posting.Dead},
		seedJob{key: "gh:a:live", title: "Untriaged", status: posting.Live},
	)
	l := listJSON(t)
	// Ranked first, then by triage fit; dropped, dead and untriaged are not "new".
	if got, want := keys(l), "gh:a:ranked gh:a:strong gh:a:partial gh:a:dom"; got != want {
		t.Fatalf("order %q, want %q", got, want)
	}
	if l.Total != 4 || l.Shown != 4 || l.Counts["new"] != 4 || l.Counts["saved"] != 0 {
		t.Fatalf("counts %+v total %d shown %d", l.Counts, l.Total, l.Shown)
	}
	if l.Records != 7 || l.Ledger != filepath.Join(state, "ledger") {
		t.Fatalf("the reply names its ledger: %q with %d records", l.Ledger, l.Records)
	}
	if l := listJSON(t, "--limit", "2"); l.Total != 4 || l.Shown != 2 || len(l.Jobs) != 2 {
		t.Fatalf("a capped list says so: total %d shown %d", l.Total, l.Shown)
	}
	if l := listJSON(t, "--q", "ENGINEER"); keys(l) != "gh:a:strong gh:a:partial" {
		t.Fatalf("query: %q", keys(l))
	}
	if l := listJSON(t, "--view", "saved"); len(l.Jobs) != 0 || l.Jobs == nil {
		t.Fatalf("an empty view is an empty array, not null: %+v", l)
	}
}

func TestJobsMarkMovesBetweenViewsAndKeepsTheNote(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:1", title: "Inference Engineer", fit: "strong_fit", status: posting.Digested},
		seedJob{key: "gh:a:2", title: "Platform Engineer", fit: "partial_fit", status: posting.Digested},
		seedJob{key: "gh:a:dead", title: "Closed role", status: posting.Dead},
	)
	o, _ := mustCode(t, 0, "jobs", "mark", "--json", "gh:a:1", "saved")
	var j jobJSON
	if err := json.Unmarshal([]byte(o), &j); err != nil || j.Mark != marks.Saved || j.MarkedAt.IsZero() || j.Title != "Inference Engineer" {
		t.Fatalf("mark reply %q (%v)", o, err)
	}
	mustCode(t, 0, "jobs", "note", "--json", "gh:a:1", "  ask about on-call  ")
	if l := listJSON(t); keys(l) != "gh:a:2" || l.Counts["saved"] != 1 {
		t.Fatalf("a saved job leaves New: %q %+v", keys(l), l.Counts)
	}
	mustCode(t, 0, "jobs", "mark", "gh:a:1", "applied")
	l := listJSON(t, "--view", "applied")
	if keys(l) != "gh:a:1" || l.Jobs[0].Note != "ask about on-call" || l.Counts["saved"] != 0 {
		t.Fatalf("applied view %+v", l)
	}
	// A marked role that closed stays in its view, flagged.
	mustCode(t, 0, "jobs", "mark", "gh:a:dead", "saved")
	if l := listJSON(t, "--view", "saved"); keys(l) != "gh:a:dead" || !l.Jobs[0].Closed {
		t.Fatalf("closed saved role %+v", l)
	}
	mustCode(t, 0, "jobs", "mark", "gh:a:2", "hidden")
	if l := listJSON(t); len(l.Jobs) != 0 || l.Counts["hidden"] != 1 || l.Counts["all"] != 3 {
		t.Fatalf("after hiding: %q %+v", keys(l), l.Counts)
	}
	mustCode(t, 0, "jobs", "mark", "gh:a:2", "clear")
	if l := listJSON(t); keys(l) != "gh:a:2" {
		t.Fatalf("clear returns it to New: %q", keys(l))
	}
	if l := listJSON(t, "--q", "on-call", "--view", "all"); keys(l) != "gh:a:1" {
		t.Fatalf("the note is searchable: %q", keys(l))
	}
}

func TestJobsRefusals(t *testing.T) {
	_, state := setup(t)
	// No ledger yet: an error reply, not an empty list that looks like "no jobs".
	o, _ := mustCode(t, 1, "jobs", "list", "--json")
	if !strings.Contains(o, `"error"`) {
		t.Fatalf("missing ledger: %q", o)
	}
	seedJobs(t, state, seedJob{key: "gh:a:1", title: "Eng", status: posting.Digested})
	for _, args := range [][]string{
		{"jobs"},
		{"jobs", "wat"},
		{"jobs", "list", "--view", "starred"},
		{"jobs", "list", "--limit", "0"},
		{"jobs", "list", "--limit", "999999"},
		{"jobs", "list", "--q", strings.Repeat("x", maxQueryBytes+1)},
		{"jobs", "list", "extra"},
		{"jobs", "mark", "gh:a:1"},
		{"jobs", "mark", "gh:a:1", "starred"},
		{"jobs", "mark", "gh:a:1", ""},
		{"jobs", "mark", "no-ats-key", "saved"},
		{"jobs", "mark", "gh:a:1\x1b[2J", "saved"},
		{"jobs", "note", "gh:a:1"},
		{"jobs", "show"},
	} {
		mustCode(t, 2, args...)
	}
	o, _ = mustCode(t, 1, "jobs", "mark", "--json", "gh:a:typo", "saved")
	if !strings.Contains(o, "no posting") {
		t.Fatalf("unknown key: %q", o)
	}
	o, _ = mustCode(t, 1, "jobs", "note", "--json", "gh:a:1", strings.Repeat("x", marks.MaxNoteBytes+1))
	if !strings.Contains(o, `"error"`) {
		t.Fatalf("oversized note: %q", o)
	}
	if m, err := marks.Read(marks.Dir(state)); err != nil || len(m) != 0 {
		t.Fatalf("refused writes left marks: %+v %v", m, err)
	}
}

// Posting text is untrusted: links are http(s) only and text is plain.
func TestJobsShowIsSafeToRender(t *testing.T) {
	_, state := setup(t)
	seedJobs(t, state,
		seedJob{key: "gh:a:1", title: "Eng\x1b[2J lead", url: "javascript:alert(1)", status: posting.Digested},
		seedJob{key: "gh:a:2", title: "Eng", url: "https://boards.example.com/a/2", status: posting.Digested},
	)
	o, _ := mustCode(t, 0, "jobs", "show", "--json", "gh:a:1")
	var j jobJSON
	if err := json.Unmarshal([]byte(o), &j); err != nil {
		t.Fatal(err)
	}
	if j.URL != "" || j.Title != "Eng [2J lead" {
		t.Fatalf("url %q title %q", j.URL, j.Title)
	}
	if j.Description != "Build things.\nWith people. [31m" {
		t.Fatalf("description %q", j.Description)
	}
	o, _ = mustCode(t, 0, "jobs", "show", "--json", "gh:a:2")
	json.Unmarshal([]byte(o), &j)
	if j.URL != "https://boards.example.com/a/2" {
		t.Fatalf("url %q", j.URL)
	}
	if l := listJSON(t, "--view", "all"); l.Jobs[0].Description != "" {
		t.Fatal("list replies carry no descriptions; show does")
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct {
		in        string
		max       int
		keepLines bool
		want      string
	}{
		{"", 10, false, ""},
		{"  a \t b  ", 10, false, "a b"},
		{"a\x00b\x7fc", 10, false, "a b c"},
		{"bad\xffutf8", 20, false, "bad�utf8"},
		{strings.Repeat("é", 10), 4, false, "éééé…"},
		{"a\n\n b\n", 10, true, "a\nb"},
		{"a\nb", 10, false, "a b"},
	}
	for _, c := range cases {
		if got := cleanText(c.in, c.max, c.keepLines); got != c.want {
			t.Errorf("cleanText(%q, %d, %v) = %q, want %q", c.in, c.max, c.keepLines, got, c.want)
		}
	}
}
