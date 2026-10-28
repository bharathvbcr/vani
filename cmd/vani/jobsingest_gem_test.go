package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestDeriveBoardGem: a jobs.gem.com page, the board's list or one of its job
// pages, is the Gem board named by its first path segment, so an apply link
// ingested from the browser extension is keyed by the adapter, not saved as a
// careers page.
func TestDeriveBoardGem(t *testing.T) {
	for _, u := range []string{
		"https://jobs.gem.com/groq",
		"https://jobs.gem.com/groq/",
		"https://JOBS.gem.com/groq/am9icG9zdDqAUz7G7JlGLHQawWv7Sec2",
		"https://jobs.gem.com/groq?utm_source=x",
	} {
		b, res := deriveBoard(u, nil, "Groq", nil)
		if res.Status != "" || b.ATS != posting.Gem || b.Token != "groq" || b.Company != "Groq" {
			t.Errorf("%s: board %+v, result %+v", u, b, res)
			continue
		}
		if err := config.ValidBoard(b); err != nil {
			t.Errorf("%s: derived board is not valid: %v", u, err)
		}
	}
	for _, u := range []string{"https://jobs.gem.com", "https://jobs.gem.com/", "http://jobs.gem.com/groq"} {
		if b, res := deriveBoard(u, nil, "Groq", nil); res.Status != searchInvalid || b.ATS != "" {
			t.Errorf("%s: board %+v, result %+v, want %s", u, b, res, searchInvalid)
		}
	}
	// Another host that merely mentions gem is not a Gem board.
	if b, _ := deriveBoard("https://jobs.gem.com.evil.test/groq", nil, "Groq", nil); b.ATS == posting.Gem {
		t.Errorf("lookalike host read as Gem: %+v", b)
	}
}
