package rippling

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

const listURL = "https://api.rippling.com/platform/api/ats/v1/board/acme/jobs"

var board = sources.Board{ATS: posting.Rippling, Token: "acme", Company: "Board Co"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func detailURL(id string) string { return listURL + "/" + id }

func listBody(ids ...string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"uuid":%q,"name":"List %s","url":"https://jobs.example/%s","department":{"label":"List dept"},"workLocation":{"label":"List place"}}`, id, id, id)
	}
	b.WriteByte(']')
	return b.String()
}

func detailBody(id, extra string) string {
	return fmt.Sprintf(`{"uuid":%q,"name":"Engineer","url":"https://jobs.example/%s","createdOn":"2026-05-06T13:32:50.826000-07:00","companyName":"Acme Robotics","workLocations":["Austin, TX"],"department":{"name":"Engineering"},"description":{"role":"<p>Build &amp; ship</p><script>x()</script>","company":"<p>About the company</p>"}%s}`, id, id, extra)
}

func replies(ids ...string) map[string]srctest.Reply {
	r := map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(listBody(ids...))}}
	for _, id := range ids {
		r[detailURL(id)] = srctest.Reply{Status: 200, Body: []byte(detailBody(id, ""))}
	}
	return r
}

func TestListAndDetail(t *testing.T) {
	s, d := newSource(replies("job1"))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.URLs()) != 2 || d.URLs()[0] != listURL || d.URLs()[1] != detailURL("job1") {
		t.Fatalf("urls %v", d.URLs())
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	p := got[0]
	wantPosted := time.Date(2026, 5, 6, 20, 32, 50, 826_000_000, time.UTC)
	if !p.PostedAt.Equal(wantPosted) {
		t.Errorf("PostedAt %v", p.PostedAt)
	}
	p.PostedAt = time.Time{}
	want := posting.Posting{
		Key: "rp:acme:job1", ATS: posting.Rippling, Company: "Acme Robotics",
		Title: "Engineer", Location: "Austin, TX", Department: "Engineering",
		URL: "https://jobs.example/job1", Description: "Build & ship", SeenAt: fixedNow,
	}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
	if !strings.HasPrefix(string(got[0].Key), board.KeyPrefix()) {
		t.Errorf("key %q prefix %q", got[0].Key, board.KeyPrefix())
	}
}

func TestRoleFallsBackToCompanyBlurb(t *testing.T) {
	r := replies("job1")
	r[detailURL("job1")] = srctest.Reply{Status: 200, Body: []byte(`{"uuid":"job1","name":"Engineer","url":"https://jobs.example/job1","description":{"role":"  ","company":"<p>About</p>"}}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil || got[0].Description != "About" {
		t.Fatalf("%v %q", err, got[0].Description)
	}
}

func TestUnlistedIsSkippedWithoutValidatingIt(t *testing.T) {
	r := replies("good")
	r[listURL] = srctest.Reply{Status: 200, Body: []byte(listBody("bad", "good"))}
	r[detailURL("bad")] = srctest.Reply{Status: 200, Body: []byte(`{"uuid":"bad","unlistedFromSearch":true}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Key != "rp:acme:good" {
		t.Fatalf("%d %v", len(got), err)
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(`[]`)}})
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 404, Body: []byte(`{}`)}})
	_, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) {
		t.Fatal(err)
	}
}

func TestDetailNotFoundFailsTheBoardAndIsNotAMissingBoard(t *testing.T) {
	r := replies("job1")
	r[detailURL("job1")] = srctest.Reply{Status: 404, Body: []byte(`{}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestServerErrorIsAnError(t *testing.T) {
	s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 500, Body: []byte(`[]`)}})
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestDetailServerErrorFailsTheBoard(t *testing.T) {
	r := replies("a", "b")
	r[detailURL("a")] = srctest.Reply{Status: 500, Body: []byte(`{}`)}
	s, d := newSource(r)
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if len(d.URLs()) != 2 {
		t.Fatalf("stopped after the failed detail, urls %v", d.URLs())
	}
}

func TestMalformedPayloadsAreErrors(t *testing.T) {
	bad := map[string]string{
		"not json":   `<html>`,
		"object":     `{}`,
		"null":       `null`,
		"empty body": ``,
		"truncated":  `[{"uuid":"a"`,
		"trailing":   `[] x`,
		"job type":   `[1]`,
		"empty uuid": `[{"name":"T"}]`,
		"colon uuid": `[{"uuid":"a:b","name":"T"}]`,
		"duplicate":  listBody("a", "A"),
		"locations":  "",
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			replies := map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(body)}}
			if name == "locations" {
				replies[listURL] = srctest.Reply{Status: 200, Body: []byte(listBody("job1"))}
				replies[detailURL("job1")] = srctest.Reply{Status: 200, Body: []byte(`{"uuid":"job1","name":"T","url":"https://jobs.example/j","workLocations":{"label":"Austin"}}`)}
			}
			s, _ := newSource(replies)
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %d", len(got))
			}
		})
	}
}

func TestDetailProblemsFailTheJob(t *testing.T) {
	cases := map[string]string{
		"empty title":    `{"uuid":"job1","url":"https://jobs.example/j"}`,
		"empty url":      `{"uuid":"job1","name":"T"}`,
		"javascript url": `{"uuid":"job1","name":"T","url":"javascript:alert(1)"}`,
		"userinfo url":   `{"uuid":"job1","name":"T","url":"https://user@evil.test/j"}`,
		"bad time":       `{"uuid":"job1","name":"T","url":"https://jobs.example/j","createdOn":"yesterday"}`,
		"wrong uuid":     `{"uuid":"other","name":"T","url":"https://jobs.example/j"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := map[string]srctest.Reply{
				listURL:           {Status: 200, Body: []byte(`[{"uuid":"job1"}]`)},
				detailURL("job1"): {Status: 200, Body: []byte(body)},
			}
			s, _ := newSource(r)
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %+v", got)
			}
		})
	}
}

func TestCompanyAndLocationFallBackToTheList(t *testing.T) {
	r := map[string]srctest.Reply{
		listURL:           {Status: 200, Body: []byte(listBody("job1"))},
		detailURL("job1"): {Status: 200, Body: []byte(`{"uuid":"job1","name":"Engineer","workLocations":[]}`)},
	}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Company != "Board Co" || got[0].Location != "List place" || got[0].URL != "https://jobs.example/job1" || got[0].Department != "List dept" {
		t.Fatalf("%+v", got[0])
	}
}

func TestPostingCap(t *testing.T) {
	over := listN(srcutil.MaxPostings + 1)
	s, d := newSource(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(over)}})
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("%d %v urls %d", len(got), err, len(d.URLs()))
	}
}

func TestCanceledBeforeTheListDoesNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, d := newSource(replies("job1"))
	_, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("%v urls %v", err, d.URLs())
	}
}

func TestCanceledDuringDetailsReturnsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == listURL {
			return srctest.Reply{Status: 200, Body: []byte(listBody("a", "b"))}, true
		}
		if r.URL == detailURL("a") {
			cancel()
			return srctest.Reply{Status: 200, Body: []byte(detailBody("a", ""))}, true
		}
		return srctest.Reply{}, false
	})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if urls := d.URLs(); len(urls) != 2 || urls[1] != detailURL("a") {
		t.Fatalf("urls %v", urls)
	}
}

func TestDetailBodyCap(t *testing.T) {
	old := maxDetailBody
	maxDetailBody = 32
	t.Cleanup(func() { maxDetailBody = old })
	r := replies("job1")
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestNilDoerAndWrongBoard(t *testing.T) {
	if _, err := New(nil).List(context.Background(), board); err == nil {
		t.Fatal("nil doer")
	}
	s, _ := newSource(replies("job1"))
	if _, err := s.List(context.Background(), sources.Board{ATS: posting.Ashby, Token: "acme"}); err == nil {
		t.Fatal("wrong ats")
	}
}

func TestNoDoerForAnUnexpectedHost(t *testing.T) {
	s, _ := newSource(nil)
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func listN(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("j%d", i+1)
	}
	return listBody(ids...)
}

func TestATS(t *testing.T) {
	if New(nil).ATS() != posting.Rippling {
		t.Fatal(New(nil).ATS())
	}
}
