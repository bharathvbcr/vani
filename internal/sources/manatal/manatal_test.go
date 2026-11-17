package manatal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const page1URL = "https://api.manatal.com/open/v3/career-page/performacentric/jobs/?page=1&page_size=100"

func board() sources.Board {
	return sources.Board{ATS: posting.Manatal, Token: "performacentric", Company: "Performacentric"}
}

func one(body string) *srctest.Doer {
	return srctest.New(map[string]srctest.Reply{page1URL: {Status: 200, Body: []byte(body)}})
}

func TestListReadsTheLiveShape(t *testing.T) {
	body := `{
		"count": 2,
		"next": "https://core.api.manatal.com/open/v3/career-page/performacentric/jobs/?page=2",
		"previous": null,
		"results": [
			{
				"id": 3963847,
				"hash": "RY787VY6",
				"position_name": "<b>ML</b>  Engineer",
				"description": "<h1>Role</h1><p>Build models</p><script>alert(1)</script>",
				"country": "United States",
				"state": "Indiana",
				"city": "Indianapolis",
				"zipcode": "",
				"address": "",
				"location_display": "Indianapolis, Indiana, United States",
				"is_remote": true,
				"contract_details": "full_time",
				"is_salary_visible": false,
				"salary_min": "70000.00",
				"salary_max": "140000.00",
				"currency_code": "USD",
				"is_pinned_in_career_page": false
			}
		]
	}`
	page2 := `{
		"count": 2,
		"next": null,
		"previous": "https://core.api.manatal.com/open/v3/career-page/performacentric/jobs/?page=1",
		"results": [
			{
				"id": 3963848,
				"hash": "934Y3W86",
				"position_name": "Backend Engineer",
				"description": "<p>Write Go</p>",
				"country": "United States",
				"state": "Illinois",
				"city": "Chicago",
				"location_display": "Chicago, Illinois, United States",
				"is_remote": false,
				"contract_details": "full_time",
				"is_salary_visible": true,
				"salary_min": "120000.00",
				"salary_max": "160000.00",
				"currency_code": "USD"
			}
		]
	}`
	d := srctest.New(map[string]srctest.Reply{
		page1URL: {Status: 200, Body: []byte(body)},
		"https://api.manatal.com/open/v3/career-page/performacentric/jobs/?page=2&page_size=100": {Status: 200, Body: []byte(page2)},
	})
	got, err := New(d).List(context.Background(), board())
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	p0 := got[0]
	if p0.Key != "mn:performacentric:ry787vy6" || p0.ATS != posting.Manatal {
		t.Errorf("key %s ats %s", p0.Key, p0.ATS)
	}
	if p0.Title != "ML Engineer" || p0.Company != "Performacentric" || !p0.Remote || p0.Location != "Indianapolis, Indiana, United States" {
		t.Errorf("posting 0: %+v", p0)
	}
	if p0.URL != "https://www.careers-page.com/performacentric/job/RY787VY6" {
		t.Errorf("url %s", p0.URL)
	}
	if !strings.Contains(p0.Description, "Build models") || strings.Contains(p0.Description, "alert") || strings.Contains(p0.Description, "70000") {
		t.Errorf("hidden salary or script leaked in description: %q", p0.Description)
	}
	if !strings.Contains(p0.Description, "Role: full-time, remote") {
		t.Errorf("role line: %q", p0.Description)
	}
	p1 := got[1]
	if p1.Remote || !strings.Contains(p1.Description, "Pay range: USD 120000 – 160000") {
		t.Errorf("visible salary missing on p1: %+v", p1)
	}
}

func TestEmptyBoard(t *testing.T) {
	got, err := New(one(`{"count":0,"next":null,"results":[]}`)).List(context.Background(), board())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestNotFoundIsGone(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		page1URL: {Status: 404, Body: []byte(`{"detail":"No ClientPortalSettings matches the given query."}`)},
	})
	got, err := New(d).List(context.Background(), board())
	if !errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPayloadAndPagingFailures(t *testing.T) {
	cases := map[string]string{
		"empty object":      `{}`,
		"missing results":   `{"count":0}`,
		"null results":      `{"count":0,"results":null}`,
		"negative count":    `{"count":-1,"results":[]}`,
		"short read":        `{"count":2,"next":null,"results":[{"hash":"A1","position_name":"Role","description":"Body"}]}`,
		"next on complete":  `{"count":1,"next":"https://core.api.manatal.com/open/v3/career-page/performacentric/jobs/?page=2","results":[{"hash":"A1","position_name":"Role","description":"Body"}]}`,
		"duplicate hash":    `{"count":2,"next":null,"results":[{"hash":"ab1","position_name":"Role 1","description":"Body"},{"hash":"AB1","position_name":"Role 2","description":"Body"}]}`,
		"empty hash":        `{"count":1,"next":null,"results":[{"hash":"","position_name":"Role","description":"Body"}]}`,
		"slash hash":        `{"count":1,"next":null,"results":[{"hash":"a/b","position_name":"Role","description":"Body"}]}`,
		"empty on hasNext":  `{"count":2,"next":"https://core.api.manatal.com/x","results":[]}`,
		"over MaxPostings":  `{"count":15001,"next":"https://core.api.manatal.com/x","results":[]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New(one(body)).List(context.Background(), board())
			if err == nil || got != nil || errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}
