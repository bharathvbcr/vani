package ashby

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestCompensationFixture: the real compensation payload puts the salary
// component first in the description, ahead of the posting text.
func TestCompensationFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/compensation.json")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newSource(one(200, string(raw)))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Pay range: USD 210000 – 280313 per year\n\n(description omitted)",
		"Pay range: USD 115000 – 162438 per year\n\n(description omitted)",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Description != w {
			t.Errorf("posting %d description %q, want %q", i, got[i].Description, w)
		}
	}
}

func TestCompensationChoices(t *testing.T) {
	const job = `{"id":"a","title":"T","jobUrl":"https://x.test/a","descriptionPlain":"Body."`
	cases := []struct {
		name, extra, want string
	}{
		{"none", ``, "Body."},
		{"null", `,"compensation":null`, "Body."},
		{"salary component wins", `,"compensation":{"compensationTierSummary":"€1 – €2","scrapeableCompensationSalarySummary":"€3 - €4",
			"summaryComponents":[{"compensationType":"Bonus","interval":"1 YEAR","currencyCode":"EUR","minValue":5,"maxValue":6},
			{"compensationType":"Salary","interval":"1 YEAR","currencyCode":"EUR","minValue":110000,"maxValue":195500}]}`,
			"Pay range: EUR 110000 – 195500 per year\n\nBody."},
		{"salary without currency falls back to summary", `,"compensation":{"compensationTierSummary":"tier","scrapeableCompensationSalarySummary":"€110K - €185K",
			"summaryComponents":[{"compensationType":"Salary","interval":"1 YEAR","currencyCode":null,"minValue":110000,"maxValue":185000}]}`,
			"Compensation: €110K - €185K\n\nBody."},
		{"tier summary last", `,"compensation":{"compensationTierSummary":"$180K – $260K • Offers Equity","summaryComponents":[]}`,
			"Compensation: $180K – $260K • Offers Equity\n\nBody."},
		{"empty compensation", `,"compensation":{"compensationTierSummary":" ","summaryComponents":[]}`, "Body."},
		{"company hides pay", `,"shouldDisplayCompensationOnJobPostings":false,"compensation":{"scrapeableCompensationSalarySummary":"$1 - $2"}`, "Body."},
		{"company shows pay", `,"shouldDisplayCompensationOnJobPostings":true,"compensation":{"scrapeableCompensationSalarySummary":"$1 - $2"}`,
			"Compensation: $1 - $2\n\nBody."},
		// The summary stays one line, so srcutil.SourceText sees all of it as
		// synthesized and none of it as board text.
		{"control characters and line breaks reduced", `,"compensation":{"scrapeableCompensationSalarySummary":"$1\u0007 -\n $2"}`,
			"Compensation: $1 - $2\n\nBody."},
	}
	for _, c := range cases {
		s, _ := newSource(one(200, `{"jobs":[`+job+c.extra+`}]}`))
		got, err := s.List(context.Background(), board)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got[0].Description != c.want {
			t.Errorf("%s: description %q, want %q", c.name, got[0].Description, c.want)
		}
	}
}

func TestCompensationMalformedIsAnError(t *testing.T) {
	s, _ := newSource(one(200, `{"jobs":[{"id":"a","title":"T","jobUrl":"https://x.test/a","compensation":{"summaryComponents":"x"}}]}`))
	if _, err := s.List(context.Background(), board); err == nil || !strings.Contains(err.Error(), "ashby") {
		t.Fatalf("got %v, want a decode error", err)
	}
}
