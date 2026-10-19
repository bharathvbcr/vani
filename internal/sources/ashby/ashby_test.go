package ashby

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

const boardURL = "https://api.ashbyhq.com/posting-api/job-board/ashby?includeCompensation=true"

var board = sources.Board{ATS: posting.Ashby, Token: "ashby", Company: "Ashby"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func one(status int, body string) map[string]srctest.Reply {
	return map[string]srctest.Reply{boardURL: {Status: status, Body: []byte(body)}}
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newSource(one(200, string(raw)))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	want := []posting.Posting{
		{
			Key: "ab:ashby:7d8ec4fe-50aa-4d7e-9eec-db7b2d14bcbd", ATS: posting.Ashby, Company: "Ashby",
			Title: "Mid-Market Solutions Engineering Manager", Location: "Remote - US", Remote: true,
			Department: "Sales",
			URL:        "https://jobs.ashbyhq.com/ashby/7d8ec4fe-50aa-4d7e-9eec-db7b2d14bcbd",
			ApplyURL:   "https://jobs.ashbyhq.com/ashby/7d8ec4fe-50aa-4d7e-9eec-db7b2d14bcbd/application",
			PostedAt:   time.Date(2026, 8, 3, 23, 5, 52, 543_000_000, time.UTC), SeenAt: fixedNow,
		},
		{
			Key: "ab:ashby:c62ec47a-8de9-4781-b1f7-563a229c4e90", ATS: posting.Ashby, Company: "Ashby",
			Title: "GTM Systems Analyst", Location: "Remote - US", Remote: true, Department: "Sales",
			URL:      "https://jobs.ashbyhq.com/ashby/c62ec47a-8de9-4781-b1f7-563a229c4e90",
			ApplyURL: "https://jobs.ashbyhq.com/ashby/c62ec47a-8de9-4781-b1f7-563a229c4e90/application",
			PostedAt: time.Date(2026, 8, 27, 15, 22, 29, 182_000_000, time.UTC), SeenAt: fixedNow,
		},
	}
	prefixes := []string{"As the Mid-Market Solutions Engineering Manager at Ashby", "THE ROLE\n\nAshby's GTM engine"}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d", len(got), len(want))
	}
	for i := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if !g.PostedAt.Equal(want[i].PostedAt) {
			t.Errorf("posting %d PostedAt %v, want %v", i, g.PostedAt, want[i].PostedAt)
		}
		g.PostedAt, want[i].PostedAt = time.Time{}, time.Time{}
		if g != want[i] {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, want[i])
		}
		if !strings.HasPrefix(desc, prefixes[i]) {
			t.Errorf("posting %d description starts %.60q, want prefix %q", i, desc, prefixes[i])
		}
		if len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description %d bytes", i, len(desc))
		}
	}
}

func TestUnlistedEntriesAreSkipped(t *testing.T) {
	body := `{"apiVersion":"1","jobs":[
	{"id":"a","title":"Listed","jobUrl":"https://x.test/a","isListed":true},
	{"id":"b","title":"Unlisted","jobUrl":"https://x.test/b","isListed":false},
	{"id":"c","title":"No flag","jobUrl":"https://x.test/c"}]}`
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	var keys []posting.Key
	for _, p := range got {
		keys = append(keys, p.Key)
	}
	if len(keys) != 2 || keys[0] != "ab:ashby:a" || keys[1] != "ab:ashby:c" {
		t.Fatalf("keys %v, want [ab:ashby:a ab:ashby:c]", keys)
	}
}

func TestUnlistedEntryIsNotValidatedOrCounted(t *testing.T) {
	// An unlisted entry is not a posting, so a malformed one must not fail
	// the board.
	body := `{"jobs":[{"id":"","isListed":false},{"id":"a","title":"T","jobUrl":"https://x.test/a"}]}`
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
}

func TestDescriptionFallsBackToHTML(t *testing.T) {
	body := `{"jobs":[{"id":"a","title":"T","jobUrl":"https://x.test/a",
	"descriptionHtml":"<h2>Role</h2><p>Build &amp; ship</p><ul><li>Go</li><li>Rust</li></ul><script>x()</script>"},
	{"id":"b","title":"T","jobUrl":"https://x.test/b","descriptionPlain":"plain wins","descriptionHtml":"<p>html loses</p>"}]}`
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Role\nBuild & ship\nGo\nRust"; got[0].Description != want {
		t.Errorf("html fallback: got %q, want %q", got[0].Description, want)
	}
	if got[1].Description != "plain wins" {
		t.Errorf("plain: got %q", got[1].Description)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := newSource(one(404, `{"error":"not found"}`))
	if _, err := s.List(context.Background(), board); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("got %v, want ErrBoardNotFound", err)
	}
}

func TestServerErrorIsAnError(t *testing.T) {
	s, _ := newSource(one(500, `{"jobs":[]}`))
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestMalformedPayloadsAreErrors(t *testing.T) {
	ok := `"id":"1","title":"T","jobUrl":"https://x.test/1"`
	bad := map[string]string{
		"not json":          `<html>`,
		"truncated":         `{"jobs":[{"id":"1"`,
		"array not object":  `[]`,
		"no jobs key":       `{"apiVersion":"1"}`,
		"null jobs":         `{"jobs":null}`,
		"empty body":        ``,
		"wrong job type":    `{"jobs":["x"]}`,
		"missing id":        `{"jobs":[{"title":"T","jobUrl":"https://x.test/1"}]}`,
		"id with separator": `{"jobs":[{"id":"a:b","title":"T","jobUrl":"https://x.test/1"}]}`,
		"missing title":     `{"jobs":[{"id":"1","jobUrl":"https://x.test/1"}]}`,
		"missing jobUrl":    `{"jobs":[{"id":"1","title":"T"}]}`,
		"data url":          `{"jobs":[{"id":"1","title":"T","jobUrl":"data:text/html,x"}]}`,
		"bad applyUrl":      `{"jobs":[{` + ok + `,"applyUrl":"javascript:x"}]}`,
		"bad publishedAt":   `{"jobs":[{` + ok + `,"publishedAt":"2026-13-45"}]}`,
		"isRemote string":   `{"jobs":[{` + ok + `,"isRemote":"yes"}]}`,
		"isListed string":   `{"jobs":[{` + ok + `,"isListed":"false"}]}`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(one(200, body))
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %d postings", len(got))
			}
		})
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _ := newSource(one(200, `{"apiVersion":"1","jobs":[]}`))
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v: want empty non-nil list and no error", got, err)
	}
}

func jobsBody(n int) string {
	var sb strings.Builder
	sb.WriteString(`{"jobs":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"j%d","title":"T","jobUrl":"https://x.test/%d"}`, i, i)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func TestPostingCap(t *testing.T) {
	s, _ := newSource(one(200, jobsBody(srcutil.MaxPostings)))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != srcutil.MaxPostings {
		t.Fatalf("at the cap: %d postings, %v", len(got), err)
	}
	s, _ = newSource(one(200, jobsBody(srcutil.MaxPostings+1)))
	got, err = s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Fatalf("over the cap: %d postings, %v", len(got), err)
	}
}

func TestDescriptionCap(t *testing.T) {
	big := strings.Repeat("lorem ", 200<<10/6)
	body := fmt.Sprintf(`{"jobs":[{"id":"a","title":"T","jobUrl":"https://x.test/a","descriptionPlain":%q},
	{"id":"b","title":"T","jobUrl":"https://x.test/b","descriptionHtml":%q}]}`, big, "<p>"+big+"</p>")
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range got {
		if n := len(p.Description); n == 0 || n > srcutil.MaxDescription {
			t.Errorf("posting %d description %d bytes, want 1..%d", i, n, srcutil.MaxDescription)
		}
	}
}

func TestWrongATSAndEmptyTokenRefused(t *testing.T) {
	s, d := newSource(one(200, `{"jobs":[]}`))
	for _, b := range []sources.Board{
		{ATS: posting.Lever, Token: "ashby"},
		{ATS: posting.Ashby, Token: ""},
		{ATS: posting.Ashby, Token: "\n"},
		{Token: "ashby"},
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("board %+v accepted", b)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("refused boards still made %d requests", n)
	}
}

func TestTokenIsEscaped(t *testing.T) {
	const want = "https://api.ashbyhq.com/posting-api/job-board/a%2Fb%3FincludeCompensation=true%23x%20y?includeCompensation=true"
	s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: []byte(`{"jobs":[]}`)}})
	if _, err := s.List(context.Background(), sources.Board{ATS: posting.Ashby, Token: "a/b?includeCompensation=true#x y"}); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != want {
		t.Fatalf("requested %v, want %s", u, want)
	}
}

func TestNewAndATS(t *testing.T) {
	src := New(nil)
	if src.ATS() != posting.Ashby {
		t.Fatal(src.ATS())
	}
	if _, err := src.List(context.Background(), board); err == nil {
		t.Fatal("nil doer must error, not panic")
	}
}
