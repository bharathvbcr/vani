package workable

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

const listURL = "https://apply.workable.com/api/v1/widget/accounts/hf"

var board = sources.Board{ATS: posting.Workable, Token: "hf", Company: "Hugging Face"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func detailURL(id string) string {
	return "https://apply.workable.com/api/v2/accounts/hf/jobs/" + id
}

func widget(jobs string) string {
	return `{"name":"Hugging Face","jobs":[` + jobs + `]}`
}

func job(id string) string {
	return fmt.Sprintf(`{"title":"List %s","shortcode":%q,"url":"https://apply.workable.com/j/%s","application_url":"https://apply.workable.com/j/%s/apply","department":"Widget","telecommuting":false,"city":"List city","country":"France"}`, id, id, id, id)
}

func detailJSON(id, extra string) string {
	return fmt.Sprintf(`{"shortcode":%q,"title":"ML Engineer","remote":true,"workplace":"remote","state":"published","published":"2026-05-29T00:00:00.000Z","department":["Open Source","ML"],"location":{"city":"Paris","region":"Île-de-France","country":"France"},"locations":[{"city":"Hidden","country":"France","hidden":true},{"city":"Paris","region":"Île-de-France","country":"France","hidden":false}],"description":"<p>Build &amp; models</p>","requirements":"<ul><li>Go</li></ul>","benefits":""%s}`, id, extra)
}

func replies(ids ...string) map[string]srctest.Reply {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = job(id)
	}
	r := map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(widget(strings.Join(parts, ",")))}}
	for _, id := range ids {
		r[detailURL(id)] = srctest.Reply{Status: 200, Body: []byte(detailJSON(id, ""))}
	}
	return r
}

func TestListAndDetail(t *testing.T) {
	s, d := newSource(replies("ABC"))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.URLs()) != 2 || d.URLs()[1] != detailURL("ABC") {
		t.Fatalf("urls %v", d.URLs())
	}
	p := got[0]
	if !p.PostedAt.Equal(time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PostedAt %v", p.PostedAt)
	}
	p.PostedAt = time.Time{}
	want := posting.Posting{
		Key: "wk:hf:abc", ATS: posting.Workable, Company: "Hugging Face",
		Title: "ML Engineer", Location: "Paris, Île-de-France, France", Remote: true,
		Department:  "Open Source, ML",
		URL:         "https://apply.workable.com/j/ABC",
		ApplyURL:    "https://apply.workable.com/j/ABC/apply",
		Description: "Build & models\n\nGo", SeenAt: fixedNow,
	}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestInternalAndUnpublishedAreSkipped(t *testing.T) {
	r := replies("pub")
	r[listURL] = srctest.Reply{Status: 200, Body: []byte(widget(job("in") + "," + job("draft") + "," + job("pub")))}
	r[detailURL("in")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"in","isInternal":true}`)}
	r[detailURL("draft")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"draft","state":"draft"}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Key != "wk:hf:pub" {
		t.Fatalf("%d %v", len(got), err)
	}
}

func TestEmptyStateIsListedAndOnSiteIsNotRemote(t *testing.T) {
	r := replies("ABC")
	r[detailURL("ABC")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"ABC","title":"T","workplace":"on-site","state":"","department":"People"}`)}
	r[listURL] = srctest.Reply{Status: 200, Body: []byte(widget(`{"shortcode":"ABC","title":"T","url":"https://apply.workable.com/j/ABC","telecommuting":true,"department":"Widget","city":"Paris"}`))}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Remote || got[0].Department != "People" || got[0].Location != "Paris" {
		t.Fatalf("%+v", got[0])
	}
}

func TestWorkplaceRemoteAndDepartmentString(t *testing.T) {
	r := replies("ABC")
	r[detailURL("ABC")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"ABC","title":"T","remote":false,"workplace":"hybrid, remote","department":"Research"}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil || !got[0].Remote || got[0].Department != "Research" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestHiddenLocationFallsThrough(t *testing.T) {
	r := replies("ABC")
	r[detailURL("ABC")] = srctest.Reply{Status: 200, Body: []byte(`{"shortcode":"ABC","title":"T","locations":[{"city":"Secret","hidden":true}],"location":{"country":"France"}}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil || got[0].Location != "France" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	body := `{"name":"HF","jobs":[]}`
	s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(body)}})
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 404, Body: []byte("{}")}})
	_, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) {
		t.Fatal(err)
	}
}

func TestDetailNotFoundIsNotAMissingBoard(t *testing.T) {
	r := replies("ABC")
	r[detailURL("ABC")] = srctest.Reply{Status: 404, Body: []byte(`{}`)}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestServerErrorAndDependencyDown(t *testing.T) {
	body := `{"jobs":[]}`
	s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 500, Body: []byte(body)}})
	if _, err := s.List(context.Background(), board); err == nil || errors.Is(err, ErrBoardNotFound) {
		t.Fatal(err)
	}
	s, _ = newSource(nil)
	if got, err := s.List(context.Background(), board); err == nil || got != nil {
		t.Fatal("down")
	}
	if _, err := New(nil).List(context.Background(), board); err == nil {
		t.Fatal("nil doer")
	}
}

func TestMalformedPayloadsAreErrors(t *testing.T) {
	bad := map[string]string{
		"not json":   `<html>`,
		"array":      `[]`,
		"no jobs":    `{"name":"HF"}`,
		"null jobs":  `{"jobs":null}`,
		"empty":      ``,
		"truncated":  `{"jobs":[`,
		"job type":   `{"jobs":[1]}`,
		"empty code": `{"jobs":[{"title":"T"}]}`,
		"colon code": `{"jobs":[{"shortcode":"a:b","title":"T"}]}`,
		"duplicate":  widget(job("Ab") + "," + job("ab")),
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(body)}})
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %d", len(got))
			}
		})
	}
}

func TestDetailProblemsFailTheJob(t *testing.T) {
	cases := map[string]struct{ list, detail string }{
		"empty title":    {`{"shortcode":"ABC","url":"https://apply.workable.com/j/ABC"}`, `{"shortcode":"ABC"}`},
		"empty url":      {`{"shortcode":"ABC","title":"T"}`, `{"shortcode":"ABC","title":"T"}`},
		"bad url":        {`{"shortcode":"ABC","title":"T","url":"javascript:x"}`, `{"shortcode":"ABC","title":"T"}`},
		"bad apply":      {`{"shortcode":"ABC","title":"T","url":"https://apply.workable.com/j/ABC","application_url":"data:text/html,x"}`, `{"shortcode":"ABC","title":"T"}`},
		"bad published":  {job("ABC"), `{"shortcode":"ABC","title":"T","published":"yesterday"}`},
		"wrong code":     {job("ABC"), `{"shortcode":"OTHER","title":"T"}`},
		"bad department": {job("ABC"), `{"shortcode":"ABC","title":"T","department":1}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := map[string]srctest.Reply{
				listURL:          {Status: 200, Body: []byte(widget(tc.list))},
				detailURL("ABC"): {Status: 200, Body: []byte(tc.detail)},
			}
			s, _ := newSource(r)
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %+v", got)
			}
		})
	}
}

func TestAccountNameFillsAnEmptyCompany(t *testing.T) {
	s, _ := newSource(replies("ABC"))
	got, err := s.List(context.Background(), sources.Board{ATS: posting.Workable, Token: "hf"})
	if err != nil || got[0].Company != "Hugging Face" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestPostingCapDoesNotFetchDetails(t *testing.T) {
	parts := make([]string, srcutil.MaxPostings+1)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"shortcode":"j%d","title":"T","url":"https://apply.workable.com/j/j%d"}`, i, i)
	}
	s, d := newSource(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(widget(strings.Join(parts, ",")))}})
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("%v urls %d", err, len(d.URLs()))
	}
}

func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, d := newSource(replies("ABC"))
	_, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("%v %v", err, d.URLs())
	}
	ctx, cancel = context.WithCancel(context.Background())
	d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == listURL {
			return srctest.Reply{Status: 200, Body: []byte(widget(job("a") + "," + job("b")))}, true
		}
		if r.URL == detailURL("a") {
			cancel()
			return srctest.Reply{Status: 200, Body: []byte(detailJSON("a", ""))}, true
		}
		return srctest.Reply{}, false
	})
	s = &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || got != nil || len(d.URLs()) != 2 {
		t.Fatalf("%v %v urls %v", got, err, d.URLs())
	}
}

func TestDetailBodyCap(t *testing.T) {
	old := maxDetailBody
	maxDetailBody = 16
	t.Cleanup(func() { maxDetailBody = old })
	s, _ := newSource(replies("ABC"))
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestDescriptionCap(t *testing.T) {
	huge := strings.Repeat("word ", srcutil.MaxDescription)
	r := replies("ABC")
	r[detailURL("ABC")] = srctest.Reply{Status: 200, Body: []byte(fmt.Sprintf(`{"shortcode":"ABC","title":"T","description":%q,"requirements":"<p>tail</p>"}`, huge))}
	s, _ := newSource(r)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Description) > srcutil.MaxDescription {
		t.Fatalf("description %d", len(got[0].Description))
	}
}

func TestATS(t *testing.T) {
	if New(nil).ATS() != posting.Workable {
		t.Fatal(New(nil).ATS())
	}
}
