package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestIBMBoardHostAndTokens(t *testing.T) {
	for _, tok := range []string{"ibm", "confluent", "IBM", "Confluent"} {
		b := sources.Board{ATS: posting.IBM, Token: tok, Search: "software engineer"}
		if err := ValidBoard(b); err != nil {
			t.Errorf("token %q: %v", tok, err)
		}
		if h, ok := BoardHost(b); !ok || h != "www-api.ibm.com" {
			t.Errorf("BoardHost(%q) = %q, %v", tok, h, ok)
		}
	}
	for name, tc := range map[string]struct {
		b    sources.Board
		want string
	}{
		"another company":   {sources.Board{ATS: posting.IBM, Token: "redhat", Search: "x"}, "must be ibm or confluent"},
		"path in token":     {sources.Board{ATS: posting.IBM, Token: "ibm/confluent", Search: "x"}, "must be ibm or confluent"},
		"dot dot":           {sources.Board{ATS: posting.IBM, Token: "..", Search: "x"}, "unsafe"},
		"colon":             {sources.Board{ATS: posting.IBM, Token: "ibm:x", Search: "x"}, "':'"},
		"space":             {sources.Board{ATS: posting.IBM, Token: "ibm x", Search: "x"}, "unsafe"},
		"no search":         {sources.Board{ATS: posting.IBM, Token: "ibm"}, "needs at least one search"},
		"blank search":      {sources.Board{ATS: posting.IBM, Token: "ibm", Search: " "}, "needs at least one search"},
		"unverified filter": {sources.Board{ATS: posting.IBM, Token: "ibm", Search: "x", Filters: map[string][]string{"country": {"India"}}}, "not supported"},
		"host":              {sources.Board{ATS: posting.IBM, Token: "ibm", Search: "x", Host: "www-api.ibm.com"}, "host applies"},
		"max_postings":      {sources.Board{ATS: posting.IBM, Token: "ibm", Search: "x", MaxPostings: 10}, "applies to wd and oc only"},
	} {
		if err := ValidBoard(tc.b); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// Searching is allowed on ib, and the message says so.
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Search: "x"}
	if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), "jb and ib only") {
		t.Errorf("search on greenhouse: %v", err)
	}
	if err := ValidBoard(sources.Board{ATS: "zz", Token: "x"}); err == nil || !strings.Contains(err.Error(), ", ib,") {
		t.Errorf("unknown ats: %v", err)
	}
}

// IBM's two boards share one host and one ATS code: both may be on a
// watchlist (they list disjoint roles), a second of either may not.
func TestIBMBoardsInAWatchlist(t *testing.T) {
	c := valid()
	c.Watchlist = append(c.Watchlist,
		sources.Board{ATS: posting.IBM, Token: "ibm", Company: "IBM", Searches: []string{"software engineer", "machine learning"}},
		sources.Board{ATS: posting.IBM, Token: "confluent", Company: "Confluent", Search: "Confluent"})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "www-api.ibm.com") {
		t.Fatalf("host not allowed: %v", err)
	}
	c.AllowedHosts = append(c.AllowedHosts, "www-api.ibm.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.RequiredHosts(), "www-api.ibm.com") {
		t.Errorf("RequiredHosts lacks www-api.ibm.com: %v", c.RequiredHosts())
	}
	c.Watchlist = append(c.Watchlist, sources.Board{ATS: posting.IBM, Token: "IBM", Search: "TPU"})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate board ib/ibm") {
		t.Fatalf("second ibm board: %v", err)
	}
}
