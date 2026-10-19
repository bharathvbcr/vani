package greenhouse

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

const jobsURL = "https://boards-api.greenhouse.io/v1/boards/gitlab/jobs?content=true"

var board = sources.Board{ATS: posting.Greenhouse, Token: "gitlab", Company: "GitLab Inc"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func one(status int, body string) map[string]srctest.Reply {
	return map[string]srctest.Reply{jobsURL: {Status: status, Body: []byte(body)}}
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
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	want := []posting.Posting{
		{
			Key: "gh:gitlab:8785825002", ATS: posting.Greenhouse, Company: "GitLab",
			Title: "Associate Renewals Manager", Location: "Remote Ireland; Remote, Israel",
			Department: "Renewals", URL: "https://job-boards.greenhouse.io/gitlab/jobs/8785825002",
			UpdatedAt: time.Date(2026, 10, 15, 19, 24, 46, 0, time.UTC), SeenAt: fixedNow,
			PostedAt: time.Date(2026, 9, 9, 15, 28, 46, 0, time.UTC),
		},
		{
			Key: "gh:gitlab:8586667002", ATS: posting.Greenhouse, Company: "GitLab",
			Title: "Engineering Manager, Build", Location: "Remote, United Kingdom",
			Department: "Platforms Engineering", URL: "https://job-boards.greenhouse.io/gitlab/jobs/8586667002",
			UpdatedAt: time.Date(2026, 10, 15, 19, 24, 46, 0, time.UTC), SeenAt: fixedNow,
			PostedAt: time.Date(2026, 8, 19, 15, 33, 14, 0, time.UTC),
		},
	}
	for i := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if !g.UpdatedAt.Equal(want[i].UpdatedAt) {
			t.Errorf("posting %d UpdatedAt %v, want %v", i, g.UpdatedAt, want[i].UpdatedAt)
		}
		if !g.PostedAt.Equal(want[i].PostedAt) {
			t.Errorf("posting %d PostedAt %v, want %v", i, g.PostedAt, want[i].PostedAt)
		}
		g.UpdatedAt, want[i].UpdatedAt = time.Time{}, time.Time{}
		g.PostedAt, want[i].PostedAt = time.Time{}, time.Time{}
		if !strings.HasPrefix(want[i].URL, "https://") || g != want[i] {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, want[i])
		}
		if !strings.HasPrefix(desc, "GitLab is the intelligent orchestration platform for DevSecOps.") {
			t.Errorf("posting %d description starts %.80q", i, desc)
		}
		if strings.ContainsAny(desc, "<>") || strings.Contains(desc, "&lt;") || strings.Contains(desc, "&amp;") || strings.Contains(desc, "&quot;") {
			t.Errorf("posting %d description still carries markup or entities: %.200q", i, desc)
		}
		if len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description %d bytes", i, len(desc))
		}
	}
	// The fixture's content is escaped HTML; the reduced text must have lost
	// its block structure into lines, not one run-on string.
	if !strings.Contains(got[0].Description, "\n") {
		t.Error("description has no line breaks")
	}
}

func TestContentIsUnescapedThenReduced(t *testing.T) {
	body := `{"jobs":[{"id":7,"title":"T","absolute_url":"https://x.test/7",
	"content":"&lt;p&gt;Fish &amp;amp; chips&lt;/p&gt;&lt;ul&gt;&lt;li&gt;one&lt;/li&gt;&lt;li&gt;two&lt;/li&gt;&lt;/ul&gt;&lt;script&gt;bad()&lt;/script&gt;"}]}`
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Fish & chips\none\ntwo"; got[0].Description != want {
		t.Fatalf("got %q, want %q", got[0].Description, want)
	}
	if got[0].Company != "GitLab Inc" {
		t.Errorf("company %q: want the board fallback", got[0].Company)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := newSource(one(404, `{"status":404,"error":"Job not found"}`))
	_, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) {
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
	bad := map[string]string{
		"not json":            `<html>`,
		"truncated":           `{"jobs":[{"id":1`,
		"array not object":    `[]`,
		"no jobs key":         `{}`,
		"null jobs":           `{"jobs":null}`,
		"wrong job type":      `{"jobs":[1]}`,
		"missing id":          `{"jobs":[{"title":"T","absolute_url":"https://x.test/1"}]}`,
		"id with separator":   `{"jobs":[{"id":"a:b","title":"T","absolute_url":"https://x.test/1"}]}`,
		"missing title":       `{"jobs":[{"id":1,"absolute_url":"https://x.test/1"}]}`,
		"missing url":         `{"jobs":[{"id":1,"title":"T"}]}`,
		"javascript url":      `{"jobs":[{"id":1,"title":"T","absolute_url":"javascript:alert(1)"}]}`,
		"bad updated_at":      `{"jobs":[{"id":1,"title":"T","absolute_url":"https://x.test/1","updated_at":"soon"}]}`,
		"bad first_published": `{"jobs":[{"id":1,"title":"T","absolute_url":"https://x.test/1","first_published":"last week"}]}`,
		"trailing garbage":    `{"jobs":[]} x`,
		"empty body":          ``,
		"jobs is an object":   `{"jobs":{}}`,
		"departments string":  `{"jobs":[{"id":1,"title":"T","absolute_url":"https://x.test/1","departments":"x"}]}`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(one(200, body))
			got, err := s.List(context.Background(), board)
			if err == nil {
				t.Fatalf("want error, got %d postings", len(got))
			}
		})
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _ := newSource(one(200, `{"jobs":[],"meta":{"total":0}}`))
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
		fmt.Fprintf(&sb, `{"id":%d,"title":"T","absolute_url":"https://x.test/%d"}`, i+1, i+1)
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
	// Escaped "<p>" + 200 KiB of words + "</p>".
	content := "&lt;p&gt;" + strings.Repeat("lorem ", 200<<10/6) + "&lt;/p&gt;"
	body := fmt.Sprintf(`{"jobs":[{"id":1,"title":"T","absolute_url":"https://x.test/1","content":%q}]}`, content)
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got[0].Description); n == 0 || n > srcutil.MaxDescription {
		t.Fatalf("description %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
}

func TestWrongATSAndEmptyTokenRefused(t *testing.T) {
	s, d := newSource(one(200, jobsBody(1)))
	for _, b := range []sources.Board{
		{ATS: posting.Lever, Token: "gitlab"},
		{ATS: posting.Greenhouse, Token: ""},
		{ATS: posting.Greenhouse, Token: "  "},
		{Token: "gitlab"},
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
	const want = "https://boards-api.greenhouse.io/v1/boards/a%2Fb%3Fc=d%23e%20f/jobs?content=true"
	s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: []byte(`{"jobs":[]}`)}})
	if _, err := s.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "a/b?c=d#e f"}); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != want {
		t.Fatalf("requested %v, want %s", u, want)
	}
}

func TestNewAndATS(t *testing.T) {
	src := New(nil)
	if src.ATS() != posting.Greenhouse {
		t.Fatal(src.ATS())
	}
	if _, err := src.List(context.Background(), board); err == nil {
		t.Fatal("nil doer must error, not panic")
	}
}
