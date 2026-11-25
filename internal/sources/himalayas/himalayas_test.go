package himalayas

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/rowstest"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }

var fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

var board = sources.Board{ATS: posting.Himalayas, Token: Token, Company: "Himalayas",
	Search: "machine learning", Filters: map[string][]string{"country": {"US"}}}

// page1 is board's first request: url.Values sorts the parameters.
const page1 = "https://himalayas.app/jobs/api/search?country=US&page=1&q=machine+learning"

func withDoer(d *srctest.Doer) *source {
	return &source{d: d, now: func() time.Time { return fixedNow }, wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
}

// row is one valid search row for job id.
func row(id string) string {
	return fmt.Sprintf(`{"title":"Role %s","companyName":"Acme","companySlug":"acme","guid":"https://himalayas.app/companies/acme/jobs/role-%s","description":"<p>Body %s</p>","pubDate":1794735000}`, id, id, id)
}

func page(total int, rows []string) string {
	return `{"totalCount":` + strconv.Itoa(total) + `,"jobs":[` + strings.Join(rows, ",") + `]}`
}

// pager answers every term with a listing of total rows, 20 a page, built by
// rowFor(term, i); the jobs list of page p is rows (p-1)*20 .. p*20-1.
func pager(t *testing.T, total func(term string) int, rowFor func(term string, i int) string) *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, err := url.Parse(r.URL)
		if err != nil || u.Scheme+"://"+u.Host+u.Path != searchURL {
			t.Errorf("unexpected request %s", r.URL)
			return srctest.Reply{}, false
		}
		term := u.Query().Get("q")
		p, _ := strconv.Atoi(u.Query().Get("page"))
		n := total(term)
		var rows []string
		for i := (p - 1) * pageSize; i < min(p*pageSize, n); i++ {
			rows = append(rows, rowFor(term, i))
		}
		return srctest.Reply{Status: 200, Body: []byte(page(n, rows))}, true
	})
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	d := srctest.New(map[string]srctest.Reply{page1: {Status: 200, Body: raw}})
	got, err := withDoer(d).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if urls := d.URLs(); len(urls) != 1 || urls[0] != page1 {
		t.Fatalf("requests %v, want only %s", urls, page1)
	}
	want := []struct {
		p    posting.Posting
		lead string
	}{
		{posting.Posting{
			Key: "hm:himalayas:acme-labs:senior-machine-learning-engineer", ATS: posting.Himalayas,
			Company: "Acme Labs", Title: "Senior Machine Learning Engineer", Location: "United States; Canada",
			Remote: true, Department: "Engineering, Data Science",
			URL:      "https://himalayas.app/companies/acme-labs/jobs/senior-machine-learning-engineer",
			ApplyURL: "https://himalayas.app/companies/acme-labs/jobs/senior-machine-learning-engineer?utm_source=api",
			PostedAt: time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC), SeenAt: fixedNow,
		}, "Pay range: USD 150000 – 190000 per year\nRole: full-time, Senior, remote\n\nBuild ranking models."},
		{posting.Posting{
			Key: "hm:himalayas:globex:data-engineer-4821", ATS: posting.Himalayas,
			Company: "Globex", Title: "Data Engineer", Location: "Worldwide", Remote: true,
			URL: "https://himalayas.app/companies/globex/jobs/data-engineer-4821",
			// No applicationLink: the job's own himalayas.app page, never the employer's.
			ApplyURL: "https://himalayas.app/companies/globex/jobs/data-engineer-4821",
			PostedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), SeenAt: fixedNow,
		}, "Role: contract, remote\n\nOwn the pipelines."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d (the expired row is left out): %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if g != w.p {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, w.p)
		}
		if !strings.HasPrefix(desc, w.lead) {
			t.Errorf("posting %d description %q, want prefix %q", i, desc, w.lead)
		}
	}
}

func TestPagesToTheTotal(t *testing.T) {
	d := pager(t, func(string) int { return 45 }, func(_ string, i int) string { return row(strconv.Itoa(i)) })
	got, err := withDoer(d).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 45 || len(d.URLs()) != 3 {
		t.Fatalf("got %d postings in %d requests, want 45 in 3", len(got), len(d.URLs()))
	}
	for _, p := range got {
		if !strings.HasPrefix(string(p.Key), board.KeyPrefix()) || p.Company != "Acme" {
			t.Fatalf("posting %+v is outside the board or lost its row's company", p)
		}
	}
}

// A total that is an exact multiple of the page size ends without asking for
// an empty page.
func TestExactPagesStopAtTheTotal(t *testing.T) {
	d := pager(t, func(string) int { return 40 }, func(_ string, i int) string { return row(strconv.Itoa(i)) })
	got, err := withDoer(d).List(context.Background(), board)
	if err != nil || len(got) != 40 || len(d.URLs()) != 2 {
		t.Fatalf("got %d postings, %v, in %d requests; want 40 in 2", len(got), err, len(d.URLs()))
	}
}

// A row pushed onto the next page by an insert is read once, and the later
// page's larger total is the one the walk ends on.
func TestShiftedRowIsReadOnce(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		switch u.Query().Get("page") {
		case "1":
			var rows []string
			for i := 0; i < 20; i++ {
				rows = append(rows, row(strconv.Itoa(i)))
			}
			return srctest.Reply{Status: 200, Body: []byte(page(25, rows))}, true
		case "2":
			// One new job ahead of the rest: row 19 reappears first.
			rows := []string{row("19")}
			for i := 20; i < 26; i++ {
				rows = append(rows, row(strconv.Itoa(i)))
			}
			return srctest.Reply{Status: 200, Body: []byte(page(26, rows))}, true
		}
		return srctest.Reply{}, false
	})
	got, err := withDoer(d).List(context.Background(), board)
	if err != nil || len(got) != 26 {
		t.Fatalf("got %d postings, %v; want 26 distinct", len(got), err)
	}
}

// A page after the first that fails fails the board: a listing read in part
// would let the reconciler age out the postings it did not reach.
func TestLaterPageFailureFailsTheBoard(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"server error": {Status: 500},
		"timeout":      {Err: context.DeadlineExceeded},
		"not json":     {Status: 200, Body: []byte(`<html>`)},
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				if strings.Contains(r.URL, "page=2&") {
					return rep, true
				}
				var rows []string
				for i := 0; i < pageSize; i++ {
					rows = append(rows, row(strconv.Itoa(i)))
				}
				return srctest.Reply{Status: 200, Body: []byte(page(45, rows))}, true
			})
			got, err := withDoer(d).List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %d postings, err %v; want an error and no postings", len(got), err)
			}
		})
	}
}

func TestOverCapFailsOnTheFirstPage(t *testing.T) {
	d := pager(t, func(string) int { return MaxPostings + 1 }, func(_ string, i int) string { return row(strconv.Itoa(i)) })
	_, err := withDoer(d).List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || !strings.Contains(err.Error(), "seniority") {
		t.Fatalf("err = %v, want ErrTooManyPostings naming the filters", err)
	}
	if n := len(d.URLs()); n != 1 {
		t.Fatalf("%d requests, want 1: an over-cap term is not paged", n)
	}
}

// Two terms each under the cap whose union is over it fail the board; two
// terms that overlap are judged on their union, not their sum.
func TestTermsAreCappedTogether(t *testing.T) {
	b := board
	b.Searches = []string{"data"}
	half := MaxPostings/2 + 10
	d := pager(t, func(string) int { return half }, func(term string, i int) string {
		return row(strings.ReplaceAll(term, " ", "-") + "-" + strconv.Itoa(i))
	})
	if _, err := withDoer(d).List(context.Background(), b); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v, want ErrTooManyPostings for the union", err)
	}
	same := pager(t, func(string) int { return half }, func(_ string, i int) string { return row(strconv.Itoa(i)) })
	got, err := withDoer(same).List(context.Background(), b)
	if err != nil || len(got) != half {
		t.Fatalf("overlapping terms: got %d postings, %v; want the %d distinct", len(got), err, half)
	}
}

func TestCustomMaxPostings(t *testing.T) {
	n := MaxPostings + 200
	b := board
	b.MaxPostings = 2500
	d := pager(t, func(string) int { return n }, func(_ string, i int) string { return row(strconv.Itoa(i)) })
	got, err := withDoer(d).List(context.Background(), b)
	if err != nil || len(got) != n {
		t.Fatalf("raised cap: got %d postings, err %v; want %d", len(got), err, n)
	}
	b.MaxPostings = n
	over := pager(t, func(string) int { return n + 1 }, func(_ string, i int) string { return row(strconv.Itoa(i)) })
	if _, err := withDoer(over).List(context.Background(), b); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("over custom cap: err = %v, want ErrTooManyPostings", err)
	}
}

func TestShapeFailures(t *testing.T) {
	for name, body := range map[string]string{
		"no total":                `{"jobs":[]}`,
		"negative total":          `{"totalCount":-1,"jobs":[]}`,
		"no jobs list":            `{"totalCount":0}`,
		"null jobs":               `{"totalCount":3,"jobs":null}`,
		"total below rows":        page(1, []string{row("1"), row("2")}),
		"short page before total": page(30, []string{row("1")}),
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
				if strings.Contains(r.URL, "page=1&") {
					return srctest.Reply{Status: 200, Body: []byte(body)}, true
				}
				return srctest.Reply{Status: 200, Body: []byte(page(30, nil))}, true
			})
			if got, err := withDoer(d).List(context.Background(), board); !errors.Is(err, ErrShape) {
				t.Fatalf("got %d postings, err %v; want ErrShape", len(got), err)
			}
		})
	}
}

// Jobs closing mid-walk leave an empty trailing page just shy of totalCount:
// within 1% shortfall (at least 1) is kept; anything larger or a non-empty
// repeating page is still ErrShape.
func TestSmallShortfallOnEmptyTrailingPage(t *testing.T) {
	serve := func(total, listed int, trailingDup bool) *srctest.Doer {
		return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			u, _ := url.Parse(r.URL)
			p, _ := strconv.Atoi(u.Query().Get("page"))
			start := (p - 1) * pageSize
			if start >= listed {
				if trailingDup && listed > 0 {
					return srctest.Reply{Status: 200, Body: []byte(page(total, []string{row("0")}))}, true
				}
				return srctest.Reply{Status: 200, Body: []byte(page(total, nil))}, true
			}
			end := min(listed, start+pageSize)
			var rows []string
			for i := start; i < end; i++ {
				rows = append(rows, row(strconv.Itoa(i)))
			}
			return srctest.Reply{Status: 200, Body: []byte(page(total, rows))}, true
		})
	}
	// 2 missing of 205 (within shortfall(205)=2): kept.
	if got, err := withDoer(serve(205, 203, false)).List(context.Background(), board); err != nil || len(got) != 203 {
		t.Fatalf("2 missing of 205: got %d postings, err %v; want 203", len(got), err)
	}
	// 3 missing of 205 (past shortfall(205)=2): ErrShape.
	if _, err := withDoer(serve(205, 202, false)).List(context.Background(), board); !errors.Is(err, ErrShape) {
		t.Fatalf("3 missing of 205: err = %v, want ErrShape", err)
	}
	// Duplicate non-empty trailing page even when within shortfall: ErrShape.
	if _, err := withDoer(serve(205, 203, true)).List(context.Background(), board); !errors.Is(err, ErrShape) {
		t.Fatalf("repeating trailing page: err = %v, want ErrShape", err)
	}
}

func TestEmptySearchIsAnEmptyBoard(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{page1: {Status: 200, Body: []byte(page(0, nil))}})
	got, err := withDoer(d).List(context.Background(), board)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d postings, %v; want an empty listing", len(got), err)
	}
}

func TestTransportAndStatusFailures(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"bad filter value is a 400": {Status: 400, Body: []byte(`{}`)},
		"not found":                 {Status: 404},
		"server error":              {Status: 500},
		"not json":                  {Status: 200, Body: []byte(`<html>`)},
		"truncated json":            {Status: 200, Body: []byte(`{"totalCount":3,"jobs":[`)},
		"timeout":                   {Err: context.DeadlineExceeded},
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.New(map[string]srctest.Reply{page1: rep})
			if got, err := withDoer(d).List(context.Background(), board); err == nil {
				t.Fatalf("got %d postings and no error", len(got))
			}
		})
	}
}

func TestBoardChecksSendNothing(t *testing.T) {
	for name, mod := range map[string]func(*sources.Board){
		"other ats":         func(b *sources.Board) { b.ATS = posting.RemoteOK },
		"other token":       func(b *sources.Board) { b.Token = "acme" },
		"upper-case token":  func(b *sources.Board) { b.Token = "Himalayas" },
		"no search":         func(b *sources.Board) { b.Search = "" },
		"unknown filter":    func(b *sources.Board) { b.Filters = map[string][]string{"timezone": {"-5"}} },
		"two filter values": func(b *sources.Board) { b.Filters = map[string][]string{"country": {"US", "CA"}} },
		"blank filter":      func(b *sources.Board) { b.Filters = map[string][]string{"country": {"  "}} },
		"max negative":      func(b *sources.Board) { b.MaxPostings = -1 },
		"max over ceiling":  func(b *sources.Board) { b.MaxPostings = sources.MaxPostingsCeiling + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			b := board
			mod(&b)
			d := srctest.New(nil)
			if _, err := withDoer(d).List(context.Background(), b); err == nil {
				t.Fatal("no error")
			}
			if n := len(d.URLs()); n != 0 {
				t.Fatalf("%d requests sent for a refused board", n)
			}
		})
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := srctest.New(nil)
	if _, err := withDoer(d).List(ctx, board); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(d.URLs()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

func TestFiltersAndTermReachTheQuery(t *testing.T) {
	b := board
	b.Search = `"ml engineer" & co`
	b.Filters = map[string][]string{"country": {"US"}, "seniority": {"Senior"}, "employment_type": {"Full Time"}}
	want := "https://himalayas.app/jobs/api/search?country=US&employment_type=Full+Time&page=1&q=%22ml+engineer%22+%26+co&seniority=Senior"
	d := srctest.New(map[string]srctest.Reply{want: {Status: 200, Body: []byte(page(0, nil))}})
	if _, err := withDoer(d).List(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func TestBadRows(t *testing.T) {
	rowstest.Check(t, rowstest.Case{
		List: func(ctx context.Context, payload string) ([]posting.Posting, error) {
			return withDoer(srctest.New(map[string]srctest.Reply{page1: {Status: 200, Body: []byte(payload)}})).List(ctx, board)
		},
		Payload: func(n int, bad map[int]string) string {
			rows := make([]string, n)
			for i := range rows {
				rows[i] = row(strconv.Itoa(i))
				if raw, ok := bad[i]; ok {
					rows[i] = raw
				}
			}
			return page(n, rows)
		},
		BadRows: map[string]string{
			"guid on another host":  `{"title":"T","companyName":"C","guid":"https://evil.example/companies/c/jobs/t-` + rowstest.Index + `"}`,
			"guid with a colon":     `{"title":"T","companyName":"C","guid":"https://himalayas.app/companies/c/jobs/a:b` + rowstest.Index + `"}`,
			"empty title":           `{"title":"  ","companyName":"C","guid":"https://himalayas.app/companies/c/jobs/t-` + rowstest.Index + `"}`,
			"empty company":         `{"title":"T","companyName":" ","guid":"https://himalayas.app/companies/c/jobs/t-` + rowstest.Index + `"}`,
			"company slug mismatch": `{"title":"T","companyName":"C","companySlug":"other","guid":"https://himalayas.app/companies/c/jobs/t-` + rowstest.Index + `"}`,
			"javascript apply link": `{"title":"T","companyName":"C","applicationLink":"javascript:alert(1)","guid":"https://himalayas.app/companies/c/jobs/t-` + rowstest.Index + `"}`,
		},
	})
}

func TestHugeLocationListIsKeptWhole(t *testing.T) {
	var places []string
	for i := 0; i < 80; i++ {
		places = append(places, fmt.Sprintf("%q", fmt.Sprintf("Country %d", i)))
	}
	places = append(places, `"United States"`)
	body := page(1, []string{`{"title":"T","companyName":"C","guid":"https://himalayas.app/companies/c/jobs/t","locationRestrictions":[` + strings.Join(places, ",") + `]}`})
	got, err := withDoer(srctest.New(map[string]srctest.Reply{page1: {Status: 200, Body: []byte(body)}})).List(context.Background(), board)
	if err != nil || len(got) != 1 || !strings.HasSuffix(got[0].Location, "; United States") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPagesArePaced(t *testing.T) {
	var waits []time.Duration
	s := withDoer(pager(t, func(string) int { return 45 }, func(_ string, i int) string { return row(strconv.Itoa(i)) }))
	s.wait = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 {
		t.Fatalf("waits = %v, want 2 gaps across 3 pages", waits)
	}
	for _, w := range waits {
		if w != paceEvery {
			t.Fatalf("wait = %v, want %v", w, paceEvery)
		}
	}
}
