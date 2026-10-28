package avature

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

const (
	host     = "careers.twosigma.com"
	listBase = "https://" + host + "/careers/OpenRoles"
	// fixtureDetailID is the id in testdata/detail.html's canonical link.
	fixtureDetailID = "13045"
)

// robotsTxt is the live rules of 2026-10-27, cut to the careers group.
const robotsTxt = "User-agent: *\nAllow: /$\nAllow: /careers\nDisallow: /careers/*qtvc=\nAllow: /portal/106\n"

func board() sources.Board {
	return sources.Board{ATS: posting.Avature, Token: "twosigma", Company: "Two Sigma", Host: host, URL: listBase}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// card is one result article in the markup of the live page (list-page1.html).
func card(id, title, loc string) string {
	link := "https://" + host + "/careers/JobDetail/New-York-United-States-Role-" + id + "/" + id
	return `<article class="article article--result" id="article--` + id + `">
<div class="article__header"><div class="article__header__text">
<h3 class="article__header__text__title title title--065">
<a class="link" href="` + link + `">
` + title + `
</a>
</h3>
<div class="article__header__content"><div class="article__header__content__text">
<span class="paragraph_inner-span">` + loc + `</span>
<div class="article__header__content__sub-text">
<span class="paragraph_inner-span">Engineering</span>
<span class="paragraph_inner-span">Experienced</span>
</div></div>
<div class="article__footer">
<a class="button button--primary button--list-results--mobile" href="` + link + `" tabindex="0">
View role
</a>
<a class="button button--secondary button--list-results--desktop" href="` + link + `" tabindex="0">
View role
</a>
</div></div></div></div>
</article>`
}

func listPage(cards ...string) string {
	return `<html><body><div class="results results--listed"><div class="results__panel">` +
		strings.Join(cards, "\n") + `</div></div></body></html>`
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(14000 + i)
	}
	return out
}

// fake is a Two Sigma portal: ids served ten to a page by jobOffset (the
// jobRecordsPerPage it is sent is ignored, as the live site does), the empty
// marker page past the end, a detail page per id made from the fixture, and a
// robots.txt.
type fake struct {
	t   *testing.T
	ids []string

	robotsStatus int
	robots       string
	// listAt, when it answers for an offset, replaces that listing page.
	listAt func(offset int) (srctest.Reply, bool)
	// detailFor, when it answers for an id, replaces that detail page.
	detailFor func(id string) (srctest.Reply, bool)

	mu   sync.Mutex
	reqs []string
}

func newFake(t *testing.T, n int) *fake {
	return &fake{t: t, ids: ids(n), robotsStatus: 200, robots: robotsTxt}
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

func (f *fake) detailPage(id string) string {
	return strings.Replace(readFile(f.t, "detail.html"), "/"+fixtureDetailID+`"`, "/"+id+`"`, 1)
}

func (f *fake) handle(r srctest.Request) (srctest.Reply, bool) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != host || r.Method != "GET" {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return srctest.Reply{}, false
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, u.RequestURI())
	f.mu.Unlock()
	switch {
	case u.Path == "/robots.txt":
		return srctest.Reply{Status: f.robotsStatus, Body: []byte(f.robots)}, true
	case u.Path == "/careers/OpenRoles/":
		q := u.Query()
		if q.Get("jobRecordsPerPage") != "10" || len(q) != 2 {
			f.t.Errorf("list query %q, want exactly jobRecordsPerPage=10 and jobOffset", u.RawQuery)
		}
		off, err := strconv.Atoi(q.Get("jobOffset"))
		if err != nil {
			f.t.Errorf("jobOffset %q", q.Get("jobOffset"))
			return srctest.Reply{}, false
		}
		if f.listAt != nil {
			if rp, handled := f.listAt(off); handled {
				return rp, true
			}
		}
		if off >= len(f.ids) {
			return ok(readFile(f.t, "list-empty.html")), true
		}
		var cards []string
		for _, id := range f.ids[off:min(off+10, len(f.ids))] {
			cards = append(cards, card(id, "Role "+id, "United States - NY New York"))
		}
		return ok(listPage(cards...)), true
	case strings.HasPrefix(u.Path, "/careers/JobDetail/"):
		parts := strings.Split(u.Path, "/")
		id := parts[len(parts)-1]
		if f.detailFor != nil {
			if rp, handled := f.detailFor(id); handled {
				return rp, true
			}
		}
		return ok(f.detailPage(id)), true
	}
	f.t.Errorf("unexpected path %s", u.Path)
	return srctest.Reply{}, false
}

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func (f *fake) source() (*source, *srctest.Doer) {
	d := srctest.NewFunc(f.handle)
	return &source{d: d, now: func() time.Time { return fixedNow }, wait: noWait}, d
}

func (f *fake) list() ([]posting.Posting, error) {
	s, _ := f.source()
	return s.List(context.Background(), board())
}

func (f *fake) listOffsets() []string {
	var out []string
	for _, r := range f.reqs {
		if strings.HasPrefix(r, "/careers/OpenRoles/") {
			u, _ := url.Parse(r)
			out = append(out, u.Query().Get("jobOffset"))
		}
	}
	return out
}

func (f *fake) detailCount() int {
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, "/careers/JobDetail/") {
			n++
		}
	}
	return n
}

// ---- the live fixtures ----

// The real first page of Two Sigma's list (two of its ten cards), then the
// real "No jobs found" page, then the real detail page.
func TestListFromLiveFixtures(t *testing.T) {
	f := newFake(t, 0)
	f.listAt = func(off int) (srctest.Reply, bool) {
		if off == 0 {
			return ok(readFile(t, "list-page1.html")), true
		}
		return ok(readFile(t, "list-empty.html")), true
	}
	ps, err := f.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("%d postings, want 2", len(ps))
	}
	p := ps[0]
	if p.Key != "av:twosigma:14291" || p.ATS != posting.Avature || p.Company != "Two Sigma" {
		t.Errorf("identity %q %q %q", p.Key, p.ATS, p.Company)
	}
	if p.Title != "AI Fluency: Program Manager" {
		t.Errorf("title %q", p.Title)
	}
	if p.Location != "United States - NY New York" {
		t.Errorf("location %q", p.Location)
	}
	if p.Department != "Engineering" {
		t.Errorf("department %q, want the detail's Function field", p.Department)
	}
	if p.URL != "https://careers.twosigma.com/careers/JobDetail/New-York-United-States-AI-Fluency-Program-Manager/14291" {
		t.Errorf("url %q", p.URL)
	}
	if !p.PostedAt.IsZero() || !p.UpdatedAt.IsZero() {
		t.Errorf("posted %v updated %v: the site states no date", p.PostedAt, p.UpdatedAt)
	}
	if p.ApplyURL != "" || p.Remote || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("apply %q remote %v seen %v", p.ApplyURL, p.Remote, p.SeenAt)
	}
	if ps[1].Key != "av:twosigma:13671" || ps[1].Title != "AI Research Scientist - Campus Full-Time" {
		t.Errorf("second %q %q", ps[1].Key, ps[1].Title)
	}
	if !strings.HasPrefix(p.Description, "Pay range: USD 165000 – 250000\n\n") {
		t.Errorf("description opens %q", firstLine(p.Description))
	}
	body := srcutil.SourceText(p.Description)
	if !strings.Contains(body, "Two Sigma is a leading quantitative investment management") ||
		!strings.Contains(body, "The base pay for this role will be between $165,000 and $250,000.") {
		t.Errorf("description lacks the posting text: %q", body)
	}
	if strings.ContainsAny(p.Description, "<>") || strings.Contains(p.Description, "Back to Open Roles") {
		t.Errorf("markup or page chrome in the description")
	}
	if pay := jobinsight.ParsePay(p.Description); len(pay) == 0 {
		t.Errorf("the pay line must read back through jobinsight.ParsePay")
	}
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

func TestListPageFixtureCards(t *testing.T) {
	st := newSite(host, ListPage{Portal: "careers", Path: "/careers/OpenRoles"}, nil)
	rows, end, err := st.parseListing(readFile(t, "list-page1.html"))
	if err != nil || end || len(rows) != 2 {
		t.Fatalf("rows %d end %v err %v", len(rows), end, err)
	}
	if rows[0].id != "14291" || rows[0].title != "AI Fluency: Program Manager" || rows[0].location != "United States - NY New York" {
		t.Errorf("first row %+v", rows[0])
	}
	rows, end, err = st.parseListing(readFile(t, "list-empty.html"))
	if err != nil || !end || len(rows) != 0 {
		t.Errorf("empty page: rows %d end %v err %v", len(rows), end, err)
	}
}

// ---- paging ----

func TestPaging(t *testing.T) {
	for _, tc := range []struct {
		n       int
		offsets []string
	}{
		{25, []string{"0", "10", "20", "25"}}, // the offset steps by the cards a page held
		{20, []string{"0", "10", "20"}},       // a full last page: the marker page ends it
		{10, []string{"0", "10"}},
		{1, []string{"0", "1"}},
	} {
		f := newFake(t, tc.n)
		ps, err := f.list()
		if err != nil || len(ps) != tc.n {
			t.Fatalf("n=%d: %d postings, %v", tc.n, len(ps), err)
		}
		if got := strings.Join(f.listOffsets(), ","); got != strings.Join(tc.offsets, ",") {
			t.Errorf("n=%d: list offsets %s, want %s", tc.n, got, strings.Join(tc.offsets, ","))
		}
		if f.detailCount() != tc.n {
			t.Errorf("n=%d: %d detail requests", tc.n, f.detailCount())
		}
		seen := map[posting.Key]bool{}
		for _, p := range ps {
			if seen[p.Key] {
				t.Errorf("n=%d: key %s twice", tc.n, p.Key)
			}
			seen[p.Key] = true
		}
	}
}

// A site that serves 7 cards where 10 were asked for must not leave a gap: the
// offset advances by what the page held.
func TestOffsetAdvancesByCardsHeld(t *testing.T) {
	f := newFake(t, 21)
	f.listAt = func(off int) (srctest.Reply, bool) {
		if off >= len(f.ids) {
			return srctest.Reply{}, false
		}
		var cards []string
		for _, id := range f.ids[off:min(off+7, len(f.ids))] {
			cards = append(cards, card(id, "Role "+id, "United States - NY New York"))
		}
		return ok(listPage(cards...)), true
	}
	ps, err := f.list()
	if err != nil || len(ps) != 21 {
		t.Fatalf("%d postings, %v; want 21", len(ps), err)
	}
	if got := strings.Join(f.listOffsets(), ","); got != "0,7,14,21" {
		t.Errorf("offsets %s", got)
	}
}

func TestEmptyBoardIsNotAnError(t *testing.T) {
	f := newFake(t, 0)
	ps, err := f.list()
	if err != nil || len(ps) != 0 {
		t.Fatalf("%d postings, %v; an empty board is a valid result", len(ps), err)
	}
	if f.detailCount() != 0 {
		t.Errorf("%d details for an empty board", f.detailCount())
	}
}

func TestDuplicateCardsListOnce(t *testing.T) {
	f := newFake(t, 0)
	f.listAt = func(off int) (srctest.Reply, bool) {
		switch off {
		case 0: // 14000 twice on one page, then 14001
			return ok(listPage(card("14000", "Role 14000", "L"), card("14000", "Role 14000", "L"), card("14001", "Role 14001", "L"))), true
		case 3: // an overlapping page: 14001 again and 14002 new
			return ok(listPage(card("14001", "Role 14001", "L"), card("14002", "Role 14002", "L"))), true
		}
		return srctest.Reply{}, false
	}
	ps, err := f.list()
	if err != nil || len(ps) != 3 {
		t.Fatalf("%d postings, %v", len(ps), err)
	}
	if f.detailCount() != 3 {
		t.Errorf("%d detail requests for 3 distinct ids", f.detailCount())
	}
}

// ---- a listing that cannot be trusted is an error, never a shorter list ----

func TestListingShapes(t *testing.T) {
	good := card("14000", "Role 14000", "L")
	other := "https://evil.example/careers/JobDetail/X/14000"
	for name, tc := range map[string]struct {
		page string
		want string
	}{
		"not a results page":    {"<html><body>Please sign in</body></html>", "no results panel"},
		"panel, no cards":       {listPage(), "neither postings nor"},
		"marker beside a card":  {listPage(good, `<article class="article article--result"><h3>No jobs found</h3></article>`), "beside 1 postings"},
		"card with no link":     {listPage(`<article class="article article--result"><h3>Hello</h3></article>`), "no job link"},
		"card links elsewhere":  {listPage(strings.ReplaceAll(good, "https://"+host, "https://evil.example")), "no job link"},
		"offsite link only":     {listPage(`<article class="article article--result"><a href="` + other + `">T</a></article>`), "no job link"},
		"other portal":          {listPage(strings.ReplaceAll(good, "/careers/JobDetail/", "/jobs/JobDetail/")), "no job link"},
		"link with a query":     {listPage(strings.ReplaceAll(good, "/14000\"", "/14000?qtvc=1\"")), "no job link"},
		"non-numeric id":        {listPage(strings.ReplaceAll(good, "/14000\"", "/abc\"")), "no job link"},
		"two jobs in one card":  {listPage(strings.Replace(good, "/14000\" tabindex=\"0\">\nView role\n</a>\n<a class=\"button button--secondary", "/14001\" tabindex=\"0\">\nView role\n</a>\n<a class=\"button button--secondary", 1)), "links jobs"},
		"title is the button":   {listPage(strings.Replace(good, "Role 14000", "View role", 1)), "no title link"},
		"empty title":           {listPage(strings.Replace(good, "Role 14000", "", 1)), "no title link"},
		"javascript link":       {listPage(`<article class="article article--result"><a href="javascript:alert(1)">T</a></article>`), "no job link"},
		"userinfo link":         {listPage(strings.ReplaceAll(good, "https://"+host, "https://a@"+host)), "no job link"},
		"http link":             {listPage(strings.ReplaceAll(good, "https://"+host, "http://"+host)), "no job link"},
		"port link":             {listPage(strings.ReplaceAll(good, "https://"+host, "https://"+host+":8443")), "no job link"},
		"dot segment slug":      {listPage(strings.ReplaceAll(good, "/careers/JobDetail/New-York-United-States-Role-14000/14000", "/careers/JobDetail/../14000")), "no job link"},
		"path traversal in url": {listPage(strings.ReplaceAll(good, "/careers/JobDetail/New-York-United-States-Role-14000/", "/careers/JobDetail/../x/")), "no job link"},
	} {
		f := newFake(t, 0)
		f.listAt = func(off int) (srctest.Reply, bool) { return ok(tc.page), true }
		ps, err := f.list()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %d postings, err %v; want an error naming %q", name, len(ps), err, tc.want)
			continue
		}
		if f.detailCount() != 0 {
			t.Errorf("%s: %d detail pages fetched from an unreadable listing", name, f.detailCount())
		}
		for _, r := range f.reqs {
			if strings.Contains(r, "evil.example") {
				t.Errorf("%s: requested %s", name, r)
			}
		}
	}
}

// A relative job link is the page's own host; the page may write it so.
func TestRelativeJobLinkIsOnTheBoardsHost(t *testing.T) {
	f := newFake(t, 0)
	rel := strings.ReplaceAll(card("14000", "Role 14000", "L"), "https://"+host, "")
	f.listAt = func(off int) (srctest.Reply, bool) {
		if off == 0 {
			return ok(listPage(rel)), true
		}
		return srctest.Reply{}, false
	}
	ps, err := f.list()
	if err != nil || len(ps) != 1 || !strings.HasPrefix(ps[0].URL, "https://"+host+"/careers/JobDetail/") {
		t.Fatalf("%v %v", ps, err)
	}
}

// A server that ignores the offset serves the first page again. That cannot be
// told from the end of a wrapping list, so it fails the board.
func TestStuckListingFails(t *testing.T) {
	f := newFake(t, 12)
	f.listAt = func(off int) (srctest.Reply, bool) {
		var cards []string
		for _, id := range f.ids[:10] {
			cards = append(cards, card(id, "Role "+id, "L"))
		}
		return ok(listPage(cards...)), true
	}
	ps, err := f.list()
	if !errors.Is(err, ErrShape) || len(ps) != 0 || !strings.Contains(err.Error(), "already read") {
		t.Fatalf("%d postings, err %v; want ErrShape for a page of already-read cards", len(ps), err)
	}
	if f.detailCount() != 0 {
		t.Errorf("details fetched from a listing that failed")
	}
}

func TestOverMaxPostingsFails(t *testing.T) {
	f := newFake(t, MaxPostings+5)
	_, err := f.list()
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings (a cap that would truncate is an error)", err)
	}
	if f.detailCount() != 0 {
		t.Errorf("details fetched before the cap was known")
	}
	// Exactly at the cap is fine.
	if _, err := newFake(t, MaxPostings).list(); err != nil {
		t.Errorf("a board of exactly %d: %v", MaxPostings, err)
	}
}

func TestEndlessListingStopsAtPageBound(t *testing.T) {
	f := newFake(t, 0)
	// One new card per page, forever, never the marker: 22 pages hold only 22 jobs.
	f.listAt = func(off int) (srctest.Reply, bool) {
		return ok(listPage(card(strconv.Itoa(20000+off), "Role", "L"))), true
	}
	_, err := f.list()
	if !errors.Is(err, ErrShape) || !strings.Contains(err.Error(), "did not end within") {
		t.Fatalf("err %v", err)
	}
	if got := len(f.listOffsets()); got != maxPages {
		t.Errorf("%d pages read, want the bound %d", got, maxPages)
	}
}

// ---- dependency down ----

func TestDownSiteIsAnErrorNotAnEmptyList(t *testing.T) {
	for name, rp := range map[string]srctest.Reply{
		"404 board":   {Status: 404, Body: []byte("not found")},
		"500":         {Status: 500, Body: []byte("boom")},
		"403 blocked": {Status: 403, Body: []byte("forbidden")},
		"200 empty":   {Status: 200, Body: nil},
		"200 junk":    {Status: 200, Body: []byte("\x00\x01 not html")},
	} {
		f := newFake(t, 5)
		f.listAt = func(off int) (srctest.Reply, bool) { return rp, true }
		ps, err := f.list()
		if err == nil || len(ps) != 0 {
			t.Errorf("%s: %d postings, err %v", name, len(ps), err)
		}
		if name == "404 board" && !errors.Is(err, srcutil.ErrBoardNotFound) {
			t.Errorf("404: %v is not ErrBoardNotFound", err)
		}
	}
}

type errDoer struct{ err error }

func (d errDoer) Do(context.Context, *http.Request) (*http.Response, error) { return nil, d.err }

func TestTimeoutIsAnError(t *testing.T) {
	s := &source{d: errDoer{context.DeadlineExceeded}, now: time.Now, wait: noWait}
	ps, err := s.List(context.Background(), board())
	if !errors.Is(err, context.DeadlineExceeded) || len(ps) != 0 {
		t.Fatalf("%d postings, err %v", len(ps), err)
	}
}

func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newFake(t, 5)
	s, _ := f.source()
	if _, err := s.List(ctx, board()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestOversizeBodies(t *testing.T) {
	big := strings.Repeat("x", pageMaxBody+1)
	f := newFake(t, 3)
	f.listAt = func(off int) (srctest.Reply, bool) { return ok(listPage(card("14000", "T", "L")) + big), true }
	if _, err := f.list(); !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Errorf("oversize list: %v", err)
	}
	f = newFake(t, 3)
	f.detailFor = func(id string) (srctest.Reply, bool) { return ok(f.detailPage(id) + big), true }
	if _, err := f.list(); !errors.Is(err, srcutil.ErrBodyTooLarge) {
		t.Errorf("oversize detail: %v", err)
	}
}

// ---- robots ----

func TestRobots(t *testing.T) {
	// The live qtvc rule is a query pattern and must not block the listing.
	if ps, err := newFake(t, 3).list(); err != nil || len(ps) != 3 {
		t.Fatalf("live robots: %d, %v", len(ps), err)
	}
	for name, tc := range map[string]struct {
		status int
		body   string
		wantOK bool
	}{
		"portal disallowed":     {200, "User-agent: *\nDisallow: /careers\n", false},
		"listing disallowed":    {200, "User-agent: *\nAllow: /careers\nDisallow: /careers/OpenRoles\n", false},
		"details disallowed":    {200, "User-agent: *\nAllow: /careers\nDisallow: /careers/JobDetail\n", false},
		"vani named, disallow":  {200, "User-agent: Vani\nDisallow: /\n\nUser-agent: *\nAllow: /\n", false},
		"everything disallowed": {200, "User-agent: *\nDisallow: /\n", false},
		"robots 500":            {500, "oops", false},
		"robots 404 allows":     {404, "", true},
		"empty robots allows":   {200, "", true},
	} {
		f := newFake(t, 3)
		f.robotsStatus, f.robots = tc.status, tc.body
		ps, err := f.list()
		switch {
		case tc.wantOK && (err != nil || len(ps) != 3):
			t.Errorf("%s: %d postings, %v", name, len(ps), err)
		case !tc.wantOK && err == nil:
			t.Errorf("%s: listed %d postings", name, len(ps))
		}
		if !tc.wantOK {
			for _, r := range f.reqs {
				if r != "/robots.txt" {
					t.Errorf("%s: requested %s past a robots.txt that refuses", name, r)
				}
			}
		}
	}
}

// ---- details ----

func TestGoneDetailsAreBounded(t *testing.T) {
	gone := srctest.Reply{Status: 404, Body: []byte("Page not found")}
	for _, tc := range []struct {
		name      string
		n         int
		gone      []string
		wantErr   bool
		wantCount int
	}{
		{"one of twelve", 12, []string{"14002"}, false, 11},
		{"two of twelve", 12, []string{"14002", "14003"}, true, 0},
		{"ten of 100", 100, ids(100)[:10], false, 90},
		{"eleven of 100", 100, ids(100)[:11], true, 0},
		{"the only posting", 1, []string{"14000"}, true, 0},
		{"one of two", 2, []string{"14001"}, false, 1},
	} {
		f := newFake(t, tc.n)
		f.detailFor = func(id string) (srctest.Reply, bool) {
			for _, g := range tc.gone {
				if g == id {
					return gone, true
				}
			}
			return srctest.Reply{}, false
		}
		var stats []Stats
		s, _ := f.source()
		s.opt = &Options{Report: func(_ sources.Board, st Stats) { stats = append(stats, st) }}
		ps, err := s.List(context.Background(), board())
		if (err != nil) != tc.wantErr || len(ps) != tc.wantCount {
			t.Errorf("%s: %d postings, err %v", tc.name, len(ps), err)
			continue
		}
		if tc.wantErr {
			if errors.Is(err, srcutil.ErrBoardNotFound) || !strings.Contains(err.Error(), "gone") {
				t.Errorf("%s: err %v must say details are gone, and is not a missing board", tc.name, err)
			}
			continue
		}
		if len(stats) != 1 || stats[0].Listed != tc.n || stats[0].Gone != len(tc.gone) {
			t.Errorf("%s: reports %+v, want listed %d gone %d", tc.name, stats, tc.n, len(tc.gone))
		}
	}
}

// The live egress client follows a same-host redirect, so a closed posting is
// a 404 here. A Doer that hands the raw 302 back is not "gone".
func TestRawRedirectIsNotGone(t *testing.T) {
	f := newFake(t, 12)
	f.detailFor = func(id string) (srctest.Reply, bool) {
		if id == "14002" {
			return srctest.Reply{Status: 302, Body: nil, Header: http.Header{"Location": {"https://" + host + "/careers/Error"}}}, true
		}
		return srctest.Reply{}, false
	}
	ps, err := f.list()
	if err == nil || strings.Contains(err.Error(), "are gone") || len(ps) != 0 {
		t.Fatalf("%d postings, err %v; want a plain failure", len(ps), err)
	}
}

func TestDetailFailures(t *testing.T) {
	good := func(id string) string { return newFake(t, 0).detailPage(id) }
	for name, mutate := range map[string]func(id, page string) srctest.Reply{
		"500": func(_, _ string) srctest.Reply { return srctest.Reply{Status: 500, Body: []byte("boom")} },
		"200 error page": func(_, _ string) srctest.Reply {
			return ok("<html><head><title>Error</title></head><body>Page not found</body></html>")
		},
		"200 empty": func(_, _ string) srctest.Reply { return ok("") },
		"no main column": func(_, p string) srctest.Reply {
			return ok(strings.Replace(p, "grid__item--main", "grid__item--other", 1))
		},
		"no description": func(_, p string) srctest.Reply {
			return ok(strings.Replace(p, "<section class=\"section js_views\">\n<div class=\"section__content\">\n<article class=\"article article--details \" >\n<div class=\"article__content\">\n<div class=\"article__content__view\">\n<div class=\"article__content__view__field \">\n<div class=\"article__content__view__field__value\">\n<div>Two Sigma is", "<nope><div>Two Sigma is", 1))
		},
		"empty section":       func(_, p string) srctest.Reply { return ok(emptyMain(p)) },
		"canonical other job": func(id, p string) srctest.Reply { return ok(strings.Replace(p, "/"+id+`"`, "/99999"+`"`, 1)) },
		"canonical off host": func(id, p string) srctest.Reply {
			return ok(strings.Replace(p, "https://"+host, "https://evil.example", 1))
		},
		"canonical other style": func(id, p string) srctest.Reply { return ok(strings.Replace(p, "/JobDetail/", "/Elsewhere/", 1)) },
	} {
		f := newFake(t, 12)
		f.detailFor = func(id string) (srctest.Reply, bool) {
			if id != "14002" {
				return srctest.Reply{}, false
			}
			return mutate(id, good(id)), true
		}
		ps, err := f.list()
		if err == nil || len(ps) != 0 {
			t.Errorf("%s: %d postings, err %v; one unreadable detail fails the board", name, len(ps), err)
			continue
		}
		if strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: %v is not a gone detail", name, err)
		}
		if !strings.Contains(err.Error(), "14002") {
			t.Errorf("%s: err %v does not name the posting", name, err)
		}
	}
}

func emptyMain(p string) string {
	i := strings.Index(p, "grid__item--main")
	j := strings.Index(p[i:], "<section")
	k := strings.Index(p[i+j:], "</section>")
	gt := strings.Index(p[i+j:], ">")
	return p[:i+j+gt+1] + p[i+j+k:]
}

// A page without a canonical link is not refused: the id is in the request URL.
func TestNoCanonicalIsAccepted(t *testing.T) {
	f := newFake(t, 2)
	f.detailFor = func(id string) (srctest.Reply, bool) {
		p := f.detailPage(id)
		i := strings.Index(p, "<link")
		j := strings.Index(p[i:], ">")
		return ok(p[:i] + p[i+j+1:]), true
	}
	if ps, err := f.list(); err != nil || len(ps) != 2 {
		t.Fatalf("%d postings, %v", len(ps), err)
	}
}

func TestDetailTextIsUntrusted(t *testing.T) {
	f := newFake(t, 1)
	f.detailFor = func(id string) (srctest.Reply, bool) {
		p := strings.Replace(f.detailPage(id), "Two Sigma is a leading", `<script>alert(1)</script><a href="https://evil.example/x">click</a> &lt;b&gt;literal&lt;/b&gt; Two Sigma is a leading`, 1)
		return ok(p), true
	}
	ps, err := f.list()
	if err != nil || len(ps) != 1 {
		t.Fatalf("%v %v", ps, err)
	}
	d := ps[0].Description
	if strings.Contains(d, "alert(1)") || strings.Contains(d, "<script") || strings.Contains(d, "evil.example") {
		t.Errorf("script or link survived: %q", d)
	}
	if !strings.Contains(d, "<b>literal</b>") {
		t.Errorf("an escaped tag must stay literal text")
	}
	for _, r := range f.reqs {
		if strings.Contains(r, "evil") {
			t.Errorf("followed a link from the page: %s", r)
		}
	}
}

func TestDescriptionIsCapped(t *testing.T) {
	f := newFake(t, 1)
	f.detailFor = func(id string) (srctest.Reply, bool) {
		return ok(strings.Replace(f.detailPage(id), "Two Sigma is a leading", strings.Repeat("word ", 40000)+"Two Sigma is a leading", 1)), true
	}
	ps, err := f.list()
	if err != nil || len(ps) != 1 {
		t.Fatalf("%v %v", ps, err)
	}
	if len(ps[0].Description) > srcutil.MaxDescription {
		t.Errorf("description is %d bytes, over %d", len(ps[0].Description), srcutil.MaxDescription)
	}
	if !strings.HasPrefix(ps[0].Description, srcutil.PayLinePrefix) {
		t.Errorf("the pay line must lead and survive the cap: %q", firstLine(ps[0].Description))
	}
}

// ---- the board ----

func TestHostileBoards(t *testing.T) {
	for name, mut := range map[string]func(*sources.Board){
		"token ..":          func(b *sources.Board) { b.Token = ".." },
		"token slash":       func(b *sources.Board) { b.Token = "a/b" },
		"token colon":       func(b *sources.Board) { b.Token = "a:b" },
		"token space":       func(b *sources.Board) { b.Token = "two sigma" },
		"token empty":       func(b *sources.Board) { b.Token = "" },
		"token dot":         func(b *sources.Board) { b.Token = "two.sigma" },
		"token query":       func(b *sources.Board) { b.Token = "a?b" },
		"host empty":        func(b *sources.Board) { b.Host = "" },
		"host with path":    func(b *sources.Board) { b.Host = host + "/x" },
		"host with space":   func(b *sources.Board) { b.Host = "a b.com" },
		"host with port":    func(b *sources.Board) { b.Host = host + ":8443" },
		"host userinfo":     func(b *sources.Board) { b.Host = "a@" + host },
		"url empty":         func(b *sources.Board) { b.URL = "" },
		"url http":          func(b *sources.Board) { b.URL = "http://" + host + "/careers/OpenRoles" },
		"url other host":    func(b *sources.Board) { b.URL = "https://evil.example/careers/OpenRoles" },
		"url userinfo":      func(b *sources.Board) { b.URL = "https://a@" + host + "/careers/OpenRoles" },
		"url port":          func(b *sources.Board) { b.URL = "https://" + host + ":8443/careers/OpenRoles" },
		"url query":         func(b *sources.Board) { b.URL = listBase + "?qtvc=1" },
		"url empty query":   func(b *sources.Board) { b.URL = listBase + "?" },
		"url fragment":      func(b *sources.Board) { b.URL = listBase + "#x" },
		"url no page":       func(b *sources.Board) { b.URL = "https://" + host + "/careers" },
		"url too deep":      func(b *sources.Board) { b.URL = "https://" + host + "/a/b/c" },
		"url dotdot":        func(b *sources.Board) { b.URL = "https://" + host + "/careers/.." },
		"url encoded slash": func(b *sources.Board) { b.URL = "https://" + host + "/careers%2FOpenRoles" },
		"url space":         func(b *sources.Board) { b.URL = "https://" + host + "/care ers/OpenRoles" },
		"url root":          func(b *sources.Board) { b.URL = "https://" + host + "/" },
		"search set":        func(b *sources.Board) { b.Search = "ml" },
		"searches set":      func(b *sources.Board) { b.Searches = []string{"ml"} },
		"filter set":        func(b *sources.Board) { b.Filters = map[string][]string{"location": {"x"}} },
		"wrong ats":         func(b *sources.Board) { b.ATS = posting.Jibe },
		"board name":        func(b *sources.Board) { b.BoardName = "x" },
	} {
		b := board()
		mut(&b)
		f := newFake(t, 3)
		s, _ := f.source()
		ps, err := s.List(context.Background(), b)
		if err == nil || len(ps) != 0 {
			t.Errorf("%s: %d postings, err %v", name, len(ps), err)
		}
		if len(f.reqs) != 0 {
			t.Errorf("%s: %d requests sent for a board that is refused: %v", name, len(f.reqs), f.reqs)
		}
	}
}

// Another portal segment is configuration: the same code reads /jobs/.
func TestPortalIsConfiguration(t *testing.T) {
	f := newFake(t, 3)
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		r.URL = strings.Replace(r.URL, "/jobs/", "/careers/", 1)
		rp, handled := f.handle(r)
		rp.Body = []byte(strings.ReplaceAll(string(rp.Body), "/careers/JobDetail/", "/jobs/JobDetail/"))
		return rp, handled
	})
	s := &source{d: d, now: time.Now, wait: noWait}
	b := board()
	b.URL = "https://" + host + "/jobs/OpenRoles"
	ps, err := s.List(context.Background(), b)
	if err != nil || len(ps) != 3 {
		t.Fatalf("%d postings, %v", len(ps), err)
	}
	for _, p := range ps {
		if !strings.Contains(p.URL, "/jobs/JobDetail/") {
			t.Errorf("url %q", p.URL)
		}
	}
	if got := d.URLs()[0]; got != "https://"+host+"/robots.txt" {
		t.Errorf("first request %s", got)
	}
}

func TestTokenIsCaseFolded(t *testing.T) {
	// A mixed-case token is refused: the key lowercases it, and a board and its
	// postings must agree on the prefix (sources.Board.KeyPrefix lowercases).
	b := board()
	b.Token = "TwoSigma"
	f := newFake(t, 1)
	s, _ := f.source()
	ps, err := s.List(context.Background(), b)
	if err != nil || len(ps) != 1 || ps[0].Key != "av:twosigma:14000" {
		t.Fatalf("%v %v", ps, err)
	}
	if !strings.HasPrefix(string(ps[0].Key), b.KeyPrefix()) {
		t.Errorf("key %q is not under the board's prefix %q", ps[0].Key, b.KeyPrefix())
	}
}

func TestParseListURL(t *testing.T) {
	for raw, want := range map[string]ListPage{
		"https://careers.twosigma.com/careers/OpenRoles":             {"careers", "/careers/OpenRoles"},
		"https://careers.twosigma.com/careers/OpenRoles/":            {"careers", "/careers/OpenRoles"},
		"https://CAREERS.twosigma.com/en_US/SearchJobs":              {"en_US", "/en_US/SearchJobs"},
		" https://careers.twosigma.com/careers/OpenRoles ":           {"careers", "/careers/OpenRoles"},
		"https://careers.twosigma.com/internal-careers/Open_Roles-2": {"internal-careers", "/internal-careers/Open_Roles-2"},
	} {
		got, err := ParseListURL(raw, host)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "careers", "http://careers.twosigma.com/careers/OpenRoles", "https://x.com/careers/OpenRoles",
		"https://careers.twosigma.com/careers/OpenRoles?a=1", "https://careers.twosigma.com/careers/OpenRoles#f",
		"https://careers.twosigma.com/careers", "https://careers.twosigma.com/a/b/c", "https://careers.twosigma.com/%2e%2e/OpenRoles",
		"https://u@careers.twosigma.com/careers/OpenRoles", "https://careers.twosigma.com:444/careers/OpenRoles", "mailto:a@b.c"} {
		if got, err := ParseListURL(raw, host); err == nil {
			t.Errorf("%q accepted as %+v", raw, got)
		}
	}
}

// ---- pay ----

func TestPayLine(t *testing.T) {
	us := "United States - NY New York"
	for name, tc := range map[string]struct{ text, loc, want string }{
		"range":          {"The base pay for this role will be between $165,000 and $250,000. More.", us, "Pay range: USD 165000 – 250000"},
		"to":             {"Base pay range is $90,000 to $120,500.50 per year", us, "Pay range: USD 90000 – 120500.5"},
		"hourly":         {"The base pay for this role will be between $45 and $60 per hour.", us, "Pay range: USD 45 – 60 per hour"},
		"same twice":     {"base pay between $1,000 and $2,000. Again, the base pay is between $1,000 and $2,000.", us, "Pay range: USD 1000 – 2000"},
		"two ranges":     {"base pay between $100,000 and $150,000 (NY). base pay between $90,000 and $120,000 (CO)", us, ""},
		"reversed":       {"The base pay will be between $250,000 and $165,000.", us, ""},
		"no pay":         {"We are an equal opportunity employer.", us, ""},
		"not US":         {"The base pay for this role will be between $165,000 and $250,000.", "United Kingdom - London", ""},
		"no location":    {"The base pay for this role will be between $165,000 and $250,000.", "", ""},
		"pound":          {"The base pay for this role will be between £90,000 and £120,000.", us, ""},
		"far from $":     {"base pay" + strings.Repeat(" word", 60) + " $1 and $2", us, ""},
		"bonus not base": {"A bonus between $10,000 and $20,000", us, ""},
	} {
		if got := payLine(tc.text, tc.loc); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// ---- pacing, reports, cancellation ----

func TestRequestsArePaced(t *testing.T) {
	f := newFake(t, 12)
	d := srctest.NewFunc(f.handle)
	var mu sync.Mutex
	waits := 0
	s := NewWith(d, Options{Wait: func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		if d != paceEvery {
			t.Errorf("wait %s, want %s", d, paceEvery)
		}
		waits++
		return nil
	}})
	ps, err := s.List(context.Background(), board())
	if err != nil || len(ps) != 12 {
		t.Fatalf("%d, %v", len(ps), err)
	}
	// 12 jobs: list pages at offsets 0, 10 and 12 (the marker), plus 12 details.
	want := len(f.listOffsets()) + f.detailCount() - 1
	if waits != want {
		t.Errorf("%d waits for %d paced requests", waits, want+1)
	}
}

// The pace budget must hold a board at its posting cap, or the largest board
// this source accepts would fail on spacing alone.
func TestPaceBudgetHoldsAFullBoard(t *testing.T) {
	if full := maxPages + MaxPostings; time.Duration(full)*paceEvery > paceBudget {
		t.Errorf("a full board is %d requests at %s, over the %s budget", full, paceEvery, paceBudget)
	}
	// And a pacer that has spent its budget fails the board rather than waiting on.
	f := newFake(t, 12)
	s := &source{d: srctest.NewFunc(f.handle), now: time.Now, wait: noWait}
	st := newSite(host, ListPage{Portal: "careers", Path: "/careers/OpenRoles"}, &srcutil.Pacer{Every: paceEvery, Budget: paceEvery, Sleep: noWait})
	if _, err := s.listing(context.Background(), st); !errors.Is(err, srcutil.ErrPaceBudget) {
		t.Errorf("err %v, want ErrPaceBudget", err)
	}
}

func TestCancelBetweenDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFake(t, 20)
	served := 0
	var stats []Stats
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if strings.Contains(r.URL, "/JobDetail/") {
			if served++; served == 3 {
				cancel()
			}
		}
		return f.handle(r)
	})
	s := &source{d: d, now: time.Now, wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		opt: &Options{Report: func(_ sources.Board, st Stats) { stats = append(stats, st) }}}
	_, err := s.List(ctx, board())
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "gone") {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if served != 3 {
		t.Errorf("%d details fetched, want 3", served)
	}
	if len(stats) != 1 || stats[0].Listed != 20 || stats[0].Gone != 0 {
		t.Errorf("reports %+v, want one {20 0}", stats)
	}
}

func TestATSAndNew(t *testing.T) {
	if New(nil).ATS() != posting.Avature || NewWith(nil, Options{}).ATS() != posting.Avature {
		t.Error("ATS")
	}
	b := board()
	if got := fmt.Sprint(b.KeyPrefix()); got != "av:twosigma:" {
		t.Errorf("key prefix %q", got)
	}
}
