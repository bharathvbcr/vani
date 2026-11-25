package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A batch from a job site's search (the extension on LinkedIn, or
// skills/indeed-to-vani) is not engagement with each company it lists; a
// batch from an employer's own page is.
func TestScrapedKeyCountsEmployerPagesNotJobSites(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"www.indeed.com", false},
		{"indeed.com", false},
		{"uk.indeed.com", false},
		{"www.linkedin.com", false},
		{"www.glassdoor.co.uk", false},
		{"www.ziprecruiter.com", false},
		{"careers.acme.com", true},
		{"jobs.example.org", true},
		{"notindeed.com", true}, // a different site, not a subdomain
	}
	for _, tc := range cases {
		k, err := posting.PageKey(scrapeToken(tc.host), "https://"+tc.host+"/job/1")
		if err != nil {
			t.Fatal(err)
		}
		if got := isScrapedKey(k); got != tc.want {
			t.Errorf("%s (%s): isScrapedKey = %v, want %v", tc.host, k, got, tc.want)
		}
	}
	// Not a scrape at all.
	for _, k := range []posting.Key{"pg:acme:abc", "gh:acme:1", "pg", "pg:www-indeed-com"} {
		if isScrapedKey(k) {
			t.Errorf("isScrapedKey(%q) = true", k)
		}
	}
}

func TestSourceLineNamesJobBoards(t *testing.T) {
	if got := sourceLine("dc:dice:46f0d513-ff59-4049-b4ab-9b45d8002fbe"); got != "Source: Dice. "+posting.SourceOf("dc:dice:x").Note {
		t.Fatalf("dice: %q", got)
	}
	if got := sourceLine("hm:himalayas:a:b"); got != "Source: Himalayas" {
		t.Fatalf("himalayas: %q", got)
	}
	if got := sourceLine("gh:acme:1"); got != "" {
		t.Fatalf("employer board: %q", got)
	}
}

func TestAggregatorReasonPointsAtTheWayIn(t *testing.T) {
	for host, want := range map[string]string{
		"www.dice.com":         "--ats dc",
		"www.ziprecruiter.com": "--ats zr",
		"www.indeed.com":       "skills/indeed-to-vani",
		"uk.indeed.com":        "skills/indeed-to-vani",
		"www.linkedin.com":     "needs a signed-in browser",
		"notdice.com":          "needs a signed-in browser",
	} {
		if got := aggregatorReason(host); !strings.Contains(got, want) {
			t.Errorf("%s: %q, want it to contain %q", host, got, want)
		}
	}
}
