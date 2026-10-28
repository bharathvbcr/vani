package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// D. E. Shaw and Atlassian are single-company boards on a fixed host that list
// their whole company with one request: no search, no filter, no host field.
func TestSingleCompanyBoards(t *testing.T) {
	for _, tc := range []struct {
		ats     posting.ATS
		token   string
		company string
		host    string
	}{
		{posting.DEShaw, "deshaw", "D. E. Shaw", "www.deshaw.com"},
		{posting.Atlassian, "atlassian", "Atlassian", "www.atlassian.com"},
	} {
		b := sources.Board{ATS: tc.ats, Token: tc.token, Company: tc.company}
		if err := ValidBoard(b); err != nil {
			t.Errorf("%s: %v", tc.ats, err)
		}
		if h, ok := BoardHost(b); !ok || h != tc.host {
			t.Errorf("%s: BoardHost = %q, %v; want %q", tc.ats, h, ok, tc.host)
		}
		if !SingleBoardATS(tc.ats) {
			t.Errorf("%s: not a single-board ATS", tc.ats)
		}
		c := valid()
		c.Watchlist = append(c.Watchlist, b)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "allowed_hosts is missing: "+tc.host) {
			t.Errorf("%s: host not required in allowed_hosts: %v", tc.ats, err)
		}
		c.AllowedHosts = append(c.AllowedHosts, tc.host)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tc.ats, err)
		}
		if !slices.Contains(c.RequiredHosts(), tc.host) {
			t.Errorf("%s: RequiredHosts lacks %s", tc.ats, tc.host)
		}
		// A second board of the same company would key every posting twice.
		c.Watchlist = append(c.Watchlist, sources.Board{ATS: tc.ats, Token: tc.token + "2", Company: tc.company})
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "a second "+string(tc.ats)+" board") {
			t.Errorf("%s: second board accepted: %v", tc.ats, err)
		}
		for name, bad := range map[string]func(b *sources.Board){
			"empty token":   func(b *sources.Board) { b.Token = "" },
			"colon token":   func(b *sources.Board) { b.Token = "a:b" },
			"slash token":   func(b *sources.Board) { b.Token = "a/b" },
			"space token":   func(b *sources.Board) { b.Token = "a b" },
			"dotdot token":  func(b *sources.Board) { b.Token = ".." },
			"query token":   func(b *sources.Board) { b.Token = "a?b" },
			"scrape suffix": func(b *sources.Board) { b.Token += "-scrape" },
			"search":        func(b *sources.Board) { b.Search = "engineer" },
			"searches":      func(b *sources.Board) { b.Searches = []string{"engineer"} },
			"filters":       func(b *sources.Board) { b.Filters = map[string][]string{"location": {"New York"}} },
			"host":          func(b *sources.Board) { b.Host = "evil.example" },
			"region":        func(b *sources.Board) { b.Region = "eu" },
			"board name":    func(b *sources.Board) { b.BoardName = "x" },
			"max postings":  func(b *sources.Board) { b.MaxPostings = 10 },
			"page url":      func(b *sources.Board) { b.URL = "https://x.example" },
			"link pattern":  func(b *sources.Board) { b.LinkPattern = "/jobs/" },
		} {
			x := b
			bad(&x)
			if err := ValidBoard(x); err == nil {
				t.Errorf("%s: %s accepted", tc.ats, name)
			}
		}
	}
	// The unknown-ATS message names both codes.
	err := ValidBoard(sources.Board{ATS: "zz", Token: "x"})
	if err == nil || !strings.Contains(err.Error(), ", ds, ") || !strings.Contains(err.Error(), ", at,") {
		t.Errorf("unknown ats: %v", err)
	}
}
