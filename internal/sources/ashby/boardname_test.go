package ashby

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// Hippocratic AI's only working Ashby board name has a space. Its token is the
// slug every key is built from; BoardName is what the API is asked for.
func TestBoardNameIsTheRequestAndTokenIsTheKey(t *testing.T) {
	const want = "https://api.ashbyhq.com/posting-api/job-board/Hippocratic%20AI?includeCompensation=true"
	raw, err := os.ReadFile("testdata/spaced_board.json")
	if err != nil {
		t.Fatal(err)
	}
	s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: raw}})
	b := sources.Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: "Hippocratic AI", Company: "Hippocratic AI"}
	got, err := s.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != want {
		t.Fatalf("requested %v, want %s", u, want)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	for _, p := range got {
		if !strings.HasPrefix(string(p.Key), b.KeyPrefix()) || strings.ContainsAny(string(p.Key), " \t") {
			t.Errorf("key %q must sit under %q and hold no space", p.Key, b.KeyPrefix())
		}
		if !strings.HasPrefix(p.URL, "https://jobs.ashbyhq.com/Hippocratic%20AI/") {
			t.Errorf("URL %q", p.URL)
		}
	}
	if got[0].Key != "ab:hippocratic-ai:91ffd74b-ff35-407c-b2b1-686172cfc029" {
		t.Errorf("key %q", got[0].Key)
	}
}

// Without a BoardName the request is the token's, as before.
func TestNoBoardNameRequestsTheToken(t *testing.T) {
	s, d := newSource(one(200, `{"jobs":[]}`))
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != boardURL {
		t.Fatalf("requested %v, want %s", u, boardURL)
	}
}

func TestBadBoardNamesAreRefusedBeforeAnyRequest(t *testing.T) {
	for name, bn := range map[string]string{
		"leading space":  " Hippocratic AI",
		"trailing space": "Hippocratic AI ",
		"double space":   "Hippocratic  AI",
		"tab":            "Hippocratic\tAI",
		"newline":        "Hippocratic\nAI",
		"slash":          "a/b",
		"dot dot":        "..",
		"dot":            ".",
		"percent":        "Hippocratic%20AI",
		"query":          "a?b",
		"fragment":       "a#b",
		"colon":          "a:b",
		"backslash":      `a\b`,
		"oversized":      strings.Repeat("a", sources.MaxBoardNameLen+1),
		"only spaces":    "   ",
	} {
		t.Run(name, func(t *testing.T) {
			s, d := newSource(one(200, `{"jobs":[]}`))
			b := sources.Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: bn}
			if _, err := s.List(context.Background(), b); err == nil {
				t.Fatalf("board_name %q accepted", bn)
			}
			if n := len(d.URLs()); n != 0 {
				t.Errorf("refused board made %d requests", n)
			}
		})
	}
}
