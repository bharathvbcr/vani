package hirehive

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

const firstURL = "https://amcsgroup.hirehive.com/api/v2/jobs?page=1&page_size=100"

func board() sources.Board {
	return sources.Board{ATS: posting.HireHive, Token: "amcsgroup", Company: "AMCS"}
}

func one(body string) *srctest.Doer {
	return srctest.New(map[string]srctest.Reply{firstURL: {Status: 200, Body: []byte(body)}})
}

func wrap(n int, items string) string {
	return fmt.Sprintf(`{"meta":{"page":1,"page_size":100,"total_items":%d,"has_next_page":false},"links":{"next":"https://evil.test/steal"},"items":[%s]}`, n, items)
}

func TestListReadsTheLiveShape(t *testing.T) {
	// Field names and the fractional published_date are from
	// amcsgroup.hirehive.com on 2026-11-09. The body is a stand-in.
	body := wrap(1, `{
		"id":"job_zo9SLD",
		"title":"<b>Enterprise</b>  Account Executive",
		"location":"Remote, US",
		"state_code":"MA",
		"country":{"name":"United States","code":"US"},
		"salary":null,
		"description":{"html":"<p>Body of the role</p><script>alert(1)</script>","text":"Other body"},
		"category":null,
		"type":{"type":"FullTime","name":"Full Time"},
		"experience":{"type":"MidLevel","name":"Mid Level"},
		"language":{"name":"English","code":"en-US"},
		"published_date":"2026-11-09T15:07:56.733Z",
		"created_date":"2026-11-09T15:00:00Z",
		"hosted_url":"https://amcsgroup.hirehive.com/enterprise-account-executive-remote-us-zo9SLD",
		"compensation_tiers":[{"label":null,"type":"none","interval":"none","currency_code":null,"min_value":null,"max_value":null}]
	}`)
	d := one(body)
	got, err := New(d).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p := got[0]
	if p.Key != "hv:amcsgroup:job_zo9sld" || p.ATS != posting.HireHive {
		t.Errorf("key %s ats %s", p.Key, p.ATS)
	}
	if p.Title != "Enterprise Account Executive" || p.Company != "AMCS" || p.Location != "Remote, US, MA, United States" || !p.Remote {
		t.Errorf("posting %+v", p)
	}
	if p.URL != "https://amcsgroup.hirehive.com/enterprise-account-executive-remote-us-zo9SLD" {
		t.Errorf("url %s", p.URL)
	}
	want := time.Date(2026, 11, 9, 15, 7, 56, 733000000, time.UTC)
	if !p.PostedAt.Equal(want) {
		t.Errorf("posted %s, want %s", p.PostedAt, want)
	}
	if !strings.Contains(p.Description, "Body of the role") || strings.Contains(p.Description, "Other body") || strings.Contains(p.Description, "alert") || strings.ContainsAny(p.Description, "<>") {
		t.Errorf("description %q", p.Description)
	}
	if !strings.Contains(p.Description, "Role: full-time, Mid Level") {
		t.Errorf("description lacks the role line: %q", p.Description)
	}
	if len(d.URLs()) != 1 || d.URLs()[0] != firstURL {
		t.Errorf("urls %v", d.URLs())
	}
}

func TestOneDigitFractionAndTextBody(t *testing.T) {
	// KSN's published_date on 2026-11-09 had a single fractional digit.
	body := wrap(1, `{
		"id":"job_J8YBUI",
		"title":"Quantity Surveyor",
		"location":"Cavan & Dublin",
		"state_code":null,
		"country":{"name":"Ireland","code":"IE"},
		"salary":"EUR 40k",
		"description":{"html":"  ","text":"<p>Plain body</p>"},
		"type":{"type":"PartTime","name":"Part Time"},
		"experience":{"type":"NA","name":"NA"},
		"published_date":"2026-10-07T10:19:36.4Z",
		"hosted_url":"https://amcsgroup.hirehive.com/quantity-surveyor-J8YBUI",
		"compensation_tiers":[{"interval":"year","currency_code":"EUR","min_value":40000,"max_value":50000}]
	}`)
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p := got[0]
	want := time.Date(2026, 10, 7, 10, 19, 36, 400000000, time.UTC)
	if !p.PostedAt.Equal(want) || p.Remote || p.Location != "Cavan & Dublin, Ireland" {
		t.Errorf("posted %s remote %v location %q", p.PostedAt, p.Remote, p.Location)
	}
	if !strings.Contains(p.Description, "Compensation: EUR 40k") || !strings.Contains(p.Description, "Pay range: EUR 40000 – 50000 per year") {
		t.Errorf("pay lines missing: %q", p.Description)
	}
	if !strings.Contains(p.Description, "Plain body") || !strings.Contains(p.Description, "Role: part-time") || strings.Contains(p.Description, "NA") {
		t.Errorf("description %q", p.Description)
	}
}

func TestRemoteIsALocationWord(t *testing.T) {
	body := wrap(1, `{
		"id":"job_1","title":"Remote analyst","location":"Austin",
		"published_date":"",
		"hosted_url":"https://amcsgroup.hirehive.com/remote-analyst",
		"description":{"html":"<p>Body</p>"}
	}`)
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 || got[0].Remote || got[0].PostedAt != (time.Time{}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompanyFallsBackToTheToken(t *testing.T) {
	body := wrap(1, `{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/role","description":{"text":"Body"}}`)
	b := sources.Board{ATS: posting.HireHive, Token: "amcsgroup"}
	got, err := New(one(body)).List(context.Background(), b)
	if err != nil || len(got) != 1 || got[0].Company != "amcsgroup" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPagesStopWhenTheTotalMatches(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != "amcsgroup.hirehive.com" || u.Path != "/api/v2/jobs" {
			return srctest.Reply{}, false
		}
		page := u.Query().Get("page")
		switch page {
		case "1":
			return ok(`{"meta":{"total_items":2,"has_next_page":true},"links":{"next":"https://evil.test/x"},"items":[{"id":"job_1","title":"Role 1","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"<p>Body 1</p>"}}]}`), true
		case "2":
			return ok(`{"meta":{"total_items":2,"has_next_page":false},"items":[{"id":"job_2","title":"Role 2","hosted_url":"https://amcsgroup.hirehive.com/2","description":{"html":"<p>Body 2</p>"}}]}`), true
		default:
			return srctest.Reply{}, false
		}
	})
	got, err := New(d).List(context.Background(), board())
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if got[0].Key != "hv:amcsgroup:job_1" || got[1].Key != "hv:amcsgroup:job_2" {
		t.Errorf("keys %s %s", got[0].Key, got[1].Key)
	}
	if len(d.URLs()) != 2 || !strings.HasSuffix(d.URLs()[1], "page=2&page_size=100") {
		t.Errorf("urls %v", d.URLs())
	}
}

func TestADuplicateAcrossPagesFailsTheBoard(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		item := `{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"<p>Body</p>"}}`
		if u.Query().Get("page") == "1" {
			return ok(`{"meta":{"total_items":2,"has_next_page":true},"items":[` + item + `]}`), true
		}
		return ok(`{"meta":{"total_items":2,"has_next_page":false},"items":[` + item + `]}`), true
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPageCapDoesNotFetchAnotherPage(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host != "amcsgroup.hirehive.com" {
			return srctest.Reply{}, false
		}
		page := u.Query().Get("page")
		// One job a page, and a total past what maxPages can reach, so the
		// page cap is what ends the listing.
		body := fmt.Sprintf(`{"meta":{"total_items":%d,"has_next_page":true},"items":[{"id":"p%s","title":"Role %s","hosted_url":"https://amcsgroup.hirehive.com/%s","description":{"html":"<p>Body %s</p>"}}]}`, maxPages*2, page, page, page, page)
		return ok(body), true
	})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || !strings.Contains(err.Error(), fmt.Sprintf("more than %d pages", maxPages)) {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(d.URLs()) != maxPages {
		t.Fatalf("%d requests, want %d", len(d.URLs()), maxPages)
	}
	if maxPages*pageSize < srcutil.MaxPostings {
		t.Fatalf("%d pages of %d cannot list a legal board of %d", maxPages, pageSize, srcutil.MaxPostings)
	}
	for _, u := range d.URLs() {
		if strings.Contains(u, fmt.Sprintf("page=%d&", maxPages+1)) {
			t.Errorf("fetched past the cap: %s", u)
		}
	}
}

func TestCountMismatchesFailTheBoard(t *testing.T) {
	cases := map[string]string{
		"short":      `{"meta":{"total_items":2,"has_next_page":false},"items":[{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"Body"}}]}`,
		"over total": `{"meta":{"total_items":0,"has_next_page":false},"items":[{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"Body"}}]}`,
		"says more":  `{"meta":{"total_items":1,"has_next_page":true},"items":[{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"Body"}}]}`,
		"empty more": `{"meta":{"total_items":3,"has_next_page":true},"items":[]}`,
		"negative":   `{"meta":{"total_items":-1,"has_next_page":false},"items":[]}`,
		"no meta":    `{"items":[]}`,
		"no total":   `{"meta":{"has_next_page":false},"items":[]}`,
		"null items": `{"meta":{"total_items":0,"has_next_page":false},"items":null}`,
		"over cap":   fmt.Sprintf(`{"meta":{"total_items":%d,"has_next_page":true},"items":[{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"Body"}}]}`, srcutil.MaxPostings+1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			d := one(body)
			got, err := New(d).List(context.Background(), board())
			if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) {
				t.Fatalf("got %v, %v", got, err)
			}
			if name == "over cap" && !errors.Is(err, srcutil.ErrTooManyPostings) {
				t.Errorf("err %v, want ErrTooManyPostings", err)
			}
			if len(d.URLs()) != 1 {
				t.Errorf("%d requests", len(d.URLs()))
			}
		})
	}
}

func TestPostingFailures(t *testing.T) {
	good := `"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"<p>Body</p>"}`
	cases := map[string]string{
		"object":      `{}`,
		"html":        `<html></html>`,
		"empty title": wrap(1, `{"id":"job_1","title":"  ","hosted_url":"https://amcsgroup.hirehive.com/1"}`),
		"tags only":   wrap(1, `{"id":"job_1","title":"<b></b>","hosted_url":"https://amcsgroup.hirehive.com/1"}`),
		"bad date":    wrap(1, `{`+good+`,"published_date":"Tuesday"}`),
		"off host":    wrap(1, `{"id":"job_1","title":"Role","hosted_url":"https://other.hirehive.com/1"}`),
		"http":        wrap(1, `{"id":"job_1","title":"Role","hosted_url":"http://amcsgroup.hirehive.com/1"}`),
		"userinfo":    wrap(1, `{"id":"job_1","title":"Role","hosted_url":"https://user:pass@amcsgroup.hirehive.com/1"}`),
		"port":        wrap(1, `{"id":"job_1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com:8443/1"}`),
		"slash id":    wrap(1, `{"id":"job/1","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1"}`),
		"colon id":    wrap(1, `{"id":"a:b","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1"}`),
		"empty id":    wrap(1, `{"id":"","title":"Role","hosted_url":"https://amcsgroup.hirehive.com/1"}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New(one(body)).List(context.Background(), board())
			if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestEmptyDateAndHTMLTitle(t *testing.T) {
	body := wrap(1, `{"id":"job_1","title":"<i>Role</i> extra","published_date":null,"hosted_url":"https://amcsgroup.hirehive.com/1","description":{"html":"<p>Body</p>"}}`)
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 || got[0].Title != "Role extra" || !got[0].PostedAt.IsZero() {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCancelledContextMakesNoRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := srctest.New(nil)
	got, err := New(d).List(ctx, board())
	if !errors.Is(err, context.Canceled) || got != nil || len(d.URLs()) != 0 {
		t.Fatalf("got %v, %v, urls %v", got, err, d.URLs())
	}
}

func TestDependencyDownIsAnError(t *testing.T) {
	got, err := New(failDoer{}).List(context.Background(), board())
	if err == nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestNotFoundIsTheOnlyGoneStatus(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{firstURL: {Status: 404, Body: []byte(`{"message":"Company not found"}`)}})
	got, err := New(d).List(context.Background(), board())
	if !errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

type failDoer struct{}

func (failDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }
