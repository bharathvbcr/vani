package gem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

const boardURL = "https://api.gem.com/job_board/v0/groq/job_posts/"

var board = sources.Board{ATS: posting.Gem, Token: "groq", Company: "Groq"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func one(status int, body string) map[string]srctest.Reply {
	return map[string]srctest.Reply{boardURL: {Status: status, Body: []byte(body)}}
}

func TestList(t *testing.T) {
	body := `[
		{"id":"am9icG9zdDqAUz7G7JlGLHQawWv7Sec2","title":"Sr.&amp; Staff  Engineer",
		 "absolute_url":"https://jobs.gem.com/groq/am9icG9zdDqAUz7G7JlGLHQawWv7Sec2",
		 "content_plain":"Build kernels.\n\nCUDA","content":"<p>ignored</p>",
		 "employment_type":"full_time",
		 "first_published_at":"2026-09-22T01:02:11.972Z","updated_at":"2026-10-26T23:29:16.989Z",
		 "created_at":"2020-01-01T00:00:00Z","location_type":"in_office",
		 "location":{"name":"Dallas, United States"},
		 "departments":[{"name":"Commercial Sales"},{"name":" "},{"name":"Systems"}],
		 "offices":[{"name":"New York City Area","location":{"name":"New York, United States"}}]},
		{"id":"4606150005","title":"Same title","location_type":"remote",
		 "location":{"name":"United States - Remote"},
		 "offices":[{"name":"United States / Canada","location":{"name":"Office loc"}}]},
		{"id":"4606150006","title":"Same title","location_type":"hybrid",
		 "location":{"name":""},"offices":[{"name":"Edinburgh","location":{"name":"  "}}]}
	]`
	s, d := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if urls := d.URLs(); len(urls) != 1 || urls[0] != boardURL {
		t.Fatalf("urls %v", urls)
	}
	want := []posting.Posting{
		{
			Key: "gm:groq:am9icg9zddqauz7g7jlglhqawwv7sec2", ATS: posting.Gem, Company: "Groq",
			Title: "Sr.& Staff Engineer", Location: "Dallas, United States", Remote: false,
			Department:  "Commercial Sales, Systems",
			URL:         "https://jobs.gem.com/groq/am9icG9zdDqAUz7G7JlGLHQawWv7Sec2",
			PostedAt:    time.Date(2026, 9, 22, 1, 2, 11, 972_000_000, time.UTC),
			UpdatedAt:   time.Date(2026, 10, 26, 23, 29, 16, 989_000_000, time.UTC),
			Description: "Role: full-time, on-site\n\nBuild kernels.\n\nCUDA", SeenAt: fixedNow,
		},
		{
			Key: "gm:groq:4606150005", ATS: posting.Gem, Company: "Groq",
			Title: "Same title", Location: "United States - Remote", Remote: true,
			URL: "https://jobs.gem.com/groq/4606150005", Description: "Role: remote\n\n", SeenAt: fixedNow,
		},
		{
			Key: "gm:groq:4606150006", ATS: posting.Gem, Company: "Groq",
			Title: "Same title", Location: "Edinburgh", Remote: false,
			URL: "https://jobs.gem.com/groq/4606150006", Description: "Role: hybrid\n\n", SeenAt: fixedNow,
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].PostedAt.Equal(want[i].PostedAt) || !got[i].UpdatedAt.Equal(want[i].UpdatedAt) {
			t.Errorf("posting %d times posted %v updated %v", i, got[i].PostedAt, got[i].UpdatedAt)
		}
		got[i].PostedAt, want[i].PostedAt = time.Time{}, time.Time{}
		got[i].UpdatedAt, want[i].UpdatedAt = time.Time{}, time.Time{}
		if got[i] != want[i] {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestHTMLFallbackAndNullTimes(t *testing.T) {
	body := `[{"id":"a","title":"T","content_plain":" \n ","content":"<h2>Role</h2><p>Build &amp; ship</p><script>x()</script>","first_published_at":null,"location":null,"offices":[{"name":"SF","location":{"name":"San Francisco"}}]}]`
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Description != "Role\nBuild & ship" {
		t.Errorf("description %q", got[0].Description)
	}
	if !got[0].PostedAt.IsZero() || !got[0].UpdatedAt.IsZero() {
		t.Errorf("null times posted %v updated %v", got[0].PostedAt, got[0].UpdatedAt)
	}
	if got[0].Location != "San Francisco" || got[0].Remote {
		t.Errorf("location %q remote %v", got[0].Location, got[0].Remote)
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _ := newSource(one(200, `[]`))
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestMalformedPayloadsAreErrors(t *testing.T) {
	bad := map[string]string{
		"not json":   `<html>`,
		"truncated":  `[{"id":"1"`,
		"object":     `{"jobs":[]}`,
		"null":       `null`,
		"number":     `1`,
		"null title": `[{"id":"a","title":null}]`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(one(200, body))
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestJobErrorsFailTheBoard(t *testing.T) {
	cases := map[string]string{
		"empty title":    `[{"id":"a","title":"  "}]`,
		"colon id":       `[{"id":"a:b","title":"T"}]`,
		"empty id":       `[{"id":"  ","title":"T"}]`,
		"duplicate":      `[{"id":"Ab","title":"T"},{"id":"ab","title":""}]`,
		"spaced dup":     `[{"id":"a","title":"T"},{"id":" a ","title":"T"}]`,
		"other host":     `[{"id":"a","title":"Ok"},{"id":"b","title":"T","absolute_url":"https://evil.test/groq/b"}]`,
		"javascript url": `[{"id":"a","title":"T","absolute_url":"javascript:alert(1)"}]`,
		"userinfo url":   `[{"id":"a","title":"T","absolute_url":"https://jobs.gem.com@evil.test/groq/a"}]`,
		"bad published":  `[{"id":"a","title":"T","first_published_at":"yesterday"}]`,
		"bad updated":    `[{"id":"a","title":"T","updated_at":"yesterday"}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(one(200, body))
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	s, d := newSource(one(404, `{"error":"not found"}`))
	if _, err := s.List(context.Background(), board); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("got %v", err)
	}
	if urls := d.URLs(); len(urls) != 1 || urls[0] != boardURL {
		t.Fatalf("urls %v", urls)
	}
}

func TestServerErrorIsAnError(t *testing.T) {
	s, _ := newSource(one(500, `[]`))
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestWrongATSAndEmptyTokenRefused(t *testing.T) {
	s, d := newSource(one(200, `[{"id":"a","title":"T"}]`))
	for _, b := range []sources.Board{
		{ATS: posting.Ashby, Token: "groq"},
		{ATS: posting.Gem, Token: ""},
		{ATS: posting.Gem, Token: "  "},
		{ATS: posting.Gem, Token: "groq:x"},
		{Token: "groq"},
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("board %+v accepted", b)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("refused boards still made %d requests", n)
	}
}

func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, d := newSource(one(200, `[{"id":"a","title":"T"}]`))
	_, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("%v %v", err, d.URLs())
	}
	ctx, cancel = context.WithCancel(context.Background())
	d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL != boardURL {
			return srctest.Reply{}, false
		}
		cancel()
		return srctest.Reply{Status: 200, Body: []byte(`[{"id":"a","title":"T"},{"id":"b","title":"T"}]`)}, true
	})
	s = &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("%v %v urls %v", got, err, d.URLs())
	}
}

func TestDotTokenDoesNoIO(t *testing.T) {
	s, d := newSource(one(200, `[]`))
	for _, tok := range []string{".", ".."} {
		if _, err := s.List(context.Background(), sources.Board{ATS: posting.Gem, Token: tok}); err == nil {
			t.Errorf("%q accepted", tok)
		}
	}
	if len(d.URLs()) != 0 {
		t.Fatalf("urls %v", d.URLs())
	}
}

func TestSuspectCap(t *testing.T) {
	for _, n := range []int{49, 51} {
		s, _ := newSource(one(200, jobsBody(n)))
		got, err := s.List(context.Background(), board)
		if err != nil || len(got) != n {
			t.Fatalf("%d posts: %d, %v", n, len(got), err)
		}
	}
	s, _ := newSource(one(200, jobsBody(50)))
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrSuspectCap) || got != nil {
		t.Fatalf("50 posts: %d, %v", len(got), err)
	}
}

func TestPostingCap(t *testing.T) {
	s, _ := newSource(one(200, jobsBody(srcutil.MaxPostings)))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != srcutil.MaxPostings {
		t.Fatalf("at the cap: %d, %v", len(got), err)
	}
	s, d := newSource(one(200, jobsBody(srcutil.MaxPostings+1)))
	got, err = s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("over the cap: %d, %v, urls %d", len(got), err, len(d.URLs()))
	}
}

func TestDescriptionCap(t *testing.T) {
	huge := strings.Repeat("word ", srcutil.MaxDescription) + "TAILMARKER"
	body := fmt.Sprintf(`[{"id":"a","title":"T","content_plain":%q,"content":"<p>TAILMARKER</p>"}]`, huge)
	s, _ := newSource(one(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	desc := got[0].Description
	if n := len(desc); n == 0 || n > srcutil.MaxDescription || strings.Contains(desc, "TAILMARKER") {
		t.Fatalf("description %d bytes, marker present %v", n, strings.Contains(desc, "TAILMARKER"))
	}
}

func TestTokenIsEscaped(t *testing.T) {
	const want = "https://api.gem.com/job_board/v0/a%2Fb/job_posts/"
	s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: []byte(`[{"id":"1","title":"T"}]`)}})
	got, err := s.List(context.Background(), sources.Board{ATS: posting.Gem, Token: "a/b", Company: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != want {
		t.Fatalf("requested %v, want %s", u, want)
	}
	if got[0].URL != "https://jobs.gem.com/a%2Fb/1" || got[0].Key != "gm:a/b:1" {
		t.Fatalf("%+v", got[0])
	}
}

func TestATS(t *testing.T) {
	if New(nil).ATS() != posting.Gem {
		t.Fatal(New(nil).ATS())
	}
}

func jobsBody(n int) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"%d","title":"T","absolute_url":"https://jobs.gem.com/groq/%d"}`, i+1, i+1)
	}
	sb.WriteByte(']')
	return sb.String()
}
