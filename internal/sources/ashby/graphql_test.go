package ashby

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func gqlList(jobs string, teams string) string {
	if teams == "" {
		teams = `[]`
	}
	return `{"data":{"jobBoard":{"teams":` + teams + `,"jobPostings":` + jobs + `}}}`
}

func gqlDetail(body string) string {
	return `{"data":{"jobPosting":` + body + `}}`
}

func jobJSON(id, title, dept, loc, workplace, html, pay string, listed any) string {
	raw, err := json.Marshal(map[string]any{
		"id": id, "title": title, "departmentExternalName": dept,
		"locationName": loc, "workplaceType": workplace,
		"descriptionHtml": html, "isListed": listed,
		"scrapeableCompensationSalarySummary": pay,
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func gqlDoer(list string, details map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case boardURL:
			return srctest.Reply{Status: 404, Body: []byte(`{"error":"not found"}`)}, true
		case graphQLURL(jobBoardOp):
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		case graphQLURL(jobPostingOp):
			var req struct {
				Variables struct {
					ID string `json:"jobPostingId"`
				} `json:"variables"`
			}
			if json.Unmarshal(r.Body, &req) != nil {
				return srctest.Reply{}, false
			}
			reply, ok := details[req.Variables.ID]
			return reply, ok
		default:
			return srctest.Reply{}, false
		}
	})
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func TestJobBoardFallback(t *testing.T) {
	list := gqlList(`[{"id":"Job1","title":"From the list","locationName":"List City","workplaceType":"OnSite","teamId":"t1"}]`,
		`[{"id":"t1","name":"Internal","externalName":"Engineering"}]`)
	detail := jobJSON("Job1", "Backend Engineer", "Product", "Toronto", "Hybrid", "<p>Build &amp; ship</p>", "CA$160,000", true)
	s, d := gqlDoer(list, map[string]srctest.Reply{
		"Job1": {Status: 200, Body: []byte(gqlDetail(detail))},
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	p := got[0]
	if p.Key != "ab:ashby:job1" || p.Company != "Ashby" || p.Title != "Backend Engineer" ||
		p.Location != "Toronto" || p.Department != "Product" || p.Remote ||
		p.URL != "https://jobs.ashbyhq.com/ashby/Job1" ||
		p.ApplyURL != "https://jobs.ashbyhq.com/ashby/Job1/application" ||
		!p.SeenAt.Equal(fixedNow) || !p.PostedAt.IsZero() {
		t.Fatalf("%+v", p)
	}
	if p.Description != "Compensation: CA$160,000\n\nBuild & ship" {
		t.Fatalf("description %q", p.Description)
	}
	urls := d.URLs()
	if len(urls) != 3 || urls[2] != graphQLURL(jobPostingOp) {
		t.Fatalf("urls %v", urls)
	}
	for _, u := range urls {
		if strings.Contains(u, "application") {
			t.Fatalf("fetched the apply page: %v", urls)
		}
	}
}

func TestJobBoardUsesTheListWhenTheDetailOmitsFields(t *testing.T) {
	list := gqlList(`[{"id":"a","title":"List Title","locationName":"Paris","workplaceType":"Remote","teamId":"t1"}]`,
		`[{"id":"t1","name":"Eng"}]`)
	detail := `{"id":"a","title":"","isListed":true,"compensationTierSummary":"tier pay"}`
	s, _ := gqlDoer(list, map[string]srctest.Reply{"a": {Status: 200, Body: []byte(gqlDetail(detail))}})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("%d %v", len(got), err)
	}
	p := got[0]
	if p.Title != "List Title" || p.Location != "Paris" || !p.Remote || p.Department != "Eng" {
		t.Fatalf("%+v", p)
	}
	if p.Description != "Compensation: tier pay" {
		t.Fatalf("description %q", p.Description)
	}
}

func TestJobBoardSkipsUnlistedBeforeValidation(t *testing.T) {
	list := gqlList(`[{"id":"bad"},{"id":"ok","title":"Kept","workplaceType":"Remote"}]`, "")
	s, _ := gqlDoer(list, map[string]srctest.Reply{
		"bad": {Status: 200, Body: []byte(gqlDetail(`{"id":"bad","isListed":false}`))},
		"ok":  {Status: 200, Body: []byte(gqlDetail(jobJSON("ok", "Kept", "", "Remote", "Remote", "<p>Go</p>", "", true)))},
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Key != "ab:ashby:ok" || !got[0].Remote {
		t.Fatalf("%v %v", got, err)
	}
}

func TestJobBoardEmptyIsValid(t *testing.T) {
	s, d := gqlDoer(gqlList(`[]`, ""), nil)
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("%#v %v", got, err)
	}
	if len(d.URLs()) != 2 {
		t.Fatalf("urls %v", d.URLs())
	}
}

func TestJobBoardNullIsNotFound(t *testing.T) {
	s, d := gqlDoer(`{"data":{"jobBoard":null}}`, nil)
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if urls := d.URLs(); len(urls) != 2 {
		t.Fatalf("urls %v", urls)
	}
}

func TestJobBoardErrors(t *testing.T) {
	cases := map[string]string{
		"graphql error": `{"errors":[{"message":"nope"}],"data":{"jobBoard":null}}`,
		"no array":      `{"data":{"jobBoard":{"jobPostings":null,"teams":[]}}}`,
		"not json":      `<html>`,
		"empty id":      gqlList(`[{"id":""}]`, ""),
		"colon id":      gqlList(`[{"id":"a:b","title":"T"}]`, ""),
		"duplicate":     gqlList(`[{"id":"A","title":"T"},{"id":"a","title":"T"}]`, ""),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, d := gqlDoer(body, nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %v %v", got, err)
			}
			if name != "graphql error" && errors.Is(err, ErrBoardNotFound) {
				t.Fatalf("shape error looked like a missing board: %v", err)
			}
			for _, u := range d.URLs() {
				if u == graphQLURL(jobPostingOp) {
					t.Fatalf("fetched a detail for %s", name)
				}
			}
		})
	}
}

func TestJobBoardServerError(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{
		boardURL:               {Status: 404, Body: []byte(`{}`)},
		graphQLURL(jobBoardOp): {Status: 500, Body: []byte(`{}`)},
	})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestJobBoardDetailMissingIsNotAMissingBoard(t *testing.T) {
	list := gqlList(`[{"id":"a","title":"T"}]`, "")
	s, _ := gqlDoer(list, map[string]srctest.Reply{
		"a": {Status: 404, Body: []byte(`{}`)},
	})
	got, err := s.List(context.Background(), board)
	if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil || !strings.Contains(err.Error(), "not on the board") {
		t.Fatalf("%v %v", got, err)
	}
}

func TestJobBoardDetailNullAndMismatch(t *testing.T) {
	list := gqlList(`[{"id":"a","title":"T"}]`, "")
	for name, body := range map[string]string{
		"null":     `{"data":{"jobPosting":null}}`,
		"mismatch": gqlDetail(`{"id":"other","title":"T","isListed":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := gqlDoer(list, map[string]srctest.Reply{"a": {Status: 200, Body: []byte(body)}})
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}

func TestJobBoardEmptyTitle(t *testing.T) {
	s, _ := gqlDoer(gqlList(`[{"id":"a"}]`, ""), map[string]srctest.Reply{
		"a": {Status: 200, Body: []byte(gqlDetail(`{"id":"a","isListed":true}`))},
	})
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !strings.Contains(err.Error(), "empty title") {
		t.Fatalf("%v %v", got, err)
	}
}

func TestJobBoardTooManyDoesNotFetchDetails(t *testing.T) {
	var jobs []string
	for i := 0; i < srcutil.MaxPostings+1; i++ {
		jobs = append(jobs, `{"id":"j`+itoa(i)+`","title":"T"}`)
	}
	s, d := gqlDoer(gqlList("["+strings.Join(jobs, ",")+"]", ""), nil)
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if len(d.URLs()) != 2 {
		t.Fatalf("urls %d", len(d.URLs()))
	}
}

func TestJobBoardBodyCap(t *testing.T) {
	old := maxGraphQLBody
	t.Cleanup(func() { maxGraphQLBody = old })
	maxGraphQLBody = 32
	list := gqlList(`[{"id":"a","title":"T"}]`, "")
	s, _ := gqlDoer(list, map[string]srctest.Reply{
		"a": {Status: 200, Body: []byte(gqlDetail(jobJSON("a", "T", "", "", "", "<p>"+strings.Repeat("x", 80)+"</p>", "", true)))},
	})
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestJobBoardCancelBeforeList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, d := gqlDoer(gqlList(`[]`, ""), nil)
	if _, err := s.List(ctx, board); err == nil {
		t.Fatal("want the cancelled context")
	}
	if len(d.URLs()) != 0 {
		t.Fatalf("urls %v", d.URLs())
	}
}

func TestJobBoardCancelDuringDetail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	list := gqlList(`[{"id":"a","title":"T"},{"id":"b","title":"U"}]`, "")
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case boardURL:
			return srctest.Reply{Status: 404, Body: []byte(`{}`)}, true
		case graphQLURL(jobBoardOp):
			cancel()
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		default:
			return srctest.Reply{}, false
		}
	})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(ctx, board)
	if err == nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	for _, u := range d.URLs() {
		if u == graphQLURL(jobPostingOp) {
			t.Fatal("fetched a detail after cancel")
		}
	}
}

func TestJobBoardDescriptionIsCapped(t *testing.T) {
	list := gqlList(`[{"id":"a","title":"T"}]`, "")
	html := "<p>" + strings.Repeat("y", srcutil.MaxDescription+100) + "</p>"
	s, _ := gqlDoer(list, map[string]srctest.Reply{
		"a": {Status: 200, Body: []byte(gqlDetail(jobJSON("a", "T", "", "", "", html, "", true)))},
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || len(got[0].Description) > srcutil.MaxDescription {
		t.Fatalf("len %d err %v", len(got), err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
