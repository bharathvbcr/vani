package tiktok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

var board = sources.Board{ATS: posting.TikTok, Token: "tiktok", Company: "TikTok"}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fake is a TikTok API: search answers come from answer(body), the city list
// from filters. Every request is kept.
type fake struct {
	t       *testing.T
	answer  func(b searchBody) srctest.Reply
	filters srctest.Reply
	reqs    []srctest.Request
	bodies  []searchBody
}

func (f *fake) handle(r srctest.Request) (srctest.Reply, bool) {
	f.reqs = append(f.reqs, r)
	if r.Method != "POST" {
		f.t.Errorf("method %s, want POST", r.Method)
	}
	switch r.URL {
	case searchURL:
		var b searchBody
		if err := json.Unmarshal(r.Body, &b); err != nil {
			f.t.Errorf("search body is not JSON: %v", err)
		}
		f.bodies = append(f.bodies, b)
		return f.answer(b), true
	case filtersURL:
		return f.filters, true
	}
	return srctest.Reply{}, false
}

func (f *fake) urls() []string {
	var u []string
	for _, r := range f.reqs {
		u = append(u, r.URL)
	}
	return u
}

func newSource(t *testing.T, answer func(b searchBody) srctest.Reply) (*source, *fake, *[]time.Duration) {
	t.Helper()
	f := &fake{t: t, answer: answer, filters: srctest.Reply{Status: 200}}
	var waits []time.Duration
	s := &source{d: srctest.NewFunc(f.handle), now: func() time.Time { return fixedNow },
		wait: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }}
	return s, f, &waits
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

// rowJSON is one valid row; city is a code from filters.json.
func rowJSON(id, title, city string) string {
	return fmt.Sprintf(`{"id":%q,"title":%q,"description":"d","requirement":"r","recruit_type":{"en_name":"Regular"},`+
		`"job_category":{"en_name":"Backend","parent":{"en_name":"R&D"}},`+
		`"city_info":{"code":%q,"en_name":"C","parent":{"code":"ST_1","en_name":"S","parent":{"code":"CN_1","en_name":"United States of America"}}}}`,
		id, title, city)
}

func answerJSON(count int, rows ...string) string {
	return fmt.Sprintf(`{"code":0,"message":"ok","error":null,"data":{"BaseResp":{"StatusCode":0},"count":%d,"job_post_list":[%s]}}`,
		count, strings.Join(rows, ","))
}

// numbered is a board of n rows, ids 1000.., served by offset and limit.
func numbered(n int) func(b searchBody) srctest.Reply {
	return func(b searchBody) srctest.Reply {
		var rows []string
		for i := b.Offset; i < n && i < b.Offset+b.Limit; i++ {
			rows = append(rows, rowJSON(fmt.Sprint(1000+i), fmt.Sprintf("Role %d", i), "CT_223"))
		}
		return ok(answerJSON(n, rows...))
	}
}

func TestFixture(t *testing.T) {
	s, f, _ := newSource(t, func(searchBody) srctest.Reply { return ok(string(readFixture(t, "posts.json"))) })
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	wants := []posting.Posting{
		{Key: "tt:tiktok:7699309261691750661", ATS: posting.TikTok, Company: "TikTok",
			Title:    "Commercial Partnership Solutions Manager, Operations - Global Monetization Product and Technology",
			Location: "Los Angeles, California, United States of America", Department: "Operations / Product ops",
			URL: "https://lifeattiktok.com/search/7699309261691750661", SeenAt: fixedNow},
		{Key: "tt:tiktok:7700932418329725237", ATS: posting.TikTok, Company: "TikTok",
			Title:    "MCN Operations Project Intern (TikTok Shop VN Agency, CN Speaking) - 2027 Start - 2027 Start",
			Location: "Ho Chi Minh City, Ho Chi Minh, Vietnam", Department: "Operations / Commerce ops",
			URL: "https://lifeattiktok.com/search/7700932418329725237", SeenAt: fixedNow},
	}
	prefixes := []string{"Role: Regular\n\n", "Role: internship\n\n"}
	for i, w := range wants {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if g != w {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, w)
		}
		if !strings.HasPrefix(desc, prefixes[i]) {
			t.Errorf("posting %d description starts %.60q, want prefix %q", i, desc, prefixes[i])
		}
		if !g.PostedAt.IsZero() || !g.UpdatedAt.IsZero() {
			t.Errorf("posting %d has a date the API never gave: %v %v", i, g.PostedAt, g.UpdatedAt)
		}
		if strings.Contains(desc, "Pay range") {
			t.Errorf("posting %d has pay the API never gave: %q", i, desc)
		}
	}
	// The requirement text follows the description, its bullets still on lines of
	// their own (htmltext.Text would have folded them into one).
	if d := got[1].Description; !strings.Contains(d, "\n\nMinimum Qualification(s)\n- Currently pursuing an undergraduate degree") ||
		!strings.Contains(d, "\n- Good understanding of user generated content.\n- Good interpersonal skills") {
		t.Errorf("requirement lines lost: %q", d)
	}
	if u := (&source{}); u.ATS() != posting.TikTok {
		t.Errorf("ATS %q", u.ATS())
	}
	if len(f.reqs) != 1 {
		t.Errorf("%d requests, want 1", len(f.reqs))
	}
}

// TestRequest pins what goes on the wire: the one URL, the header from the
// allowlist and the body the site sends, every list an empty array.
func TestRequest(t *testing.T) {
	s, f, _ := newSource(t, numbered(1))
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	r := f.reqs[0]
	if r.URL != "https://api.lifeattiktok.com/api/v1/public/supplier/search/job/posts" || r.Method != "POST" {
		t.Fatalf("%s %s", r.Method, r.URL)
	}
	if got := r.Header.Get("Website-Path"); got != "tiktok" {
		t.Errorf("website-path %q", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type %q", got)
	}
	want := `{"keyword":"","limit":100,"offset":0,"job_category_id_list":[],"tag_id_list":[],"location_code_list":[],` +
		`"subject_id_list":[],"recruitment_id_list":[],"portal_type":6,"job_function_id_list":[],"storefront_id_list":[],"portal_entrance":1}`
	if string(r.Body) != want {
		t.Errorf("body\n got %s\nwant %s", r.Body, want)
	}
}

func TestTokenIsAllowlisted(t *testing.T) {
	for _, tok := range []string{"bytedance", "tiktok\r\nX-Evil: 1", "tiktok ", " tiktok", "..", "../tiktok", "tik/tok", "tiktok:1", "tik tok", "", "tiktokk", "tiktok\n"} {
		s, f, _ := newSource(t, numbered(1))
		_, err := s.List(context.Background(), sources.Board{ATS: posting.TikTok, Token: tok})
		if err == nil {
			t.Errorf("token %q was accepted", tok)
		}
		if len(f.reqs) != 0 {
			t.Errorf("token %q reached the network: %v", tok, f.urls())
		}
	}
	// The token is only a lookup key: its case is folded, and the header and the
	// key come out the same.
	s, f, _ := newSource(t, numbered(1))
	got, err := s.List(context.Background(), sources.Board{ATS: posting.TikTok, Token: "TikTok"})
	if err != nil || len(got) != 1 || got[0].Key != "tt:tiktok:1000" || f.reqs[0].Header.Get("Website-Path") != "tiktok" {
		t.Fatalf("TikTok: %v %v", got, err)
	}
	if _, err := s.List(context.Background(), sources.Board{ATS: posting.Lever, Token: "tiktok"}); err == nil {
		t.Error("a board of another ATS was accepted")
	}
}

func TestPaging(t *testing.T) {
	s, f, waits := newSource(t, numbered(250))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 || got[0].Key != "tt:tiktok:1000" || got[249].Key != "tt:tiktok:1249" {
		t.Fatalf("got %d postings", len(got))
	}
	var offs []int
	for _, b := range f.bodies {
		offs = append(offs, b.Offset)
		if b.Limit != 100 {
			t.Errorf("limit %d", b.Limit)
		}
	}
	if fmt.Sprint(offs) != "[0 100 200]" {
		t.Errorf("offsets %v", offs)
	}
	// One second before every request after the first.
	if len(*waits) != 2 || (*waits)[0] != time.Second || (*waits)[1] != time.Second {
		t.Errorf("waits %v, want two of 1s", *waits)
	}
}

func TestExactMultipleOfThePageStopsWithoutAnExtraRequest(t *testing.T) {
	s, f, _ := newSource(t, numbered(200))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 200 || len(f.reqs) != 2 {
		t.Fatalf("%d postings, %d requests, %v", len(got), len(f.reqs), err)
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _, _ := newSource(t, numbered(0))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestPagingRefusals(t *testing.T) {
	page := func(from, to int) []string {
		var rows []string
		for i := from; i < to; i++ {
			rows = append(rows, rowJSON(fmt.Sprint(1000+i), "R", "CT_223"))
		}
		return rows
	}
	for name, tc := range map[string]struct {
		answer func(b searchBody) srctest.Reply
		want   string
	}{
		"wrapped to page one": {func(b searchBody) srctest.Reply { return ok(answerJSON(250, page(0, 100)...)) }, "read twice"},
		"repeated id in a page": {func(b searchBody) srctest.Reply {
			return ok(answerJSON(2, rowJSON("5", "A", "CT_223"), rowJSON("5", "B", "CT_223")))
		}, "read twice"},
		"short page before the count": {func(b searchBody) srctest.Reply {
			if b.Offset == 0 {
				return ok(answerJSON(250, page(0, 100)...))
			}
			return ok(answerJSON(250, page(100, 150)...))
		}, "a page of 50 rows"},
		"empty page before the count": {func(b searchBody) srctest.Reply {
			if b.Offset == 0 {
				return ok(answerJSON(250, page(0, 100)...))
			}
			return ok(answerJSON(250))
		}, "no rows with 100 of 250"},
		"count moves": {func(b searchBody) srctest.Reply {
			if b.Offset == 0 {
				return ok(answerJSON(250, page(0, 100)...))
			}
			return ok(answerJSON(251, page(100, 200)...))
		}, "count moved from 250 to 251"},
		"more rows than the count": {func(b searchBody) srctest.Reply { return ok(answerJSON(1, page(0, 3)...)) }, "3 rows read but the count is 1"},
		"zero count with rows":     {func(b searchBody) srctest.Reply { return ok(answerJSON(0, page(0, 1)...)) }, "1 rows read but the count is 0"},
		"nonnumeric id":            {func(b searchBody) srctest.Reply { return ok(answerJSON(1, rowJSON("12ab", "A", "CT_223"))) }, `id "12ab"`},
		"path id":                  {func(b searchBody) srctest.Reply { return ok(answerJSON(1, rowJSON("../1", "A", "CT_223"))) }, `id "../1"`},
		"empty id":                 {func(b searchBody) srctest.Reply { return ok(answerJSON(1, rowJSON("", "A", "CT_223"))) }, `id ""`},
		"empty title":              {func(b searchBody) srctest.Reply { return ok(answerJSON(1, rowJSON("1", "  ", "CT_223"))) }, "empty title"},
		"api code":                 {func(b searchBody) srctest.Reply { return ok(`{"code":5,"message":"boom\nx","data":null}`) }, "code 5"},
		"no code":                  {func(b searchBody) srctest.Reply { return ok(`{"data":{"count":0,"job_post_list":[]}}`) }, "code missing"},
		"no data":                  {func(b searchBody) srctest.Reply { return ok(`{"code":0,"message":"ok"}`) }, "no count or job_post_list"},
		"null list":                {func(b searchBody) srctest.Reply { return ok(`{"code":0,"data":{"count":3,"job_post_list":null}}`) }, "no count or job_post_list"},
		"no count":                 {func(b searchBody) srctest.Reply { return ok(`{"code":0,"data":{"job_post_list":[]}}`) }, "no count or job_post_list"},
		"negative count":           {func(b searchBody) srctest.Reply { return ok(`{"code":0,"data":{"count":-1,"job_post_list":[]}}`) }, "no count or job_post_list"},
		"malformed":                {func(b searchBody) srctest.Reply { return ok(`{"code":0,"data":{"count":`) }, "decode"},
		"not json":                 {func(b searchBody) srctest.Reply { return ok(`<html>blocked</html>`) }, "decode"},
		"empty body":               {func(b searchBody) srctest.Reply { return ok(``) }, "decode"},
		"numeric id": {func(b searchBody) srctest.Reply {
			return ok(`{"code":0,"data":{"count":1,"job_post_list":[{"id":12,"title":"T"}]}}`)
		}, "decode"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newSource(t, tc.answer)
			got, err := s.List(context.Background(), board)
			if err == nil || len(got) != 0 {
				t.Fatalf("got %d postings and %v, want an error", len(got), err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

func TestCapIsAnErrorNotATruncation(t *testing.T) {
	s, f, _ := newSource(t, func(searchBody) srctest.Reply {
		return ok(answerJSON(srcutil.MaxPostings+1, rowJSON("1", "A", "CT_223")))
	})
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || len(got) != 0 {
		t.Fatalf("got %d postings, %v", len(got), err)
	}
	if !strings.Contains(err.Error(), "narrow it") || len(f.reqs) != 1 {
		t.Errorf("%v after %d requests", err, len(f.reqs))
	}
}

// TestTermsTogetherOverTheCapFail: each term is within the cap, their union is
// not, and the board fails rather than being cut at the cap.
func TestTermsTogetherOverTheCapFail(t *testing.T) {
	n := srcutil.MaxPostings
	s, _, _ := newSource(t, func(b searchBody) srctest.Reply {
		var rows []string
		for i := b.Offset; i < n && i < b.Offset+b.Limit; i++ {
			rows = append(rows, rowJSON(fmt.Sprintf("%s%d", b.Keyword, i), "R", "CT_223"))
		}
		return ok(answerJSON(n, rows...))
	})
	b := board
	b.Searches = []string{"1", "2"}
	_, err := s.List(context.Background(), b)
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings", err)
	}
}

func TestSearchTermsAreUnioned(t *testing.T) {
	s, f, _ := newSource(t, func(b searchBody) srctest.Reply {
		switch b.Keyword {
		case "backend":
			return ok(answerJSON(2, rowJSON("1", "A", "CT_223"), rowJSON("2", "B", "CT_223")))
		case "ml":
			return ok(answerJSON(2, rowJSON("2", "B", "CT_223"), rowJSON("3", "C", "CT_223")))
		}
		t.Errorf("unexpected keyword %q", b.Keyword)
		return ok(answerJSON(0))
	})
	b := board
	b.Search = "backend"
	b.Searches = []string{"ml", "BACKEND", " "}
	got, err := s.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, p := range got {
		keys = append(keys, string(p.Key))
	}
	if fmt.Sprint(keys) != "[tt:tiktok:1 tt:tiktok:2 tt:tiktok:3]" {
		t.Errorf("keys %v", keys)
	}
	if len(f.bodies) != 2 {
		t.Errorf("%d searches, want 2 (duplicate and blank terms dropped)", len(f.bodies))
	}
}

func TestTermWithNoMatchIsAnEmptyBoard(t *testing.T) {
	s, _, _ := newSource(t, func(b searchBody) srctest.Reply { return ok(answerJSON(0)) })
	b := board
	b.Search = "zzzz"
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func filtered(t *testing.T, answer func(searchBody) srctest.Reply, filters map[string][]string) (*source, *fake, sources.Board) {
	s, f, _ := newSource(t, answer)
	f.filters = ok(string(readFixture(t, "filters.json")))
	b := board
	b.Filters = filters
	return s, f, b
}

func TestCountryFilterExpandsToCityCodes(t *testing.T) {
	s, f, b := filtered(t, func(b searchBody) srctest.Reply {
		return ok(answerJSON(2, rowJSON("1", "A", "CT_223"), rowJSON("2", "B", "MDCY00039300")))
	}, map[string][]string{"country": {" united  states of AMERICA "}})
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if fmt.Sprint(f.urls()) != "["+filtersURL+" "+searchURL+"]" {
		t.Errorf("requests %v", f.urls())
	}
	if string(f.reqs[0].Body) != "{}" || f.reqs[0].Header.Get("Website-Path") != "tiktok" {
		t.Errorf("city list request: %s %v", f.reqs[0].Body, f.reqs[0].Header)
	}
	// The US cities of the fixture, in the list's order.
	if fmt.Sprint(f.bodies[0].LocationCodeList) != "[CT_223 CT_1103355 MDCY00039300]" {
		t.Errorf("location_code_list %v", f.bodies[0].LocationCodeList)
	}
}

func TestLocationAndCountryFiltersUnionWithoutDuplicates(t *testing.T) {
	s, f, b := filtered(t, func(b searchBody) srctest.Reply { return ok(answerJSON(0)) },
		map[string][]string{"country": {"Germany"}, "location": {"CT_163", "CT_1100743"}})
	if _, err := s.List(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got := f.bodies[0].LocationCodeList
	if len(got) != 2 || got[0] != "CT_1100743" || got[1] != "CT_163" {
		t.Errorf("location_code_list %v", got)
	}
}

func TestFilterRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		filters map[string][]string
		want    string
	}{
		"unknown country":  {map[string][]string{"country": {"Atlantis"}}, `country "Atlantis"`},
		"country code":     {map[string][]string{"country": {"CN_6"}}, `country "CN_6"`},
		"empty country":    {map[string][]string{"country": {""}}, `country ""`},
		"unknown city":     {map[string][]string{"location": {"CT_9999999"}}, `location "CT_9999999"`},
		"country as place": {map[string][]string{"location": {"CN_6"}}, `location "CN_6"`},
		"unverified key":   {map[string][]string{"department": {"x"}}, `filter "department" is not one of country, location`},
		"no value":         {map[string][]string{"country": {}}, `filter "country" has no value`},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, b := filtered(t, numbered(1), tc.filters)
			got, err := s.List(context.Background(), b)
			if err == nil || len(got) != 0 || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %d postings and %v, want error with %q", len(got), err, tc.want)
			}
			for _, u := range f.urls() {
				if u == searchURL {
					t.Errorf("the board was searched, unfiltered, after the filter failed")
				}
			}
		})
	}
}

// TestRowOutsideTheFilterFails: an API that ignored the location codes would
// list the whole board; the rows give it away.
func TestRowOutsideTheFilterFails(t *testing.T) {
	for name, row := range map[string]string{
		"other city": rowJSON("1", "A", "CT_163"),
		"no city":    `{"id":"1","title":"A","description":"d","requirement":"r"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, b := filtered(t, func(searchBody) srctest.Reply { return ok(answerJSON(1, row)) },
				map[string][]string{"country": {"United States of America"}})
			got, err := s.List(context.Background(), b)
			if !errors.Is(err, ErrShape) || len(got) != 0 {
				t.Fatalf("%d postings, %v", len(got), err)
			}
		})
	}
}

func TestUnfilteredBoardKeepsRowsWithoutACity(t *testing.T) {
	s, _, _ := newSource(t, func(searchBody) srctest.Reply {
		return ok(answerJSON(1, `{"id":"1","title":"A","description":"d","requirement":"r"}`))
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Location != "" || got[0].Department != "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestBadCityList(t *testing.T) {
	for name, body := range map[string]string{
		"api code":   `{"code":3,"message":"no","data":null}`,
		"no data":    `{"code":0}`,
		"null list":  `{"code":0,"data":{"city_list":null}}`,
		"bad code":   `{"code":0,"data":{"city_list":[{"code":"CT 1;x","en_name":"X"}]}}`,
		"not json":   `nope`,
		"empty body": ``,
	} {
		t.Run(name, func(t *testing.T) {
			s, f, b := filtered(t, numbered(1), map[string][]string{"country": {"Germany"}})
			f.filters = ok(body)
			got, err := s.List(context.Background(), b)
			if err == nil || len(got) != 0 {
				t.Fatalf("%d postings, %v", len(got), err)
			}
			if len(f.bodies) != 0 {
				t.Errorf("searched after a bad city list")
			}
		})
	}
}

func TestHTTPFailuresAreErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		reply srctest.Reply
		is    error
	}{
		"404":      {srctest.Reply{Status: 404}, srcutil.ErrBoardNotFound},
		"500":      {srctest.Reply{Status: 500}, nil},
		"400":      {srctest.Reply{Status: 400, Body: []byte(`{"code":400}`)}, nil},
		"403":      {srctest.Reply{Status: 403}, nil},
		"oversize": {srctest.Reply{Status: 200, Body: []byte(strings.Repeat(" ", pageBytes+1))}, srcutil.ErrBodyTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newSource(t, func(searchBody) srctest.Reply { return tc.reply })
			got, err := s.List(context.Background(), board)
			if err == nil || len(got) != 0 {
				t.Fatalf("%d postings, %v; a board that cannot be read is an error, not an empty list", len(got), err)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("err %v, want %v", err, tc.is)
			}
		})
	}
	var se *egress.StatusError
	s, _, _ := newSource(t, func(searchBody) srctest.Reply { return srctest.Reply{Status: 500} })
	if _, err := s.List(context.Background(), board); !errors.As(err, &se) || se.Code != 500 {
		t.Errorf("500 is not a status error: %v", err)
	}
}

func TestErrorPartwayLeavesNoPartialList(t *testing.T) {
	s, _, _ := newSource(t, func(b searchBody) srctest.Reply {
		if b.Offset > 0 {
			return srctest.Reply{Status: 500}
		}
		return numbered(250)(b)
	})
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil {
		t.Fatalf("%d postings, %v", len(got), err)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	slow := &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false }),
		now: time.Now, wait: srcutil.Sleep}
	if _, err := slow.List(context.Background(), board); err == nil {
		t.Fatal("a failing transport was read as an empty board")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, f, _ := newSource(t, numbered(1))
	if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) || len(f.reqs) != 0 {
		t.Fatalf("err %v after %d requests", err, len(f.reqs))
	}
	// A wait cut short between pages ends the listing with the context's error.
	s2, _, _ := newSource(t, numbered(250))
	s2.wait = func(context.Context, time.Duration) error { return context.DeadlineExceeded }
	if got, err := s2.List(context.Background(), board); !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("err %v, %d postings", err, len(got))
	}
}

// TestLargestBoardFitsThePaceBudget: a board at the cap is 50 pages, 49 paced
// waits, far inside the pacer's budget, so the budget never ends a legal board.
func TestLargestBoardFitsThePaceBudget(t *testing.T) {
	s, f, _ := newSource(t, numbered(srcutil.MaxPostings))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != srcutil.MaxPostings || len(f.reqs) != 50 {
		t.Fatalf("%d postings, %d requests, %v", len(got), len(f.reqs), err)
	}
}

func TestDescriptionIsBoundedAndPlain(t *testing.T) {
	big := strings.Repeat("word ", 40000)
	row := fmt.Sprintf(`{"id":"1","title":"T","description":%q,"requirement":"x\u0000y‮","recruit_type":{"en_name":"Intern"}}`,
		"<script>alert(1)</script>\n- one\n- two\n"+big)
	s, _, _ := newSource(t, func(searchBody) srctest.Reply { return ok(answerJSON(1, row)) })
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	d := got[0].Description
	if len(d) > srcutil.MaxDescription {
		t.Errorf("description is %d bytes", len(d))
	}
	if !strings.HasPrefix(d, "Role: internship\n\n") {
		t.Errorf("role line cut: %.40q", d)
	}
	if !strings.Contains(d, "\n- one\n- two\n") {
		t.Errorf("lines folded: %.120q", d)
	}
	// Untrusted text stays text: it is neither run nor cleaned of its words.
	if strings.ContainsRune(d, 0) {
		t.Error("control character kept")
	}
}

func TestTitleIsOneLine(t *testing.T) {
	s, _, _ := newSource(t, func(searchBody) srctest.Reply {
		return ok(answerJSON(1, rowJSON("1", "Software  Engineer\n&amp; Co ", "CT_223")))
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Title != "Software Engineer & Co" {
		t.Fatalf("%+v %v", got, err)
	}
}
