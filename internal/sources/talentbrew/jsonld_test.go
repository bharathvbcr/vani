package talentbrew

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// Citi's job 102455422752 (2026-10-28) wrote a raw tab inside its description
// string, which encoding/json refuses, and the whole board failed every run.
func TestRawControlCharactersInTheBlockAreSpaces(t *testing.T) {
	page := string(fixture(t, "citi-job.html"))
	const at = `"description": "<p>The Applications`
	if !strings.Contains(page, at) {
		t.Fatal("fixture changed: no description to put a tab in")
	}
	page = strings.Replace(page, at, `"description": "<p>●`+"\t"+`Design`+"\n"+`The Applications`, 1)
	j, ok := findJobPosting(page, "jobs.citi.com", "287", "102161084880")
	if !ok {
		t.Fatal("a JobPosting with a raw tab in its description was passed over")
	}
	if !strings.Contains(j.Description, "● Design The Applications") {
		t.Fatalf("description %.80q: the tab and newline should read as spaces", j.Description)
	}
	if got := controlToSpace("a\tb\nc\x00d\re"); got != "a b c d e" {
		t.Fatalf("controlToSpace = %q", got)
	}
}

func TestLDBlocks(t *testing.T) {
	ld := func(body string) string { return `<script type="application/ld+json">` + body + `</script>` }
	for name, tc := range map[string]struct {
		doc  string
		want []string
	}{
		"one block":           {`<html><head>` + ld(`{"a":1}`) + `</head>`, []string{`{"a":1}`}},
		"other types skipped": {`<script type="text/javascript">var x = 1;</script>` + ld(`{"a":2}`) + `<script>var y;</script>`, []string{`{"a":2}`}},
		"two blocks in order": {ld(`1`) + ld(`2`), []string{`1`, `2`}},
		"case-insensitive":    {`<SCRIPT TYPE="Application/LD+JSON" id=x>{"a":3}</SCRIPT>`, []string{`{"a":3}`}},
		"single quotes":       {`<script type='application/ld+json'>{"a":4}</script>`, []string{`{"a":4}`}},
		"self-closing slash":  {`<script/type="application/ld+json">{"a":5}</script>`, []string{`{"a":5}`}},
		"not a script tag":    {`<scripts type="application/ld+json">{"a":6}</scripts>` + ld(`{"a":7}`), []string{`{"a":7}`}},
		// A "<script" inside another script's text is part of that text.
		"inside a script":                     {`<script>var s = '<script type="application/ld+json">{"a":8}';</script>` + ld(`{"a":9}`), []string{`{"a":9}`}},
		"no blocks":                           {`<html><body>nothing</body></html>`, nil},
		"empty":                               {``, nil},
		"unterminated script":                 {`<script type="application/ld+json">{"a":10}`, nil},
		"unterminated tag":                    {`<script type="application/ld+json"`, nil},
		"tag over the cap":                    {`<script data-x="` + strings.Repeat("a", maxTagLen) + `" type="application/ld+json">{"a":11}</script>`, nil},
		"bare <script at end":                 {`<script`, nil},
		"keeps the first before a broken one": {ld(`{"a":12}`) + `<script type="application/ld+json">{"a":13`, []string{`{"a":12}`}},
	} {
		t.Run(name, func(t *testing.T) {
			got := ldBlocks(tc.doc)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("capped", func(t *testing.T) {
		got := ldBlocks(strings.Repeat(ld(`{}`), maxLDBlocks+5))
		if len(got) != maxLDBlocks {
			t.Fatalf("%d blocks, want the cap %d", len(got), maxLDBlocks)
		}
	})
}

func TestParseDate(t *testing.T) {
	for in, want := range map[string]time.Time{
		"":                          {},
		"2026-10-27":                time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		"2024-07-10":                time.Date(2024, 7, 10, 0, 0, 0, 0, time.UTC),
		"2026-10-27T10:00:00Z":      time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC),
		"2026-10-27T10:00:00-05:00": time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC),
	} {
		got, err := parseDate(in)
		if err != nil || !got.Equal(want) || got.Location() != time.UTC && !got.IsZero() {
			t.Errorf("parseDate(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "2026-13-01", "2026-10-27 10:00", "10/27/2026", "2026-10-27T", "0"} {
		if _, err := parseDate(bad); err == nil {
			t.Errorf("parseDate(%q) accepted", bad)
		}
	}
}

func TestPayLine(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		// The template every live page carries.
		"live empty template": {`{"@type":"MonetaryAmount","currency":"","value":{"@type":"QuantitativeValue","unitText":""}}`, ""},
		"absent":              {``, ""},
		"null":                {`null`, ""},
		"range":               {`{"currency":"USD","value":{"minValue":100000,"maxValue":150000,"unitText":"YEAR"}}`, "Pay range: USD 100000 – 150000 per year"},
		"strings":             {`{"currency":"USD","value":{"minValue":"90.5","maxValue":"120","unitText":"HOUR"}}`, "Pay range: USD 90.5 – 120 per hour"},
		"single value":        {`{"currency":"CAD","value":{"value":85000,"unitText":"YEAR"}}`, "Pay range: CAD 85000 per year"},
		"min only":            {`{"currency":"USD","value":{"minValue":70000,"unitText":"YEAR"}}`, "Pay range: USD 70000 per year"},
		"max only":            {`{"currency":"USD","value":{"maxValue":70000,"unitText":""}}`, "Pay range: USD 70000"},
		"no currency":         {`{"currency":"","value":{"minValue":1,"maxValue":2,"unitText":"YEAR"}}`, ""},
		"bad currency":        {`{"currency":"US","value":{"minValue":1,"maxValue":2}}`, ""},
		"digits currency":     {`{"currency":"U5D","value":{"minValue":1,"maxValue":2}}`, ""},
		"inverted":            {`{"currency":"USD","value":{"minValue":200,"maxValue":100}}`, ""},
		"zero":                {`{"currency":"USD","value":{"minValue":0,"maxValue":0}}`, ""},
		"negative":            {`{"currency":"USD","value":{"minValue":-5,"maxValue":10}}`, ""},
		"not a number":        {`{"currency":"USD","value":{"minValue":"lots","maxValue":10}}`, ""},
		"infinite":            {`{"currency":"USD","value":{"minValue":"Inf","maxValue":"Inf"}}`, ""},
		"nan":                 {`{"currency":"USD","value":{"minValue":"NaN"}}`, ""},
		"value is a number":   {`{"currency":"USD","value":120000}`, ""},
		"an array":            {`[{"currency":"USD"}]`, ""},
		"a string":            {`"USD 100000"`, ""},
		"truncated":           {`{"currency":"USD","value":{"minValue":1`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			if got := payLine(raw); got != tc.want {
				t.Fatalf("payLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaces(t *testing.T) {
	p := func(loc, region, country string) ldPlace {
		var x ldPlace
		x.Address.Locality, x.Address.Region, x.Address.Country = loc, region, country
		return x
	}
	for name, tc := range map[string]struct {
		in     []ldPlace
		want   string
		remote bool
	}{
		"intuit names":           {[]ldPlace{p("San Diego", "California", "United States")}, "San Diego, California, United States", false},
		"citi codes":             {[]ldPlace{p("Pune", "MH", "IN")}, "Pune, MH, IN", false},
		"numeric region":         {[]ldPlace{p("Olsztyn", "85", "PL"), p("Mexico City", "09", "MX"), p("Seoul", "11", "KR")}, "Olsztyn, PL; Mexico City, MX; Seoul, KR", false},
		"alphanumeric region":    {[]ldPlace{p("Somewhere", "A1", "ZZ"), p("Elsewhere", "1st District", "ZZ")}, "Somewhere, A1, ZZ; Elsewhere, 1st District, ZZ", false},
		"empty parts dropped":    {[]ldPlace{p("Mexico City", "", "MX")}, "Mexico City, MX", false},
		"several":                {[]ldPlace{p("A", "B", "C"), p("D", "E", "F")}, "A, B, C; D, E, F", false},
		"repeats once":           {[]ldPlace{p("A", "B", "C"), p("a", "b", "c")}, "A, B, C", false},
		"remote marker":          {[]ldPlace{p("Sacramento", "California", "United States"), p("", "", "Remote")}, "Sacramento, California, United States", true},
		"only remote":            {[]ldPlace{p("", "", "Remote")}, "", true},
		"remote city is a place": {[]ldPlace{p("Remote", "", "")}, "Remote", false},
		"none":                   {nil, "", false},
		"entities decoded":       {[]ldPlace{p("St&#39;s", "", "")}, "St's", false},
	} {
		t.Run(name, func(t *testing.T) {
			loc, remote := places(tc.in)
			if loc != tc.want || remote != tc.remote {
				t.Fatalf("places = %q, %v; want %q, %v", loc, remote, tc.want, tc.remote)
			}
		})
	}
	t.Run("capped", func(t *testing.T) {
		var in []ldPlace
		for i := 0; i < maxPlaces+10; i++ {
			in = append(in, p(fmt.Sprintf("City%d", i), "", ""))
		}
		loc, _ := places(in)
		if n := len(strings.Split(loc, "; ")); n != maxPlaces {
			t.Fatalf("%d places, want %d", n, maxPlaces)
		}
	})
}

func TestStringList(t *testing.T) {
	for in, want := range map[string]string{`"Full-Time"`: "[Full-Time]", `["A","B"]`: "[A B]", `null`: "[]"} {
		var l stringList
		if err := json.Unmarshal([]byte(in), &l); err != nil || fmt.Sprint([]string(l)) != want {
			t.Errorf("%s -> %v, %v; want %s", in, l, err, want)
		}
	}
	var l stringList
	for _, bad := range []string{`5`, `{"a":1}`, `[1]`, `true`} {
		if err := json.Unmarshal([]byte(bad), &l); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// A JobPosting a hostile page could not fake: the page names its own job.
func TestSameJob(t *testing.T) {
	const host, company, id = "jobs.intuit.com", "27595", "87369450000"
	for in, want := range map[string]bool{
		mlURL: true,
		"https://JOBS.intuit.com/job/x/y/27595/87369450000":           true,
		"https://jobs.intuit.com/job/x/y/27595/87369450001":           false,
		"https://jobs.intuit.com/job/x/y/27596/87369450000":           false,
		"http://jobs.intuit.com/job/x/y/27595/87369450000":            false,
		"https://jobs.intuit.com:444/job/x/y/27595/87369450000":       false,
		"https://u@jobs.intuit.com/job/x/y/27595/87369450000":         false,
		"https://jobs.intuit.com.evil.test/job/x/y/27595/87369450000": false,
		"https://jobs.intuit.com/search-jobs/x/y/27595/87369450000":   false,
		"": false, "not a url": false, "%": false,
	} {
		if got := sameJob(in, host, company, id); got != want {
			t.Errorf("sameJob(%q) = %v, want %v", in, got, want)
		}
	}
}

// A populated baseSalary reaches the description as a pay line the engine's
// ParsePay reads, ahead of the role line.
func TestPayAndRoleLinesLeadTheDescription(t *testing.T) {
	loc := jobAt(board.Host, "27595", 3)
	page := strings.Replace(ldPage(loc, "Engineer"), `"hiringOrganization"`,
		`"baseSalary":{"currency":"USD","value":{"minValue":120000,"maxValue":180000,"unitText":"YEAR"}},`+
			`"employmentType":["Full-Time","Contract"],"jobLocationType":"TELECOMMUTE","hiringOrganization"`, 1)
	d := siteDoer([]string{loc}, map[string]srctest.Reply{loc: ok(page)})
	got, err := newSource(d).List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	want := "Pay range: USD 120000 – 180000 per year\nRole: full-time, contract, remote\n\nBuild & ship"
	if got[0].Description != want || !got[0].Remote {
		t.Fatalf("description %q remote %v, want %q", got[0].Description, got[0].Remote, want)
	}
}

func TestSlugText(t *testing.T) {
	for in, want := range map[string]string{
		"staff-software-engineer":           "Staff Software Engineer",
		"san-diego":                         "San Diego",
		"sr-staff-product-manager-turbotax": "Sr Staff Product Manager Turbotax",
		"a--b":                              "A B",
		"-":                                 "",
		"%e5%9b%bd%e5%86%85":                "国内",
		"m%c5%82odszy-analityk":             "Młodszy Analityk",
		"bad%zzescape":                      "Bad%zzescape",
		"2028":                              "2028",
		"under_score":                       "Under Score",
	} {
		if got := slugText(in); got != want {
			t.Errorf("slugText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugText(strings.Repeat("word-", 400)); len(got) > 300 {
		t.Errorf("slug text is %d bytes, over the title bound", len(got))
	}
}
