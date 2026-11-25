package config

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Himalayas and Remote OK are job boards with one public API each: a fixed
// host and token, one board a watchlist, and Himalayas needs a search.
func TestJobBoardBoards(t *testing.T) {
	hm := sources.Board{ATS: posting.Himalayas, Token: "himalayas", Company: "Himalayas", Search: "machine learning engineer",
		Filters: map[string][]string{"country": {"US"}, "seniority": {"Senior"}, "employment_type": {"Full Time"}}}
	ro := sources.Board{ATS: posting.RemoteOK, Token: "remoteok", Company: "Remote OK"}
	for _, tc := range []struct {
		b    sources.Board
		host string
	}{{hm, "himalayas.app"}, {ro, "remoteok.com"}} {
		if err := ValidBoard(tc.b); err != nil {
			t.Fatalf("%s: %v", tc.b.ATS, err)
		}
		if h, ok := BoardHost(tc.b); !ok || h != tc.host {
			t.Errorf("%s: BoardHost = %q, %v; want %s", tc.b.ATS, h, ok, tc.host)
		}
		if !SingleBoardATS(tc.b.ATS) {
			t.Errorf("%s: not a single-board system; two boards would key one job twice", tc.b.ATS)
		}
	}
	for name, bad := range map[string]struct {
		b    sources.Board
		want string
	}{
		"hm other token":       {with(hm, func(b *sources.Board) { b.Token = "acme" }), "must be himalayas"},
		"hm upper-case token":  {with(hm, func(b *sources.Board) { b.Token = "Himalayas" }), "must be himalayas"},
		"hm no search":         {with(hm, func(b *sources.Board) { b.Search = "" }), "needs at least one search"},
		"hm two filter values": {with(hm, func(b *sources.Board) { b.Filters = map[string][]string{"country": {"US", "CA"}} }), "takes one value"},
		"hm unknown filter":    {with(hm, func(b *sources.Board) { b.Filters = map[string][]string{"timezone": {"-5"}} }), "not one of country, seniority, employment_type"},
		"hm host":              {with(hm, func(b *sources.Board) { b.Host = "evil.example.com" }), "host applies"},
		"hm url":               {with(hm, func(b *sources.Board) { b.URL = "https://himalayas.app/jobs" }), "url applies"},
		"hm max postings":      {with(hm, func(b *sources.Board) { b.MaxPostings = 100 }), "applies to wd, oc and ad only"},
		"ro other token":       {with(ro, func(b *sources.Board) { b.Token = "remote-ok" }), "must be remoteok"},
		"ro search":            {with(ro, func(b *sources.Board) { b.Search = "golang" }), "search applies to"},
		"gh search names hm":   {sources.Board{ATS: posting.Greenhouse, Token: "acme", Search: "x"}, " hm,"},
		"ro filter":            {with(ro, func(b *sources.Board) { b.Filters = map[string][]string{"tag": {"go"}} }), "filters are not supported"},
		"ro header injection":  {with(ro, func(b *sources.Board) { b.Token = "remoteok\r\nX-Evil" }), "unsafe characters"},
	} {
		if err := ValidBoard(bad.b); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, bad.want)
		}
	}
	if err := ValidBoard(sources.Board{ATS: "zz", Token: "x"}); err == nil ||
		!strings.Contains(err.Error(), " hm,") || !strings.Contains(err.Error(), " ro") {
		t.Errorf("the unknown-code message does not list hm and ro: %v", err)
	}
}

func TestOneJobBoardOfEachKind(t *testing.T) {
	c := Default()
	c.AllowedHosts = append(c.AllowedHosts, "himalayas.app")
	b := sources.Board{ATS: posting.Himalayas, Token: "himalayas", Company: "Himalayas", Search: "go"}
	c.Watchlist = []sources.Board{b, with(b, func(b *sources.Board) { b.Search = "rust" })}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "a second hm board") {
		t.Fatalf("two himalayas boards: %v", err)
	}
}

func with(b sources.Board, mod func(*sources.Board)) sources.Board {
	b.Filters = cloneFilters(b.Filters)
	mod(&b)
	return b
}

func cloneFilters(f map[string][]string) map[string][]string {
	if f == nil {
		return nil
	}
	out := make(map[string][]string, len(f))
	for k, v := range f {
		out[k] = append([]string(nil), v...)
	}
	return out
}
