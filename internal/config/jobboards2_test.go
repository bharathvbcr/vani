package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/adzuna"
	"github.com/bharathvbcr/Vani/internal/sources/dice"
	"github.com/bharathvbcr/Vani/internal/sources/ziprecruiter"
)

// ZipRecruiter is a job board read through its MCP server: a fixed host and
// token, one board a watchlist, at least one search, and only the filters its
// adapter verified, with their values checked here as the adapter checks them.
func TestZipRecruiterBoard(t *testing.T) {
	zr := sources.Board{ATS: posting.ZipRecruiter, Token: "ziprecruiter", Company: "ZipRecruiter", Search: "machine learning engineer",
		Filters: map[string][]string{"location": {"Austin, TX"}, "country": {"US"}, "posted_within_days": {"3"}, "location_type": {"remote", "hybrid"}}}
	if err := ValidBoard(zr); err != nil {
		t.Fatal(err)
	}
	if h, ok := BoardHost(zr); !ok || h != "api.ziprecruiter.com" {
		t.Fatalf("BoardHost = %q %v", h, ok)
	}
	if !SingleBoardATS(posting.ZipRecruiter) {
		t.Fatal("zr must be a single-board system")
	}
	for name, bad := range map[string]struct {
		b    sources.Board
		want string
	}{
		"other token":       {with(zr, func(b *sources.Board) { b.Token = "zip" }), "must be ziprecruiter"},
		"no search":         {with(zr, func(b *sources.Board) { b.Search = "" }), "needs at least one search"},
		"unknown filter":    {with(zr, func(b *sources.Board) { b.Filters["salary_min"] = []string{"100000"} }), "not one of"},
		"bad country":       {with(zr, func(b *sources.Board) { b.Filters["country"] = []string{"GB"} }), "country"},
		"two locations":     {with(zr, func(b *sources.Board) { b.Filters["location"] = []string{"Austin, TX", "Dallas, TX"} }), "location"},
		"bad posted":        {with(zr, func(b *sources.Board) { b.Filters["posted_within_days"] = []string{"0"} }), "posted_within_days"},
		"bad location type": {with(zr, func(b *sources.Board) { b.Filters["location_type"] = []string{"moon"} }), "location_type"},
		"host":              {with(zr, func(b *sources.Board) { b.Host = "evil.example.com" }), "host applies"},
		"max postings":      {with(zr, func(b *sources.Board) { b.MaxPostings = 10 }), "applies to wd, oc and ad only"},
	} {
		if err := ValidBoard(bad.b); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, bad.want)
		}
	}
	if err := ValidBoard(sources.Board{ATS: "zz", Token: "x"}); err == nil || !strings.Contains(err.Error(), " zr") {
		t.Errorf("the unknown-code message does not list zr: %v", err)
	}
}

// sources.FilterKeys cannot import the adapters, so each new job board's list
// is written out there; it must be the adapter's own.
func TestJobBoardFilterKeysMatchTheirAdapters(t *testing.T) {
	for ats, want := range map[posting.ATS][]string{
		posting.ZipRecruiter: ziprecruiter.FilterKeys,
		posting.Dice:         dice.FilterKeys,
		posting.Adzuna:       adzuna.FilterKeys,
	} {
		got := slices.Clone(sources.FilterKeys[ats])
		w := slices.Clone(want)
		slices.Sort(got)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("%s: sources.FilterKeys %v, adapter %v", ats, got, w)
		}
	}
}

// Dice is a job board read through its MCP server, like ZipRecruiter.
func TestDiceBoard(t *testing.T) {
	dc := sources.Board{ATS: posting.Dice, Token: "dice", Company: "Dice", Searches: []string{"machine learning engineer", "data scientist"},
		Filters: map[string][]string{"location": {"Austin, TX"}, "radius_miles": {"25"}, "workplace_type": {"remote", "hybrid"}, "sponsorship": {"true"}}}
	if err := ValidBoard(dc); err != nil {
		t.Fatal(err)
	}
	if h, ok := BoardHost(dc); !ok || h != "mcp.dice.com" {
		t.Fatalf("BoardHost = %q %v", h, ok)
	}
	if !SingleBoardATS(posting.Dice) {
		t.Fatal("dc must be a single-board system")
	}
	for name, bad := range map[string]struct {
		b    sources.Board
		want string
	}{
		"other token":     {with(dc, func(b *sources.Board) { b.Token = "dice.com" }), "must be dice"},
		"no search":       {with(dc, func(b *sources.Board) { b.Searches = nil }), "needs at least one search"},
		"remote location": {with(dc, func(b *sources.Board) { b.Filters["location"] = []string{"Remote"} }), "location"},
		"radius alone":    {with(dc, func(b *sources.Board) { delete(b.Filters, "location") }), "radius"},
		"bad posted":      {with(dc, func(b *sources.Board) { b.Filters["posted_within_days"] = []string{"5"} }), "posted_within_days"},
		"bad sponsorship": {with(dc, func(b *sources.Board) { b.Filters["sponsorship"] = []string{"yes"} }), "sponsorship"},
		"unknown filter":  {with(dc, func(b *sources.Board) { b.Filters["easy_apply"] = []string{"true"} }), "not one of"},
		"max postings":    {with(dc, func(b *sources.Board) { b.MaxPostings = 10 }), "applies to wd, oc and ad only"},
	} {
		if err := ValidBoard(bad.b); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, bad.want)
		}
	}
}

// Adzuna boards are one per country (the token), need a search, and may set a
// lower posting cap to spend fewer of the free tier's requests.
func TestAdzunaBoard(t *testing.T) {
	ad := sources.Board{ATS: posting.Adzuna, Token: "us", Company: "Adzuna", Search: "machine learning engineer",
		Filters: map[string][]string{"where": {"Austin"}, "max_days_old": {"7"}, "full_time": {"true"}}, MaxPostings: 200}
	if err := ValidBoard(ad); err != nil {
		t.Fatal(err)
	}
	if h, ok := BoardHost(ad); !ok || h != "api.adzuna.com" {
		t.Fatalf("BoardHost = %q %v", h, ok)
	}
	if SingleBoardATS(posting.Adzuna) {
		t.Fatal("ad boards are one per country, not one per watchlist")
	}
	gb := with(ad, func(b *sources.Board) { b.Token = "gb" })
	if err := ValidBoard(gb); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]struct {
		b    sources.Board
		want string
	}{
		"unknown country":      {with(ad, func(b *sources.Board) { b.Token = "jp" }), "country codes"},
		"upper-case country":   {with(ad, func(b *sources.Board) { b.Token = "US" }), "country codes"},
		"no search":            {with(ad, func(b *sources.Board) { b.Search = "" }), "needs at least one search"},
		"contradiction":        {with(ad, func(b *sources.Board) { b.Filters["part_time"] = []string{"true"} }), "part_time"},
		"two values":           {with(ad, func(b *sources.Board) { b.Filters["where"] = []string{"Austin", "Dallas"} }), "where"},
		"bad age":              {with(ad, func(b *sources.Board) { b.Filters["max_days_old"] = []string{"0"} }), "max_days_old"},
		"cap too high":         {with(ad, func(b *sources.Board) { b.MaxPostings = adzuna.MaxPostingsCeiling + 1 }), "max_postings"},
		"cap negative":         {with(ad, func(b *sources.Board) { b.MaxPostings = -1 }), "max_postings"},
		"c1 control in search": {with(ad, func(b *sources.Board) { b.Search = "machine\u0085learning" }), "adzuna board"},
	} {
		if err := ValidBoard(bad.b); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, bad.want)
		}
	}
	// Two countries are two boards; the same country twice is one board twice.
	c := Default()
	c.AllowedHosts = append(c.AllowedHosts, "api.adzuna.com")
	c.Watchlist = []sources.Board{ad, gb}
	if err := c.Validate(); err != nil {
		t.Fatalf("us and gb boards: %v", err)
	}
	c.Watchlist = []sources.Board{ad, ad}
	if err := c.Validate(); err == nil {
		t.Fatal("two us boards were accepted")
	}
}
