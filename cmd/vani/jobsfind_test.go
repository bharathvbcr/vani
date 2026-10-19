package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/joburl"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type findSeed struct {
	key, url, apply string
	status          posting.Status
}

// seedFind writes a ledger of postings that carry links and leaves it open, as
// seedJobs does.
func seedFind(t *testing.T, stateDir string, jobs ...findSeed) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for _, j := range jobs {
		p := posting.Posting{Key: posting.Key(j.key), ATS: "gh", Company: "Acme", Title: "Role " + j.key, URL: j.url, ApplyURL: j.apply}
		e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: j.status, Posting: &p}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func findJSON(t *testing.T, url string) jobFindJSON {
	t.Helper()
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", url)
	var out jobFindJSON
	if err := json.Unmarshal([]byte(o), &out); err != nil {
		t.Fatalf("find reply %q: %v", o, err)
	}
	return out
}

func TestJobsFindMatchesAPostingByItsPageOrItsApplyPage(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "gh:a:1", url: "https://boards.greenhouse.io/acme/jobs/1", status: posting.Digested},
		findSeed{key: "lv:a:2", url: "https://jobs.lever.co/acme/uuid-2", apply: "https://jobs.lever.co/acme/uuid-2/apply", status: posting.Scored},
		findSeed{key: "gh:a:dead", url: "https://boards.greenhouse.io/acme/jobs/9", status: posting.Dead},
	)
	cases := []struct {
		name, url, key, on string
	}{
		{"exact", "https://boards.greenhouse.io/acme/jobs/1", "gh:a:1", "url"},
		{"query and fragment stripped", "https://boards.greenhouse.io/acme/jobs/1?gh_src=x&token=secret#app", "gh:a:1", "url"},
		{"trailing slash and host case", "https://Boards.Greenhouse.io/acme/jobs/1/", "gh:a:1", "url"},
		{"default port", "https://boards.greenhouse.io:443/acme/jobs/1", "gh:a:1", "url"},
		{"the posting page of an apply-link board", "https://jobs.lever.co/acme/uuid-2", "lv:a:2", "url"},
		{"apply_url", "https://jobs.lever.co/acme/uuid-2/apply?lever-source=x", "lv:a:2", "apply_url"},
		{"a closed role is still found", "https://boards.greenhouse.io/acme/jobs/9", "gh:a:dead", "url"},
	}
	for _, c := range cases {
		out := findJSON(t, c.url)
		if out.Job == nil || string(out.Job.Key) != c.key || out.Job.MatchedOn != c.on || out.Matches != 1 || out.Ambiguous {
			t.Errorf("%s: %+v job %+v", c.name, out, out.Job)
		}
		if out.Job != nil && (out.Job.Company != "Acme" || out.Job.Title != "Role "+c.key) {
			t.Errorf("%s: job fields %+v", c.name, out.Job)
		}
	}
	if out := findJSON(t, "https://boards.greenhouse.io/acme/jobs/9"); !out.Job.Closed {
		t.Fatalf("a closed role says so: %+v", out.Job)
	}
	out := findJSON(t, "https://boards.greenhouse.io/acme/jobs/1?x=1")
	if out.URL != "https://boards.greenhouse.io/acme/jobs/1" || out.Records != 3 || out.Ledger != filepath.Join(state, "ledger") {
		t.Fatalf("the reply names what was compared and where: %+v", out)
	}
}

func TestJobsFindNoMatchIsAnAnswerNotAnError(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state, findSeed{key: "gh:a:1", url: "https://boards.greenhouse.io/acme/jobs/1", status: posting.Digested})
	for _, url := range []string{
		"https://boards.greenhouse.io/acme/jobs/2",
		"https://boards.greenhouse.io/acme/jobs",
		"https://boards.greenhouse.io/acme/jobs/1/extra",
		"https://evil.example/acme/jobs/1",
		"http://boards.greenhouse.io/acme/jobs/1", // another scheme is another page
		"https://boards.greenhouse.io.evil.example/acme/jobs/1",
	} {
		out := findJSON(t, url)
		if out.Job != nil || out.Matches != 0 || out.Ambiguous || out.Records != 1 {
			t.Errorf("%s: %+v", url, out)
		}
	}
	// The body keys are always present for a client that decodes by name.
	o, _ := mustCode(t, 0, "jobs", "find", "--json", "--url", "https://nothing.example/x")
	if strings.Contains(o, `"job"`) || !strings.Contains(o, `"matches":0`) || !strings.Contains(o, `"records":1`) {
		t.Fatalf("no-match reply %q", o)
	}
}

func TestJobsFindReturnsTheMarkSoTheExtensionCanSkipAppliedJobs(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state, findSeed{key: "gh:a:1", url: "https://boards.greenhouse.io/acme/jobs/1", status: posting.Digested})
	if out := findJSON(t, "https://boards.greenhouse.io/acme/jobs/1"); out.Job.Mark != "" {
		t.Fatalf("unmarked: %+v", out.Job)
	}
	mustCode(t, 0, "jobs", "mark", "gh:a:1", "applied")
	if out := findJSON(t, "https://boards.greenhouse.io/acme/jobs/1"); out.Job == nil || out.Job.Mark != marks.Applied {
		t.Fatalf("marked applied: %+v", out.Job)
	}
	// Finding never writes a mark.
	mustCode(t, 0, "jobs", "mark", "gh:a:1", "clear")
	findJSON(t, "https://boards.greenhouse.io/acme/jobs/1")
	if m, err := marks.Read(marks.Dir(state)); err != nil || len(m) != 0 {
		t.Fatalf("find left marks: %+v %v", m, err)
	}
}

// Two live postings that share a page (a board that tells jobs apart by a
// query parameter) are not guessed between; a duplicate of a posting is not a
// second claimant.
func TestJobsFindDoesNotGuessBetweenPostings(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "gh:a:1", url: "https://careers.example.com/jobs?opening=1", status: posting.Digested},
		findSeed{key: "gh:a:2", url: "https://careers.example.com/jobs?opening=2", status: posting.Digested},
		findSeed{key: "gh:a:3", url: "https://careers.example.com/open/3", status: posting.Digested},
		findSeed{key: "lv:a:3dup", url: "https://careers.example.com/open/3", status: posting.Duplicate},
		findSeed{key: "lv:a:only-dup", url: "https://careers.example.com/open/4", status: posting.Duplicate},
		findSeed{key: "gh:a:5", url: "https://careers.example.com/open/5", apply: "https://careers.example.com/open/5-apply", status: posting.Digested},
		findSeed{key: "gh:a:6", url: "https://careers.example.com/open/6", apply: "https://careers.example.com/open/5-apply", status: posting.Digested},
	)
	out := findJSON(t, "https://careers.example.com/jobs?opening=2")
	if out.Job != nil || !out.Ambiguous || out.Matches != 2 {
		t.Fatalf("two postings on one page: %+v", out)
	}
	if out := findJSON(t, "https://careers.example.com/open/5-apply"); out.Job != nil || !out.Ambiguous {
		t.Fatalf("two apply links on one page: %+v", out)
	}
	out = findJSON(t, "https://careers.example.com/open/3")
	if out.Job == nil || out.Job.Key != "gh:a:3" || out.Matches != 2 || out.Ambiguous {
		t.Fatalf("the live posting wins over its duplicate: %+v", out)
	}
	if out := findJSON(t, "https://careers.example.com/open/4"); out.Job == nil || out.Job.Key != "lv:a:only-dup" {
		t.Fatalf("a lone duplicate still answers: %+v", out)
	}
}

// Greenhouse serves every posting of a company that uses its own careers page
// from one path and tells them apart by gh_jid; that parameter is the identity.
func TestJobsFindUsesTheParametersThatNameAJob(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "gh:a:1", url: "https://www.example.com/careers?gh_jid=4001", status: posting.Digested},
		findSeed{key: "gh:a:2", url: "https://www.example.com/careers?gh_jid=4002&gh_src=x", status: posting.Digested},
	)
	for url, key := range map[string]string{
		"https://www.example.com/careers?gh_jid=4001":                          "gh:a:1",
		"https://www.example.com/careers/?gh_jid=4002&utm_source=x#apply":      "gh:a:2",
		"https://www.example.com/careers?session=SECRET&gh_jid=4001&email=a@b": "gh:a:1",
	} {
		if out := findJSON(t, url); out.Job == nil || string(out.Job.Key) != key || out.Ambiguous {
			t.Errorf("%s: %+v", url, out)
		}
	}
	if out := findJSON(t, "https://www.example.com/careers?gh_jid=4003"); out.Job != nil || out.Matches != 0 {
		t.Fatalf("another job id on the same path is another job: %+v", out)
	}
	if out := findJSON(t, "https://www.example.com/careers"); out.Job != nil || out.Matches != 0 {
		t.Fatalf("the bare path is not either job: %+v", out)
	}
}

func TestJobsFindKnowsGreenhouseEmbedsAndBoardHosts(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "gh:acme:123", url: "https://boards.greenhouse.io/acme/jobs/123", status: posting.Digested},
		findSeed{key: "gh:beta:77", url: "https://job-boards.greenhouse.io/beta/jobs/77", status: posting.Digested},
	)
	for url, key := range map[string]string{
		"https://boards.greenhouse.io/embed/job_app?for=acme&token=123":           "gh:acme:123",
		"https://boards.greenhouse.io/embed/job_app?token=123&for=acme&gh_src=ab": "gh:acme:123",
		"https://job-boards.greenhouse.io/acme/jobs/123":                          "gh:acme:123",
		"https://boards.greenhouse.io/beta/jobs/77":                               "gh:beta:77",
		"https://boards.greenhouse.io/embed/job_app?for=beta&token=77":            "gh:beta:77",
	} {
		if out := findJSON(t, url); out.Job == nil || string(out.Job.Key) != key {
			t.Errorf("%s: %+v", url, out)
		}
	}
	if out := findJSON(t, "https://boards.greenhouse.io/embed/job_app?for=acme&token=999"); out.Job != nil {
		t.Fatalf("an embed of another job: %+v", out)
	}
	if out := findJSON(t, "https://boards.greenhouse.io/embed/job_app?for=acme&token=12x"); out.Job != nil {
		t.Fatalf("a token that is not a job id: %+v", out)
	}
}

// An application page is a sub-page of the posting: /apply, /application,
// Workday's /apply/applyManually. An exact link wins over the fallback.
func TestJobsFindMatchesApplicationSubPages(t *testing.T) {
	_, state := setup(t)
	wd := "https://acme.wd5.myworkdayjobs.com/en-US/careers/job/Austin/Engineer_JR1"
	seedFind(t, state,
		findSeed{key: "wd:acme:1", url: wd, status: posting.Digested},
		findSeed{key: "lv:a:2", url: "https://jobs.lever.co/acme/uuid-2", status: posting.Digested},
		findSeed{key: "ab:a:3", url: "https://jobs.ashbyhq.com/acme/uuid-3", apply: "https://jobs.ashbyhq.com/acme/uuid-3/application", status: posting.Digested},
		findSeed{key: "ab:a:4", url: "https://jobs.ashbyhq.com/acme/uuid-4/application", status: posting.Digested},
	)
	cases := []struct{ url, key, on string }{
		{wd + "/apply/applyManually?q=1", "wd:acme:1", "base"},
		{wd + "/apply", "wd:acme:1", "base"},
		{"https://jobs.lever.co/acme/uuid-2/apply", "lv:a:2", "base"},
		{"https://jobs.ashbyhq.com/acme/uuid-3/application", "ab:a:3", "apply_url"}, // exact wins
		{"https://jobs.ashbyhq.com/acme/uuid-3/application/thanks", "ab:a:3", "base"}, // a page under the application
		{"https://jobs.ashbyhq.com/acme/uuid-4", "ab:a:4", "base"},                   // the posting page of a stored apply link
		{"https://jobs.ashbyhq.com/acme/uuid-4/application", "ab:a:4", "url"},
	}
	for _, c := range cases {
		out := findJSON(t, c.url)
		switch {
		case c.key == "" && (out.Job != nil || out.Ambiguous):
			t.Errorf("%s: %+v", c.url, out)
		case c.key != "" && (out.Job == nil || string(out.Job.Key) != c.key || out.Job.MatchedOn != c.on):
			t.Errorf("%s: %+v job %+v", c.url, out, out.Job)
		}
	}
	// A sub-page of a posting Vani never saw is not matched to a neighbour.
	for _, url := range []string{"https://jobs.lever.co/acme/uuid-9/apply", "https://jobs.lever.co/apply", "https://jobs.lever.co/acme/apply"} {
		if out := findJSON(t, url); out.Job != nil {
			t.Errorf("%s matched %+v", url, out.Job)
		}
	}
}

func TestJobsFindSubPageFallbackDoesNotGuess(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "lv:a:1", url: "https://jobs.lever.co/acme/same", status: posting.Digested},
		findSeed{key: "lv:b:1", url: "https://jobs.lever.co/acme/same/apply", status: posting.Digested},
	)
	// Two postings reduce to the same base: ambiguous, not the first one.
	if out := findJSON(t, "https://jobs.lever.co/acme/same/application"); out.Job != nil || !out.Ambiguous || out.Matches != 2 {
		t.Fatalf("%+v", out)
	}
	// But the page one of them names exactly is exact.
	if out := findJSON(t, "https://jobs.lever.co/acme/same/apply"); out.Job == nil || out.Job.Key != "lv:b:1" {
		t.Fatalf("%+v", out)
	}
}

func TestJobsFindIgnoresUnusableStoredLinks(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state,
		findSeed{key: "gh:a:1", url: "javascript:alert(1)", apply: "not a url", status: posting.Digested},
		findSeed{key: "gh:a:2", url: "", status: posting.Digested},
		findSeed{key: "gh:a:3", url: strings.Repeat("h", joburl.MaxBytes+1), status: posting.Digested},
	)
	if out := findJSON(t, "https://example.com/"); out.Job != nil || out.Matches != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestJobsFindRefusals(t *testing.T) {
	_, state := setup(t)
	// No ledger yet: an error reply, not a no-match that looks like "untracked".
	o, _ := mustCode(t, 1, "jobs", "find", "--json", "--url", "https://example.com/a")
	if !strings.Contains(o, `"error"`) {
		t.Fatalf("missing ledger: %q", o)
	}
	seedFind(t, state, findSeed{key: "gh:a:1", url: "https://example.com/a", status: posting.Digested})
	for _, args := range [][]string{
		{"jobs", "find"},
		{"jobs", "find", "--json"},
		{"jobs", "find", "--url", ""},
		{"jobs", "find", "--url", "   "},
		{"jobs", "find", "--url", "javascript:alert(1)"},
		{"jobs", "find", "--url", "file:///etc/passwd"},
		{"jobs", "find", "--url", "/relative/path"},
		{"jobs", "find", "--url", "https://example.com/a b"},
		{"jobs", "find", "--url", "https://example.com/a\x1b[2J"},
		{"jobs", "find", "--url", "https://example.com/" + strings.Repeat("a", joburl.MaxBytes)},
		{"jobs", "find", "--url", "https://example.com/a?" + strings.Repeat("q", joburl.MaxBytes)},
		{"jobs", "find", "--url", "https://example.com/a", "extra"},
		{"jobs", "find", "https://example.com/a"},
	} {
		mustCode(t, 2, args...)
	}
	// A refusal never echoes the whole oversized URL.
	_, e := mustCode(t, 2, "jobs", "find", "--url", "https://example.com/"+strings.Repeat("a", 5000))
	if strings.Contains(e, strings.Repeat("a", 100)) {
		t.Fatalf("an oversized URL was echoed back (%d bytes of stderr)", len(e))
	}
}

// A URL starting with a dash is a value of --url, never another flag.
func TestJobsFindTreatsTheURLAsAValue(t *testing.T) {
	_, state := setup(t)
	seedFind(t, state, findSeed{key: "gh:a:1", url: "https://example.com/a", status: posting.Digested})
	mustCode(t, 2, "jobs", "find", "--url", "-json")
	mustCode(t, 2, "jobs", "find", "--url=--json")
}
