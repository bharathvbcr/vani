package smartrecruiters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func offsets(a *api) []string {
	var out []string
	for _, q := range a.listQueries() {
		out = append(out, q.Get("offset"))
	}
	return out
}

func TestPagesByHundredsUntilTheCount(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want []string
	}{
		{1, []string{"0"}},
		{99, []string{"0"}},
		{100, []string{"0"}}, // a total that is a whole page needs no page past it
		{101, []string{"0", "100"}},
		{200, []string{"0", "100"}},
		{250, []string{"0", "100", "200"}},
	} {
		a := board(tc.n)
		got, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
		if err != nil || len(got) != tc.n {
			t.Fatalf("n=%d: %d postings, %v", tc.n, len(got), err)
		}
		if g := offsets(a); !slices.Equal(g, tc.want) {
			t.Errorf("n=%d: listing offsets %v, want %v", tc.n, g, tc.want)
		}
		for _, q := range a.listQueries() {
			if q.Get("limit") != "100" {
				t.Errorf("n=%d: limit %q, want 100 (the API clamps a larger one)", tc.n, q.Get("limit"))
			}
		}
	}
}

func TestListingKeepsListingOrder(t *testing.T) {
	a := board(130)
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range got {
		if want := posting.Key("sr:servicenow:" + idOf(i)); p.Key != want {
			t.Fatalf("posting %d is %q, want %q", i, p.Key, want)
		}
	}
}

func TestDuplicateIDsAreOnePosting(t *testing.T) {
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) {
		return okReply(page(3, []string{idOf(0), idOf(1), idOf(0)})), true
	}
	got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d postings, want the repeated id once", len(got))
	}
	if ids := a.fetchedIDs(); len(ids) != 2 {
		t.Fatalf("%d detail requests for 2 distinct ids: %v", len(ids), ids)
	}
}

func TestMalformedAnswersFailTheBoard(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         "{not json <html>",
		"html error page":  "<html><body>Error</body></html>",
		"empty body":       "",
		"null":             "null",
		"no content":       `{"totalFound":2}`,
		"null content":     `{"totalFound":2,"content":null}`,
		"no total":         `{"content":[]}`,
		"null total":       `{"totalFound":null,"content":[]}`,
		"negative total":   `{"totalFound":-1,"content":[]}`,
		"wrong total type": `{"totalFound":"2","content":[]}`,
		"row is a string":  `{"totalFound":1,"content":["x"]}`,
	} {
		a := board(0)
		a.list = func(string, int) (srctest.Reply, bool) { return okReply(body), true }
		got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
		if err == nil {
			t.Errorf("%s: got %d postings and no error; a bad answer is never an empty board", name, len(got))
		}
		if errors.Is(err, srcutil.ErrBoardNotFound) {
			t.Errorf("%s: read as board-not-found: %v", name, err)
		}
	}
}

func TestMissingContentIsAShapeError(t *testing.T) {
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) { return okReply(`{"totalFound":0}`), true }
	_, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if !errors.Is(err, ErrShape) {
		t.Fatalf("err = %v, want ErrShape", err)
	}
}

func TestHostileRowIDsFailTheBoard(t *testing.T) {
	for _, id := range []string{"", "../x", "a/b", "a b", "a:b", "x?y", "%2e%2e", strings.Repeat("9", 65)} {
		a := board(0)
		a.list = func(string, int) (srctest.Reply, bool) {
			return okReply(page(1, []string{id})), true
		}
		_, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
		if !errors.Is(err, ErrShape) {
			t.Errorf("id %q: err = %v, want ErrShape", id, err)
		}
		if len(a.fetchedIDs()) != 0 {
			t.Errorf("id %q: a detail was requested: %v", id, a.fetchedIDs())
		}
	}
}

func TestBadReleasedDateFailsTheBoard(t *testing.T) {
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) {
		return okReply(strings.Replace(page(1, []string{idOf(0)}), stamp, "yesterday", 1)), true
	}
	if _, err := src(t, a.doer(), nil).List(context.Background(), srBoard()); !errors.Is(err, ErrShape) {
		t.Fatalf("err = %v, want ErrShape", err)
	}
}

func TestOversizedListingBodyFails(t *testing.T) {
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) {
		return okReply(`{"totalFound":0,"content":[],"pad":"` + strings.Repeat("x", listMaxBody) + `"}`), true
	}
	_, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestServerErrorsAreErrorsNeverAnEmptyListing(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"404":  notFound(),
		"500":  {Status: 500, Body: []byte("{}")},
		"502":  {Status: 502},
		"403":  {Status: 403, Body: []byte("{}")},
		"400":  {Status: 400, Body: []byte(`{"code":"VALIDATION_ERROR"}`)},
		"429":  {Status: 429, Header: http.Header{"Retry-After": {"3600"}}},
		"410":  {Status: 410},
		"html": okReply("<html>maintenance</html>"),
	} {
		a := board(0)
		a.list = func(string, int) (srctest.Reply, bool) { return rep, true }
		got, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
		if err == nil {
			t.Errorf("%s: got %d postings and no error", name, len(got))
		}
	}
	// The one the contract names: a 404 board is ErrBoardNotFound.
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) { return notFound(), true }
	if _, err := src(t, a.doer(), nil).List(context.Background(), srBoard()); !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("404 listing: err = %v, want ErrBoardNotFound", err)
	}
}

type failDoer struct{ err error }

func (f failDoer) Do(context.Context, *http.Request) (*http.Response, error) { return nil, f.err }

var _ egress.Doer = failDoer{}

func TestDependencyDownAndTimeoutAreErrors(t *testing.T) {
	for name, e := range map[string]error{
		"connection refused": errors.New("dial tcp: connection refused"),
		"timeout":            context.DeadlineExceeded,
	} {
		s := &source{d: failDoer{e}, now: func() time.Time { return fixedNow }, wait: noWait}
		got, err := s.List(context.Background(), srBoard())
		if err == nil || !errors.Is(err, e) && name == "timeout" {
			t.Errorf("%s: %d postings, err = %v", name, len(got), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := board(5)
	d := a.doer()
	if _, err := src(t, d, nil).List(ctx, srBoard()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: err = %v", err)
	}
	if len(d.URLs()) != 0 {
		t.Fatalf("requests sent on a cancelled context: %v", d.URLs())
	}
}

func TestCancelledMidListingStops(t *testing.T) {
	a := board(450)
	ctx, cancel := context.WithCancel(context.Background())
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		if offset == 100 {
			cancel()
		}
		return srctest.Reply{}, false
	}
	_, err := src(t, a.doer(), nil).List(ctx, srBoard())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(a.listQueries()); n > 2 {
		t.Fatalf("%d listing requests after the cancel at offset 100", n)
	}
}

// A server that ignores the offset serves page 1 again: a wrap, never more
// rows. The listing must fail rather than return page 1 as the board.
func TestWrapIsDetected(t *testing.T) {
	a := board(250)
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		return okReply(page(250, board(250).ids("")[:100])), true
	}
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if !errors.Is(err, errIncomplete) {
		t.Fatalf("got %d postings, err = %v; want errIncomplete", len(got), err)
	}
	if len(a.fetchedIDs()) != 0 {
		t.Fatal("details were fetched for a listing that failed")
	}
}

// A page that is not the first again, but holds nothing new, is a stuck cursor.
func TestRepeatedPageIsDetected(t *testing.T) {
	a := board(250)
	ids := board(250).ids("")
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		if offset == 0 {
			return okReply(page(250, ids[:100])), true
		}
		rot := append(append([]string{}, ids[50:100]...), ids[:50]...) // seen ids, another first row
		return okReply(page(250, rot)), true
	}
	_, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if !errors.Is(err, errIncomplete) || !strings.Contains(err.Error(), "nothing new") {
		t.Fatalf("err = %v, want a nothing-new errIncomplete", err)
	}
}

// The API clamps limit to 100; a server that serves fewer rows than asked for
// must still be walked from the rows read, not from the page size.
func TestShortPagesAreWalkedFromTheRowsRead(t *testing.T) {
	ids := board(120).ids("")
	a := board(0)
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		return okReply(page(120, ids[min(offset, 120):min(offset+50, 120)])), true
	}
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if err != nil || len(got) != 120 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if g := offsets(a); !slices.Equal(g, []string{"0", "50", "100"}) {
		t.Fatalf("offsets %v, want 0 50 100", g)
	}
}

func TestListingThatEndsShortFails(t *testing.T) {
	ids := board(250).ids("")
	for name, tc := range map[string]struct {
		serve   int // rows the board really serves
		wantErr bool
	}{
		"all":                {250, false},
		"one closed":         {249, false},
		"two closed":         {248, false},
		"three closed":       {247, true},
		"only first page":    {100, true},
		"half":               {125, true},
		"count 250, none":    {0, true},
		"all but ten":        {240, true},
		"one over the count": {251, false},
	} {
		a := board(0)
		a.list = func(_ string, offset int) (srctest.Reply, bool) {
			hi := min(offset+100, tc.serve)
			if offset >= hi {
				return okReply(page(250, nil)), true
			}
			return okReply(page(250, idsFrom(ids, tc.serve)[offset:hi])), true
		}
		got, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
		switch {
		case tc.wantErr && !errors.Is(err, errIncomplete):
			t.Errorf("%s: %d postings, err = %v; want errIncomplete", name, len(got), err)
		case !tc.wantErr && err != nil:
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// idsFrom is the first n ids of the board, extended when n is over len(ids).
func idsFrom(ids []string, n int) []string {
	out := append([]string(nil), ids[:min(n, len(ids))]...)
	for i := len(ids); i < n; i++ {
		out = append(out, idOf(i))
	}
	return out
}

func TestFirstPageEmptyWithACountFails(t *testing.T) {
	// One posting counted, none served: the one-row tolerance must not read it
	// as an empty board.
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) { return okReply(page(1, nil)), true }
	_, err := src(t, a.doer(), nil).List(context.Background(), srBoard())
	if !errors.Is(err, errIncomplete) {
		t.Fatalf("err = %v, want errIncomplete", err)
	}
}

func TestTotalOverTheCapFailsBeforePaging(t *testing.T) {
	a := board(0)
	a.list = func(string, int) (srctest.Reply, bool) {
		return okReply(page(MaxPostings+1, board(100).ids(""))), true
	}
	_, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err = %v, want ErrTooManyPostings", err)
	}
	if n := len(a.listQueries()); n != 1 || len(a.fetchedIDs()) != 0 {
		t.Fatalf("%d listing requests and %d details after an over-cap total", n, len(a.fetchedIDs()))
	}
}

func TestEndlessListingIsBounded(t *testing.T) {
	// Every page is new rows and the count is never reached: the page bound ends it.
	// The count is within the cap but no page serves enough rows to reach it
	// within the page bound, and none is empty or repeats.
	a := board(0)
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		ids := make([]string, 50)
		for i := range ids {
			ids[i] = strconv.Itoa(idBase + offset + i)
		}
		return okReply(page(MaxPostings, ids)), true
	}
	_, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if !errors.Is(err, errIncomplete) {
		t.Fatalf("err = %v, want errIncomplete for a listing that never ends", err)
	}
	if n := len(a.listQueries()); n > maxPages {
		t.Fatalf("%d listing requests, more than the %d-page bound", n, maxPages)
	}
}

func TestTotalIsReadFromTheFirstPageOnly(t *testing.T) {
	// Later pages that claim a larger total must not extend the walk.
	a := board(0)
	ids := board(150).ids("")
	a.list = func(_ string, offset int) (srctest.Reply, bool) {
		if offset == 0 {
			return okReply(page(150, ids[:100])), true
		}
		return okReply(page(9999, ids[100:])), true
	}
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard())
	if err != nil || len(got) != 150 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if n := len(a.listQueries()); n != 2 {
		t.Fatalf("%d listing requests, want 2", n)
	}
}

// ---- country filter ----

func countryBoard(vals ...string) sources.Board {
	b := srBoard()
	b.Filters = map[string][]string{"country": vals}
	return b
}

func TestCountryFilterIsSentLowerCaseOneQueryPerValue(t *testing.T) {
	a := board(0)
	a.ids = func(cc string) []string {
		switch cc {
		case "us":
			return []string{idOf(1), idOf(2), idOf(3)}
		case "ca":
			return []string{idOf(3), idOf(4)} // 3 is in both: one posting
		}
		return nil
	}
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), countryBoard("US", " ca ", "us"))
	if err != nil {
		t.Fatal(err)
	}
	var ccs []string
	for _, q := range a.listQueries() {
		ccs = append(ccs, q.Get("country"))
	}
	if !slices.Equal(ccs, []string{"ca", "us"}) {
		t.Fatalf("country parameters %v, want ca then us once each, lower-case", ccs)
	}
	var keys []string
	for _, p := range got {
		keys = append(keys, string(p.Key))
	}
	want := []string{}
	for _, i := range []int{3, 4, 1, 2} { // ca's rows first, then us's new ones
		want = append(want, "sr:servicenow:"+idOf(i))
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("postings %v, want the union %v", keys, want)
	}
	if ids := a.fetchedIDs(); len(ids) != 4 {
		t.Fatalf("%d detail requests for 4 distinct postings: %v", len(ids), ids)
	}
}

func TestUnfilteredBoardSendsNoCountry(t *testing.T) {
	a := board(2)
	if _, err := src(t, a.doer(), &Options{}).List(context.Background(), srBoard()); err != nil {
		t.Fatal(err)
	}
	for _, q := range a.listQueries() {
		if q.Has("country") {
			t.Fatalf("unfiltered listing sent %v", q)
		}
	}
}

func TestCountryFilterPagesEachCountry(t *testing.T) {
	a := board(0)
	a.ids = func(cc string) []string {
		n := map[string]int{"us": 205, "ca": 95}[cc]
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d", map[string]string{"us": "1", "ca": "2"}[cc], 100000+i)
		}
		return out
	}
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), countryBoard("us", "ca"))
	if err != nil || len(got) != 300 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	type q struct{ cc, off string }
	var seen []q
	for _, v := range a.listQueries() {
		seen = append(seen, q{v.Get("country"), v.Get("offset")})
	}
	want := []q{{"ca", "0"}, {"us", "0"}, {"us", "100"}, {"us", "200"}}
	if !slices.Equal(seen, want) {
		t.Fatalf("queries %v, want %v", seen, want)
	}
}

// Each of these answers zero postings from the real API; sending one would
// make the reconciler age the whole board.
func TestCountryFilterValuesTheAPIWouldEmptyAreRefused(t *testing.T) {
	for name, vals := range map[string][]string{
		"three letters":  {"usa"},
		"comma list":     {"us,ca"},
		"empty":          {""},
		"blank":          {"  "},
		"digit":          {"u1"},
		"one letter":     {"u"},
		"name":           {"United States"},
		"one bad of two": {"us", "xx1"},
		"unicode":        {"üü"},
		"too many":       {"aa", "ab", "ac", "ad", "ae", "af", "ag", "ah", "ai", "aj", "ak"},
	} {
		a := board(3)
		d := a.doer()
		if _, err := src(t, d, &Options{}).List(context.Background(), countryBoard(vals...)); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if len(d.URLs()) != 0 {
			t.Errorf("%s: requests sent: %v", name, d.URLs())
		}
	}
	// A filter key with no values is refused by sources.CheckFilters too.
	a := board(3)
	b := srBoard()
	b.Filters = map[string][]string{"country": nil}
	if _, err := src(t, a.doer(), &Options{}).List(context.Background(), b); err == nil {
		t.Error("a country filter with no value must fail")
	}
}

func TestFilteredToNothingIsAnEmptyListing(t *testing.T) {
	a := board(0)
	a.ids = func(string) []string { return nil }
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), countryBoard("zz"))
	if err != nil || len(got) != 0 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
}

func TestAnyQueryFailingFailsTheBoard(t *testing.T) {
	// us lists fine, ca answers 500: the board's union would lack ca's postings,
	// which the reconciler would age.
	a := board(5)
	a.list = func(cc string, _ int) (srctest.Reply, bool) {
		if cc == "ca" {
			return srctest.Reply{Status: 500, Body: []byte("{}")}, true
		}
		return srctest.Reply{}, false
	}
	got, err := src(t, a.doer(), &Options{}).List(context.Background(), countryBoard("us", "ca"))
	if err == nil {
		t.Fatalf("got %d postings and no error from a half-read board", len(got))
	}
}

func TestStatsCountListingRequests(t *testing.T) {
	var st Stats
	a := board(250)
	s := src(t, a.doer(), &Options{Report: func(_ sources.Board, s Stats) { st = s }})
	if _, err := s.List(context.Background(), srBoard()); err != nil {
		t.Fatal(err)
	}
	if st.Listed != 250 || st.Requests != 3 || st.Fetched != 250 {
		t.Fatalf("stats %+v", st)
	}
}
