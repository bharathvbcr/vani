package site

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

func htmlReply(body string) srctest.Reply {
	return srctest.Reply{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   []byte(body),
	}
}

func xmlReply(body string) srctest.Reply {
	return srctest.Reply{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/xml"}},
		Body:   []byte(body),
	}
}

func assertOnlyHostRequested(t *testing.T, urls []string, wantHost string) {
	t.Helper()
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("requested unparseable URL %q: %v", raw, err)
		}
		if !strings.EqualFold(u.Hostname(), wantHost) {
			t.Fatalf("fetched cross-host URL %q (want only %s)", raw, wantHost)
		}
	}
}

func assertUserAgentOnAll(t *testing.T, reqs []srctest.Request) {
	t.Helper()
	for _, req := range reqs {
		if got := req.Header.Get("User-Agent"); got != page.UserAgent {
			t.Errorf("%s %s User-Agent = %q, want %q", req.Method, req.URL, got, page.UserAgent)
		}
	}
}

func TestDiscoverValidation(t *testing.T) {
	t.Parallel()

	okDoer := srctest.New(map[string]srctest.Reply{})
	invalidDomains := []struct {
		name   string
		domain string
	}{
		{name: "empty", domain: ""},
		{name: "single label", domain: "localhost"},
		{name: "uppercase", domain: "Acme.com"},
		{name: "scheme", domain: "https://acme.com"},
		{name: "port", domain: "acme.com:443"},
		{name: "userinfo", domain: "user@acme.com"},
		{name: "path", domain: "acme.com/careers"},
		{name: "ipv4", domain: "127.0.0.1"},
		{name: "ipv6", domain: "::1"},
		{name: "ipv6 brackets", domain: "[::1]"},
		{name: "leading dot", domain: ".acme.com"},
		{name: "trailing dot", domain: "acme.com."},
		{name: "double dot", domain: "a..acme.com"},
		{name: "space", domain: " acme.com"},
		{name: "control char", domain: "acme.com\n"},
		{name: "unicode", domain: "bücher.example"},
		{name: "too long", domain: strings.Repeat("a.", 128) + "com"},
	}

	for _, tc := range invalidDomains {
		t.Run("domain/"+tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := Discover(context.Background(), okDoer, tc.domain, Options{})
			if err == nil {
				t.Fatalf("Discover(%q) succeeded (%+v), want validation error", tc.domain, res)
			}
			if len(okDoer.URLs()) != 0 {
				t.Fatalf("Discover(%q) made network requests %v", tc.domain, okDoer.URLs())
			}
		})
	}

	t.Run("nil doer", func(t *testing.T) {
		t.Parallel()
		if _, err := Discover(context.Background(), nil, "acme.com", Options{}); err == nil {
			t.Fatal("Discover with nil doer succeeded, want error")
		}
	})

	t.Run("nil context", func(t *testing.T) {
		t.Parallel()
		var nilCtx context.Context
		if _, err := Discover(nilCtx, okDoer, "acme.com", Options{}); err == nil {
			t.Fatal("Discover with nil context succeeded, want error")
		}
	})

	t.Run("negative options", func(t *testing.T) {
		t.Parallel()
		for _, opts := range []Options{
			{MaxSitemaps: -1},
			{MaxCareerPages: -1},
			{RequestTimeout: -1},
		} {
			if _, err := Discover(context.Background(), okDoer, "acme.com", opts); err == nil {
				t.Fatalf("Discover with %+v succeeded, want error", opts)
			}
		}
	})
}

func TestDiscoverRobotsFailureModes(t *testing.T) {
	t.Parallel()

	t.Run("500 fail closed", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusInternalServerError, Body: []byte("oops")},
		})
		if _, err := Discover(context.Background(), d, "acme.com", Options{}); err == nil {
			t.Fatal("robots 500 succeeded, want fail-closed error")
		}
		if got := d.URLs(); len(got) != 1 || got[0] != "https://acme.com/robots.txt" {
			t.Fatalf("unexpected requests after robots 500: %v", got)
		}
	})

	t.Run("503 fail closed", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusServiceUnavailable},
		})
		if _, err := Discover(context.Background(), d, "acme.com", Options{}); err == nil {
			t.Fatal("robots 503 succeeded, want fail-closed error")
		}
	})

	t.Run("network error fail closed", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Err: errors.New("connection reset")},
		})
		if _, err := Discover(context.Background(), d, "acme.com", Options{}); err == nil {
			t.Fatal("robots transport error succeeded, want fail-closed error")
		}
	})

	t.Run("oversized robots fail closed", func(t *testing.T) {
		t.Parallel()
		body := &stopAfter{allow: MaxRobotsBytes + 1, ch: '#'}
		d := swapBodyDoer{
			base: srctest.New(map[string]srctest.Reply{
				"https://acme.com/robots.txt": {Status: http.StatusOK},
			}),
			swap: func(req *http.Request, resp *http.Response) {
				resp.Body.Close()
				resp.Body = body
			},
		}
		_, err := Discover(context.Background(), d, "acme.com", Options{})
		if !errors.Is(err, errBodyTooLarge) {
			t.Fatalf("err = %v, want errBodyTooLarge", err)
		}
		if body.over || body.n != MaxRobotsBytes+1 {
			t.Fatalf("read %d bytes (over=%v), want exact stop at %d", body.n, body.over, MaxRobotsBytes+1)
		}
	})

	t.Run("404 means no robots and allows crawling", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/":            htmlReply(`<title>Acme</title><a href="/careers">Careers</a>`),
			"https://acme.com/careers":     htmlReply(`<a href="https://boards.greenhouse.io/acme">Apply</a>`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.RobotsFetched {
			t.Fatal("RobotsFetched = true on 404, want false")
		}
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Greenhouse || res.Candidates[0].Board.Token != "acme" {
			t.Fatalf("candidates = %+v, want gh/acme", res.Candidates)
		}
		assertUserAgentOnAll(t, d.Requests())
	})
}

func TestDiscoverRobotsRFC9309AndCrossHostSitemaps(t *testing.T) {
	t.Parallel()

	robotsTxt := strings.Join([]string{
		"User-agent: *",
		"Disallow: /",
		"Sitemap: https://evil.example/sitemap.xml",
		"Sitemap: http://acme.com/insecure.xml",
		"Sitemap: https://acme.com/sitemap.xml#frag",
		"Sitemap: https://acme.com/jobs-sitemap.xml",
		"",
		"User-agent: Vani/0.1 (+personal)",
		"Allow: /$",
		"Allow: /jobs-sitemap.xml",
		"Disallow: /careers",
		"Disallow: /secret/",
	}, "\n")

	d := srctest.New(map[string]srctest.Reply{
		"https://acme.com/robots.txt": {Status: http.StatusOK, Body: []byte(robotsTxt)},
		"https://acme.com/jobs-sitemap.xml": xmlReply(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://acme.com/secret/swe-1</loc></url>
  <url><loc>https://acme.com/secret/swe-2</loc></url>
</urlset>`),
		"https://acme.com/": htmlReply(`
<html>
  <head><title>Acme</title></head>
  <body>
    <a href="/careers">Careers</a>
    <a href="https://evil.example/careers">External Careers</a>
  </body>
</html>`),
	})

	res, err := Discover(context.Background(), d, "acme.com", Options{})
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if !res.RobotsFetched {
		t.Fatal("RobotsFetched = false, want true")
	}
	if !slices.Contains(res.RobotsDisallowed, "/careers") {
		t.Fatalf("RobotsDisallowed = %v, want /careers recorded", res.RobotsDisallowed)
	}
	assertOnlyHostRequested(t, d.URLs(), "acme.com")
	for _, u := range d.URLs() {
		if strings.Contains(u, "/careers") || strings.Contains(u, "insecure.xml") || strings.Contains(u, "#frag") {
			t.Fatalf("unexpectedly fetched %q", u)
		}
	}
	if res.SitemapsRead != 1 || res.PagesRead != 1 {
		t.Fatalf("SitemapsRead=%d PagesRead=%d, want 1, 1", res.SitemapsRead, res.PagesRead)
	}
}

func TestDiscoverSitemapIndexPriorityAndCaps(t *testing.T) {
	t.Parallel()

	t.Run("sitemapindex prioritizes career child sitemaps and respects MaxSitemaps", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusOK, Body: []byte("User-agent: *\nAllow: /\nSitemap: https://acme.com/sitemap_index.xml\n")},
			"https://acme.com/sitemap_index.xml": xmlReply(`
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://acme.com/blog.xml</loc></sitemap>
  <sitemap><loc>https://evil.example/jobs.xml</loc></sitemap>
  <sitemap><loc>https://acme.com/careers-jobs.xml</loc></sitemap>
  <sitemap><loc>https://acme.com/marketing.xml</loc></sitemap>
</sitemapindex>`),
			"https://acme.com/careers-jobs.xml": xmlReply(`
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://jobs.lever.co/acme/role-1</loc></url>
  <url><loc>https://acme.com/careers-index-nested.xml</loc></url>
</urlset>`),
			"https://acme.com/": htmlReply(`<title>Acme</title>`),
		})

		res, err := Discover(context.Background(), d, "acme.com", Options{MaxSitemaps: 2})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.SitemapsRead != 2 {
			t.Fatalf("SitemapsRead = %d, want 2", res.SitemapsRead)
		}
		if !res.Capped {
			t.Fatal("Capped = false, want true when remaining child sitemaps were skipped")
		}
		assertOnlyHostRequested(t, d.URLs(), "acme.com")
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Lever || res.Candidates[0].Board.Token != "acme" {
			t.Fatalf("candidates = %+v, want lv/acme", res.Candidates)
		}
	})

	t.Run("nested sitemapindex ignored at depth 1", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": xmlReply(`
<sitemapindex>
  <sitemap><loc>https://acme.com/child-index.xml</loc></sitemap>
</sitemapindex>`),
			"https://acme.com/child-index.xml": xmlReply(`
<sitemapindex>
  <sitemap><loc>https://acme.com/leaf.xml</loc></sitemap>
</sitemapindex>`),
			"https://acme.com/": htmlReply(`<title>Acme</title>`),
		})

		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.SitemapsRead != 1 {
			t.Fatalf("SitemapsRead = %d, want 1 (nested sitemapindex must not be counted)", res.SitemapsRead)
		}
		for _, u := range d.URLs() {
			if strings.Contains(u, "leaf.xml") {
				t.Fatalf("nested sitemapindex child %q was fetched", u)
			}
		}
	})

	t.Run("urlset capped at MaxSitemapEntries", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for i := range MaxSitemapEntries + 25 {
			fmt.Fprintf(&sb, "<url><loc>https://acme.com/pages/%d</loc></url>", i)
		}
		sb.WriteString(`</urlset>`)

		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": xmlReply(sb.String()),
			"https://acme.com/":            htmlReply(`<title>Acme</title>`),
		})

		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if !res.Capped {
			t.Fatal("Capped = false, want true")
		}
		if res.SitemapsRead != 1 {
			t.Fatalf("SitemapsRead = %d, want 1", res.SitemapsRead)
		}
		if res.URLsExamined != MaxSitemapEntries {
			t.Fatalf("URLsExamined = %d, want %d", res.URLsExamined, MaxSitemapEntries)
		}
	})
}

func TestDiscoverHomepageCareersAndATSDiscovery(t *testing.T) {
	t.Parallel()

	t.Run("embedded greenhouse and structured ATS across career pages", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/": htmlReply(`<!doctype html>
<html>
<head>
  <title>Careers | Acme Robotics</title>
  <meta property="og:site_name" content="Acme Robotics">
  <link rel="icon" type="image/svg+xml" href="/logo.svg">
  <link rel="icon" href="/favicon.ico">
  <link rel="apple-touch-icon" href="/apple-touch-icon.png">
</head>
<body>
  <a href="/careers">Careers</a>
  <a href="/jobs">Jobs</a>
  <a href="/join-us">Join Us</a>
  <a href="/work-with-us">Work With Us</a>
</body>
</html>`),
			"https://acme.com/careers": htmlReply(`
<iframe src="https://boards.greenhouse.io/embed/job_board?for=acme-robotics"></iframe>
<a href="https://job-boards.greenhouse.io/embed/job_board?for=acme-robotics">Board</a>
<a href="https://boards.greenhouse.io/acme-robotics/jobs/101">Job 101</a>
<a href="https://jobs.ashbyhq.com/Hippocratic%20AI/role-1">Ashby</a>
`),
			"https://acme.com/jobs": htmlReply(`
<a href="https://acme.wd5.myworkdayjobs.com/en-US/Careers/job/Remote/SWE_1?q=ml">Workday</a>
<a href="https://eeho.fa.us2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/jobs?keyword=infra">Oracle</a>
`),
			"https://acme.com/join-us": htmlReply(`
<a href="https://acme.eightfold.ai/careers?domain=acme.com&amp;query=systems">Eightfold</a>
<a href="https://careers.acme-sub.example/careers?domain=acme.com">Eightfold Custom</a>
`),
			"https://acme.com/work-with-us": htmlReply(`
<a href="https://boards.greenhouse.io/should-not-fetch-4th-career-page">4th</a>
`),
		})

		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.Company != "Acme Robotics" {
			t.Fatalf("Company = %q, want %q", res.Company, "Acme Robotics")
		}
		if res.IconURL != "https://acme.com/apple-touch-icon.png" {
			t.Fatalf("IconURL = %q, want apple-touch-icon.png", res.IconURL)
		}
		// 1 homepage + MaxCareerPages (3) career pages = 4.
		if res.PagesRead != 4 {
			t.Fatalf("PagesRead = %d, want 4", res.PagesRead)
		}
		assertOnlyHostRequested(t, d.URLs(), "acme.com")
		for _, u := range d.URLs() {
			if strings.HasSuffix(u, "/work-with-us") {
				t.Fatalf("fetched 4th career page %q past MaxCareerPages=3", u)
			}
		}

		byKey := map[string]Candidate{}
		for _, c := range res.Candidates {
			if err := config.ValidBoard(c.Board); err != nil {
				t.Fatalf("candidate %+v failed config.ValidBoard: %v", c, err)
			}
			if c.Board.Domain != "acme.com" || c.Board.Icon != "https://acme.com/apple-touch-icon.png" {
				t.Errorf("candidate metadata missing: %+v", c.Board)
			}
			if c.Board.Company != "Acme Robotics" {
				t.Errorf("candidate Company = %q, want Acme Robotics", c.Board.Company)
			}
			byKey[string(c.Board.ATS)+":"+strings.ToLower(c.Board.Token)] = c
		}

		gh, ok := byKey["gh:acme-robotics"]
		if !ok {
			t.Fatalf("missing gh:acme-robotics in %+v", res.Candidates)
		}
		if _, embedBad := byKey["gh:embed"]; embedBad {
			t.Fatalf("incorrectly parsed embed path as token 'embed': %+v", byKey["gh:embed"])
		}
		if gh.EvidenceCount != 3 || len(gh.SampleURLs) != 3 {
			t.Fatalf("gh EvidenceCount=%d SampleURLs=%v, want 3 and 3", gh.EvidenceCount, gh.SampleURLs)
		}

		ab, ok := byKey["ab:hippocratic-ai"]
		if !ok || ab.Board.BoardName != "Hippocratic AI" {
			t.Fatalf("missing or bad ashby candidate: %+v", ab)
		}
		wd, ok := byKey["wd:acme/careers"]
		if !ok || wd.Board.Host != "acme.wd5.myworkdayjobs.com" || wd.Board.Search != "ml" {
			t.Fatalf("missing or bad workday candidate: %+v", wd)
		}
		oc, ok := byKey["oc:eeho/cx_1"]
		if !ok || oc.Board.Host != "eeho.fa.us2.oraclecloud.com" || oc.Board.Search != "infra" {
			t.Fatalf("missing or bad oracle candidate: %+v", oc)
		}
		ef, ok := byKey["ef:acme.com"]
		if !ok || ef.Board.Host != "acme.eightfold.ai" || ef.Board.Search != "systems" {
			t.Fatalf("missing or bad eightfold candidate: %+v", ef)
		}
	})

	t.Run("talentbrew on domain", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://jobs.intuit.com/robots.txt": {Status: http.StatusNotFound},
			"https://jobs.intuit.com/sitemap.xml": xmlReply(`
<urlset>
  <url><loc>https://jobs.intuit.com/job/san-diego/staff-software-engineer/27595/102521439872</loc></url>
  <url><loc>https://jobs.intuit.com/job/mountain-view/ml-engineer/27595/102521439873</loc></url>
</urlset>`),
			"https://jobs.intuit.com/": htmlReply(`<title>Intuit Careers</title>`),
		})
		res, err := Discover(context.Background(), d, "jobs.intuit.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 {
			t.Fatalf("candidates = %+v, want 1", res.Candidates)
		}
		c := res.Candidates[0]
		if c.Board.ATS != posting.TalentBrew || c.Board.Host != "jobs.intuit.com" || c.Board.Token != "jobs-intuit-com" {
			t.Fatalf("unexpected talentbrew candidate: %+v", c.Board)
		}
		if c.EvidenceCount != 2 {
			t.Fatalf("EvidenceCount = %d, want 2", c.EvidenceCount)
		}
		if err := config.ValidBoard(c.Board); err != nil {
			t.Fatalf("ValidBoard failed: %v", err)
		}
	})

	t.Run("avature listing URLs on domain", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://careers.twosigma.com/robots.txt":  {Status: http.StatusNotFound},
			"https://careers.twosigma.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://careers.twosigma.com/": htmlReply(`
<title>Two Sigma | Careers</title>
<a href="/careers/OpenRoles">Open Roles</a>
<a href="/careers/SearchJobs?jobOffset=10">Search Jobs</a>
`),
			"https://careers.twosigma.com/careers/OpenRoles":  htmlReply(`<title>Open Roles</title>`),
			"https://careers.twosigma.com/careers/SearchJobs": htmlReply(`<title>Search Jobs</title>`),
		})
		res, err := Discover(context.Background(), d, "careers.twosigma.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 {
			t.Fatalf("candidates = %+v, want 1", res.Candidates)
		}
		c := res.Candidates[0]
		if c.Board.ATS != posting.Avature || c.Board.Host != "careers.twosigma.com" ||
			c.Board.Token != "careers-twosigma-com" || c.Board.URL != "https://careers.twosigma.com/careers/OpenRoles" {
			t.Fatalf("unexpected avature candidate: %+v", c.Board)
		}
		if err := config.ValidBoard(c.Board); err != nil {
			t.Fatalf("ValidBoard failed: %v", err)
		}
	})

	t.Run("eightfold without query param gets valid default search", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/":            htmlReply(`<a href="https://acme.eightfold.ai/careers">Jobs</a>`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Eightfold {
			t.Fatalf("candidates = %+v, want 1 eightfold", res.Candidates)
		}
		if err := config.ValidBoard(res.Candidates[0].Board); err != nil {
			t.Fatalf("ValidBoard failed: %v", err)
		}
	})

	t.Run("greenhouse embed without for param and plain careers host do not produce bogus boards", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/": htmlReply(`
<iframe src="https://boards.greenhouse.io/embed/job_board"></iframe>
<a href="https://careers.other.example/careers">Not Eightfold</a>
<a href="https://careers.acme-group.example/careers?domain=acme.com">Eightfold Custom</a>
`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Eightfold ||
			res.Candidates[0].Board.Host != "careers.acme-group.example" || res.Candidates[0].Board.Token != "acme.com" {
			t.Fatalf("candidates = %+v, want only ef/acme.com", res.Candidates)
		}
		if err := config.ValidBoard(res.Candidates[0].Board); err != nil {
			t.Fatalf("ValidBoard failed: %v", err)
		}
	})
}

func TestDiscoverPGSynthesisAndRobotsBlocked(t *testing.T) {
	t.Parallel()

	t.Run("pg synthesis from career page links allowed by robots", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusOK, Body: []byte("User-agent: *\nAllow: /\n")},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/": htmlReply(`
<title>Careers at Acme</title>
<a href="/careers">Join us</a>
`),
			"https://acme.com/careers": htmlReply(`
<a href="/careers/benefits">Benefits</a>
<a href="/careers/openings/staff-swe">Staff SWE</a>
<a href="/careers/openings/ml-researcher">ML Researcher</a>
<a href="/positions/single-only">One Position</a>
`),
		})

		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 {
			t.Fatalf("candidates = %+v, want 1 pg candidate", res.Candidates)
		}
		c := res.Candidates[0]
		if c.Board.ATS != posting.Page {
			t.Fatalf("ATS = %s, want pg", c.Board.ATS)
		}
		if c.Board.Token != "acme-com" || c.Board.URL != "https://acme.com/careers" {
			t.Fatalf("board = %+v", c.Board)
		}
		wantPattern := `^https://acme\.com/careers/openings/.+`
		if c.Board.LinkPattern != wantPattern {
			t.Fatalf("LinkPattern = %q, want %q", c.Board.LinkPattern, wantPattern)
		}
		re := regexp.MustCompile(c.Board.LinkPattern)
		for _, sample := range c.SampleURLs {
			if !re.MatchString(sample) {
				t.Errorf("sample %q does not match LinkPattern %q", sample, c.Board.LinkPattern)
			}
		}
		if c.RobotsBlocked {
			t.Fatal("RobotsBlocked = true, want false")
		}
		if c.Board.Company != "Acme" {
			t.Fatalf("Board.Company = %q, want Acme", c.Board.Company)
		}
		if err := config.ValidBoard(c.Board); err != nil {
			t.Fatalf("ValidBoard failed: %v", err)
		}
	})

	t.Run("pg synthesis marks RobotsBlocked when posting URLs disallowed", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusOK, Body: []byte("User-agent: *\nAllow: /\nDisallow: /jobs/\n")},
			"https://acme.com/sitemap.xml": xmlReply(`
<urlset>
  <url><loc>https://acme.com/jobs/role-1</loc></url>
  <url><loc>https://acme.com/jobs/role-2</loc></url>
</urlset>`),
			"https://acme.com/": htmlReply(`<title>Join our team!</title>`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.Company != "" {
			t.Fatalf("Company = %q, want empty for generic title", res.Company)
		}
		if len(res.Candidates) != 1 {
			t.Fatalf("candidates = %+v, want 1", res.Candidates)
		}
		c := res.Candidates[0]
		if c.Board.ATS != posting.Page || !c.RobotsBlocked {
			t.Fatalf("got %+v, want pg with RobotsBlocked=true", c)
		}
		if c.Board.Company != "acme.com" {
			t.Fatalf("Company fallback = %q, want acme.com", c.Board.Company)
		}
	})

	t.Run("no pg synthesis when structured board exists or fewer than 2 URLs", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/": htmlReply(`
<a href="https://boards.greenhouse.io/acme">GH</a>
<a href="/jobs/role-1">Role 1</a>
<a href="/jobs/role-2">Role 2</a>
`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Greenhouse {
			t.Fatalf("candidates = %+v, want only gh", res.Candidates)
		}
	})

	t.Run("pg synthesis preserves listing URL when job URLs also appear in sitemap and supports multi-team subdirs", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": xmlReply(`
<urlset>
  <url><loc>https://acme.com/careers/engineering/swe-1</loc></url>
  <url><loc>https://acme.com/careers/product/pm-1</loc></url>
</urlset>`),
			"https://acme.com/":        htmlReply(`<title>Acme</title><a href="/careers">Careers</a>`),
			"https://acme.com/careers": htmlReply(`<a href="/careers/engineering/swe-1">SWE</a><a href="/careers/product/pm-1">PM</a>`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if len(res.Candidates) != 1 {
			t.Fatalf("candidates = %+v, want 1", res.Candidates)
		}
		c := res.Candidates[0]
		if c.Board.ATS != posting.Page || c.Board.URL != "https://acme.com/careers" ||
			c.Board.LinkPattern != `^https://acme\.com/careers/.+` {
			t.Fatalf("unexpected pg board: %+v", c.Board)
		}
		if err := config.ValidBoard(c.Board); err != nil {
			t.Fatalf("ValidBoard failed: %v", err)
		}
	})
}

func TestIconAndCompanyCleanup(t *testing.T) {
	t.Parallel()

	t.Run("skips svg and insecure http and prefers png over ico", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/": htmlReply(`
<base href="https://acme.com/en/">
<meta property="og:site_name" content="Greenhouse">
<title>Jobs &amp; Careers | Acme&#32;AI</title>
<link rel="icon" type="image/svg+xml" href="/icon.svg">
<link rel="apple-touch-icon" href="http://cdn.acme.com/insecure.png">
<link rel="icon" href="https://cdn.acme.com:8443/badport.png">
<link rel="icon" href="assets/favicon.ico">
<link rel="icon" type="image/png" href="assets/logo.png#frag">
`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.IconURL != "https://acme.com/en/assets/logo.png" {
			t.Fatalf("IconURL = %q, want https://acme.com/en/assets/logo.png", res.IconURL)
		}
		if res.Company != "Acme AI" {
			t.Fatalf("Company = %q, want %q", res.Company, "Acme AI")
		}
	})
}

func TestOversizeRefusalAndNonHTML(t *testing.T) {
	t.Parallel()

	t.Run("oversized sitemap refused without reading past cap", func(t *testing.T) {
		t.Parallel()
		body := &stopAfter{allow: MaxSitemapBytes + 1, ch: ' '}
		d := swapBodyDoer{
			base: srctest.New(map[string]srctest.Reply{
				"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
				"https://acme.com/sitemap.xml": {Status: http.StatusOK},
				"https://acme.com/":            htmlReply(`<a href="https://jobs.lever.co/acme">Jobs</a>`),
			}),
			swap: func(req *http.Request, resp *http.Response) {
				if req.URL.Path == "/sitemap.xml" {
					resp.Body.Close()
					resp.Body = body
				}
			},
		}
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.SitemapsRead != 0 {
			t.Fatalf("SitemapsRead = %d, want 0 for oversized sitemap", res.SitemapsRead)
		}
		if body.over || body.n != MaxSitemapBytes+1 {
			t.Fatalf("read %d bytes (over=%v), want exact stop at %d", body.n, body.over, MaxSitemapBytes+1)
		}
		if !slices.ContainsFunc(res.Notes, func(n string) bool { return strings.Contains(n, "size limit") }) {
			t.Fatalf("expected size limit note in %v", res.Notes)
		}
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Lever {
			t.Fatalf("expected fallback HTML discovery to still find lv/acme, got %+v", res.Candidates)
		}
	})

	t.Run("oversized HTML refused without scanning partial bytes", func(t *testing.T) {
		t.Parallel()
		body := &stopAfter{allow: MaxHTMLBytes + 1, ch: 'a'}
		d := swapBodyDoer{
			base: srctest.New(map[string]srctest.Reply{
				"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
				"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
				"https://acme.com/":            htmlReply(""),
			}),
			swap: func(req *http.Request, resp *http.Response) {
				if req.URL.Path == "/" {
					resp.Body.Close()
					resp.Body = body
				}
			},
		}
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.PagesRead != 0 {
			t.Fatalf("PagesRead = %d, want 0 for oversized HTML", res.PagesRead)
		}
		if body.over || body.n != MaxHTMLBytes+1 {
			t.Fatalf("read %d bytes (over=%v), want exact stop at %d", body.n, body.over, MaxHTMLBytes+1)
		}
	})

	t.Run("non-HTML responses refused", func(t *testing.T) {
		t.Parallel()
		for name, rp := range map[string]srctest.Reply{
			"pdf": {
				Status: http.StatusOK,
				Header: http.Header{"Content-Type": {"application/pdf"}},
				Body:   []byte("%PDF-1.7 <a href='https://boards.greenhouse.io/evil'>evil</a>"),
			},
			"json body": {
				Status: http.StatusOK,
				Body:   []byte(`{"html":"<a href='https://boards.greenhouse.io/evil'>evil</a>"}`),
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				d := srctest.New(map[string]srctest.Reply{
					"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
					"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
					"https://acme.com/":            rp,
				})
				res, err := Discover(context.Background(), d, "acme.com", Options{})
				if err != nil {
					t.Fatalf("Discover failed: %v", err)
				}
				if res.PagesRead != 0 || len(res.Candidates) != 0 {
					t.Fatalf("%s should not be scanned: PagesRead=%d Candidates=%+v", name, res.PagesRead, res.Candidates)
				}
			})
		}
	})
}

func TestMalformedXMLAndHostileHTML(t *testing.T) {
	t.Parallel()

	t.Run("malformed XML discards partial sitemap entries", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt": {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": xmlReply(`<urlset>
  <url><loc>https://boards.greenhouse.io/partial-evil</loc></url>
  <url><loc>https://acme.com/broken`),
			"https://acme.com/": htmlReply(`<title>Acme</title>`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.SitemapsRead != 0 || len(res.Candidates) != 0 {
			t.Fatalf("malformed XML leaked entries: SitemapsRead=%d Candidates=%+v", res.SitemapsRead, res.Candidates)
		}
	})

	t.Run("gzip sitemap refused", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusOK, Body: []byte{0x1f, 0x8b, 0x08, 0x00}},
			"https://acme.com/":            htmlReply(`<title>Acme</title>`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.SitemapsRead != 0 {
			t.Fatalf("SitemapsRead = %d, want 0", res.SitemapsRead)
		}
	})

	t.Run("hostile and truncated HTML stops safely without script injection", func(t *testing.T) {
		t.Parallel()
		doc := `
<meta property="og:site_name" content="Acme&#x0007; &amp; Co">
<svg><title/></svg>
<title>Ignored Later</title>
<script>const decoy = '<a href="https://boards.greenhouse.io/decoy">decoy</a>';</script>
<a href="/careers">Careers</a>
<!-- unterminated comment with fake <a href="https://boards.greenhouse.io/fake">fake</a>`
		d := srctest.New(map[string]srctest.Reply{
			"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
			"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
			"https://acme.com/":            htmlReply(doc),
			"https://acme.com/careers": htmlReply(`
<a href="https://jobs.lever.co/acme">Lever</a>
<script>var unclosed = '<a href="https://boards.greenhouse.io/unclosed">x</a>'`),
		})
		res, err := Discover(context.Background(), d, "acme.com", Options{})
		if err != nil {
			t.Fatalf("Discover failed: %v", err)
		}
		if res.Company != "Acme & Co" {
			t.Fatalf("Company = %q, want %q", res.Company, "Acme & Co")
		}
		if len(res.Candidates) != 1 || res.Candidates[0].Board.ATS != posting.Lever {
			t.Fatalf("candidates = %+v, want only lv/acme", res.Candidates)
		}
	})
}

func TestContextCancellationAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("cancelled before start", func(t *testing.T) {
		t.Parallel()
		d := srctest.New(map[string]srctest.Reply{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Discover(ctx, d, "acme.com", Options{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if len(d.URLs()) != 0 {
			t.Fatalf("made requests after cancel: %v", d.URLs())
		}
	})

	t.Run("cancelled during page fetch", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d := &funcDoer{fn: func(rctx context.Context, req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/robots.txt":
				return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			case "/sitemap.xml":
				cancel()
				<-rctx.Done()
				return nil, rctx.Err()
			default:
				return nil, fmt.Errorf("unexpected %s", req.URL.String())
			}
		}}
		_, err := Discover(ctx, d, "acme.com", Options{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("per-request timeout on robots fails closed", func(t *testing.T) {
		t.Parallel()
		d := &funcDoer{fn: func(rctx context.Context, _ *http.Request) (*http.Response, error) {
			<-rctx.Done()
			return nil, rctx.Err()
		}}
		_, err := Discover(context.Background(), d, "acme.com", Options{RequestTimeout: 20 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("redirect off domain refused", func(t *testing.T) {
		t.Parallel()
		d := &funcDoer{fn: func(_ context.Context, req *http.Request) (*http.Response, error) {
			evilReq := req.Clone(context.Background())
			evilReq.URL, _ = url.Parse("https://evil.example/robots.txt")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("User-agent: *\nAllow: /\n")),
				Request:    evilReq,
			}, nil
		}}
		if _, err := Discover(context.Background(), d, "acme.com", Options{}); err == nil {
			t.Fatal("off-domain redirect succeeded, want fail-closed error")
		}
	})
}

func TestMaxCandidatesCap(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	sb.WriteString(`<html><body>`)
	for i := range MaxCandidates + 10 {
		fmt.Fprintf(&sb, `<a href="https://boards.greenhouse.io/company-%03d">Job %d</a>`, i, i)
	}
	sb.WriteString(`</body></html>`)

	d := srctest.New(map[string]srctest.Reply{
		"https://acme.com/robots.txt":  {Status: http.StatusNotFound},
		"https://acme.com/sitemap.xml": {Status: http.StatusNotFound},
		"https://acme.com/":            htmlReply(sb.String()),
	})
	res, err := Discover(context.Background(), d, "acme.com", Options{})
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if !res.Capped {
		t.Fatal("Capped = false, want true")
	}
	if len(res.Candidates) != MaxCandidates {
		t.Fatalf("len(Candidates) = %d, want %d", len(res.Candidates), MaxCandidates)
	}
}

type stopAfter struct {
	allow int
	n     int
	over  bool
	ch    byte
}

func (s *stopAfter) Read(p []byte) (int, error) {
	if s.n >= s.allow {
		s.over = true
		return 0, errors.New("read past cap")
	}
	room := s.allow - s.n
	if len(p) > room {
		p = p[:room]
	}
	for i := range p {
		p[i] = s.ch
	}
	s.n += len(p)
	return len(p), nil
}

func (s *stopAfter) Close() error { return nil }

type swapBodyDoer struct {
	base egress.Doer
	swap func(*http.Request, *http.Response)
}

func (s swapBodyDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := s.base.Do(ctx, req)
	if err != nil || resp == nil || s.swap == nil {
		return resp, err
	}
	s.swap(req, resp)
	return resp, nil
}

type funcDoer struct {
	mu   sync.Mutex
	urls []string
	fn   func(context.Context, *http.Request) (*http.Response, error)
}

func (f *funcDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.urls = append(f.urls, req.URL.String())
	f.mu.Unlock()
	return f.fn(ctx, req)
}
