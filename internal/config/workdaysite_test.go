package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A Workday board on the shared host wd{N}.myworkdaysite.com (Snap's is
// snapchat/snap on wd1) has no tenant label to check, so the token's tenant
// stands alone.
func TestWorkdaySiteHostBoards(t *testing.T) {
	snap := sources.Board{ATS: posting.Workday, Token: "snapchat/snap", Company: "Snap", Host: "wd1.myworkdaysite.com", Search: "machine learning"}
	if err := ValidBoard(snap); err != nil {
		t.Fatalf("snap board refused: %v", err)
	}
	for _, host := range []string{"WD1.MyWorkdaySite.com", " wd12.myworkdaysite.com "} {
		b := snap
		b.Host = host
		if err := ValidBoard(b); err != nil {
			t.Errorf("host %q refused: %v", host, err)
		}
	}
	if h, ok := BoardHost(snap); !ok || h != "wd1.myworkdaysite.com" {
		t.Errorf("BoardHost = %q, %v", h, ok)
	}

	c := valid()
	c.Watchlist = append(c.Watchlist, snap)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "allowed_hosts is missing: wd1.myworkdaysite.com") {
		t.Fatalf("an unallowed site host must be reported, got %v", err)
	}
	if !slices.Contains(c.RequiredHosts(), "wd1.myworkdaysite.com") {
		t.Errorf("RequiredHosts lacks the site host: %v", c.RequiredHosts())
	}
	c.AllowedHosts = append(c.AllowedHosts, "wd1.myworkdaysite.com")
	if err := c.Validate(); err != nil {
		t.Fatalf("allowed site host refused: %v", err)
	}
}

func TestWorkdaySiteHostRefusals(t *testing.T) {
	good := sources.Board{ATS: posting.Workday, Token: "snapchat/snap", Company: "Snap", Host: "wd1.myworkdaysite.com"}
	for name, tc := range map[string]struct {
		mut  func(*sources.Board)
		want string
	}{
		"host with path":       {func(b *sources.Board) { b.Host = "wd1.myworkdaysite.com/x" }, "needs host"},
		"host with port":       {func(b *sources.Board) { b.Host = "wd1.myworkdaysite.com:443" }, "needs host"},
		"host in userinfo":     {func(b *sources.Board) { b.Host = "wd1.myworkdaysite.com@evil.example" }, "needs host"},
		"host with scheme":     {func(b *sources.Board) { b.Host = "https://wd1.myworkdaysite.com" }, "needs host"},
		"suffix host":          {func(b *sources.Board) { b.Host = "wd1.myworkdaysite.com.evil.example" }, "needs host"},
		"extra label":          {func(b *sources.Board) { b.Host = "snapchat.wd1.myworkdaysite.com" }, "needs host"},
		"no wd number":         {func(b *sources.Board) { b.Host = "wd.myworkdaysite.com" }, "needs host"},
		"bare domain":          {func(b *sources.Board) { b.Host = "myworkdaysite.com" }, "needs host"},
		"empty host":           {func(b *sources.Board) { b.Host = "" }, "needs host"},
		"other site":           {func(b *sources.Board) { b.Host = "wd1.myworkdaysite.org" }, "needs host"},
		"token without site":   {func(b *sources.Board) { b.Token = "snapchat" }, "tenant/site"},
		"empty tenant":         {func(b *sources.Board) { b.Token = "/snap" }, "tenant/site"},
		"empty site":           {func(b *sources.Board) { b.Token = "snapchat/" }, "tenant/site"},
		"two slashes":          {func(b *sources.Board) { b.Token = "snapchat/snap/x" }, "tenant/site"},
		"percent in tenant":    {func(b *sources.Board) { b.Token = "snap%2e/snap" }, "tenant/site"},
		"percent in site":      {func(b *sources.Board) { b.Token = "snapchat/sn%2fap" }, "tenant/site"},
		"dot dot":              {func(b *sources.Board) { b.Token = "snapchat/.." }, "unsafe"},
		"space in token":       {func(b *sources.Board) { b.Token = "snap chat/snap" }, "unsafe"},
		"colon in token":       {func(b *sources.Board) { b.Token = "snap:chat/snap" }, "':'"},
		"backslash in site":    {func(b *sources.Board) { b.Token = `snapchat/sn\ap` }, "tenant/site"},
		"query in site":        {func(b *sources.Board) { b.Token = "snapchat/snap?x" }, "unsafe"},
		"oversized token part": {func(b *sources.Board) { b.Token = "snapchat/" + strings.Repeat("a", 10000) + "%" }, "tenant/site"},
	} {
		t.Run(name, func(t *testing.T) {
			b := good
			tc.mut(&b)
			err := ValidBoard(b)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// The tenant-labelled host is untouched: still tenant-checked, and it still
// does not take the shared host's token rules (an unusual site name fails at
// fetch, as before).
func TestWorkdayJobsHostUnchanged(t *testing.T) {
	if err := ValidBoard(sources.Board{ATS: posting.Workday, Token: "acme/Ext", Host: "acme.wd5.myworkdayjobs.com"}); err != nil {
		t.Fatalf("tenant host refused: %v", err)
	}
	err := ValidBoard(sources.Board{ATS: posting.Workday, Token: "acme/Ext", Host: "globex.wd5.myworkdayjobs.com"})
	if err == nil || !strings.Contains(err.Error(), "does not belong to tenant") {
		t.Fatalf("tenant mismatch err = %v", err)
	}
}
