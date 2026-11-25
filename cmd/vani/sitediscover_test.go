package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/site"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

func fakeSiteDoer(t *testing.T, fn func(srctest.Request) (srctest.Reply, bool)) *srctest.Doer {
	t.Helper()
	d := srctest.NewFunc(fn)
	old := newSiteDiscoverDoer
	newSiteDiscoverDoer = func(string) (egress.Doer, error) { return d, nil }
	t.Cleanup(func() { newSiteDiscoverDoer = old })
	return d
}

func siteHTML(body string) srctest.Reply {
	return srctest.Reply{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:   []byte(body),
	}
}

func siteDiscoverJSONOf(t *testing.T, out string) siteDiscoverJSON {
	t.Helper()
	var doc siteDiscoverJSON
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil || dec.More() {
		t.Fatalf("not siteDiscoverJSON: %v\n%s", err, out)
	}
	return doc
}

func TestSiteDiscoverRefusesWhenHostNotAllowed(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	d := fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: http.StatusNotFound}, true
	})

	_, e := mustCode(t, 1, "watchlist", "discover", "--site", "acme.com")
	if !strings.Contains(e, "acme.com") || !strings.Contains(e, "allowed_hosts") || !strings.Contains(e, "--allow-host") {
		t.Errorf("refusal stderr = %q", e)
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("%d requests sent before host allowed: %v", n, d.URLs())
	}
	cfg := watchlistOf(t, cfgPath)
	if slices.Contains(cfg.AllowedHosts, "acme.com") || len(cfg.Watchlist) != 0 {
		t.Errorf("refused discover changed config: allowed=%v watchlist=%v", cfg.AllowedHosts, cfg.Watchlist)
	}

	// Under --json, refusal is a single runtime error envelope with zero requests.
	o, _ := mustCode(t, 1, "watchlist", "discover", "--site", "acme.com", "--json")
	if r := onlyError(t, []string{"watchlist", "discover", "--site", "acme.com", "--json"}, o); r.Code != codeRuntime ||
		!strings.Contains(r.Error, "--allow-host") {
		t.Errorf("json refusal = %+v", r)
	}
	if len(d.URLs()) != 0 {
		t.Errorf("requests sent under --json refusal: %v", d.URLs())
	}
}

func TestSiteDiscoverAllowHostPersistenceAndWildcard(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	d := fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case "https://acme.com/robots.txt":
			return srctest.Reply{Status: http.StatusNotFound}, true
		case "https://acme.com/sitemap.xml":
			return srctest.Reply{Status: http.StatusNotFound}, true
		case "https://acme.com/":
			return siteHTML(`<title>Acme</title>`), true
		default:
			return srctest.Reply{Status: http.StatusNotFound}, true
		}
	})

	o, e := mustCode(t, 0, "watchlist", "discover", "--site", "acme.com", "--allow-host")
	if !strings.Contains(e, "added acme.com to allowed_hosts") {
		t.Errorf("stderr %q lacks added notice", e)
	}
	if !strings.Contains(o, "acme.com: read 0 sitemap(s), 1 page(s)") {
		t.Errorf("stdout %q", o)
	}
	cfg := watchlistOf(t, cfgPath)
	if n := countOf(cfg.AllowedHosts, "acme.com"); n != 1 {
		t.Errorf("allowed_hosts = %v, acme.com count = %d", cfg.AllowedHosts, n)
	}
	if len(d.URLs()) == 0 {
		t.Fatal("expected network requests after --allow-host")
	}

	// Once allowed, it works without --allow-host, and repeating --allow-host is idempotent.
	mustCode(t, 0, "watchlist", "discover", "--site", "acme.com")
	_, e = mustCode(t, 0, "watchlist", "discover", "--site", "acme.com", "--allow-host")
	if strings.Contains(e, "added") || countOf(watchlistOf(t, cfgPath).AllowedHosts, "acme.com") != 1 {
		t.Errorf("second --allow-host: stderr=%q hosts=%v", e, watchlistOf(t, cfgPath).AllowedHosts)
	}

	// Wildcard entry in allowed_hosts allows subdomains without --allow-host.
	cfg = watchlistOf(t, cfgPath)
	cfg.AllowedHosts = append(cfg.AllowedHosts, "*.acme.com")
	writeConfig(t, cfgPath, cfg)
	fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == "https://careers.acme.com/" {
			return siteHTML(`<title>Acme Careers</title>`), true
		}
		return srctest.Reply{Status: http.StatusNotFound}, true
	})
	mustCode(t, 0, "watchlist", "discover", "--site", "careers.acme.com")
	if slices.Contains(watchlistOf(t, cfgPath).AllowedHosts, "careers.acme.com") {
		t.Errorf("wildcard-allowed host should not append literal entry: %v", watchlistOf(t, cfgPath).AllowedHosts)
	}
}

func TestSiteDiscoverJSONEmptyArraysNeverNull(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case "https://quiet.example/robots.txt":
			return srctest.Reply{Status: http.StatusOK, Body: []byte("User-agent: *\nAllow: /\n")}, true
		case "https://quiet.example/sitemap.xml":
			return srctest.Reply{Status: http.StatusOK, Body: []byte(`<urlset></urlset>`)}, true
		case "https://quiet.example/":
			return siteHTML(`<html><body><p>Welcome</p></body></html>`), true
		default:
			return srctest.Reply{Status: http.StatusNotFound}, true
		}
	})

	o, _ := mustCode(t, 0, "watchlist", "discover", "--site", "quiet.example", "--allow-host", "--json")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(o), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"robots_disallowed", "candidates", "notes"} {
		if string(raw[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, raw[k])
		}
	}
	doc := siteDiscoverJSONOf(t, o)
	if doc.Domain != "quiet.example" || !doc.RobotsFetched || doc.SitemapsRead != 1 || doc.PagesRead != 1 {
		t.Errorf("unexpected doc: %+v", doc)
	}

	o, _ = mustCode(t, 0, "watchlist", "discover", "--site", "quiet.example")
	if !strings.Contains(o, "no candidate boards found") {
		t.Errorf("text output = %q", o)
	}
}

func TestSiteDiscoverJSONWatchedMarkingAndIconAllowHost(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	// Watch gh/Acme (different case) and pg/other without touching allowed_hosts for boards-api/cdn.
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "Acme", "--company", "Acme", "--allow-host")

	fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case "https://acme.com/robots.txt":
			return srctest.Reply{Status: http.StatusOK, Body: []byte("User-agent: *\nAllow: /\nDisallow: /careers\n")}, true
		case "https://acme.com/sitemap.xml":
			return srctest.Reply{Status: http.StatusNotFound}, true
		case "https://acme.com/":
			return siteHTML(`
<html><head>
  <meta property="og:site_name" content="O'Brien &amp; $(id) Co">
  <link rel="icon" href="https://cdn.acme-assets.example/favicon.png">
</head><body>
  <a href="/careers">Careers</a>
  <a href="https://boards.greenhouse.io/acme/jobs/1">Job 1</a>
  <a href="https://boards.greenhouse.io/acme/jobs/2">Job 2</a>
  <a href="https://jobs.lever.co/acme-eu/role-1">EU Role</a>
</body></html>`), true
		default:
			return srctest.Reply{Status: http.StatusNotFound}, true
		}
	})

	before := watchlistOf(t, cfgPath)
	o, _ := mustCode(t, 0, "watchlist", "discover", "--site", "acme.com", "--allow-host", "--json")
	doc := siteDiscoverJSONOf(t, o)
	if doc.Domain != "acme.com" || doc.Company != "O'Brien & $(id) Co" ||
		doc.IconURL != "https://cdn.acme-assets.example/favicon.png" ||
		!slices.Equal(doc.RobotsDisallowed, []string{"/careers"}) {
		t.Fatalf("doc header = %+v", doc)
	}
	if len(doc.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2", doc.Candidates)
	}

	var ghCand, lvCand siteCandidateJSON
	for _, c := range doc.Candidates {
		switch c.ATS {
		case "gh":
			ghCand = c
		case "lv":
			lvCand = c
		}
	}
	if !ghCand.Watched || ghCand.AddCommand != "" || ghCand.EvidenceCount != 2 {
		t.Errorf("gh watched candidate = %+v", ghCand)
	}
	if lvCand.Watched || lvCand.Token != "acme-eu" || lvCand.Domain != "acme.com" ||
		lvCand.Icon != "https://cdn.acme-assets.example/favicon.png" {
		t.Errorf("lv candidate = %+v", lvCand)
	}
	if !strings.Contains(lvCand.AddCommand, "--company 'O'\\''Brien & $(id) Co'") ||
		!strings.Contains(lvCand.AddCommand, "--domain acme.com") ||
		!strings.Contains(lvCand.AddCommand, "--icon https://cdn.acme-assets.example/favicon.png") ||
		!strings.Contains(lvCand.AddCommand, "--allow-host") {
		t.Errorf("lv addCommand = %q", lvCand.AddCommand)
	}

	// Even when api.lever.co and acme.com are allowed, a distinct icon host still requires --allow-host!
	cfg := watchlistOf(t, cfgPath)
	cfg.AllowedHosts = append(cfg.AllowedHosts, "api.lever.co")
	writeConfig(t, cfgPath, cfg)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--site", "acme.com", "--json")
	doc = siteDiscoverJSONOf(t, o)
	for _, c := range doc.Candidates {
		if c.ATS == "lv" {
			if !strings.Contains(c.AddCommand, "--allow-host") {
				t.Errorf("expected --allow-host when IconHost cdn.acme-assets.example is not allowed: %q", c.AddCommand)
			}
		}
	}

	// Once cdn.acme-assets.example is also allowed, --allow-host is omitted!
	cfg = watchlistOf(t, cfgPath)
	cfg.AllowedHosts = append(cfg.AllowedHosts, "cdn.acme-assets.example")
	writeConfig(t, cfgPath, cfg)
	o, _ = mustCode(t, 0, "watchlist", "discover", "--site", "acme.com", "--json")
	doc = siteDiscoverJSONOf(t, o)
	for _, c := range doc.Candidates {
		if c.ATS == "lv" {
			if strings.Contains(c.AddCommand, "--allow-host") {
				t.Errorf("unexpected --allow-host when all hosts allowed: %q", c.AddCommand)
			}
		}
	}

	// Verify discovery never modifies the watchlist.
	after := watchlistOf(t, cfgPath)
	if len(after.Watchlist) != len(before.Watchlist) {
		t.Errorf("watchlist changed: %d -> %d", len(before.Watchlist), len(after.Watchlist))
	}
}

func TestSiteDiscoverPGSynthesisAndTextOutput(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case "https://acme.com/robots.txt":
			return srctest.Reply{Status: http.StatusOK, Body: []byte("User-agent: *\nAllow: /\nDisallow: /jobs\nDisallow: /careers/openings/\n")}, true
		case "https://acme.com/sitemap.xml":
			return srctest.Reply{Status: http.StatusNotFound}, true
		case "https://acme.com/":
			return siteHTML(`<title>Acme Labs | Build Systems</title><a href="/careers">Careers</a><a href="/jobs">Jobs</a>`), true
		case "https://acme.com/careers":
			return siteHTML(`
<a href="/careers/openings/staff-go">Staff Go</a>
<a href="/careers/openings/rust-eng">Rust Eng</a>
`), true
		default:
			return srctest.Reply{Status: http.StatusNotFound}, true
		}
	})

	o, _ := mustCode(t, 0, "watchlist", "discover", "--site", "acme.com", "--allow-host", "--json")
	doc := siteDiscoverJSONOf(t, o)
	if len(doc.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want 1 pg candidate", doc.Candidates)
	}
	c := doc.Candidates[0]
	if c.ATS != "pg" || c.Token != "acme-com" || c.URL != "https://acme.com/careers" ||
		!c.RobotsBlocked || c.Watched {
		t.Fatalf("pg candidate = %+v", c)
	}
	// acme.com is already in allowed_hosts and there is no external icon host, so --allow-host is omitted.
	if strings.Contains(c.AddCommand, "--allow-host") {
		t.Errorf("pg addCommand should omit --allow-host when acme.com is already allowed: %q", c.AddCommand)
	}
	if !strings.Contains(c.AddCommand, "--url https://acme.com/careers") ||
		!strings.Contains(c.AddCommand, "--link-pattern '^https://acme\\.com/careers/openings/.+'") ||
		!strings.Contains(c.AddCommand, "--domain acme.com") {
		t.Errorf("pg addCommand missing expected flags: %q", c.AddCommand)
	}

	// Text mode prints readable summary, disallowed path, robots-blocked status, and add command.
	textOut, _ := mustCode(t, 0, "watchlist", "discover", "--site", "acme.com")
	for _, want := range []string{
		"acme.com: read 0 sitemap(s), 2 page(s)",
		"company: Acme Labs",
		"robots.txt disallowed (1): /jobs",
		"2  pg  acme-com  Acme Labs  (via careers)  [new, robots-blocked]",
		c.AddCommand,
	} {
		if !strings.Contains(textOut, want) {
			t.Errorf("text output lacks %q:\n%s", want, textOut)
		}
	}
}

func TestSiteDiscoverUsageAndConflictingFlags(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	d := fakeSiteDoer(t, func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: http.StatusNotFound}, true
	})

	badCases := [][]string{
		{"--site", "acme.com", "--search", "ml"},
		{"--site", "acme.com", "--profile"},
		{"--site", "acme.com", "--country", "US"},
		{"--site", "acme.com", "--work-mode", "remote"},
		{"--site", "acme.com", "--seniority", "senior"},
		{"--site", "acme.com", "--pages", "2"},
		{"--site", "acme.com", "--max-searches", "2"},
		{"--site", "acme.com", "--max-skills", "1"},
		{"--site", "acme.com", "--no-kb"},
		{"--site", "acme.com", "--dry-run"},
		{"--site", "acme.com", "--add", "1"},
		{"--site", "acme.com", "--allow-board-hosts"},
		{"--site", ""},
		{"--site", "   "},
		{"--site", "Acme.com"},
		{"--site", "https://acme.com"},
		{"--site", "acme.com:443"},
		{"--site", "acme.com/careers"},
		{"--site", "user@acme.com"},
		{"--site", "localhost"},
		{"--site", "127.0.0.1"},
		{"--site", "::1"},
		{"--site", "acme.com\n"},
		{"--site", "acme.com", "extra"},
	}
	for _, args := range badCases {
		full := append([]string{"watchlist", "discover"}, args...)
		if code, _, e := do(t, append(full, "--allow-host")...); code != 2 {
			t.Errorf("vani %v: exit %d, want 2 (stderr %q)", full, code, e)
		}
		// Command's own --json usage error exits 2 with empty stdout.
		if code, o, e := do(t, append(full, "--json")...); code != 2 || o != "" {
			t.Errorf("vani %v --json: exit=%d stdout=%q stderr=%q", full, code, o, e)
		}
		// Global --json usage error exits 2 with usage JSON envelope.
		globalArgs := append([]string{"--json"}, full...)
		if code, o, _ := do(t, globalArgs...); code != 2 {
			t.Errorf("vani %v: exit %d, want 2", globalArgs, code)
		} else if r := onlyError(t, globalArgs, o); r.Code != codeUsage {
			t.Errorf("vani %v: envelope=%+v", globalArgs, r)
		}
	}
	if len(d.URLs()) != 0 {
		t.Errorf("refused flags sent %d requests: %v", len(d.URLs()), d.URLs())
	}
}

func TestSiteDiscoverFailClosedErrors(t *testing.T) {
	t.Run("robots 500", func(t *testing.T) {
		cfgPath, _ := setup(t)
		mustCode(t, 0, "init")
		fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
			if r.URL == "https://acme.com/robots.txt" {
				return srctest.Reply{Status: http.StatusInternalServerError}, true
			}
			return srctest.Reply{Status: http.StatusNotFound}, true
		})
		o, e := mustCode(t, 1, "watchlist", "discover", "--site", "acme.com", "--allow-host", "--json")
		if !strings.Contains(e, "status 500") {
			t.Errorf("stderr = %q", e)
		}
		if r := onlyError(t, nil, o); r.Code != codeRuntime {
			t.Errorf("envelope = %+v", r)
		}
		if len(watchlistOf(t, cfgPath).Watchlist) != 0 {
			t.Error("failed site discover modified watchlist")
		}
	})

	t.Run("oversized robots body", func(t *testing.T) {
		setup(t)
		mustCode(t, 0, "init")
		fakeSiteDoer(t, func(r srctest.Request) (srctest.Reply, bool) {
			if r.URL == "https://acme.com/robots.txt" {
				return srctest.Reply{Status: http.StatusOK, Body: bytes.Repeat([]byte("a"), site.MaxRobotsBytes+1)}, true
			}
			return srctest.Reply{Status: http.StatusNotFound}, true
		})
		o, _ := mustCode(t, 1, "watchlist", "discover", "--site", "acme.com", "--allow-host", "--json")
		if r := onlyError(t, nil, o); r.Code != codeRuntime || !strings.Contains(r.Error, "exceeds size limit") {
			t.Errorf("envelope = %+v", r)
		}
	})

	t.Run("redirect off domain refused", func(t *testing.T) {
		setup(t)
		mustCode(t, 0, "init")
		old := newSiteDiscoverDoer
		newSiteDiscoverDoer = func(string) (egress.Doer, error) {
			return siteFuncDoer(func(_ context.Context, req *http.Request) (*http.Response, error) {
				evil := req.Clone(context.Background())
				evil.URL, _ = url.Parse("https://evil.example/robots.txt")
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader("User-agent: *\nAllow: /\n")),
					Request:    evil,
				}, nil
			}), nil
		}
		t.Cleanup(func() { newSiteDiscoverDoer = old })

		o, _ := mustCode(t, 1, "watchlist", "discover", "--site", "acme.com", "--allow-host", "--json")
		if r := onlyError(t, nil, o); r.Code != codeRuntime || !strings.Contains(r.Error, "redirected off") {
			t.Errorf("envelope = %+v", r)
		}
	})

	t.Run("doer constructor error", func(t *testing.T) {
		setup(t)
		mustCode(t, 0, "init")
		old := newSiteDiscoverDoer
		newSiteDiscoverDoer = func(string) (egress.Doer, error) {
			return nil, errors.New("boom doer")
		}
		t.Cleanup(func() { newSiteDiscoverDoer = old })

		o, e := mustCode(t, 1, "watchlist", "discover", "--site", "acme.com", "--allow-host", "--json")
		if !strings.Contains(e, "boom doer") {
			t.Errorf("stderr = %q", e)
		}
		if r := onlyError(t, nil, o); r.Code != codeRuntime || !strings.Contains(r.Error, "boom doer") {
			t.Errorf("envelope = %+v", r)
		}
	})
}

func TestSiteDiscoverDoerReachesOnlyTargetDomain(t *testing.T) {
	d, err := newSiteDiscoverDoer("acme.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://example.com/", "https://freehire.me/", "https://sub.acme.com/"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if resp, err := d.Do(context.Background(), req); err == nil {
			resp.Body.Close()
			t.Fatalf("site discover doer reached %s", target)
		} else if !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("%s err = %v, want allowlist refusal", target, err)
		}
	}
}

func TestWatchlistAddPageHardeningFlagsAndAddCommand(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")

	o, _ := mustCode(t, 0, "watchlist", "add",
		"--ats", "pg",
		"--token", "acme-pg",
		"--company", "Acme",
		"--url", "https://careers.acme.example/jobs",
		"--link-pattern", `^https://careers\.acme\.example/jobs/\d+$`,
		"--next-pattern", `\?page=\d+$`,
		"--scroll-steps", "4",
		"--wait-selector", ".job-card, [data-job='1']",
		"--extract-title", "h1.role",
		"--extract-description", "main .jd",
		"--extract-location", ".loc",
		"--extract-company", ".co",
		"--domain", "acme.example",
		"--icon", "https://cdn.acme.example/icon.png",
		"--allow-host",
		"--json",
	)
	var reply boardReply
	decodeOne(t, o, &reply)
	b := reply.Board
	if b.ATS != posting.Page || b.NextPattern != `\?page=\d+$` || b.ScrollSteps != 4 ||
		b.WaitSelector != ".job-card, [data-job='1']" || b.Extract == nil ||
		b.Extract.Title != "h1.role" || b.Extract.Description != "main .jd" ||
		b.Extract.Location != ".loc" || b.Extract.Company != ".co" {
		t.Fatalf("unexpected boardReply: %+v (extract %+v)", b, b.Extract)
	}

	// Persists in config.json and surfaces in `watchlist list --json` and `config check`.
	mustCode(t, 0, "config", "check")
	cfg := watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 1 {
		t.Fatalf("watchlist len = %d", len(cfg.Watchlist))
	}
	saved := cfg.Watchlist[0]
	if saved.NextPattern != `\?page=\d+$` || saved.ScrollSteps != 4 ||
		saved.WaitSelector != ".job-card, [data-job='1']" || saved.Extract == nil || *saved.Extract != *b.Extract {
		t.Fatalf("saved board = %+v (extract %+v)", saved, saved.Extract)
	}

	listOut, _ := mustCode(t, 0, "watchlist", "list", "--json")
	var listed []watchlistJSON
	decodeOne(t, listOut, &listed)
	if len(listed) != 1 || listed[0].NextPattern != saved.NextPattern ||
		listed[0].ScrollSteps != 4 || listed[0].WaitSelector != saved.WaitSelector ||
		listed[0].Extract == nil || *listed[0].Extract != *saved.Extract {
		t.Fatalf("listed JSON = %+v", listed)
	}

	// addCommand emits all non-empty/non-zero pg fields, checks Domain & IconHost, and stays shell-safe.
	cmdStr := addCommand(saved, []string{"careers.acme.example", "acme.example"})
	for _, want := range []string{
		"--url https://careers.acme.example/jobs",
		"--link-pattern '^https://careers\\.acme\\.example/jobs/\\d+$'",
		"--next-pattern '\\?page=\\d+$'",
		"--scroll-steps 4",
		"--wait-selector '.job-card, [data-job='\\''1'\\'']'",
		"--extract-title h1.role",
		"--extract-description 'main .jd'",
		"--extract-location .loc",
		"--extract-company .co",
		"--domain acme.example",
		"--icon https://cdn.acme.example/icon.png",
		"--allow-host", // cdn.acme.example is not yet in allowed
	} {
		if !strings.Contains(cmdStr, want) {
			t.Errorf("addCommand missing %s:\n%s", want, cmdStr)
		}
	}
	if full := addCommand(saved, []string{"careers.acme.example", "acme.example", "cdn.acme.example"}); strings.Contains(full, "--allow-host") {
		t.Errorf("addCommand emitted --allow-host when all hosts allowed: %s", full)
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		// Verify shell tokenization of the emitted addCommand round-trips the wait-selector and link-pattern intact.
		shCmd := exec.Command(sh, "-c", "set -- "+cmdStr+`; printf '%s\n' "$@"`)
		shCmd.Dir = t.TempDir()
		if out, err := shCmd.Output(); err != nil {
			t.Fatalf("sh parse failed: %v", err)
		} else if lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !slices.Contains(lines, ".job-card, [data-job='1']") ||
			!slices.Contains(lines, `^https://careers\.acme\.example/jobs/\d+$`) {
			t.Errorf("shell-parsed args = %q", lines)
		}
	}

	// Partial extract sets only provided field; omitting all extract flags leaves Extract nil (no "extract" in JSON).
	o, _ = mustCode(t, 0, "watchlist", "add",
		"--ats", "pg", "--token", "partial-pg", "--company", "Partial",
		"--url", "https://partial.example/jobs", "--link-pattern", `/jobs/\d+$`,
		"--extract-title", "h1.job", "--allow-host", "--json")
	var partialReply boardReply
	decodeOne(t, o, &partialReply)
	if partialReply.Board.Extract == nil || partialReply.Board.Extract.Title != "h1.job" ||
		partialReply.Board.Extract.Description != "" || partialReply.Board.Extract.Location != "" || partialReply.Board.Extract.Company != "" {
		t.Fatalf("partial extract reply = %+v", partialReply.Board.Extract)
	}
	if strings.Contains(o, `"description"`) || strings.Contains(o, `"location"`) || strings.Contains(o, `"company":`) && strings.Contains(o, `"extract":{"title":"h1.job","company"`) {
		t.Errorf("partial extract should omit unset extract subfields: %s", o)
	}

	o, _ = mustCode(t, 0, "watchlist", "add",
		"--ats", "pg", "--token", "plain-pg", "--company", "Plain",
		"--url", "https://plain.example/jobs", "--link-pattern", `/jobs/\d+$`, "--allow-host", "--json")
	if strings.Contains(o, `"extract"`) || strings.Contains(o, `"next_pattern"`) ||
		strings.Contains(o, `"scroll_steps"`) || strings.Contains(o, `"wait_selector"`) {
		t.Errorf("unset pg options should be omitted in JSON: %s", o)
	}
	if plainCmd := addCommand(sources.Board{
		ATS: posting.Page, Token: "plain-pg", Company: "Plain",
		URL: "https://plain.example/jobs", LinkPattern: `/jobs/\d+$`,
	}, []string{"plain.example"}); strings.Contains(plainCmd, "--next-pattern") ||
		strings.Contains(plainCmd, "--scroll-steps") || strings.Contains(plainCmd, "--wait-selector") ||
		strings.Contains(plainCmd, "--extract-") || strings.Contains(plainCmd, "--domain") || strings.Contains(plainCmd, "--icon") ||
		strings.Contains(plainCmd, "--allow-host") {
		t.Errorf("plain pg addCommand emitted zero flags: %s", plainCmd)
	}
}

func TestWatchlistAddRejectsPageOptionsOnNonPageAndInvalidValues(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")

	nonPGFlags := [][]string{
		{"--next-pattern", "page=2"},
		{"--scroll-steps", "2"},
		{"--wait-selector", ".jobs"},
		{"--extract-title", "h1"},
		{"--extract-description", ".jd"},
		{"--extract-location", ".loc"},
		{"--extract-company", ".co"},
	}
	for _, extra := range nonPGFlags {
		args := append([]string{"watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--allow-host"}, extra...)
		code, _, e := do(t, args...)
		if code != 2 || !strings.Contains(e, "apply to pg boards only") {
			t.Errorf("vani %v: exit=%d stderr=%q", args, code, e)
		}
	}

	invalidPGFlags := [][]string{
		{"--next-pattern", "("},
		{"--next-pattern", "   "},
		{"--scroll-steps", "-1"},
		{"--scroll-steps", "11"},
		{"--wait-selector", "   "},
		{"--wait-selector", ".a\nb"},
		{"--extract-title", "   "},
		{"--extract-description", ".jd\x00"},
	}
	for _, extra := range invalidPGFlags {
		args := append([]string{
			"watchlist", "add", "--ats", "pg", "--token", "acme-pg", "--company", "Acme",
			"--url", "https://acme.example/jobs", "--link-pattern", `/jobs/\d+$`, "--allow-host",
		}, extra...)
		if code, _, e := do(t, args...); code != 2 {
			t.Errorf("vani %v: exit=%d, want 2 (stderr=%q)", args, code, e)
		}
	}

	if cfg := watchlistOf(t, cfgPath); len(cfg.Watchlist) != 0 {
		t.Errorf("invalid adds modified watchlist: %+v", cfg.Watchlist)
	}
}

type siteFuncDoer func(context.Context, *http.Request) (*http.Response, error)

func (f siteFuncDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return f(ctx, req)
}
