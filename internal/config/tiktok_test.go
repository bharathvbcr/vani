package config

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestTikTokBoard(t *testing.T) {
	good := sources.Board{ATS: posting.TikTok, Token: "tiktok", Company: "TikTok"}
	if err := ValidBoard(good); err != nil {
		t.Fatalf("the bare board: %v", err)
	}
	if h, ok := BoardHost(good); !ok || h != "api.lifeattiktok.com" {
		t.Errorf("BoardHost = %q, %v", h, ok)
	}
	for name, ok := range map[string]sources.Board{
		"mixed case token": {ATS: posting.TikTok, Token: "TikTok"},
		"search":           {ATS: posting.TikTok, Token: "tiktok", Searches: []string{"software engineer", "ml"}},
		"country filter":   {ATS: posting.TikTok, Token: "tiktok", Filters: map[string][]string{"country": {"United States of America"}}},
		"location filter":  {ATS: posting.TikTok, Token: "tiktok", Filters: map[string][]string{"location": {"CT_223"}}},
	} {
		if err := ValidBoard(ok); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, bad := range map[string]struct {
		mod  func(*sources.Board)
		want string
	}{
		"bytedance":      {func(b *sources.Board) { b.Token = "bytedance" }, "must be tiktok"},
		"header":         {func(b *sources.Board) { b.Token = "tiktok\r\nX-Evil" }, "unsafe characters"},
		"slash":          {func(b *sources.Board) { b.Token = "tiktok/x" }, "'/'"},
		"colon":          {func(b *sources.Board) { b.Token = "tiktok:x" }, "':'"},
		"dots":           {func(b *sources.Board) { b.Token = ".." }, "unsafe characters"},
		"empty":          {func(b *sources.Board) { b.Token = "" }, "empty token"},
		"host":           {func(b *sources.Board) { b.Host = "evil.example.com" }, "host applies"},
		"url":            {func(b *sources.Board) { b.URL = "https://lifeattiktok.com/search" }, "page boards only"},
		"unknown filter": {func(b *sources.Board) { b.Filters = map[string][]string{"department": {"x"}} }, "not one of country, location"},
		"empty filter":   {func(b *sources.Board) { b.Filters = map[string][]string{"country": {" "}} }, "bytes with no control"},
		"max postings":   {func(b *sources.Board) { b.MaxPostings = 100 }, "applies to wd and oc only"},
	} {
		b := good
		bad.mod(&b)
		if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, bad.want)
		}
	}
	if err := ValidBoard(sources.Board{ATS: "zz", Token: "x"}); err == nil || !strings.Contains(err.Error(), ", tt,") {
		t.Errorf("the unknown-code message does not list tt: %v", err)
	}
}
