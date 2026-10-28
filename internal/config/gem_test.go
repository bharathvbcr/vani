package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestGemBoard(t *testing.T) {
	good := sources.Board{ATS: posting.Gem, Token: "groq", Company: "Groq"}
	if err := ValidBoard(good); err != nil {
		t.Fatalf("a Gem board was refused: %v", err)
	}
	if h, ok := BoardHost(good); !ok || h != "api.gem.com" {
		t.Fatalf("BoardHost = %q, %v, want api.gem.com", h, ok)
	}

	// The board's host must be allowed, and is required of the config.
	c := valid()
	c.Watchlist = []sources.Board{good}
	c.AllowedHosts = []string{"openrouter.ai"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "api.gem.com") {
		t.Fatalf("Gem host not allowed: got %v, want an error naming api.gem.com", err)
	}
	c.AllowedHosts = append(c.AllowedHosts, "api.gem.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.RequiredHosts(), "api.gem.com") {
		t.Errorf("RequiredHosts lacks api.gem.com: %v", c.RequiredHosts())
	}

	for name, tc := range map[string]struct {
		mod  func(*sources.Board)
		want string
	}{
		"empty token":     {func(b *sources.Board) { b.Token = "" }, "empty token"},
		"colon in token":  {func(b *sources.Board) { b.Token = "a:b" }, "':'"},
		"dots in token":   {func(b *sources.Board) { b.Token = ".." }, "unsafe"},
		"slash in token":  {func(b *sources.Board) { b.Token = "a/b" }, "'/'"},
		"space in token":  {func(b *sources.Board) { b.Token = "a b" }, "unsafe"},
		"scrape suffix":   {func(b *sources.Board) { b.Token = "groq-scrape" }, "reserved"},
		"search":          {func(b *sources.Board) { b.Search = "ml" }, "search applies"},
		"host":            {func(b *sources.Board) { b.Host = "jobs.gem.com" }, "host applies"},
		"region":          {func(b *sources.Board) { b.Region = "eu" }, "lever only"},
		"board name":      {func(b *sources.Board) { b.BoardName = "Groq" }, "ab only"},
		"url":             {func(b *sources.Board) { b.URL = "https://jobs.gem.com/groq" }, "page boards only"},
		"filter":          {func(b *sources.Board) { b.Filters = map[string][]string{"location": {"x"}} }, "not supported"},
		"max postings":    {func(b *sources.Board) { b.MaxPostings = 300 }, "applies to wd only"},
		"wrong code (gn)": {func(b *sources.Board) { b.ATS = "gn" }, "is not one of gh, lv, ab, wd, az, gg, ap, ef, jb, gm,"},
	} {
		b := good
		tc.mod(&b)
		if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, tc.want)
		}
	}
}
