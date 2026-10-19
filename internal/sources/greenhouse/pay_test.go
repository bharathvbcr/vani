package greenhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func listURL(token string) string {
	return "https://boards-api.greenhouse.io/v1/boards/" + token + "/jobs?content=true"
}

func payURL(token, id string) string {
	return "https://boards-api.greenhouse.io/v1/boards/" + token + "/jobs/" + id + "?pay_transparency=true"
}

// listOf is a board list of jobs with ids, each with content "Body <id>.".
func listOf(ids ...string) string {
	var sb strings.Builder
	sb.WriteString(`{"jobs":[`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%s,"title":"T","absolute_url":"https://x.test/%s","content":"Body %s."}`, id, id, id)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

// paySource is a pay-enabled source over d, with stored postings and a
// recorded report.
type paySource struct {
	*source
	mu      sync.Mutex
	stored  map[posting.Key]posting.Posting
	reports []PayStats
}

func newPaySource(t *testing.T, d interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}) *paySource {
	t.Helper()
	ps := &paySource{stored: map[posting.Key]posting.Posting{}}
	src, err := NewWithPay(d, Pay{
		Known: func(k posting.Key) (posting.Posting, bool) {
			ps.mu.Lock()
			defer ps.mu.Unlock()
			p, ok := ps.stored[k]
			return p, ok
		},
		Report: func(_ sources.Board, st PayStats) {
			ps.mu.Lock()
			defer ps.mu.Unlock()
			ps.reports = append(ps.reports, st)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ps.source = src.(*source)
	ps.source.now = func() time.Time { return fixedNow }
	return ps
}

func (ps *paySource) store(ps2 []posting.Posting) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, p := range ps2 {
		ps.stored[p.Key] = p
	}
}

func (ps *paySource) lastReport(t *testing.T) PayStats {
	t.Helper()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.reports) == 0 {
		t.Fatal("no pay report")
	}
	return ps.reports[len(ps.reports)-1]
}

func payRequests(d *srctest.Doer) int {
	n := 0
	for _, u := range d.URLs() {
		if strings.Contains(u, "pay_transparency") {
			n++
		}
	}
	return n
}

// TestPayFixture: real single-job responses become pay lines ahead of the
// description, and jobinsight.ParsePay (what `vani jobs list --json` shows as
// pay) reads them back.
func TestPayFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/pay.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		token, id string
		want      string
		pay       []jobinsight.Pay
	}{
		{"anthropic", "4461450008", "Pay range: USD 222800 – 290000 per year\n\nBody 4461450008.",
			[]jobinsight.Pay{{Currency: "USD", Period: jobinsight.Year, Min: 222800, Max: 290000}}},
		{"anthropic", "5391313008", "Pay range: EUR 155000 – 205000 per year\n\nBody 5391313008.",
			[]jobinsight.Pay{{Currency: "EUR", Period: jobinsight.Year, Min: 155000, Max: 205000}}},
		{"anthropic", "5183044008", "Body 5183044008.", nil},
		// Three location zones, titled by place: no period word, so none is
		// written and the year-sized amounts read as per year. One currency's
		// bands become one envelope.
		{"robinhood", "6669758", "Pay range: USD 166000 – 195000\nPay range: USD 147000 – 172000\nPay range: USD 130000 – 152000\n\nBody 6669758.",
			[]jobinsight.Pay{{Currency: "USD", Period: jobinsight.Year, Min: 130000, Max: 195000}}},
	}
	for _, c := range cases {
		t.Run(c.token+"/"+c.id, func(t *testing.T) {
			job, ok := fixture[c.token+"/"+c.id]
			if !ok {
				t.Fatal("not in fixture")
			}
			d := srctest.New(map[string]srctest.Reply{
				listURL(c.token):      {Status: 200, Body: []byte(listOf(c.id))},
				payURL(c.token, c.id): {Status: 200, Body: job},
			})
			ps := newPaySource(t, d)
			got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: c.token})
			if err != nil {
				t.Fatal(err)
			}
			if got[0].Description != c.want {
				t.Fatalf("description %q, want %q", got[0].Description, c.want)
			}
			pays := jobinsight.ParsePay(got[0].Description)
			if len(pays) != len(c.pay) {
				t.Fatalf("ParsePay read %+v, want %+v", pays, c.pay)
			}
			for i, w := range c.pay {
				g := pays[i]
				if g.Currency != w.Currency || g.Period != w.Period || g.Min != w.Min || g.Max != w.Max {
					t.Errorf("ParsePay %d: %+v, want %+v", i, g, w)
				}
			}
			st := ps.lastReport(t)
			wantPay := 0
			if c.pay != nil {
				wantPay = 1
			}
			if st.Fetched != 1 || st.WithPay != wantPay || st.Reused != 0 || st.Failed != 0 || st.Skipped != 0 {
				t.Errorf("stats %+v", st)
			}
		})
	}
}

// TestPayOnlyForNewOrChanged: a posting whose content is unchanged since it
// was stored keeps its stored description, pay line or not, with no request;
// a new or changed one is fetched once.
func TestPayOnlyForNewOrChanged(t *testing.T) {
	const tok = "acme"
	b := sources.Board{ATS: posting.Greenhouse, Token: tok}
	payBody := []byte(`{"pay_input_ranges":[{"min_cents":10000000,"max_cents":12000000,"currency_type":"USD","title":"Annual"}]}`)
	d := srctest.New(map[string]srctest.Reply{
		listURL(tok):     {Status: 200, Body: []byte(listOf("1", "2", "3"))},
		payURL(tok, "1"): {Status: 200, Body: payBody},
		payURL(tok, "2"): {Status: 200, Body: payBody},
		payURL(tok, "3"): {Status: 200, Body: payBody},
	})
	ps := newPaySource(t, d)

	// Run 1: all new, all fetched.
	first, err := ps.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if n := payRequests(d); n != 3 {
		t.Fatalf("run 1 made %d pay requests, want 3", n)
	}
	const line = "Pay range: USD 100000 – 120000 per year\n\n"
	for i, p := range first {
		if want := line + fmt.Sprintf("Body %d.", i+1); p.Description != want {
			t.Fatalf("run 1 posting %d: %q", i, p.Description)
		}
	}
	ps.store(first)
	// A posting stored before pay existed: same content, no pay line.
	legacy := posting.Posting{Key: "gh:acme:3", Description: "Body 3."}
	ps.store([]posting.Posting{legacy})

	// Run 2: nothing changed. Same descriptions, so the same hashes, and no
	// request.
	before := len(d.URLs())
	second, err := ps.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.URLs()) - before; n != 1 {
		t.Fatalf("run 2 made %d requests, want only the list", n)
	}
	want := []string{line + "Body 1.", line + "Body 2.", "Body 3."}
	for i, p := range second {
		if p.Description != want[i] {
			t.Errorf("run 2 posting %d: %q, want %q", i, p.Description, want[i])
		}
	}
	if st := ps.lastReport(t); st.Reused != 3 || st.Fetched != 0 {
		t.Errorf("run 2 stats %+v", st)
	}

	// Run 3: posting 2's content changed; only it is fetched.
	d.Replies[listURL(tok)] = srctest.Reply{Status: 200, Body: []byte(strings.Replace(listOf("1", "2", "3"), "Body 2.", "Body 2, revised.", 1))}
	before = len(d.URLs())
	third, err := ps.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	urls := d.URLs()[before:]
	if len(urls) != 2 || urls[1] != payURL(tok, "2") {
		t.Fatalf("run 3 requested %v", urls)
	}
	if third[1].Description != line+"Body 2, revised." {
		t.Errorf("run 3 posting 2: %q", third[1].Description)
	}
	if st := ps.lastReport(t); st.Reused != 2 || st.Fetched != 1 || st.WithPay != 1 {
		t.Errorf("run 3 stats %+v", st)
	}
}

// TestPayReuseNearDescriptionCap: with a description at the cap, the pay
// lines push its tail out; the stored description is still recognised as
// unchanged.
func TestPayReuseNearDescriptionCap(t *testing.T) {
	base := strings.TrimSpace(strings.Repeat("lorem ipsum ", srcutil.MaxDescription/12+10))
	if len(base) <= srcutil.MaxDescription {
		t.Fatal("base not over the cap")
	}
	lines := []string{"Pay range: USD 1 – 2", "Pay range: EUR 3 – 4"}
	capped := base[:srcutil.MaxDescription]
	stored := withPay(lines, capped)
	if len(stored) > srcutil.MaxDescription {
		t.Fatalf("stored %d bytes", len(stored))
	}
	got, ok := reuse(stored, capped)
	if !ok || got != stored {
		t.Fatalf("reuse = %v, %.60q", ok, got)
	}
	if _, ok := reuse(stored, "changed "+capped); ok {
		t.Fatal("a changed description was reused")
	}
	// A change only in the tail the pay lines push past the cap leaves the
	// stored description as it was, so it is no change (the cap hides it
	// either way).
	if got, ok := reuse(stored, capped[:len(capped)-20]+" changed tail"); !ok || got != stored {
		t.Fatal("a change past the cap counted as a change")
	}
	// A posting whose own text starts with a pay line, stored without one.
	own := "Pay range: USD 9 – 10\n\nBody."
	if got, ok := reuse(own, own); !ok || got != own {
		t.Fatalf("own pay text: %v %q", ok, got)
	}
}

// doerFunc is an egress.Doer from a function, for failures srctest cannot
// make: a transport error, or a reply that waits on the context.
type doerFunc func(context.Context, *http.Request) (*http.Response, error)

func (f doerFunc) Do(ctx context.Context, r *http.Request) (*http.Response, error) { return f(ctx, r) }

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader([]byte(body)))}
}

// listThen answers the list with list and every pay request with pay.
func listThen(list string, pay func(context.Context, *http.Request) (*http.Response, error)) doerFunc {
	return func(ctx context.Context, r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.RawQuery, "pay_transparency") {
			return pay(ctx, r)
		}
		return reply(200, list), nil
	}
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprint(i + 1)
	}
	return out
}

// TestPayFailuresKeepPostings: whatever goes wrong with a pay request, the
// board is read, every posting is kept without a pay line, and the failure is
// counted.
func TestPayFailuresKeepPostings(t *testing.T) {
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme"}
	cases := []struct {
		name  string
		pay   func(context.Context, *http.Request) (*http.Response, error)
		check func(PayStats) bool
		setup func(t *testing.T)
	}{
		{"endpoint down", func(context.Context, *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}, func(s PayStats) bool { return s.Failed == 1 && strings.Contains(s.FirstError, "connection refused") }, nil},
		{"404 is the posting, not the board", func(context.Context, *http.Request) (*http.Response, error) {
			return reply(404, `{"status":404}`), nil
		}, func(s PayStats) bool { return s.NotFound == 1 && s.Failed == 0 }, nil},
		{"500", func(context.Context, *http.Request) (*http.Response, error) {
			return reply(500, `{}`), nil
		}, func(s PayStats) bool { return s.Failed == 1 }, nil},
		{"not json", func(context.Context, *http.Request) (*http.Response, error) {
			return reply(200, `<html>`), nil
		}, func(s PayStats) bool { return s.Failed == 1 }, nil},
		{"ranges not an array", func(context.Context, *http.Request) (*http.Response, error) {
			return reply(200, `{"pay_input_ranges":{"min_cents":1}}`), nil
		}, func(s PayStats) bool { return s.Failed == 1 }, nil},
		{"oversized body", func(context.Context, *http.Request) (*http.Response, error) {
			return reply(200, `{"pay_input_ranges":[],"content":"`+strings.Repeat("x", 2048)+`"}`), nil
		}, func(s PayStats) bool {
			return s.Failed == 1 && strings.Contains(s.FirstError, srcutil.ErrBodyTooLarge.Error())
		}, func(t *testing.T) {
			old := maxPayBody
			maxPayBody = 1024
			t.Cleanup(func() { maxPayBody = old })
		}},
		{"timeout", func(ctx context.Context, _ *http.Request) (*http.Response, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, func(s PayStats) bool { return s.Failed == 1 && strings.Contains(s.FirstError, "deadline") }, func(t *testing.T) {
			old := payRequestTimeout
			payRequestTimeout = 20 * time.Millisecond
			t.Cleanup(func() { payRequestTimeout = old })
		}},
		{"null and missing ranges are no pay", func(context.Context, *http.Request) (*http.Response, error) {
			return reply(200, `{"pay_input_ranges":null}`), nil
		}, func(s PayStats) bool { return s.Fetched == 1 && s.WithPay == 0 && s.Failed == 0 }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			ps := newPaySource(t, listThen(listOf("7"), c.pay))
			got, err := ps.List(context.Background(), b)
			if err != nil {
				t.Fatalf("pay failure failed the board: %v", err)
			}
			if len(got) != 1 || got[0].Description != "Body 7." {
				t.Fatalf("got %+v", got)
			}
			if st := ps.lastReport(t); !c.check(st) {
				t.Errorf("stats %+v", st)
			}
		})
	}
}

// TestMalformedRanges: an unusable range is left out and counted; the usable
// ones stand. Duplicates are written once.
func TestMalformedRanges(t *testing.T) {
	body := `{"pay_input_ranges":[
		{"min_cents":"lots","max_cents":2,"currency_type":"USD"},
		{"min_cents":10000000,"max_cents":5000000,"currency_type":"USD"},
		{"min_cents":-1,"max_cents":0,"currency_type":"USD"},
		{"min_cents":10000000,"max_cents":12000000,"currency_type":"US$"},
		{"min_cents":10000000,"max_cents":12000000},
		7,
		{"min_cents":10000000,"max_cents":12000000,"currency_type":"usd","title":"Hourly? no: Daytona Beach"},
		{"min_cents":10000000,"max_cents":12000000,"currency_type":"USD","title":"Zone 1"},
		{"min_cents":4000,"max_cents":null,"currency_type":"GBP","title":"Hourly rate"}
	]}`
	ps := newPaySource(t, listThen(listOf("7"), func(context.Context, *http.Request) (*http.Response, error) {
		return reply(200, body), nil
	}))
	got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	// "Hourly? no: Daytona Beach" names "hourly" as a whole word, so it is
	// per hour; the place name alone would never read as "per day".
	want := "Pay range: USD 100000 – 120000 per hour\nPay range: USD 100000 – 120000\nPay range: GBP 40 per hour\n\nBody 7."
	if got[0].Description != want {
		t.Fatalf("description %q, want %q", got[0].Description, want)
	}
	if st := ps.lastReport(t); st.BadRanges != 6 || st.WithPay != 1 || st.Failed != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestRangeCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"pay_input_ranges":[`)
	for i := 0; i < maxPayRanges+5; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"min_cents":%d,"max_cents":%d,"currency_type":"USD"}`, (100000+i)*100, (110000+i)*100)
	}
	sb.WriteString(`]}`)
	ps := newPaySource(t, listThen(listOf("7"), func(context.Context, *http.Request) (*http.Response, error) {
		return reply(200, sb.String()), nil
	}))
	got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got[0].Description, srcutil.PayLinePrefix); n != maxPayRanges {
		t.Fatalf("%d pay lines, want %d", n, maxPayRanges)
	}
}

func TestPeriodIsWholeWord(t *testing.T) {
	cases := map[string]string{
		"Annual Salary:":             "year",
		"NYC Salary Range":           "",
		"Zone 3 (Lake Mary, FL)":     "",
		"Daytona Beach, FL":          "",
		"Holiday pay":                "",
		"Hourly Rate":                "hour",
		"Per-hour wage":              "hour",
		"Monthly stipend":            "month",
		"Yearly (Washington, DC)":    "year",
		"Daily rate for contractors": "day",
		"":                           "",
	}
	for title, want := range cases {
		if got := period(title); got != want {
			t.Errorf("period(%q) = %q, want %q", title, got, want)
		}
	}
}

// countingDoer counts pay requests and answers each with pay.
func countingDoer(n *int, mu *sync.Mutex, list string, pay func() (*http.Response, error)) doerFunc {
	return listThen(list, func(context.Context, *http.Request) (*http.Response, error) {
		mu.Lock()
		*n++
		mu.Unlock()
		return pay()
	})
}

func TestPerBoardCap(t *testing.T) {
	var (
		n  int
		mu sync.Mutex
	)
	ps := newPaySource(t, countingDoer(&n, &mu, listOf(ids(maxPayFetchesPerBoard+7)...), func() (*http.Response, error) {
		return reply(200, `{"pay_input_ranges":[]}`), nil
	}))
	got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme"})
	if err != nil || len(got) != maxPayFetchesPerBoard+7 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if n != maxPayFetchesPerBoard {
		t.Errorf("%d pay requests, want %d", n, maxPayFetchesPerBoard)
	}
	if st := ps.lastReport(t); st.Fetched != maxPayFetchesPerBoard || st.Skipped != 7 {
		t.Errorf("stats %+v", st)
	}
}

// TestDownEndpointStopsEarly: once a board's pay requests have failed
// maxPayFailuresPerBoard times, the rest are skipped, not tried.
func TestDownEndpointStopsEarly(t *testing.T) {
	var (
		n  int
		mu sync.Mutex
	)
	ps := newPaySource(t, countingDoer(&n, &mu, listOf(ids(60)...), func() (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme"})
	if err != nil || len(got) != 60 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if n < maxPayFailuresPerBoard || n > maxPayFailuresPerBoard+payFanOut-1 {
		t.Errorf("%d pay requests, want %d..%d", n, maxPayFailuresPerBoard, maxPayFailuresPerBoard+payFanOut-1)
	}
	if st := ps.lastReport(t); st.Failed != n || st.Failed+st.Skipped != 60 {
		t.Errorf("stats %+v after %d requests", st, n)
	}
}

// TestRunBudgetSpent: once the run's pay budget is spent, later boards make
// no pay request and their postings are counted as skipped.
func TestRunBudgetSpent(t *testing.T) {
	var (
		n  int
		mu sync.Mutex
	)
	ps := newPaySource(t, countingDoer(&n, &mu, listOf("1", "2"), func() (*http.Response, error) {
		return reply(200, `{"pay_input_ranges":[]}`), nil
	}))
	ps.pay.deadline = time.Now().Add(-time.Second)
	got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme"})
	if err != nil || len(got) != 2 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if n != 0 {
		t.Errorf("%d pay requests after the budget", n)
	}
	if st := ps.lastReport(t); st.Skipped != 2 {
		t.Errorf("stats %+v", st)
	}
}

// TestRunBudgetStartsOnce: the budget is fixed by the first pay request and
// not renewed per board.
func TestRunBudgetStartsOnce(t *testing.T) {
	p := &payer{}
	t0 := time.Now()
	first := p.runDeadline(t0)
	if second := p.runDeadline(t0.Add(time.Hour)); !second.Equal(first) || !first.Equal(t0.Add(payRunBudget)) {
		t.Fatalf("deadlines %v then %v", first, second)
	}
}

func TestCanceledRunFailsTheBoard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ps := newPaySource(t, listThen(listOf("1"), func(context.Context, *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	}))
	if _, err := ps.List(ctx, sources.Board{ATS: posting.Greenhouse, Token: "acme"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestNewWithPayNeedsKnown(t *testing.T) {
	if _, err := NewWithPay(srctest.New(nil), Pay{}); err == nil {
		t.Fatal("pay without stored postings accepted")
	}
}

func TestPayIDIsEscaped(t *testing.T) {
	const want = "https://boards-api.greenhouse.io/v1/boards/a%2Fb/jobs/12?pay_transparency=true"
	d := srctest.New(map[string]srctest.Reply{
		"https://boards-api.greenhouse.io/v1/boards/a%2Fb/jobs?content=true": {Status: 200, Body: []byte(listOf("12"))},
		want: {Status: 200, Body: []byte(`{}`)},
	})
	ps := newPaySource(t, d)
	if _, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "a/b"}); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 2 || u[1] != want {
		t.Fatalf("requested %v", u)
	}
}
