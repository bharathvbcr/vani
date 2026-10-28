package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// No ingest test may reach an Oracle HCM host: a test that saves an Oracle HCM
// search installs its own answer with stubOracleHCMSize, and one that forgets
// fails here.
func init() {
	checkOracleHCMSize = func(_ context.Context, b sources.Board) error {
		return fmt.Errorf("test reached the network for %s", b.Token)
	}
}

// stubOracleHCMSize makes the Oracle HCM size check answer err, and records
// the boards it was asked about.
func stubOracleHCMSize(t *testing.T, err error) *[]sources.Board {
	t.Helper()
	old := checkOracleHCMSize
	t.Cleanup(func() { checkOracleHCMSize = old })
	var asked []sources.Board
	checkOracleHCMSize = func(_ context.Context, b sources.Board) error {
		asked = append(asked, b)
		return err
	}
	return &asked
}

const (
	jpmcListing = "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001/jobs"
	jpmcHost    = "jpmc.fa.oraclecloud.com"
)

// The pages an extension user is on when they save a JPMorgan Chase or Oracle
// search become oc boards: tenant from the host, site from the path, the
// search box's text from the "keyword" query.
func TestDeriveBoardOracleHCM(t *testing.T) {
	for _, tc := range []struct {
		name, url           string
		token, host, search string
		invalid             bool
	}{
		{"listing with keyword", jpmcListing + "?keyword=software+engineer&mode=location", "jpmc/CX_1001", jpmcHost, "software engineer", false},
		{"listing without keyword", jpmcListing, "jpmc/CX_1001", jpmcHost, "", false},
		{"percent-encoded keyword", jpmcListing + "?keyword=data%20scientist", "jpmc/CX_1001", jpmcHost, "data scientist", false},
		{"blank keyword", jpmcListing + "?keyword=%20", "jpmc/CX_1001", jpmcHost, "", false},
		{"a job page", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001/job/210789792/?keyword=quantitative+researcher&mode=location", "jpmc/CX_1001", jpmcHost, "quantitative researcher", false},
		{"site home", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1001", "jpmc/CX_1001", jpmcHost, "", false},
		{"regional host", "https://eeho.fa.us2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/jobsearch/jobs?keyword=gpu", "eeho/jobsearch", "eeho.fa.us2.oraclecloud.com", "gpu", false},
		{"other locale", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/pt-BR/sites/CX_1001/jobs", "jpmc/CX_1001", jpmcHost, "", false},
		{"upper-case host", "https://JPMC.FA.ORACLECLOUD.COM/hcmUI/CandidateExperience/en/sites/CX_1001/jobs", "jpmc/CX_1001", jpmcHost, "", false},
		{"no site", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites", "", "", "", true},
		{"no path", "https://jpmc.fa.oraclecloud.com/", "", "", "", true},
		{"login page", "https://jpmc.fa.oraclecloud.com/hcmUI/faces/AtkHomePageWelcome", "", "", "", true},
		{"locale missing", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/sites/CX_1001/jobs/x", "", "", "", true},
		{"locale not a locale", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/..%2f/sites/CX_1001/jobs", "", "", "", true},
		{"sites not sites", "https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/other/CX_1001/jobs", "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, res := deriveBoard(tc.url, nil, "JPMorgan Chase", nil)
			if tc.invalid {
				if res.Status != searchInvalid || !strings.Contains(res.Reason, "Oracle HCM") {
					t.Fatalf("got %+v, %+v; want invalid", b, res)
				}
				return
			}
			if res.Status != "" {
				t.Fatalf("refused: %+v", res)
			}
			if b.ATS != posting.OracleHCM || b.Token != tc.token || b.Host != tc.host || b.Search != tc.search || b.Company != "JPMorgan Chase" {
				t.Errorf("board %+v, want oc %s on %s searching %q", b, tc.token, tc.host, tc.search)
			}
		})
	}
}

// Hosts that only look like Oracle's are never a board: they fall to the
// careers-page route, not oc.
func TestDeriveBoardOracleHCMLookalikeHostsAreNotOracle(t *testing.T) {
	for _, host := range []string{
		"jpmc.fa.oraclecloud.com.evil.test", "evil.jpmc.fa.oraclecloud.com", "jpmc.oraclecloud.com", "jpmc.fa.example.com", "jpmc.fa.us-2.oraclecloud.com",
	} {
		b, _ := deriveBoard("https://"+host+"/hcmUI/CandidateExperience/en/sites/CX_1001/jobs", nil, "X", nil)
		if b.ATS == posting.OracleHCM {
			t.Errorf("%s became an oc board: %+v", host, b)
		}
	}
}

func TestIngestSavesAnOracleHCMSearch(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	asked := stubOracleHCMSize(t, nil)

	out, _, _ := ingestCLI(t, 0, ingestPayload(t, jpmcListing+"?keyword=software+engineer&mode=location", true, ingestJobs(jpmcHost, 2)))
	if s := out.Search; s.Status != searchAdded || s.ATS != posting.OracleHCM || s.Token != "jpmc/CX_1001" {
		t.Fatalf("search = %+v", s)
	}
	cfg := watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 1 {
		t.Fatalf("watchlist %+v", cfg.Watchlist)
	}
	if b := cfg.Watchlist[0]; b.ATS != posting.OracleHCM || b.Host != jpmcHost || b.Search != "software engineer" {
		t.Errorf("board %+v", b)
	}
	if !slices.Contains(cfg.AllowedHosts, jpmcHost) {
		t.Errorf("allowed_hosts %v lacks the board's host", cfg.AllowedHosts)
	}
	if len(*asked) != 1 || (*asked)[0].Token != "jpmc/CX_1001" || (*asked)[0].Search != "software engineer" {
		t.Errorf("size check asked %+v, want the saved board once", *asked)
	}
	// The jobs of the page fold as scraped ones, whatever the saved board.
	for _, r := range ledgerRecords(t, stateDir) {
		if !strings.HasPrefix(string(r.Key), "pg:") {
			t.Errorf("key %s", r.Key)
		}
	}
	mustCode(t, 0, "config", "check")
	if out, _, _ := ingestCLI(t, 0, ingestPayload(t, jpmcListing+"?keyword=software+engineer", true, ingestJobs(jpmcHost, 2))); out.Search.Status != searchExists {
		t.Errorf("again: %+v", out.Search)
	}
	if len(*asked) != 1 {
		t.Errorf("size check ran %d times, want only for the first save", len(*asked))
	}
}

// JPMorgan's whole board counts 7,317 postings: saved without a keyword, the
// daemon would fail it every day. The search is refused with the way out, and
// the jobs are still queued.
func TestIngestRefusesAnOracleHCMSearchTooBigToList(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	asked := stubOracleHCMSize(t, fmt.Errorf("oraclehcm: %w: it lists 7317 postings, over the 5000 limit (max_postings)", srcutil.ErrTooManyPostings))

	out, _, _ := ingestCLI(t, 0, ingestPayload(t, jpmcListing, true, ingestJobs(jpmcHost, 2)))
	if s := out.Search; s.Status != searchInvalid || !strings.Contains(s.Reason, "narrow") || !strings.Contains(s.Reason, "7317") || !strings.Contains(s.Reason, "Oracle HCM") {
		t.Fatalf("search = %+v, want invalid with a narrowing reason", s)
	}
	if len(*asked) != 1 {
		t.Errorf("size check ran %d times", len(*asked))
	}
	if out.Queued != 2 || spooled(t, stateDir)+len(ledgerRecords(t, stateDir)) == 0 {
		t.Errorf("the jobs must still be queued: %+v", out)
	}
	if n := len(watchlistOf(t, cfgPath).Watchlist); n != 0 {
		t.Errorf("a refused search was saved: %d boards", n)
	}
}

// A check that could not run is not a pass.
func TestIngestOracleHCMSizeCheckFailureIsNotAPass(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	stubOracleHCMSize(t, errors.New("HTTP 403 blocked by WAF"))

	out, _, _ := ingestCLI(t, 0, ingestPayload(t, jpmcListing+"?keyword=quant", true, ingestJobs(jpmcHost, 2)))
	if s := out.Search; s.Status != searchInvalid || !strings.Contains(s.Reason, "could not check") || !strings.Contains(s.Reason, "try again") {
		t.Fatalf("search = %+v", s)
	}
	if out.Queued != 2 || len(watchlistOf(t, cfgPath).Watchlist) != 0 {
		t.Errorf("queued %d, watchlist %+v", out.Queued, watchlistOf(t, cfgPath).Watchlist)
	}
}

// A keyword the finder cannot carry never becomes a board that fails daily.
func TestIngestRefusesAnOracleHCMKeywordWithAQuote(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	asked := stubOracleHCMSize(t, nil)

	out, _, _ := ingestCLI(t, 0, ingestPayload(t, jpmcListing+`?keyword=x%22%2Climit%3D5`, true, ingestJobs(jpmcHost, 2)))
	if s := out.Search; s.Status != searchInvalid || !strings.Contains(s.Reason, "quote") {
		t.Fatalf("search = %+v", s)
	}
	if len(*asked) != 0 || len(watchlistOf(t, cfgPath).Watchlist) != 0 {
		t.Errorf("asked %d, watchlist %+v", len(*asked), watchlistOf(t, cfgPath).Watchlist)
	}
}

// The Workday refusal's words are unchanged by sharing its check with oc.
func TestSizeRefusalWordsPerSystem(t *testing.T) {
	big := fmt.Errorf("x: %w: 9 postings", srcutil.ErrTooManyPostings)
	if got := sizeRefusal("Workday", func(context.Context, sources.Board) error { return big }, sources.Board{}); got !=
		"this Workday search lists more postings than Vani can follow (x: sources: board exceeds posting limit: 9 postings); narrow it with search terms and save it again" {
		t.Errorf("workday too big: %q", got)
	}
	if got := sizeRefusal("Oracle HCM", func(context.Context, sources.Board) error { return errors.New("boom") }, sources.Board{}); got !=
		"could not check how many postings this Oracle HCM search lists, so it was not saved; try again: boom" {
		t.Errorf("oc unchecked: %q", got)
	}
	if got := sizeRefusal("Oracle HCM", func(context.Context, sources.Board) error { return nil }, sources.Board{}); got != "" {
		t.Errorf("a pass is %q", got)
	}
}
