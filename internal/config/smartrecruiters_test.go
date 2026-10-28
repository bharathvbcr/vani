package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestSmartRecruitersBoardsAreValidAndNeedTheAPIHost(t *testing.T) {
	good := []sources.Board{
		{ATS: posting.SmartRecruiters, Token: "servicenow", Company: "ServiceNow"},
		{ATS: posting.SmartRecruiters, Token: "Canva", Company: "Canva", Filters: map[string][]string{"country": {"us", "CA"}}},
		{ATS: posting.SmartRecruiters, Token: "Bosch-Group_2", Company: "Bosch"},
	}
	for _, b := range good {
		if err := ValidBoard(b); err != nil {
			t.Errorf("ValidBoard(%+v) = %v", b, err)
		}
		if h, ok := BoardHost(b); !ok || h != "api.smartrecruiters.com" {
			t.Errorf("BoardHost(%s) = %q, %v", b.Token, h, ok)
		}
	}
	c := valid()
	c.Watchlist = append(c.Watchlist, good...)
	if !slices.Contains(c.RequiredHosts(), "api.smartrecruiters.com") {
		t.Fatalf("RequiredHosts lacks api.smartrecruiters.com: %v", c.RequiredHosts())
	}
	// Without the host allowed the config is refused; with it, it is valid.
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "api.smartrecruiters.com") {
		t.Fatalf("Validate without the allowed host: %v", err)
	}
	c.AllowedHosts = append(c.AllowedHosts, "api.smartrecruiters.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSmartRecruitersBoardsThatCannotWorkAreRefused(t *testing.T) {
	base := sources.Board{ATS: posting.SmartRecruiters, Token: "servicenow", Company: "ServiceNow"}
	mod := func(f func(*sources.Board)) sources.Board { b := base; f(&b); return b }
	for name, tc := range map[string]struct {
		b    sources.Board
		want string
	}{
		"token with a dot":     {mod(func(b *sources.Board) { b.Token = "now.com" }), "company identifier"},
		"token with a slash":   {mod(func(b *sources.Board) { b.Token = "a/b" }), "token"},
		"token dot dot":        {mod(func(b *sources.Board) { b.Token = ".." }), "token"},
		"token with a space":   {mod(func(b *sources.Board) { b.Token = "service now" }), "token"},
		"token with a colon":   {mod(func(b *sources.Board) { b.Token = "a:b" }), "token"},
		"token with a query":   {mod(func(b *sources.Board) { b.Token = "a?x" }), "token"},
		"token percent":        {mod(func(b *sources.Board) { b.Token = "%2e%2e" }), "company identifier"},
		"token leading dash":   {mod(func(b *sources.Board) { b.Token = "-now" }), "company identifier"},
		"token too long":       {mod(func(b *sources.Board) { b.Token = strings.Repeat("a", 101) }), "company identifier"},
		"empty token":          {mod(func(b *sources.Board) { b.Token = "" }), "empty"},
		"host":                 {mod(func(b *sources.Board) { b.Host = "api.smartrecruiters.com" }), "host applies"},
		"search":               {mod(func(b *sources.Board) { b.Search = "engineer" }), "search applies"},
		"searches":             {mod(func(b *sources.Board) { b.Searches = []string{"a"} }), "search applies"},
		"region":               {mod(func(b *sources.Board) { b.Region = "eu" }), "region"},
		"max_postings":         {mod(func(b *sources.Board) { b.MaxPostings = 100 }), "applies to wd only"},
		"url":                  {mod(func(b *sources.Board) { b.URL = "https://jobs.smartrecruiters.com/x" }), "page boards only"},
		"unverified filter":    {mod(func(b *sources.Board) { b.Filters = map[string][]string{"location": {"x"}} }), "is not one of country"},
		"country three letter": {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {"usa"}} }), "two-letter"},
		"country list":         {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {"us,ca"}} }), "two-letter"},
		"country digit":        {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {"u1"}} }), "two-letter"},
		"country padded":       {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {"us "}} }), "two-letter"},
		"country name":         {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {"United States"}} }), "two-letter"},
		"one bad country":      {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {"us", "x"}} }), "two-letter"},
		"country no values":    {mod(func(b *sources.Board) { b.Filters = map[string][]string{"country": {}} }), "values"},
	} {
		if err := ValidBoard(tc.b); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestBoardATSMessageNamesSmartRecruiters(t *testing.T) {
	err := ValidBoard(sources.Board{ATS: "zz", Token: "x"})
	if err == nil || !strings.Contains(err.Error(), ", sr,") {
		t.Fatalf("err = %v, want the list to name sr", err)
	}
}
