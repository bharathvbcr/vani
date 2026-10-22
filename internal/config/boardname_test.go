package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Hippocratic AI's Ashby board is named "Hippocratic AI". A token cannot hold
// the space (posting keys, --board refs and the Mac app's key check all refuse
// it), so the token is a slug and board_name carries the literal name.
func TestAshbyBoardNameWithASpace(t *testing.T) {
	good := sources.Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: "Hippocratic AI", Company: "Hippocratic AI"}
	if err := ValidBoard(good); err != nil {
		t.Fatalf("slug token with board_name refused: %v", err)
	}
	c := valid()
	c.Watchlist = append(c.Watchlist, good)
	if err := c.Validate(); err != nil {
		t.Fatalf("config refused: %v", err)
	}

	// A space in the token itself stays refused: nothing about existing rules moves.
	err := ValidBoard(sources.Board{ATS: posting.Ashby, Token: "Hippocratic AI", Company: "x"})
	if err == nil || !strings.Contains(err.Error(), "unsafe characters") {
		t.Fatalf("a spaced token must still be refused, got %v", err)
	}
}

func TestBoardNameRefusals(t *testing.T) {
	base := sources.Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: "Hippocratic AI"}
	for name, tc := range map[string]struct {
		mut  func(*sources.Board)
		want string
	}{
		"on greenhouse":       {func(b *sources.Board) { b.ATS = posting.Greenhouse }, "applies to ab only"},
		"on lever":            {func(b *sources.Board) { b.ATS = posting.Lever }, "applies to ab only"},
		"on workday":          {func(b *sources.Board) { b.ATS = posting.Workday; b.Token = "a/b"; b.Host = "a.wd5.myworkdayjobs.com" }, "applies to ab only"},
		"leading space":       {func(b *sources.Board) { b.BoardName = " Hippocratic AI" }, "board_name"},
		"trailing space":      {func(b *sources.Board) { b.BoardName = "Hippocratic AI " }, "board_name"},
		"double space":        {func(b *sources.Board) { b.BoardName = "Hippocratic  AI" }, "board_name"},
		"slash":               {func(b *sources.Board) { b.BoardName = "a/b" }, "board_name"},
		"percent":             {func(b *sources.Board) { b.BoardName = "Hippocratic%20AI" }, "board_name"},
		"dot dot":             {func(b *sources.Board) { b.BoardName = ".." }, "board_name"},
		"colon":               {func(b *sources.Board) { b.BoardName = "a:b" }, "board_name"},
		"control":             {func(b *sources.Board) { b.BoardName = "a\nb" }, "board_name"},
		"oversized":           {func(b *sources.Board) { b.BoardName = strings.Repeat("a", sources.MaxBoardNameLen+1) }, "board_name"},
		"token still checked": {func(b *sources.Board) { b.Token = "hippocratic ai" }, "unsafe characters"},
	} {
		t.Run(name, func(t *testing.T) {
			b := base
			tc.mut(&b)
			err := ValidBoard(b)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// board_name round-trips through the config file, and an old config (no field)
// loads as before; the strict loader names an unknown field, so the field must
// be known to the binary that reads it.
func TestBoardNameRoundTrips(t *testing.T) {
	c := valid()
	c.Watchlist = append(c.Watchlist, sources.Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: "Hippocratic AI", Company: "Hippocratic AI"})
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"board_name":"Hippocratic AI"`) {
		t.Fatalf("not serialised: %s", raw)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Watchlist[len(back.Watchlist)-1]; got.BoardName != "Hippocratic AI" || got.Token != "hippocratic-ai" {
		t.Errorf("round trip: %+v", got)
	}
	old, _ := json.Marshal(valid())
	if strings.Contains(string(old), "board_name") {
		t.Errorf("a board without the field must not write it: %s", old)
	}
}
