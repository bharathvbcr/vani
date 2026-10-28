package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Recruitee, Pinpoint, BambooHR and Personio each live on one host per
// tenant, built from the token: the host a board is allowed is the host its
// adapter asks, and a token that is not one host label is refused.
func TestPerTenantBoards(t *testing.T) {
	for _, tc := range []struct {
		ats  posting.ATS
		host string
	}{
		{posting.Recruitee, "syrve.recruitee.com"},
		{posting.Pinpoint, "syrve.pinpointhq.com"},
		{posting.BambooHR, "syrve.bamboohr.com"},
		{posting.Personio, "syrve.jobs.personio.com"},
	} {
		t.Run(string(tc.ats), func(t *testing.T) {
			good := sources.Board{ATS: tc.ats, Token: "syrve", Company: "Syrve"}
			if err := ValidBoard(good); err != nil {
				t.Fatalf("board refused: %v", err)
			}
			if h, ok := BoardHost(good); !ok || h != tc.host {
				t.Fatalf("BoardHost = %q, %v, want %q", h, ok, tc.host)
			}

			c := valid()
			c.Watchlist = []sources.Board{good}
			c.AllowedHosts = []string{"openrouter.ai"}
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.host) {
				t.Fatalf("host not allowed: got %v, want an error naming %s", err, tc.host)
			}
			c.AllowedHosts = append(c.AllowedHosts, tc.host)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(c.RequiredHosts(), tc.host) {
				t.Errorf("RequiredHosts lacks %s: %v", tc.host, c.RequiredHosts())
			}

			for name, bad := range map[string]struct {
				mod  func(*sources.Board)
				want string
			}{
				"empty token":      {func(b *sources.Board) { b.Token = "" }, "empty token"},
				"colon in token":   {func(b *sources.Board) { b.Token = "a:b" }, "':'"},
				"dots in token":    {func(b *sources.Board) { b.Token = ".." }, "unsafe"},
				"another host":     {func(b *sources.Board) { b.Token = "evil.example" }, "one lower-case host label"},
				"upper case":       {func(b *sources.Board) { b.Token = "Syrve" }, "one lower-case host label"},
				"leading hyphen":   {func(b *sources.Board) { b.Token = "-syrve" }, "one lower-case host label"},
				"trailing hyphen":  {func(b *sources.Board) { b.Token = "syrve-" }, "one lower-case host label"},
				"underscore":       {func(b *sources.Board) { b.Token = "sy_rve" }, "one lower-case host label"},
				"userinfo":         {func(b *sources.Board) { b.Token = "a@evil" }, "one lower-case host label"},
				"unicode":          {func(b *sources.Board) { b.Token = "sýrve" }, "one lower-case host label"},
				"64 byte label":    {func(b *sources.Board) { b.Token = strings.Repeat("a", 64) }, "one lower-case host label"},
				"slash in token":   {func(b *sources.Board) { b.Token = "a/b" }, "'/'"},
				"search":           {func(b *sources.Board) { b.Search = "ml" }, "search applies"},
				"host":             {func(b *sources.Board) { b.Host = "x.recruitee.com" }, "host applies"},
				"region":           {func(b *sources.Board) { b.Region = "eu" }, "lever only"},
				"board name":       {func(b *sources.Board) { b.BoardName = "Syrve" }, "ab only"},
				"url":              {func(b *sources.Board) { b.URL = "https://syrve.example/jobs" }, "page boards only"},
				"filter":           {func(b *sources.Board) { b.Filters = map[string][]string{"location": {"x"}} }, "not supported"},
				"max postings":     {func(b *sources.Board) { b.MaxPostings = 300 }, "applies to wd and oc only"},
				"scrape suffix":    {func(b *sources.Board) { b.Token = "syrve-scrape" }, "reserved"},
				"wrong code (zz)":  {func(b *sources.Board) { b.ATS = "zz" }, "is not one of gh, lv, ab, wd, az, gg, ap, ef, jb, gm, sr, ib, av, ds, at, oc, rp, wk, rc, pp, bh, ps, fd, pg"},
				"label with a dot": {func(b *sources.Board) { b.Token = "a.b" }, "one lower-case host label"},
			} {
				b := good
				bad.mod(&b)
				if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), bad.want) {
					t.Errorf("%s: got %v, want an error containing %q", name, err, bad.want)
				}
			}
		})
	}
}

// A token that is not a host label has no host to allow: BoardHost never
// builds "evil.example.recruitee.com" from it, and 63 bytes is the longest label.
func TestPerTenantBoardHostRefusesNonLabels(t *testing.T) {
	for _, ats := range []posting.ATS{posting.Recruitee, posting.Pinpoint, posting.BambooHR, posting.Personio} {
		for _, token := range []string{"", "a.b", "A", "a b", "a/b", "a:b", "-a", "a-", strings.Repeat("a", 64)} {
			if h, ok := BoardHost(sources.Board{ATS: ats, Token: token}); ok {
				t.Errorf("%s/%q: BoardHost = %q, want none", ats, token, h)
			}
		}
		if h, ok := BoardHost(sources.Board{ATS: ats, Token: strings.Repeat("a", 63)}); !ok || !strings.HasPrefix(h, strings.Repeat("a", 63)+".") {
			t.Errorf("%s: a 63-byte label gave %q, %v", ats, h, ok)
		}
	}
}
