package lever

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestFixtureKeepsClosingText: the real board's pay ranges live in
// additionalPlain, which now ends the description.
func TestFixtureKeepsClosingText(t *testing.T) {
	raw, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newSource(us(200, string(raw)))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range []string{
		"Spotify is an equal opportunity employer.",
		"The United States base range for this position is $92,126–$131,609, plus equity.",
		"The United States base range for this position is $74,000 – $106,000 USD, plus equity.",
	} {
		if !strings.Contains(got[i].Description, "\n\n"+w) {
			t.Errorf("posting %d description lacks closing text %q: …%q", i, w, tail(got[i].Description, 300))
		}
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func TestSalaryFields(t *testing.T) {
	const post = `{"id":"p1","text":"T","hostedUrl":"https://x.test/p1","descriptionPlain":"Body."`
	cases := []struct {
		name, extra, want string
	}{
		{"none", ``, "Body."},
		// Verbatim from a live board (api.lever.co/v0/postings/outreach, 2026-10-19): all 17
		// salaryRange objects there had numeric min/max, "USD" and "per-year-salary".
		{"range first", `,"salaryRange":{"max":110000,"currency":"USD","interval":"per-year-salary","min":70000}`,
			"Pay range: USD 70000 – 110000 per year\n\nBody."},
		{"hourly", `,"salaryRange":{"currency":"usd","interval":"per-hour-wage","min":40,"max":52.5}`,
			"Pay range: USD 40 – 52.5 per hour\n\nBody."},
		{"unknown interval", `,"salaryRange":{"currency":"EUR","interval":"one-time","min":70000,"max":90000}`,
			"Pay range: EUR 70000 – 90000\n\nBody."},
		{"unusable range is left out", `,"salaryRange":{"currency":"","min":1,"max":2}`, "Body."},
		{"null range", `,"salaryRange":null`, "Body."},
		{"salary description and closing text", `,"salaryDescriptionPlain":"Pay depends on level.","additionalPlain":"Base range $1–$2."`,
			"Body.\n\nPay depends on level.\n\nBase range $1–$2."},
		{"blank closing text adds nothing", `,"additionalPlain":"  \n "`, "Body."},
	}
	for _, c := range cases {
		s, _ := newSource(us(200, `[`+post+c.extra+`}]`))
		got, err := s.List(context.Background(), board)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got[0].Description != c.want {
			t.Errorf("%s: description %q, want %q", c.name, got[0].Description, c.want)
		}
	}
}

func TestSalaryRangeMalformedIsAnError(t *testing.T) {
	s, _ := newSource(us(200, `[{"id":"p1","text":"T","hostedUrl":"https://x.test/p1","salaryRange":{"min":"lots"}}]`))
	if _, err := s.List(context.Background(), board); err == nil {
		t.Fatal("a non-numeric salaryRange.min must fail the board, not read as no pay")
	}
}
