package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A TalentBrew site is on the customer's own host, so the board names it: the
// host a board is allowed is the host it was given, lower-cased, and nothing
// else.
func TestTalentBrewBoards(t *testing.T) {
	good := sources.Board{ATS: posting.TalentBrew, Token: "intuit", Company: "Intuit", Host: "jobs.intuit.com"}
	if err := ValidBoard(good); err != nil {
		t.Fatalf("board refused: %v", err)
	}
	if h, ok := BoardHost(good); !ok || h != "jobs.intuit.com" {
		t.Fatalf("BoardHost = %q, %v", h, ok)
	}
	mixed := good
	mixed.Host = " Jobs.Intuit.COM "
	if h, ok := BoardHost(mixed); !ok || h != "jobs.intuit.com" {
		t.Fatalf("BoardHost of a mixed-case host = %q, %v; want the host the adapter asks", h, ok)
	}
	if err := ValidBoard(mixed); err != nil {
		t.Fatalf("mixed-case host refused: %v", err)
	}
	if h, ok := BoardHost(sources.Board{ATS: posting.TalentBrew, Token: "intuit"}); ok {
		t.Fatalf("BoardHost without a host = %q, true", h)
	}

	c := valid()
	c.Watchlist = []sources.Board{good}
	c.AllowedHosts = []string{"openrouter.ai"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "jobs.intuit.com") {
		t.Fatalf("host not allowed: got %v, want an error naming jobs.intuit.com", err)
	}
	c.AllowedHosts = append(c.AllowedHosts, "jobs.intuit.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.RequiredHosts(), "jobs.intuit.com") {
		t.Errorf("RequiredHosts lacks jobs.intuit.com: %v", c.RequiredHosts())
	}

	for name, bad := range map[string]struct {
		mod  func(*sources.Board)
		want string
	}{
		"empty token":      {func(b *sources.Board) { b.Token = "" }, "empty token"},
		"colon in token":   {func(b *sources.Board) { b.Token = "a:b" }, "':'"},
		"dots in token":    {func(b *sources.Board) { b.Token = ".." }, "unsafe"},
		"slash in token":   {func(b *sources.Board) { b.Token = "a/b" }, "'/'"},
		"upper case":       {func(b *sources.Board) { b.Token = "Intuit" }, "lower-case slug"},
		"underscore":       {func(b *sources.Board) { b.Token = "in_tuit" }, "lower-case slug"},
		"leading hyphen":   {func(b *sources.Board) { b.Token = "-intuit" }, "lower-case slug"},
		"no host":          {func(b *sources.Board) { b.Host = "" }, "needs host"},
		"url as host":      {func(b *sources.Board) { b.Host = "https://jobs.intuit.com" }, "needs host"},
		"path in host":     {func(b *sources.Board) { b.Host = "jobs.intuit.com/job" }, "needs host"},
		"port in host":     {func(b *sources.Board) { b.Host = "jobs.intuit.com:8443" }, "needs host"},
		"userinfo in host": {func(b *sources.Board) { b.Host = "evil.test@jobs.intuit.com" }, "needs host"},
		"ip host":          {func(b *sources.Board) { b.Host = "10.0.0.1" }, "needs host"},
		"one label host":   {func(b *sources.Board) { b.Host = "localhost" }, "needs host"},
		"search":           {func(b *sources.Board) { b.Search = "engineer" }, "search applies to"},
		"filter":           {func(b *sources.Board) { b.Filters = map[string][]string{"country": {"us"}} }, "filters are not supported"},
		"region":           {func(b *sources.Board) { b.Region = "eu" }, "region eu applies to lever only"},
		"max postings":     {func(b *sources.Board) { b.MaxPostings = 10 }, "max_postings applies to wd and oc only"},
		"url":              {func(b *sources.Board) { b.URL = "https://jobs.intuit.com/search-jobs" }, "url applies to"},
		"link pattern":     {func(b *sources.Board) { b.LinkPattern = "x" }, "link_pattern"},
		"board name":       {func(b *sources.Board) { b.BoardName = "Intuit" }, "board_name applies to ab only"},
	} {
		t.Run(name, func(t *testing.T) {
			b := good
			bad.mod(&b)
			if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), bad.want) {
				t.Fatalf("ValidBoard(%+v) = %v, want an error containing %q", b, err, bad.want)
			}
		})
	}
}

// A host belongs to the boards that take one; the message names them all, tb
// included, and a board of another ATS is still refused one.
func TestHostOnlyAppliesToTheATSsThatTakeOne(t *testing.T) {
	b := sources.Board{ATS: posting.Lever, Token: "acme", Host: "jobs.intuit.com"}
	if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), "host applies to wd, oc, ef, jb, av and tb only") {
		t.Fatalf("lever with a host: %v", err)
	}
	b = sources.Board{ATS: posting.SmartRecruiters, Token: "acme", Host: "jobs.intuit.com"}
	if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), "host applies to wd, oc, ef, jb, av and tb only") {
		t.Fatalf("smartrecruiters with a host: %v", err)
	}
}

// The "is not one of" message names tb, so an owner who mistypes the kind sees
// the code that is accepted.
func TestUnknownATSMessageNamesTalentBrew(t *testing.T) {
	err := ValidBoard(sources.Board{ATS: "zz", Token: "x"})
	if err == nil || !strings.Contains(err.Error(), ", tb,") {
		t.Fatalf("got %v, want the list of accepted kinds to include tb", err)
	}
}
