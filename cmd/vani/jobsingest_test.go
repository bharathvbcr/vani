package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/inbox"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

type ij = map[string]any

// No ingest test may reach a Workday host: a test that saves a Workday search
// installs its own answer with stubWorkdaySize, and one that forgets fails here.
func init() {
	checkWorkdaySize = func(_ context.Context, b sources.Board) error {
		return fmt.Errorf("test reached the network for %s", b.Token)
	}
}

// stubWorkdaySize makes the Workday size check answer err, and counts its calls.
func stubWorkdaySize(t *testing.T, err error) *[]sources.Board {
	t.Helper()
	old := checkWorkdaySize
	t.Cleanup(func() { checkWorkdaySize = old })
	var asked []sources.Board
	checkWorkdaySize = func(_ context.Context, b sources.Board) error {
		asked = append(asked, b)
		return err
	}
	return &asked
}

func ingestJobs(host string, n int) []ij {
	var out []ij
	for i := 1; i <= n; i++ {
		out = append(out, ij{"title": fmt.Sprintf("Engineer %d", i), "company": "Acme", "location": "Remote",
			"url": fmt.Sprintf("https://%s/careers/jobs/%d", host, i)})
	}
	return out
}

func ingestPayload(t *testing.T, page string, save bool, jobs []ij) string {
	t.Helper()
	b, err := json.Marshal(ij{"page": ij{"url": page, "title": "Jobs at Acme"}, "save_search": save, "jobs": jobs})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ingestCLI runs `vani jobs ingest --json` with payload on stdin.
func ingestCLI(t *testing.T, want int, payload string) (out ingestJSON, stdout, stderr string) {
	t.Helper()
	old := ingestInput
	t.Cleanup(func() { ingestInput = old })
	ingestInput = strings.NewReader(payload)
	stdout, stderr = mustCode(t, want, "jobs", "ingest", "--json")
	if want == 0 {
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("stdout is not the contract's JSON: %v\n%s", err, stdout)
		}
	}
	return out, stdout, stderr
}

func ledgerRecords(t *testing.T, stateDir string) []ledger.Record {
	t.Helper()
	snap, err := ledger.ReadSnapshot(filepath.Join(stateDir, "ledger"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return snap.Records
}

func spooled(t *testing.T, stateDir string) int {
	t.Helper()
	n, err := inbox.Pending(filepath.Join(stateDir, inbox.Dir))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func watchlistOf(t *testing.T, cfgPath string) config.Config {
	t.Helper()
	cfg, _, err := config.LoadWithDeprecations(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestIngestFoldsWhenLedgerIsFree(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	jobs := ingestJobs("careers.acme.com", 3)
	jobs = append(jobs, ij{"title": "", "url": "https://careers.acme.com/careers/jobs/9"})               // rejected
	jobs = append(jobs, ij{"title": "dup", "url": "https://CAREERS.acme.com/careers/jobs/1/?utm_x=1#f"}) // duplicate of #1
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/careers/search?q=eng", false, jobs))
	if out.Queued != 3 || out.Duplicates != 1 || len(out.Rejected) != 1 || out.Rejected[0].Index != 3 {
		t.Fatalf("output %+v", out)
	}
	if out.Search.Status != searchNotRequested || out.Analysis != analysisNextRun {
		t.Fatalf("search %+v, analysis %q", out.Search, out.Analysis)
	}
	recs := ledgerRecords(t, stateDir)
	if len(recs) != 3 {
		t.Fatalf("%d records, want 3", len(recs))
	}
	for _, r := range recs {
		if r.Status != posting.Live || !strings.HasPrefix(string(r.Key), "pg:careers-acme-com-scrape:") || r.Posting.Company != "Acme" {
			t.Errorf("record %s %s %+v", r.Key, r.Status, r.Posting)
		}
	}
	if n := spooled(t, stateDir); n != 0 {
		t.Errorf("%d files left in the inbox after the drain", n)
	}

	// The same page again: idempotent.
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/careers/search?q=eng", false, jobs))
	if out.Queued != 3 || len(ledgerRecords(t, stateDir)) != 3 {
		t.Fatalf("repeat ingest: %+v, %d records", out, len(ledgerRecords(t, stateDir)))
	}
}

// With the ledger locked (a daemon), the batch is spooled and not folded, and
// the output says the daemon will analyse it.
func TestIngestWhileDaemonHoldsTheLedger(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	held, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search", false, ingestJobs("careers.acme.com", 2)))
	if out.Analysis != analysisDaemon || out.Queued != 2 {
		t.Fatalf("output %+v", out)
	}
	if n := spooled(t, stateDir); n != 1 {
		t.Fatalf("%d spooled files, want 1", n)
	}
	if len(held.All()) != 0 {
		t.Fatal("the CLI wrote the ledger it did not own")
	}

	// The owner drains it, as the daemon's poll does.
	res, err := drainInbox(t.Context(), held, stateDir, func(posting.Key) bool { return false })
	if err != nil || res.Report.Folded != 1 || len(res.Live) != 2 || res.New != 2 {
		t.Fatalf("drain: %+v, %v", res, err)
	}
	if n := spooled(t, stateDir); n != 0 {
		t.Errorf("%d spooled files after the drain", n)
	}
	_ = held.Close()
}

func TestIngestRefusals(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	before, _ := os.ReadFile(cfgPath)
	good := ingestJobs("careers.acme.com", 2)
	for name, payload := range map[string]string{
		"empty":         "",
		"not json":      "<html>",
		"no jobs":       ingestPayload(t, "https://careers.acme.com/", false, nil),
		"too many jobs": ingestPayload(t, "https://careers.acme.com/", false, ingestJobs("careers.acme.com", inbox.MaxJobs+1)),
		"oversize":      ingestPayload(t, "https://careers.acme.com/", false, []ij{{"title": "t", "url": "https://a.example/1", "snippet": strings.Repeat("x", inbox.MaxStdinBytes)}}),
		"save on http":  ingestPayload(t, "http://careers.acme.com/", true, good),
		"long page url": ingestPayload(t, "https://careers.acme.com/"+strings.Repeat("a", inbox.MaxPageURL), false, good),
	} {
		_, stdout, stderr := ingestCLI(t, 2, payload)
		if !strings.Contains(stdout, `"error"`) {
			t.Errorf("%s: stdout %q has no error object", name, stdout)
		}
		if strings.Contains(stdout+stderr, "careers.acme.com") {
			t.Errorf("%s: the page URL was echoed:\n%s\n%s", name, stdout, stderr)
		}
	}
	if n := spooled(t, stateDir); n != 0 {
		t.Errorf("%d files spooled by refused requests", n)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
		t.Error("a refused request changed the config")
	}
}

func TestIngestNoValidJobExitsTwoButReportsWhy(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	jobs := []ij{{"title": "", "url": "https://a.example/1"}, {"title": "t", "url": "ftp://a.example/2"}}
	_, stdout, _ := ingestCLI(t, 2, ingestPayload(t, "https://a.example/", false, jobs))
	var out ingestJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	if out.Queued != 0 || len(out.Rejected) != 2 || out.Rejected[1].Reason != "url is not http(s)" {
		t.Fatalf("output %+v", out)
	}
	if spooled(t, stateDir) != 0 {
		t.Error("a batch with no valid job was spooled")
	}
}

// A search that cannot be saved (here: no readable config) is reported in the
// reply; the jobs were validated and queueing needs no config, so they still
// reach the ledger (or the inbox when it is held).
func TestIngestSaveSearchFailureStillSpools(t *testing.T) {
	_, stateDir := setup(t) // no init: no config file
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search", true, ingestJobs("careers.acme.com", 2)))
	if out.Search.Status != "error" || out.Search.Reason == "" {
		t.Errorf("search %+v, want status error with a reason", out.Search)
	}
	if n := len(ledgerRecords(t, stateDir)) + spooled(t, stateDir); out.Queued != 2 || n == 0 {
		t.Errorf("jobs were dropped: queued %d, ledger+inbox %d", out.Queued, n)
	}
}

func TestScrubPageURLDropsCredentials(t *testing.T) {
	const base = "https://x.example/s?"
	for _, drop := range []string{"access_token", "accessToken", "auth", "Authorization", "jsessionid", "JSESSIONID", "email", "user_email",
		"key", "api_key", "apiKey", "apikey", "access_key", "secret_key", "private_key", "secret", "client-secret", "session", "sessionid", "sid", "id_token", "token",
		"PHPSESSID", "password", "passwd", "pwd", "jwt", "bearer", "signature", "sig", "x-amz-signature", "csrf", "xsrfToken", "otp", "credential", "credentials"} {
		got := scrubPageURL(base + "q=go&" + drop + "=v&location=Remote")
		if got != base+"location=Remote&q=go" {
			t.Errorf("scrubPageURL kept %q: %s", drop, got)
		}
	}
	for _, keep := range []string{"q", "location", "department", "keyword", "keywords", "search", "team", "author", "remote", "page",
		"state", "code", "jobkey", "jobKey", "sortKey", "categoryKey", "department_key", "monkey", "turkey", "hockey"} {
		in := base + keep + "=v"
		if got := scrubPageURL(in); got != in {
			t.Errorf("scrubPageURL(%q) = %q, want the parameter kept", in, got)
		}
	}
}

func TestIngestSaveSearch(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")

	// Greenhouse: board gh/acme, its API host allowed.
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://boards.greenhouse.io/acme?gh_src=x", true, ingestJobs("boards.greenhouse.io", 2)))
	if s := out.Search; s.Status != searchAdded || s.ATS != posting.Greenhouse || s.Token != "acme" || s.Company != "Acme" {
		t.Fatalf("greenhouse: %+v", s)
	}
	cfg := watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 1 || !slices.Contains(cfg.AllowedHosts, "boards-api.greenhouse.io") {
		t.Fatalf("config %+v", cfg)
	}
	// Jobs of a non-pg board fold under the host slug, not the ATS.
	for _, r := range ledgerRecords(t, stateDir) {
		if !strings.HasPrefix(string(r.Key), "pg:boards-greenhouse-io-scrape:") {
			t.Errorf("key %s", r.Key)
		}
	}
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://job-boards.greenhouse.io/ACME", true, ingestJobs("boards.greenhouse.io", 2)))
	if out.Search.Status != searchExists {
		t.Fatalf("again: %+v", out.Search)
	}

	// Lever EU, Ashby.
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://jobs.eu.lever.co/globex", true, ingestJobs("jobs.eu.lever.co", 2)))
	if out.Search.Status != searchAdded || out.Search.ATS != posting.Lever {
		t.Fatalf("lever: %+v", out.Search)
	}
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://jobs.ashbyhq.com/initech", true, ingestJobs("jobs.ashbyhq.com", 2)))
	if out.Search.Status != searchAdded || out.Search.ATS != posting.Ashby {
		t.Fatalf("ashby: %+v", out.Search)
	}
	cfg = watchlistOf(t, cfgPath)
	if b := cfg.Watchlist[1]; b.Region != "eu" || b.Token != "globex" {
		t.Errorf("lever board %+v", b)
	}

	// Workday, with the locale skipped and the search taken from q.
	asked := stubWorkdaySize(t, nil)
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://acme.wd5.myworkdayjobs.com/en-US/External?q=platform+engineer", true, ingestJobs("acme.wd5.myworkdayjobs.com", 2)))
	if out.Search.Status != searchAdded || out.Search.Token != "acme/External" {
		t.Fatalf("workday: %+v", out.Search)
	}
	cfg = watchlistOf(t, cfgPath)
	if b := cfg.Watchlist[3]; b.Host != "acme.wd5.myworkdayjobs.com" || b.Search != "platform engineer" {
		t.Errorf("workday board %+v", b)
	}
	if len(*asked) != 1 || (*asked)[0].Token != "acme/External" {
		t.Errorf("size check asked %+v, want the saved board once", *asked)
	}

	// An aggregator is unsupported, and its jobs are still queued.
	n := len(cfg.Watchlist)
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://www.linkedin.com/jobs/search?keywords=go", true, ingestJobs("www.linkedin.com", 2)))
	if out.Search.Status != searchUnsupported || !strings.Contains(out.Search.Reason, "needs a signed-in browser") || out.Queued != 2 {
		t.Fatalf("aggregator: %+v queued %d", out.Search, out.Queued)
	}
	if len(watchlistOf(t, cfgPath).Watchlist) != n {
		t.Error("an unsupported page changed the watchlist")
	}
	mustCode(t, 0, "config", "check")
}

func TestIngestSavesAPageBoard(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	page := "https://careers.acme.com/search?q=engineer&location=Remote&utm_source=x&gclid=1&session_id=2"
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, page, true, ingestJobs("careers.acme.com", 3)))
	s := out.Search
	if s.Status != searchAdded || s.ATS != posting.Page || s.Token != "careers-acme-com" || s.Company != "Acme" {
		t.Fatalf("search %+v", s)
	}
	if s.URL != "https://careers.acme.com/search?location=Remote&q=engineer" {
		t.Errorf("url %q: tracking parameters not scrubbed, or the search lost", s.URL)
	}
	cfg := watchlistOf(t, cfgPath)
	b := cfg.Watchlist[0]
	if b.URL != s.URL || b.LinkPattern != `^https://careers\.acme\.com/careers/jobs/` || !slices.Contains(cfg.AllowedHosts, "careers.acme.com") {
		t.Fatalf("board %+v, hosts %v", b, cfg.AllowedHosts)
	}
	for _, r := range ledgerRecords(t, stateDir) {
		if !strings.HasPrefix(string(r.Key), "pg:careers-acme-com:") {
			t.Errorf("key %s is not under the saved board", r.Key)
		}
	}

	// Same search, tracking noise differs: it already exists, same token.
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search?location=Remote&q=engineer&utm_campaign=y", true, ingestJobs("careers.acme.com", 3)))
	if out.Search.Status != searchExists || out.Search.Token != "careers-acme-com" {
		t.Fatalf("again: %+v", out.Search)
	}

	// Another search on the same host gets its own token, with a path hash.
	out, _, _ = ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search?q=designer", true, ingestJobs("careers.acme.com", 3)))
	if out.Search.Status != searchAdded || out.Search.Token == "careers-acme-com" || !strings.HasPrefix(out.Search.Token, "careers-acme-com-") {
		t.Fatalf("second search: %+v", out.Search)
	}
	if got := len(watchlistOf(t, cfgPath).Watchlist); got != 2 {
		t.Fatalf("%d boards, want 2", got)
	}
	mustCode(t, 0, "config", "check")
}

func TestIngestPageBoardNeedsUsableJobLinks(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	cases := map[string][]ij{
		"one job":     ingestJobs("careers.acme.com", 1),
		"two hosts":   {{"title": "a", "url": "https://careers.acme.com/jobs/1"}, {"title": "b", "url": "https://other.example/jobs/2"}},
		"http link":   {{"title": "a", "url": "http://careers.acme.com/jobs/1"}, {"title": "b", "url": "http://careers.acme.com/jobs/2"}},
		"long prefix": {{"title": "a", "url": "https://careers.acme.com/" + strings.Repeat("p/", 150) + "1"}, {"title": "b", "url": "https://careers.acme.com/" + strings.Repeat("p/", 150) + "2"}},
	}
	for name, jobs := range cases {
		out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search", true, jobs))
		if out.Search.Status != searchInvalid || out.Search.Reason == "" || out.Queued != len(jobs) {
			t.Errorf("%s: search %+v queued %d", name, out.Search, out.Queued)
		}
	}
	if got := len(watchlistOf(t, cfgPath).Watchlist); got != 0 {
		t.Errorf("%d boards saved from unusable pages", got)
	}
}

func TestLinkPattern(t *testing.T) {
	mk := func(urls ...string) []inbox.Job {
		var out []inbox.Job
		for _, u := range urls {
			out = append(out, inbox.Job{Title: "t", URL: u})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		jobs    []inbox.Job
		pattern string
		reason  bool
	}{
		{"segment boundary", mk("https://h.example/jobs/123", "https://h.example/jobs/124"), `^https://h\.example/jobs/`, false},
		{"deeper prefix", mk("https://h.example/en/jobs/a/1", "https://h.example/en/jobs/a/2", "https://h.example/en/jobs/b/3"), `^https://h\.example/en/jobs/`, false},
		{"flat links", mk("https://h.example/123", "https://h.example/456"), "", true},
		{"job under a job", mk("https://h.example/jobs/1", "https://h.example/jobs/1/apply"), `^https://h\.example/jobs/`, false},
		{"metacharacters", mk("https://h.example/a+b(c)/1", "https://h.example/a+b(c)/2"), `^https://h\.example/a\+b\(c\)/`, false},
		{"one link", mk("https://h.example/jobs/1"), "", true},
		{"none", nil, "", true},
		{"two hosts", mk("https://h.example/jobs/1", "https://g.example/jobs/2"), "", true},
		{"http", mk("http://h.example/jobs/1", "http://h.example/jobs/2"), "", true},
	} {
		p, reason := linkPattern(tc.jobs)
		if (reason != "") != tc.reason || p != tc.pattern {
			t.Errorf("%s: pattern %q reason %q", tc.name, p, reason)
		}
	}
}

func TestScrubPageURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.example/s?q=a&utm_medium=m&trackingId=1&refid=2&ref=3&trk=4&sid=5&sessionToken=6&token=7&msclkid=8&fbclid=9&gclid=0&department=eng": "https://x.example/s?department=eng&q=a",
		"https://u:p@x.example/s?q=a#frag": "https://x.example/s?q=a",
		"https://x.example/s":              "https://x.example/s",
		"::not a url":                      "",
	} {
		if got := scrubPageURL(in); got != want {
			t.Errorf("scrubPageURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIngestCompanyAndSlug(t *testing.T) {
	jobs := []inbox.Job{{Company: "Beta"}, {Company: "Acme"}, {Company: "acme"}, {Company: ""}}
	if got := ingestCompany(inbox.Page{Title: "Page"}, jobs); got != "Acme" {
		t.Errorf("majority company = %q", got)
	}
	if got := ingestCompany(inbox.Page{Title: " Jobs at Acme "}, []inbox.Job{{Title: "t"}}); got != "Jobs at Acme" {
		t.Errorf("page title fallback = %q", got)
	}
	if got := ingestCompany(inbox.Page{URL: "https://careers.acme.com/x"}, []inbox.Job{{Title: "t"}}); got != "careers.acme.com" {
		t.Errorf("host fallback = %q", got)
	}
	if got := ingestCompany(inbox.Page{Title: strings.Repeat("é", 300)}, nil); len(got) > inbox.MaxCompany {
		t.Errorf("company is %d bytes", len(got))
	}
	for in, want := range map[string]string{"Careers.Acme.com": "careers-acme-com", "..x..": "x", "": "page", "a--b": "a-b"} {
		if got := hostSlug(in); got != want {
			t.Errorf("hostSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := hostSlug(strings.Repeat("ab.", 100)); len(got) > inbox.MaxTokenLen || !inbox.ValidToken(got) {
		t.Errorf("long slug %q", got)
	}
}

func TestWatchlistListJSON(t *testing.T) {
	_, _ = setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "watchlist", "list", "--json")
	if strings.TrimSpace(o) != "[]" {
		t.Fatalf("empty watchlist printed %q, want []", o)
	}
	mustCode(t, 0, "watchlist", "add", "--ats", "lv", "--token", "globex", "--company", "Globex", "--region", "eu", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "pg", "--token", "acme", "--company", "Acme", "--url", "https://careers.acme.com/s?q=x",
		"--link-pattern", `^https://careers\.acme\.com/jobs/`, "--allow-host")
	o, _ = mustCode(t, 0, "watchlist", "list", "--json")
	var got []map[string]any
	if err := json.Unmarshal([]byte(o), &got); err != nil || len(got) != 2 {
		t.Fatalf("%v: %q", err, o)
	}
	if got[0]["ats"] != "lv" || got[0]["region"] != "eu" || got[0]["company"] != "Globex" {
		t.Errorf("lever entry %v", got[0])
	}
	if got[1]["url"] != "https://careers.acme.com/s?q=x" || got[1]["link_pattern"] == "" {
		t.Errorf("page entry %v", got[1])
	}
	if _, has := got[0]["host"]; has {
		t.Errorf("empty host emitted: %v", got[0])
	}
}

// addBoard is what both `watchlist add` and `jobs ingest` use.
func TestAddBoard(t *testing.T) {
	cfg := config.Default()
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	if err := addBoard(&cfg, sources.Board{ATS: "zz", Token: "x"}, true); err == nil || len(cfg.Watchlist) != 0 {
		t.Fatalf("an invalid board was added: %v", err)
	}
	if err := addBoard(&cfg, b, false); err != nil || slices.Contains(cfg.AllowedHosts, "boards-api.greenhouse.io") {
		t.Fatalf("without allowHost: %v, hosts %v", err, cfg.AllowedHosts)
	}
	if err := addBoard(&cfg, sources.Board{ATS: posting.Greenhouse, Token: "ACME", Company: "again"}, true); err == nil || len(cfg.Watchlist) != 1 {
		t.Fatalf("a duplicate was added: %v", err)
	}
	if err := addBoard(&cfg, sources.Board{ATS: posting.Ashby, Token: "x", Company: "X"}, true); err != nil || !slices.Contains(cfg.AllowedHosts, "api.ashbyhq.com") {
		t.Fatalf("with allowHost: %v, hosts %v", err, cfg.AllowedHosts)
	}
}

// The saved board's full listing ages what it does not list. A scrape of
// another search on the same host must not share its token, or the board's
// next Reconcile would kill the scraped jobs.
func TestUnsavedScrapeSurvivesTheSavedBoardsFullReconcile(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search?q=engineer", true, ingestJobs("careers.acme.com", 2)))
	if out.Search.Status != searchAdded {
		t.Fatalf("%+v", out.Search)
	}
	sales := []ij{
		{"title": "Sales Lead", "company": "Acme", "url": "https://careers.acme.com/careers/jobs/s1"},
		{"title": "Sales Rep", "company": "Acme", "url": "https://careers.acme.com/careers/jobs/s2"},
	}
	ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search?q=sales", false, sales))
	ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search?q=support", true, sales[:1])) // one link: not saved

	board := watchlistOf(t, cfgPath).Watchlist[0]
	led, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()
	rc := pipeline.NewReconciler(led)
	var listed []posting.Posting
	for _, r := range led.All() {
		if strings.HasPrefix(string(r.Key), board.KeyPrefix()) {
			listed = append(listed, r.Posting)
		}
	}
	if len(listed) != 2 {
		t.Fatalf("%d records under the saved board's token, want its own 2", len(listed))
	}
	for i := 0; i < pipeline.MissesToDead+1; i++ { // the board lists none of the sales jobs
		if _, err := rc.Reconcile(t.Context(), board, listed, now()); err != nil {
			t.Fatal(err)
		}
	}
	scraped := 0
	for _, r := range led.All() {
		if strings.Contains(string(r.Key), "-scrape:") {
			scraped++
			if r.Status != posting.Live || r.MissedRuns != 0 {
				t.Errorf("%s is %s, missed %d", r.Key, r.Status, r.MissedRuns)
			}
		}
	}
	if scraped != 2 {
		t.Errorf("%d scraped records, want 2", scraped)
	}
}

func TestIngestNoValidJobCarriesAnError(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	_, stdout, _ := ingestCLI(t, 2, ingestPayload(t, "https://a.example/", false, []ij{{"title": "t", "url": "ftp://a.example/2"}}))
	var out ingestJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error != "no job in the batch was valid: job 0: url is not http(s)" || len(out.Rejected) != 1 {
		t.Fatalf("%+v", out)
	}
}

func TestIngestFlatJobLinksAreInvalid(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	jobs := []ij{{"title": "a", "url": "https://careers.acme.com/1"}, {"title": "b", "url": "https://careers.acme.com/2"}}
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://careers.acme.com/search", true, jobs))
	want := "the job links share no path on careers.acme.com; add the board with `vani watchlist add --ats pg` and a link pattern"
	if out.Search.Status != searchInvalid || out.Search.Reason != want || out.Queued != 2 {
		t.Fatalf("%+v", out)
	}
	if len(watchlistOf(t, cfgPath).Watchlist) != 0 {
		t.Error("a board was saved")
	}
}

// hostIngestTimeout is JobsClient.ingestTimeout in macos/Sources/VaniApplyKit/
// JobIngest.swift: the Swift host kills `vani jobs ingest` after it. A size
// check that used all of it would get the CLI killed before it could reply with
// its reason, so the check keeps at least two thirds of the budget free.
const hostIngestTimeout = 30 * time.Second

func TestWorkdayCheckTimeoutLeavesRoomBeforeTheHostKillsTheCLI(t *testing.T) {
	if workdayCheckTimeout <= 0 || workdayCheckTimeout > hostIngestTimeout/3 {
		t.Fatalf("workdayCheckTimeout %v must be in (0, %v] so the CLI can still reply before the host's %v limit",
			workdayCheckTimeout, hostIngestTimeout/3, hostIngestTimeout)
	}
}

const nvidiaListing = "https://nvidia.wd5.myworkdayjobs.com/en-US/NVIDIAExternalCareerSite"

// A Workday search the adapter cannot list (over MaxPostings, or NVIDIA's
// capped 2000) is not saved, or the daemon would fail it every day. Its jobs
// are still queued, and the reason says how to narrow it.
func TestIngestRefusesAWorkdaySearchTooBigToList(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	before := watchlistOf(t, cfgPath)
	asked := stubWorkdaySize(t, fmt.Errorf("workday: %w: it lists 2000 postings, over the 200 limit", srcutil.ErrTooManyPostings))

	out, _, _ := ingestCLI(t, 0, ingestPayload(t, nvidiaListing, true, ingestJobs("nvidia.wd5.myworkdayjobs.com", 2)))
	if s := out.Search; s.Status != searchInvalid || !strings.Contains(s.Reason, "narrow") || !strings.Contains(s.Reason, "2000") || s.URL != nvidiaListing {
		t.Fatalf("search = %+v, want invalid with a narrowing reason", s)
	}
	if strings.Contains(out.Search.Reason, "facet") {
		t.Errorf("the adapter refuses facets, so the reason must not suggest one: %q", out.Search.Reason)
	}
	if len(*asked) != 1 {
		t.Errorf("size check ran %d times", len(*asked))
	}
	if out.Queued != 2 || spooled(t, stateDir)+len(ledgerRecords(t, stateDir)) == 0 {
		t.Errorf("the jobs must still be queued: %+v", out)
	}
	after := watchlistOf(t, cfgPath)
	if len(after.Watchlist) != len(before.Watchlist) || !slices.Equal(after.AllowedHosts, before.AllowedHosts) {
		t.Errorf("a refused search changed the config: %+v -> %+v", before, after)
	}
}

// A check that could not run is not a pass: the search is not saved, and the
// reason says it was unchecked rather than too big.
func TestIngestWorkdaySizeCheckFailureIsNotAPass(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	stubWorkdaySize(t, errors.New("HTTP 503"))

	out, _, _ := ingestCLI(t, 0, ingestPayload(t, nvidiaListing+"?q=intern", true, ingestJobs("nvidia.wd5.myworkdayjobs.com", 2)))
	if s := out.Search; s.Status != searchInvalid || !strings.Contains(s.Reason, "could not check") || !strings.Contains(s.Reason, "try again") {
		t.Fatalf("search = %+v", s)
	}
	if out.Queued != 2 || len(watchlistOf(t, cfgPath).Watchlist) != 0 {
		t.Errorf("queued %d, watchlist %+v", out.Queued, watchlistOf(t, cfgPath).Watchlist)
	}
}

// The check only gates a Workday save: a request without save_search and a
// board that already exists make no request to the Workday host.
func TestIngestWorkdaySizeCheckOnlyOnANewSave(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	asked := stubWorkdaySize(t, nil)
	jobs := ingestJobs("nvidia.wd5.myworkdayjobs.com", 2)

	if out, _, _ := ingestCLI(t, 0, ingestPayload(t, nvidiaListing, false, jobs)); out.Search.Status != searchNotRequested {
		t.Fatalf("search = %+v", out.Search)
	}
	if out, _, _ := ingestCLI(t, 0, ingestPayload(t, nvidiaListing, true, jobs)); out.Search.Status != searchAdded {
		t.Fatalf("search = %+v", out.Search)
	}
	if out, _, _ := ingestCLI(t, 0, ingestPayload(t, nvidiaListing, true, jobs)); out.Search.Status != searchExists {
		t.Fatalf("search = %+v", out.Search)
	}
	if len(*asked) != 1 {
		t.Errorf("size check ran %d times, want only for the first save", len(*asked))
	}
}

func TestAddBoardRefusesTheScrapeSuffix(t *testing.T) {
	cfg := config.Default()
	b := sources.Board{ATS: posting.Page, Token: "careers-acme-com-scrape", Company: "Acme", URL: "https://careers.acme.com/s", LinkPattern: `^https://careers\.acme\.com/jobs/`}
	cfg.AllowedHosts = append(cfg.AllowedHosts, "careers.acme.com")
	if err := addBoard(&cfg, b, true); !errors.As(err, new(invalidBoardError)) || len(cfg.Watchlist) != 0 {
		t.Fatalf("err = %v", err)
	}
}
