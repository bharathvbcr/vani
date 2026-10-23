package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani watchlist add` takes Hippocratic AI's Ashby board: a slug token and the
// literal board name.
func TestWatchlistAddAshbyBoardName(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "ab", "--token", "hippocratic-ai", "--board-name", "Hippocratic AI",
		"--company", "Hippocratic AI", "--allow-host")
	mustCode(t, 0, "config", "check")
	cfg := watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 1 || cfg.Watchlist[0].Token != "hippocratic-ai" || cfg.Watchlist[0].BoardName != "Hippocratic AI" {
		t.Fatalf("watchlist %+v", cfg.Watchlist)
	}
	o, _ := mustCode(t, 0, "watchlist", "list")
	if !strings.Contains(o, "hippocratic-ai") {
		t.Fatalf("list %q", o)
	}

	// --board names the token; the same board is not added twice.
	_, e := mustCode(t, 1, "watchlist", "add", "--ats", "ab", "--token", "Hippocratic-AI", "--board-name", "Hippocratic AI", "--company", "x")
	if !strings.Contains(e, "already on the watchlist") {
		t.Errorf("duplicate stderr %q", e)
	}
	// The token still may not hold a space, with or without a board name.
	for _, args := range [][]string{
		{"--token", "Hippocratic AI"},
		{"--token", "Hippocratic AI", "--board-name", "Hippocratic AI"},
		{"--token", "other", "--board-name", "a/b"},
		{"--token", "other", "--board-name", " lead"},
		{"--token", "other", "--board-name", "dou  ble"},
		{"--token", "other", "--board-name", "%20"},
	} {
		full := append([]string{"watchlist", "add", "--ats", "ab", "--company", "x"}, args...)
		if _, e := mustCode(t, 2, full...); e == "" {
			t.Errorf("%v: no message", args)
		}
	}
	if _, e := mustCode(t, 2, "watchlist", "add", "--ats", "gh", "--token", "acme", "--board-name", "Acme", "--company", "x"); !strings.Contains(e, "board_name applies to ab only") {
		t.Errorf("gh board_name stderr %q", e)
	}
	// A refused add leaves the config as it was.
	if n := len(watchlistOf(t, cfgPath).Watchlist); n != 1 {
		t.Errorf("watchlist has %d boards after refusals", n)
	}
}

// A saved search from jobs.ashbyhq.com/Hippocratic%20AI becomes a board whose
// token is the slug (a key part cannot hold a space) and whose board_name is
// the literal name.
func TestIngestSavesAnAshbyBoardWithASpace(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	out, _, _ := ingestCLI(t, 0, ingestPayload(t, "https://jobs.ashbyhq.com/Hippocratic%20AI", true, ingestJobs("jobs.ashbyhq.com", 2)))
	if out.Search.Status != searchAdded || out.Search.ATS != posting.Ashby || out.Search.Token != "hippocratic-ai" {
		t.Fatalf("search = %+v", out.Search)
	}
	cfg := watchlistOf(t, cfgPath)
	if len(cfg.Watchlist) != 1 || cfg.Watchlist[0].Token != "hippocratic-ai" || cfg.Watchlist[0].BoardName != "Hippocratic AI" {
		t.Fatalf("watchlist %+v", cfg.Watchlist)
	}
}

func TestDeriveAshbyBoardName(t *testing.T) {
	for name, tc := range map[string]struct {
		url, token, boardName string
		invalid               bool
	}{
		"plain":            {url: "https://jobs.ashbyhq.com/ashby", token: "ashby"},
		"space":            {url: "https://jobs.ashbyhq.com/Hippocratic%20AI", token: "hippocratic-ai", boardName: "Hippocratic AI"},
		"space and job id": {url: "https://jobs.ashbyhq.com/Hippocratic%20AI/91ffd74b-ff35-407c-b2b1-686172cfc029", token: "hippocratic-ai", boardName: "Hippocratic AI"},
		"two spaces":       {url: "https://jobs.ashbyhq.com/Two%20Words%20Co", token: "two-words-co", boardName: "Two Words Co"},
		// A name the rule refuses keeps today's behaviour: the token is the segment,
		// and ValidBoard refuses it when the board is added.
		"double space": {url: "https://jobs.ashbyhq.com/A%20%20B", token: "A  B"},
		"percent":      {url: "https://jobs.ashbyhq.com/A%2520B", token: "A%20B"},
		"empty":        {url: "https://jobs.ashbyhq.com/", invalid: true},
	} {
		t.Run(name, func(t *testing.T) {
			b, res := deriveBoard(tc.url, nil, "Co", nil)
			if tc.invalid {
				if res.Status != searchInvalid {
					t.Fatalf("result %+v", res)
				}
				return
			}
			if res.Status != "" || b.ATS != posting.Ashby || b.Token != tc.token || b.BoardName != tc.boardName {
				t.Errorf("board %+v result %+v", b, res)
			}
		})
	}
}
