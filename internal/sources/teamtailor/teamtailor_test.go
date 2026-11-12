package teamtailor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const listURL = "https://acme.teamtailor.com/jobs.json"

func board() sources.Board {
	return sources.Board{ATS: posting.Teamtailor, Token: "acme", Company: "Acme"}
}

func one(body string) *srctest.Doer {
	return srctest.New(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(body)}})
}

func TestListReadsTheLiveShape(t *testing.T) {
	body := `{
		"version":"https://jsonfeed.org/version/1.1",
		"title":"Polestar",
		"feed_url":"https://acme.teamtailor.com/jobs.json",
		"items":[{
			"id":"2370D02D-914F-44E0-ABF2-8EC6EB860415",
			"title":"<b>Regional</b>  HR partner",
			"url":"https://acme.teamtailor.com/jobs/8690571-regional-hr-business-partner",
			"content_html":"<p>Same body</p>",
			"date_published":"2026-11-09T13:43:21+01:00",
			"_jobposting":{
				"@type":"JobPosting",
				"datePosted":"2026-11-09T13:43:21+01:00",
				"description":"<p>Body of the role</p><script>alert(1)</script>",
				"employmentType":"FULL_TIME",
				"directApply":true,
				"hiringOrganization":{"name":"Polestar","sameAs":"https://acme.teamtailor.com"},
				"identifier":{"name":"Polestar","value":"8690571"},
				"jobLocation":[{"@type":"Place","address":{"@type":"PostalAddress","streetAddress":null,"addressLocality":"Gothenburg","addressRegion":"VG","postalCode":"41756","addressCountry":"SE"}}]
			}
		}]
	}`
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p := got[0]
	if p.Key != "tm:acme:2370d02d-914f-44e0-abf2-8ec6eb860415" || p.ATS != posting.Teamtailor {
		t.Errorf("key %s ats %s", p.Key, p.ATS)
	}
	if p.Title != "Regional HR partner" || p.Company != "Acme" || p.Location != "Gothenburg" || p.Remote {
		t.Errorf("posting %+v", p)
	}
	if p.URL != "https://acme.teamtailor.com/jobs/8690571-regional-hr-business-partner" {
		t.Errorf("url %s", p.URL)
	}
	want := time.Date(2026, 11, 9, 12, 43, 21, 0, time.UTC)
	if !p.PostedAt.Equal(want) {
		t.Errorf("posted %s, want %s", p.PostedAt, want)
	}
	if !strings.Contains(p.Description, "Body of the role") || strings.Contains(p.Description, "Same body") || strings.Contains(p.Description, "alert") || strings.ContainsAny(p.Description, "<>") {
		t.Errorf("description %q", p.Description)
	}
	if !strings.Contains(p.Description, "Role: full-time") {
		t.Errorf("description lacks the role line: %q", p.Description)
	}
}

func TestEmploymentAndLocationShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		job    string
		loc    string
		remote bool
	}{
		"type array and one place": {
			job:    `"employmentType":["PART_TIME","CONTRACTOR"],"jobLocation":{"name":"Berlin","address":{"addressLocality":"Berlin"}}`,
			loc:    "Berlin",
			remote: false,
		},
		"string location": {
			job:    `"jobLocation":"Remote"`,
			loc:    "",
			remote: true,
		},
		"locality says remote": {
			job:    `"jobLocation":[{"address":{"addressLocality":"Remote"}}]`,
			loc:    "Remote",
			remote: true,
		},
		"title does not mark remote": {
			job:    `"jobLocation":[{"address":{"addressLocality":"Austin"}}]`,
			loc:    "Austin",
			remote: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			title := "On-site analyst"
			if name == "title does not mark remote" {
				title = "Remote analyst"
			}
			body := fmt.Sprintf(`{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[{"id":"1","title":%q,"url":"https://acme.teamtailor.com/jobs/1","_jobposting":{%s}}]}`, title, tc.job)
			got, err := New(one(body)).List(context.Background(), board())
			if err != nil || len(got) != 1 {
				t.Fatalf("got %+v, %v", got, err)
			}
			if got[0].Location != tc.loc || got[0].Remote != tc.remote {
				t.Errorf("location %q remote %v, want %q %v", got[0].Location, got[0].Remote, tc.loc, tc.remote)
			}
			if name == "type array and one place" && !strings.Contains(got[0].Description, "Role: part-time, contract") {
				t.Errorf("description %q", got[0].Description)
			}
		})
	}
}

func TestCompanyFallbackAndSameAs(t *testing.T) {
	item := `{"id":"1","title":"Role","url":"https://acme.teamtailor.com/jobs/1","_jobposting":{"hiringOrganization":{"name":"Org","sameAs":"https://acme.teamtailor.com"}}}`
	b := sources.Board{ATS: posting.Teamtailor, Token: "acme"}
	got, err := New(one(`{"version":"https://jsonfeed.org/version/1","title":"Feed Co","items":[`+item+`]}`)).List(context.Background(), b)
	if err != nil || got[0].Company != "Feed Co" {
		t.Fatalf("title fallback %+v, %v", got, err)
	}
	got, err = New(one(`{"version":"https://jsonfeed.org/version/1.1","title":"","items":[`+item+`]}`)).List(context.Background(), b)
	if err != nil || got[0].Company != "Org" {
		t.Fatalf("org fallback %+v, %v", got, err)
	}
	for _, same := range []string{
		"https://evil.test/",
		"http://acme.teamtailor.com/",
		"https://user:pass@acme.teamtailor.com/",
		"https://acme.teamtailor.com.evil.test/",
	} {
		body := fmt.Sprintf(`{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[{"id":"1","title":"Role","url":"https://acme.teamtailor.com/jobs/1","_jobposting":{"hiringOrganization":{"sameAs":%q}}}]}`, same)
		got, err := New(one(body)).List(context.Background(), board())
		if err == nil || got != nil {
			t.Fatalf("sameAs %s: got %+v, %v", same, got, err)
		}
	}
}

func TestBadItemFailsTheBoard(t *testing.T) {
	for name, items := range map[string]string{
		"off host":    `{"id":"1","title":"Role","url":"https://evil.test/jobs/1"}`,
		"userinfo":    `{"id":"1","title":"Role","url":"https://u:p@acme.teamtailor.com/jobs/1"}`,
		"http":        `{"id":"1","title":"Role","url":"http://acme.teamtailor.com/jobs/1"}`,
		"empty title": `{"id":"1","title":"<br>","url":"https://acme.teamtailor.com/jobs/1"}`,
		"bad date":    `{"id":"1","title":"Role","url":"https://acme.teamtailor.com/jobs/1","date_published":"yesterday"}`,
		"bad posted":  `{"id":"1","title":"Role","url":"https://acme.teamtailor.com/jobs/1","_jobposting":{"datePosted":"nope","description":"ok"}}`,
		"colon id":    `{"id":"a:b","title":"Role","url":"https://acme.teamtailor.com/jobs/1"}`,
		"bad place":   `{"id":"1","title":"Role","url":"https://acme.teamtailor.com/jobs/1","_jobposting":{"jobLocation":1}}`,
		"bad type":    `{"id":"1","title":"Role","url":"https://acme.teamtailor.com/jobs/1","_jobposting":{"employmentType":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[` + items + `]}`
			got, err := New(one(body)).List(context.Background(), board())
			if err == nil || got != nil || errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestFeedShape(t *testing.T) {
	for name, body := range map[string]string{
		"no version":    `{"title":"Acme","items":[]}`,
		"other version": `{"version":"https://example.com/feed","items":[]}`,
		"items null":    `{"version":"https://jsonfeed.org/version/1.1","items":null}`,
		"items missing": `{"version":"https://jsonfeed.org/version/1.1"}`,
		"html":          `<html></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := New(one(body)).List(context.Background(), board())
			if err == nil || got != nil {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
	got, err := New(one(`{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[]}`)).List(context.Background(), board())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty feed got %#v, %v", got, err)
	}
}

func TestNextURLIsFollowedOnlyOnTheTenant(t *testing.T) {
	page2 := "https://acme.teamtailor.com/jobs.json?page=2&per_page=5"
	d := srctest.New(map[string]srctest.Reply{
		listURL: {Status: 200, Body: []byte(feedPage("1", "Role 1", page2))},
		page2:   {Status: 200, Body: []byte(feedPage("2", "Role 2", ""))},
	})
	got, err := New(d).List(context.Background(), board())
	if err != nil || len(got) != 2 || got[0].Key != "tm:acme:1" || got[1].Key != "tm:acme:2" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if strings.Join(d.URLs(), " ") != listURL+" "+page2 {
		t.Errorf("urls %v", d.URLs())
	}
}

func TestEmptySecondPageEndsTheWalk(t *testing.T) {
	page2 := "https://acme.teamtailor.com/jobs.json?page=2"
	d := srctest.New(map[string]srctest.Reply{
		listURL: {Status: 200, Body: []byte(feedPage("1", "Role", page2))},
		page2:   {Status: 200, Body: []byte(`{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[]}`)},
	})
	got, err := New(d).List(context.Background(), board())
	if err != nil || len(got) != 1 || len(d.URLs()) != 2 {
		t.Fatalf("got %+v, %v, urls %v", got, err, d.URLs())
	}
}

func TestBadNextURLIsNotRequested(t *testing.T) {
	for name, next := range map[string]string{
		"off host":    "https://evil.test/jobs.json?page=2",
		"http":        "http://acme.teamtailor.com/jobs.json?page=2",
		"userinfo":    "https://u:p@acme.teamtailor.com/jobs.json?page=2",
		"wrong path":  "https://acme.teamtailor.com/jobs?page=2",
		"extra query": "https://acme.teamtailor.com/jobs.json?page=2&x=1",
		"port":        "https://acme.teamtailor.com:8443/jobs.json",
		"fragment":    "https://acme.teamtailor.com/jobs.json#page",
		"too long":    "https://acme.teamtailor.com/jobs.json?page=" + strings.Repeat("1", maxPageURL),
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				if r.URL != listURL {
					return srctest.Reply{}, false
				}
				return srctest.Reply{Status: 200, Body: []byte(feedPage("1", "Role", next))}, true
			})
			got, err := New(d).List(context.Background(), board())
			if err == nil || got != nil {
				t.Fatalf("got %+v, %v", got, err)
			}
			if urls := d.URLs(); len(urls) != 1 || urls[0] != listURL {
				t.Errorf("requested %v", urls)
			}
		})
	}
}

func TestRepeatedNextURLIsACycle(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL != listURL {
			return srctest.Reply{}, false
		}
		return srctest.Reply{Status: 200, Body: []byte(feedPage("1", "Role", listURL))}, true
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("got %+v, %v, urls %v", got, err, d.URLs())
	}
}

func TestQueryOrderDoesNotResetTheCycle(t *testing.T) {
	first := listURL + "?per_page=5&page=2"
	second := listURL + "?page=2&per_page=5"
	d := srctest.New(map[string]srctest.Reply{
		listURL: {Status: 200, Body: []byte(feedPage("1", "Role", first))},
		first:   {Status: 200, Body: []byte(feedPage("2", "Role 2", second))},
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if urls := d.URLs(); len(urls) != 2 || urls[1] != first {
		t.Errorf("urls %v", urls)
	}
}

func TestFullCapWithAFurtherPageIsAnError(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[`)
	for i := 0; i < srcutil.MaxPostings; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"%d","title":"Role %d","url":"https://acme.teamtailor.com/jobs/%d"}`, i, i, i)
	}
	b.WriteString(`],"next_url":"https://acme.teamtailor.com/jobs.json?page=2"}`)
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL != listURL {
			return srctest.Reply{}, false
		}
		return srctest.Reply{Status: 200, Body: []byte(b.String())}, true
	})
	got, err := New(d).List(context.Background(), board())
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("got %d postings, %v, urls %d", len(got), err, len(d.URLs()))
	}
}

func TestPageCapStopsBeforeTheNextFetch(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != "acme.teamtailor.com" || u.Path != "/jobs.json" {
			return srctest.Reply{}, false
		}
		page := u.Query().Get("page")
		if page == "" {
			page = "1"
		}
		var n int
		if _, err := fmt.Sscan(page, &n); err != nil || n < 1 || n > maxFeedPages+1 {
			return srctest.Reply{}, false
		}
		next := fmt.Sprintf("https://acme.teamtailor.com/jobs.json?page=%d", n+1)
		return srctest.Reply{Status: 200, Body: []byte(feedPage(fmt.Sprint(n), "Role "+page, next))}, true
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if len(d.URLs()) != maxFeedPages {
		t.Fatalf("%d requests, want %d", len(d.URLs()), maxFeedPages)
	}
	if maxFeedPages*100 < srcutil.MaxPostings {
		t.Fatalf("%d pages of 100 cannot list a legal board of %d", maxFeedPages, srcutil.MaxPostings)
	}
	for _, u := range d.URLs() {
		if strings.Contains(u, fmt.Sprintf("page=%d", maxFeedPages+1)) {
			t.Errorf("fetched past the cap: %s", u)
		}
	}
}

func TestCancelledContextMakesNoRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := one(`{"version":"https://jsonfeed.org/version/1.1","items":[]}`)
	got, err := New(d).List(ctx, board())
	if !errors.Is(err, context.Canceled) || got != nil || len(d.URLs()) != 0 {
		t.Fatalf("got %+v, %v, urls %v", got, err, d.URLs())
	}
}

func TestDependencyDownIsAnError(t *testing.T) {
	d := &errDoer{}
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

type errDoer struct{}

func (errDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}

func feedPage(id, title, next string) string {
	item := fmt.Sprintf(`{"id":%q,"title":%q,"url":"https://acme.teamtailor.com/jobs/%s","content_html":"<p>Body %s</p>"}`, id, title, id, id)
	if next == "" {
		return `{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[` + item + `]}`
	}
	return `{"version":"https://jsonfeed.org/version/1.1","title":"Acme","items":[` + item + `],"next_url":` + fmt.Sprintf("%q", next) + `}`
}
