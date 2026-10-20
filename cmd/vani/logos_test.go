package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func cacheIcon(t *testing.T, stateDir, file string) string {
	t.Helper()
	p := filepath.Join(stateDir, "logos", file)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLogoIndex(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.Config{Watchlist: []sources.Board{
		{ATS: posting.Greenhouse, Token: "Acme", Company: "Acme", Domain: "www.acme.com"},
		{ATS: posting.Workday, Token: "globex/Ext", Company: "Globex", Domain: "www.globex.com"},
		{ATS: posting.Ashby, Token: "initech", Company: "Initech", Domain: "www.initech.com"}, // nothing cached
		{ATS: posting.Lever, Token: "hooli-a", Company: "Hooli", Domain: "a.hooli.com"},
		{ATS: posting.Lever, Token: "hooli-b", Company: " hooli ", Domain: "b.hooli.com"},
		{ATS: posting.Ashby, Token: "nodomain", Company: "Plain"},
	}}
	acme := cacheIcon(t, stateDir, "www.acme.com.png")
	globex := cacheIcon(t, stateDir, "www.globex.com.png")
	hooliA := cacheIcon(t, stateDir, "a.hooli.com.png")
	ix := newLogoIndex(cfg, stateDir)
	for _, c := range []struct {
		key, company, want string
	}{
		{"gh:acme:123", "Acme Corp", acme},           // by board, whatever the posting calls itself
		{"wd:globex:ext:R1", "Globex", globex},       // Workday's tenant:site prefix
		{"x:0123456789abcdef01234567", "ACME", acme}, // no board: by name
		{"gh:acme-scrape:9", "  acme ", acme},        // the extension's scrapes: by name
		{"ab:initech:1", "Initech", ""},              // a domain with no icon yet
		{"lv:hooli-a:1", "Hooli", hooliA},            // by board
		{"x:1123456789abcdef01234567", "Hooli", ""},  // two boards disagree: no guess
		{"ab:nodomain:1", "Plain", ""},               // no domain
		{"gh:acmesub:1", "Other", ""},                // a prefix of a prefix is not a match
		{"malformed", "", ""},
	} {
		if got := ix.path(posting.Key(c.key), c.company); got != c.want {
			t.Errorf("path(%s, %q) = %q, want %q", c.key, c.company, got, c.want)
		}
	}
	var nilIx *logoIndex
	if got := nilIx.path("gh:acme:1", "Acme"); got != "" {
		t.Errorf("a nil index answered %q", got)
	}
}

func TestLogoDomainsSkipsUnallowed(t *testing.T) {
	cfg := config.Config{
		Watchlist: []sources.Board{
			{ATS: posting.Greenhouse, Token: "a", Domain: "www.a.com"},
			{ATS: posting.Greenhouse, Token: "b", Domain: "www.b.com"},
			{ATS: posting.Greenhouse, Token: "c", Domain: "www.a.com"},
			{ATS: posting.Greenhouse, Token: "d"},
		},
		AllowedHosts: []string{" WWW.A.com ", "boards-api.greenhouse.io"},
	}
	allowed, missing := logoDomains(cfg)
	if !slices.Equal(allowed, []string{"www.a.com"}) || !slices.Equal(missing, []string{"www.b.com"}) {
		t.Fatalf("allowed %v missing %v", allowed, missing)
	}
}

func TestWatchlistDomain(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--allow-host")
	before, _ := os.ReadFile(cfgPath)

	_, e := mustCode(t, 1, "watchlist", "domain", "--ats", "gh", "--token", "acme", "--domain", "www.acme.com")
	if !strings.Contains(e, "allowed_hosts is missing: www.acme.com") {
		t.Errorf("stderr %q", e)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("a refused domain changed the config")
	}
	mustCode(t, 2, "watchlist", "domain", "--ats", "gh", "--token", "acme", "--domain", "https://www.acme.com")
	mustCode(t, 2, "watchlist", "domain", "--ats", "gh")
	mustCode(t, 1, "watchlist", "domain", "--ats", "gh", "--token", "nope", "--domain", "www.acme.com")

	mustCode(t, 0, "watchlist", "domain", "--ats", "gh", "--token", "ACME", "--domain", "www.acme.com", "--allow-host")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Watchlist[0].Domain != "www.acme.com" || !slices.Contains(cfg.AllowedHosts, "www.acme.com") {
		t.Fatalf("watchlist %+v allowed %v", cfg.Watchlist, cfg.AllowedHosts)
	}
	mustCode(t, 0, "watchlist", "domain", "--ats", "gh", "--token", "acme", "--domain", "www.acme.com", "--allow-host")
	if cfg, _ = config.Load(cfgPath); countOf(cfg.AllowedHosts, "www.acme.com") != 1 {
		t.Fatalf("allowed_hosts repeats the domain: %v", cfg.AllowedHosts)
	}

	o, _ := mustCode(t, 0, "watchlist", "domain", "--ats", "gh", "--token", "acme", "--domain", "")
	if !strings.Contains(o, "cleared") {
		t.Errorf("stdout %q", o)
	}
	if cfg, _ = config.Load(cfgPath); cfg.Watchlist[0].Domain != "" {
		t.Fatalf("domain not cleared: %+v", cfg.Watchlist[0])
	}
}

func TestWatchlistAddWithDomain(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 2, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--domain", "acme")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--domain", "www.acme.com", "--allow-host")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"boards-api.greenhouse.io", "www.acme.com"} {
		if !slices.Contains(cfg.AllowedHosts, h) {
			t.Errorf("allowed_hosts %v lacks %s", cfg.AllowedHosts, h)
		}
	}
}

func TestJobsListCarriesTheCachedLogo(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--domain", "www.acme.com", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "globex", "--company", "Globex", "--domain", "www.globex.com", "--allow-host")
	seedJobs(t, stateDir,
		seedJob{key: "gh:acme:1", title: "Engineer", status: posting.Explained},
		seedJob{key: "gh:globex:2", title: "Engineer", status: posting.Explained, company: "Globex"},
	)
	icon := cacheIcon(t, stateDir, "www.acme.com.png")
	byKey := map[posting.Key]string{}
	for _, j := range listJSON(t, "--view", "all").Jobs {
		byKey[j.Key] = j.Logo
	}
	if byKey["gh:acme:1"] != icon || byKey["gh:globex:2"] != "" {
		t.Fatalf("logos %v; want acme's icon and none for globex (not fetched)", byKey)
	}
	o, _ := mustCode(t, 0, "jobs", "show", "--json", "gh:acme:1")
	var shown jobJSON
	if err := json.Unmarshal([]byte(o), &shown); err != nil || shown.Logo != icon {
		t.Fatalf("show logo %q, %v", shown.Logo, err)
	}
	if strings.Contains(o, `"logo":""`) {
		t.Fatal("an absent logo must be omitted")
	}
}

func TestLogosCommand(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 2, "logos")
	mustCode(t, 2, "logos", "fetch")
	o, _ := mustCode(t, 0, "logos", "refresh")
	if !strings.Contains(o, "no board has a domain") {
		t.Errorf("stdout %q", o)
	}
	o, _ = mustCode(t, 0, "logos", "refresh", "--json")
	var res struct {
		Fetched, Failed int
	}
	if err := json.Unmarshal([]byte(o), &res); err != nil || res.Fetched != 0 {
		t.Fatalf("json %q: %v", o, err)
	}
}

func countOf(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}
