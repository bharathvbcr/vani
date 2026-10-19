package lever

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

const (
	usURL = "https://api.lever.co/v0/postings/spotify?mode=json"
	euURL = "https://api.eu.lever.co/v0/postings/spotify?mode=json"
)

var board = sources.Board{ATS: posting.Lever, Token: "spotify", Company: "Spotify"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func us(status int, body string) map[string]srctest.Reply {
	return map[string]srctest.Reply{usURL: {Status: status, Body: []byte(body)}}
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newSource(us(200, string(raw)))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		p      posting.Posting
		prefix string
	}
	want := []row{
		{posting.Posting{
			Key: "lv:spotify:92375b47-968c-4bad-83ee-c89b26b24e1d", ATS: posting.Lever, Company: "Spotify",
			Title: "Client Partner, Emerging & Scaled", Location: "São Paulo",
			Department: "Advertising Sales - Emerging and Scaled Sales",
			URL:        "https://jobs.lever.co/spotify/92375b47-968c-4bad-83ee-c89b26b24e1d",
			ApplyURL:   "https://jobs.lever.co/spotify/92375b47-968c-4bad-83ee-c89b26b24e1d/apply",
			PostedAt:   time.UnixMilli(1791551922340).UTC(), SeenAt: fixedNow,
		}, "Sell what you love."},
		{posting.Posting{
			Key: "lv:spotify:1f56d4be-4e54-441a-ad05-946b9efcc391", ATS: posting.Lever, Company: "Spotify",
			Title: "Talent Relations Manager", Location: "Los Angeles, CA", Department: "Podcast Business",
			URL:      "https://jobs.lever.co/spotify/1f56d4be-4e54-441a-ad05-946b9efcc391",
			ApplyURL: "https://jobs.lever.co/spotify/1f56d4be-4e54-441a-ad05-946b9efcc391/apply",
			PostedAt: time.UnixMilli(1791310998061).UTC(), SeenAt: fixedNow,
		}, "The Ringer is looking"},
		{posting.Posting{
			Key: "lv:spotify:4310f31c-90e1-4e8d-bdcf-dce39c145b8c", ATS: posting.Lever, Company: "Spotify",
			Title: "Associate – Audiobook Licensing & Author Partnerships", Location: "New York, NY",
			Department: "Audio Books & Gated Content", Remote: true,
			URL:      "https://jobs.lever.co/spotify/4310f31c-90e1-4e8d-bdcf-dce39c145b8c",
			ApplyURL: "https://jobs.lever.co/spotify/4310f31c-90e1-4e8d-bdcf-dce39c145b8c/apply",
			PostedAt: time.UnixMilli(1783608935950).UTC(), SeenAt: fixedNow,
		}, "The Audiobooks Mission is a team"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if !g.PostedAt.Equal(w.p.PostedAt) {
			t.Errorf("posting %d PostedAt %v, want %v", i, g.PostedAt, w.p.PostedAt)
		}
		g.PostedAt, w.p.PostedAt = time.Time{}, time.Time{}
		if g != w.p {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, w.p)
		}
		if !strings.HasPrefix(desc, w.prefix) {
			t.Errorf("posting %d description starts %.60q, want prefix %q", i, desc, w.prefix)
		}
		for _, h := range []string{"What You'll Do\n", "Who You Are\n", "Where You'll Be\n"} {
			if !strings.Contains(desc, h) {
				t.Errorf("posting %d description lacks list %q", i, h)
			}
		}
		if strings.ContainsAny(desc, "<>") || len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description carries markup or is %d bytes: %.200q", i, len(desc), desc)
		}
	}
}

func TestDescriptionIsPlainPlusReducedLists(t *testing.T) {
	body := `[{"id":"p1","text":"T","hostedUrl":"https://x.test/p1","descriptionPlain":"Intro line.\n",
	"lists":[{"text":"Requirements","content":"<li>Go &amp; Rust</li><li>SQL<ul><li>joins</li></ul></li>"},
	{"text":"Perks","content":"<li>Tea</li>"}]}]`
	s, _ := newSource(us(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	want := "Intro line.\n\nRequirements\nGo & Rust\nSQL\njoins\n\nPerks\nTea"
	if got[0].Description != want {
		t.Fatalf("got %q\nwant %q", got[0].Description, want)
	}
}

func TestRemoteOnlyForWorkplaceTypeRemote(t *testing.T) {
	for wt, want := range map[string]bool{"remote": true, "hybrid": false, "onsite": false, "on-site": false, "unspecified": false, "": false, "Remote": false} {
		body := fmt.Sprintf(`[{"id":"p1","text":"T","hostedUrl":"https://x.test/p1","workplaceType":%q}]`, wt)
		s, _ := newSource(us(200, body))
		got, err := s.List(context.Background(), board)
		if err != nil || got[0].Remote != want {
			t.Errorf("workplaceType %q: remote=%v err=%v, want %v", wt, got[0].Remote, err, want)
		}
	}
}

func TestEURegionHost(t *testing.T) {
	s, d := newSource(map[string]srctest.Reply{euURL: {Status: 200, Body: []byte(`[]`)}})
	eu := board
	eu.Region = "eu"
	if _, err := s.List(context.Background(), eu); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != euURL {
		t.Fatalf("requested %v, want %s", u, euURL)
	}
	// No region means the US host; an unrecognised region is refused rather
	// than silently routed to the US host.
	s, d = newSource(us(200, `[]`))
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != usURL {
		t.Fatalf("requested %v, want %s", u, usURL)
	}
	bad := board
	bad.Region = "apac"
	if _, err := s.List(context.Background(), bad); err == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestNotFound(t *testing.T) {
	s, _ := newSource(us(404, `{"ok":false,"error":"Document not found"}`))
	if _, err := s.List(context.Background(), board); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("got %v, want ErrBoardNotFound", err)
	}
}

func TestServerErrorIsAnError(t *testing.T) {
	s, _ := newSource(us(500, `[]`))
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestMalformedPayloadsAreErrors(t *testing.T) {
	ok := `"hostedUrl":"https://x.test/1"`
	bad := map[string]string{
		"not json":            `<html>`,
		"truncated":           `[{"id":"1"`,
		"object not array":    `{"ok":false,"error":"rate limited"}`,
		"null":                `null`,
		"empty body":          ``,
		"wrong element type":  `[1]`,
		"missing id":          `[{"text":"T",` + ok + `}]`,
		"id with separator":   `[{"id":"a:b","text":"T",` + ok + `}]`,
		"missing text":        `[{"id":"1",` + ok + `}]`,
		"missing hostedUrl":   `[{"id":"1","text":"T"}]`,
		"file url":            `[{"id":"1","text":"T","hostedUrl":"file:///etc/passwd"}]`,
		"javascript applyUrl": `[{"id":"1","text":"T",` + ok + `,"applyUrl":"javascript:x"}]`,
		"createdAt string":    `[{"id":"1","text":"T",` + ok + `,"createdAt":"yesterday"}]`,
		"createdAt float":     `[{"id":"1","text":"T",` + ok + `,"createdAt":1.5}]`,
		"lists not array":     `[{"id":"1","text":"T",` + ok + `,"lists":"x"}]`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(us(200, body))
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %d postings", len(got))
			}
		})
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _ := newSource(us(200, `[]`))
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v: want empty non-nil list and no error", got, err)
	}
}

func postsBody(n int) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"p%d","text":"T","hostedUrl":"https://x.test/%d"}`, i, i)
	}
	sb.WriteByte(']')
	return sb.String()
}

func TestPostingCap(t *testing.T) {
	s, _ := newSource(us(200, postsBody(srcutil.MaxPostings)))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != srcutil.MaxPostings {
		t.Fatalf("at the cap: %d postings, %v", len(got), err)
	}
	s, _ = newSource(us(200, postsBody(srcutil.MaxPostings+1)))
	got, err = s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Fatalf("over the cap: %d postings, %v", len(got), err)
	}
}

func TestDescriptionCap(t *testing.T) {
	plain := strings.Repeat("lorem ", 100<<10/6)
	list := "<li>" + strings.Repeat("ipsum ", 100<<10/6) + "</li>"
	body := fmt.Sprintf(`[{"id":"p","text":"T","hostedUrl":"https://x.test/p","descriptionPlain":%q,"lists":[{"text":"L","content":%q}]}]`, plain, list)
	s, _ := newSource(us(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got[0].Description); n == 0 || n > srcutil.MaxDescription {
		t.Fatalf("description %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
}

func TestWrongATSAndEmptyTokenRefused(t *testing.T) {
	s, d := newSource(us(200, `[]`))
	for _, b := range []sources.Board{
		{ATS: posting.Greenhouse, Token: "spotify"},
		{ATS: posting.Lever, Token: ""},
		{ATS: posting.Lever, Token: " \t"},
		{Token: "spotify"},
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
	const want = "https://api.lever.co/v0/postings/a%2Fb%3Fmode=x%23y%20z?mode=json"
	s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: []byte(`[]`)}})
	if _, err := s.List(context.Background(), sources.Board{ATS: posting.Lever, Token: "a/b?mode=x#y z"}); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != want {
		t.Fatalf("requested %v, want %s", u, want)
	}
}

func TestNewAndATS(t *testing.T) {
	src := New(nil)
	if src.ATS() != posting.Lever {
		t.Fatal(src.ATS())
	}
	if _, err := src.List(context.Background(), board); err == nil {
		t.Fatal("nil doer must error, not panic")
	}
}
