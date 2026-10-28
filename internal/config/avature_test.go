package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func avBoard() sources.Board {
	return sources.Board{ATS: posting.Avature, Token: "twosigma", Company: "Two Sigma",
		Host: "careers.twosigma.com", URL: "https://careers.twosigma.com/careers/OpenRoles"}
}

func TestAvatureBoardIsValidAndItsHostIsRequired(t *testing.T) {
	b := avBoard()
	if err := ValidBoard(b); err != nil {
		t.Fatal(err)
	}
	if h, ok := BoardHost(b); !ok || h != "careers.twosigma.com" {
		t.Errorf("BoardHost = %q, %v", h, ok)
	}
	// The host check is strict: the host must be in allowed_hosts like any board's.
	c := valid()
	c.Watchlist = append(c.Watchlist, b)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "careers.twosigma.com") {
		t.Fatalf("avature board with its host not allowed: %v", err)
	}
	c.AllowedHosts = append(c.AllowedHosts, "careers.twosigma.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.RequiredHosts(), "careers.twosigma.com") {
		t.Errorf("RequiredHosts lacks the portal host")
	}
	// Another customer's portal path is configuration.
	b.URL = "https://careers.twosigma.com/en_US/SearchJobs"
	if err := ValidBoard(b); err != nil {
		t.Errorf("another portal path: %v", err)
	}
}

func TestAvatureBoardRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*sources.Board)
		want string
	}{
		"no host":           {func(b *sources.Board) { b.Host = "" }, "needs host"},
		"ip host":           {func(b *sources.Board) { b.Host = "10.0.0.1" }, "needs host"},
		"host with path":    {func(b *sources.Board) { b.Host = "careers.twosigma.com/x" }, "needs host"},
		"host with port":    {func(b *sources.Board) { b.Host = "careers.twosigma.com:8443" }, "needs host"},
		"no url":            {func(b *sources.Board) { b.URL = "" }, "listing page"},
		"url on other host": {func(b *sources.Board) { b.URL = "https://evil.example/careers/OpenRoles" }, "not on the board's host"},
		"url http":          {func(b *sources.Board) { b.URL = "http://careers.twosigma.com/careers/OpenRoles" }, "listing page"},
		"url with query":    {func(b *sources.Board) { b.URL += "?qtvc=1" }, "no port, user, query"},
		"url with user":     {func(b *sources.Board) { b.URL = "https://a@careers.twosigma.com/careers/OpenRoles" }, "no port, user, query"},
		"url without page":  {func(b *sources.Board) { b.URL = "https://careers.twosigma.com/careers" }, "{portal}/{page}"},
		"url too deep":      {func(b *sources.Board) { b.URL = "https://careers.twosigma.com/a/b/c" }, "{portal}/{page}"},
		"token upper case":  {func(b *sources.Board) { b.Token = "TwoSigma" }, "lower-case slug"},
		"token dot":         {func(b *sources.Board) { b.Token = "two.sigma" }, "lower-case slug"},
		"token with space":  {func(b *sources.Board) { b.Token = "two sigma" }, "unsafe"},
		"token with colon":  {func(b *sources.Board) { b.Token = "two:sigma" }, "':'"},
		"token with slash":  {func(b *sources.Board) { b.Token = "two/sigma" }, "'/'"},
		"token dotdot":      {func(b *sources.Board) { b.Token = ".." }, "unsafe"},
		"scrape suffix":     {func(b *sources.Board) { b.Token = "two-scrape" }, "reserved"},
		"link pattern":      {func(b *sources.Board) { b.LinkPattern = "^x" }, "link_pattern"},
		"search":            {func(b *sources.Board) { b.Search = "ml" }, "search applies"},
		"filter":            {func(b *sources.Board) { b.Filters = map[string][]string{"location": {"x"}} }, "not supported"},
		"max_postings":      {func(b *sources.Board) { b.MaxPostings = 10 }, "applies to wd and oc only"},
		"region":            {func(b *sources.Board) { b.Region = "eu" }, "lever only"},
		"board_name":        {func(b *sources.Board) { b.BoardName = "x" }, "board_name applies to ab only"},
	} {
		b := avBoard()
		tc.mut(&b)
		if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// url is av's and pg's; every other board kind still refuses it, and host
// still belongs to wd, ef, jb and av alone.
func TestURLAndHostStayOffOtherBoardKinds(t *testing.T) {
	gh := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
	withURL, withHost := gh, gh
	withURL.URL = "https://careers.twosigma.com/careers/OpenRoles"
	withHost.Host = "careers.twosigma.com"
	if err := ValidBoard(withURL); err == nil || !strings.Contains(err.Error(), "url applies to pg and av boards") {
		t.Errorf("url on greenhouse: %v", err)
	}
	if err := ValidBoard(withHost); err == nil || !strings.Contains(err.Error(), "host applies to wd, oc, ef, jb and av only") {
		t.Errorf("host on greenhouse: %v", err)
	}
	if err := ValidBoard(sources.Board{ATS: "zz", Token: "x"}); err == nil || !strings.Contains(err.Error(), ", av,") {
		t.Errorf("unknown ats: %v", err)
	}
}
