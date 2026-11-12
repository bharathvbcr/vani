package hibob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const listURL = "https://corlytics.careers.hibob.com/api/job-ad"

func board() sources.Board {
	return sources.Board{ATS: posting.HiBob, Token: "corlytics", Company: "Corlytics"}
}

func one(body string) *srctest.Doer {
	return srctest.New(map[string]srctest.Reply{listURL: {Status: 200, Body: []byte(body)}})
}

func wrap(ads string) string {
	return `{"filterGroups":{},"jobAdDetails":[` + ads + `]}`
}

func TestListReadsTheLiveShape(t *testing.T) {
	// Field names are from corlytics.careers.hibob.com on 2026-11-09. The
	// body is a stand-in; the id, title, site and publishedAt are the public ad.
	body := wrap(`{
		"id":"d1c264ea-1ab6-4ff8-be27-227fa82f179a",
		"title":"<b>Head</b>  of Customer Success",
		"departmentId":"Operations",
		"department":"Operations",
		"employmentTypeId":"Permanent",
		"employmentType":"Permanent",
		"siteId":2521837,
		"site":"Ireland",
		"country":"Ireland",
		"language":"en",
		"description":"<p>Body of the role</p><script>alert(1)</script>",
		"requirements":"&middot; Ten years<br>",
		"responsibilities":"<br>Lead the team<br>",
		"benefits":"",
		"sectionLabels":{},
		"publishedAt":"2026-09-24T13:45:03.660569Z",
		"workspaceTypeId":"hybrid",
		"workspaceType":"Hybrid",
		"payTransparencyMinSalary":null,
		"payTransparencyMaxSalary":null,
		"payTransparencySalaryCurrency":null,
		"payTransparencySalaryPayPeriod":null,
		"payTransparencySalaryAdditionalInfo":null,
		"postingLocations":[]
	}`)
	d := one(body)
	got, err := New(d).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p := got[0]
	if p.Key != "hb:corlytics:d1c264ea-1ab6-4ff8-be27-227fa82f179a" || p.ATS != posting.HiBob {
		t.Errorf("key %s ats %s", p.Key, p.ATS)
	}
	if p.Title != "Head of Customer Success" || p.Company != "Corlytics" || p.Location != "Ireland" || p.Remote || p.Department != "Operations" {
		t.Errorf("posting %+v", p)
	}
	if p.URL != "https://corlytics.careers.hibob.com/jobs/d1c264ea-1ab6-4ff8-be27-227fa82f179a" {
		t.Errorf("url %s", p.URL)
	}
	want := time.Date(2026, 9, 24, 13, 45, 3, 660569000, time.UTC)
	if !p.PostedAt.Equal(want) {
		t.Errorf("posted %s, want %s", p.PostedAt, want)
	}
	if !strings.Contains(p.Description, "Body of the role") || strings.Contains(p.Description, "alert") || strings.ContainsAny(p.Description, "<>") {
		t.Errorf("description %q", p.Description)
	}
	if !strings.Contains(p.Description, "Responsibilities\n") || !strings.Contains(p.Description, "Lead the team") || !strings.Contains(p.Description, "Requirements\n") || !strings.Contains(p.Description, "Ten years") {
		t.Errorf("sections missing: %q", p.Description)
	}
	if strings.Contains(p.Description, "Benefits") {
		t.Errorf("empty benefits kept a heading: %q", p.Description)
	}
	if !strings.Contains(p.Description, "Role: Permanent, Hybrid") {
		t.Errorf("role line: %q", p.Description)
	}
	if len(d.Requests()) != 1 || d.Requests()[0].Header.Get("Referer") != "https://corlytics.careers.hibob.com/jobs" {
		t.Errorf("referer %v", d.Requests())
	}
}

func TestWorkspaceAndPlaces(t *testing.T) {
	// postingLocations.name on Bond's board (2026-11-09) was the legal entity,
	// not the city. The city is what the location keeps.
	body := wrap(`{
		"id":"794892dc-81e7-41d8-8b1c-e89884bf1d43",
		"title":"Engineer",
		"employmentType":"Employee",
		"site":"United Kingdom",
		"country":"United Kingdom",
		"description":"<p>Body</p>",
		"workspaceTypeId":"remote",
		"workspaceType":"Remote",
		"payTransparencyMinSalary":90000,
		"payTransparencyMaxSalary":120000,
		"payTransparencySalaryCurrency":"GBP",
		"payTransparencySalaryPayPeriod":"Annual",
		"payTransparencySalaryAdditionalInfo":"plus bonus",
		"postingLocations":[{"sourceType":"site","name":"Bond Brand Loyalty UK Limited","country":"United Kingdom","region":"England","city":"Bristol","displayValue":"Bond Brand Loyalty UK Limited"}]
	}`)
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p := got[0]
	if !p.Remote || p.Location != "United Kingdom, Bristol, England" || strings.Contains(p.Location, "Bond") {
		t.Errorf("location %q remote %v", p.Location, p.Remote)
	}
	if !strings.Contains(p.Description, "Pay range: GBP 90000 – 120000 per year") || !strings.Contains(p.Description, "Compensation: plus bonus") {
		t.Errorf("pay lines: %q", p.Description)
	}
}

func TestOnSiteIsNotRemote(t *testing.T) {
	body := wrap(`{"id":"abc","title":"Role","site":"London","country":"United Kingdom","workspaceType":"On site","workspaceTypeId":"on-site","description":"<p>Body</p>"}`)
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 || got[0].Remote || got[0].Location != "London, United Kingdom" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompanyFallsBackToTheToken(t *testing.T) {
	body := wrap(`{"id":"abc","title":"Role","description":"Body"}`)
	b := sources.Board{ATS: posting.HiBob, Token: "corlytics"}
	got, err := New(one(body)).List(context.Background(), b)
	if err != nil || len(got) != 1 || got[0].Company != "corlytics" || got[0].URL != "https://corlytics.careers.hibob.com/jobs/abc" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestEmptyBoard(t *testing.T) {
	got, err := New(one(`{"jobAdDetails":[]}`)).List(context.Background(), board())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestPayloadFailures(t *testing.T) {
	cases := map[string]string{
		"object":      `{}`,
		"html":        `<html></html>`,
		"null ads":    `{"jobAdDetails":null}`,
		"missing":     `{"filterGroups":{}}`,
		"empty id":    wrap(`{"id":"","title":"Role","description":"Body"}`),
		"slash id":    wrap(`{"id":"a/b","title":"Role","description":"Body"}`),
		"colon id":    wrap(`{"id":"a:b","title":"Role","description":"Body"}`),
		"empty title": wrap(`{"id":"abc","title":" <b></b> ","description":"Body"}`),
		"bad date":    wrap(`{"id":"abc","title":"Role","publishedAt":"Tuesday","description":"Body"}`),
		"duplicate":   wrap(`{"id":"abc","title":"Role","description":"Body"},{"id":"ABC","title":"Other","description":"Body"}`),
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

func TestEmptyDateIsZero(t *testing.T) {
	body := wrap(`{"id":"abc","title":"<i>Role</i> extra","publishedAt":null,"description":"<p>Body</p>"}`)
	got, err := New(one(body)).List(context.Background(), board())
	if err != nil || len(got) != 1 || got[0].Title != "Role extra" || !got[0].PostedAt.IsZero() {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestUnauthorizedIsNotAMissingBoard(t *testing.T) {
	// A missing host and a missing Referer both answered 401 on 2026-11-09.
	d := srctest.New(map[string]srctest.Reply{listURL: {Status: 401, Body: []byte(`{"message":"Unauthorized","status":"401"}`)}})
	got, err := New(d).List(context.Background(), board())
	if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(d.Requests()) != 1 || d.Requests()[0].Header.Get("Referer") == "" {
		t.Errorf("requests %v", d.Requests())
	}
}

func TestNotFoundIsGone(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{listURL: {Status: 404, Body: []byte(`{}`)}})
	got, err := New(d).List(context.Background(), board())
	if !errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v", got, err)
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

func TestOverCapIsAnError(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"jobAdDetails":[`)
	for i := 0; i < srcutil.MaxPostings+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"id%d","title":"Role","description":"Body"}`, i)
	}
	b.WriteString(`]}`)
	d := one(b.String())
	got, err := New(d).List(context.Background(), board())
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("got %v, %v, urls %d", got, err, len(d.URLs()))
	}
}

type failDoer struct{}

func (failDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}
