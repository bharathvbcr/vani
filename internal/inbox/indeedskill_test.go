package inbox

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// indeedJobURL is the one job URL form skills/indeed-to-vani tells the agent
// to write: an ingested job is keyed by its canonical URL, so any other form
// of the same job would be a new posting on every run.
var indeedJobURL = regexp.MustCompile(`^https://www\.indeed\.com/viewjob\?jk=[0-9a-f]{16}$`)

// The skill's example batch is what `vani jobs ingest` accepts, every job of
// it, in the URL form the skill requires.
func TestIndeedSkillExampleIsAccepted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "indeed-to-vani", "example.json"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if req.SaveSearch || req.Page.URL != "https://www.indeed.com/jobs" {
		t.Fatalf("page = %+v save_search = %v; the skill says page.url https://www.indeed.com/jobs and save_search false", req.Page, req.SaveSearch)
	}
	jobs, dups, rejected := req.Validate()
	if len(rejected) != 0 || dups != 0 || len(jobs) != len(req.Jobs) {
		t.Fatalf("jobs %d of %d, duplicates %d, rejected %v", len(jobs), len(req.Jobs), dups, rejected)
	}
	for _, j := range req.Jobs {
		if !indeedJobURL.MatchString(j.URL) {
			t.Errorf("url %q is not the form the skill requires", j.URL)
		}
		if len(j.Snippet) > MaxSnippet {
			t.Errorf("snippet is %d bytes", len(j.Snippet))
		}
	}
}

// Why the skill insists on one URL form: the forms Indeed links with are
// different keys, and the SKILL.md must keep saying so.
func TestIndeedURLFormsAreDifferentKeys(t *testing.T) {
	base, err := posting.PageKey("www-indeed-com-scrape", "https://www.indeed.com/viewjob?jk=0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{
		"https://www.indeed.com/viewjob?jk=0123456789abcdef&from=serp",
		"https://www.indeed.com/rc/clk?jk=0123456789abcdef",
		"https://indeed.com/viewjob?jk=0123456789abcdef",
	} {
		k, err := posting.PageKey("www-indeed-com-scrape", other)
		if err != nil {
			t.Fatal(err)
		}
		if k == base {
			t.Fatalf("%s keys the same as the canonical form; the skill's rule can be relaxed", other)
		}
	}
	skill, err := os.ReadFile(filepath.Join("..", "..", "skills", "indeed-to-vani", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"https://www.indeed.com/viewjob?jk=", "Never call the resume tool", "**stop**", "VANI_NO_KEYCHAIN=1", "save_search` is always `false`"} {
		if !strings.Contains(string(skill), must) {
			t.Errorf("SKILL.md no longer says %q", must)
		}
	}
}
