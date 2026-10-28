package avature

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Bloomberg's Avature portal (bloomberg.avature.net, read 2026-10-28) uses the
// same ATS as Two Sigma with other markup: <span class="list-item-location">
// on the list card, twelve cards a page whatever jobRecordsPerPage says, 341
// postings, and a detail page whose Location, Business Area and Ref # fields
// sit inside the main section with the description. The fixtures are real
// pages trimmed to the cards and sections this source reads.

const (
	bbHost     = "bloomberg.avature.net"
	bbListBase = "https://" + bbHost + "/careers/SearchJobs"
	bbPageSize = 12
	// bbFixtureID is the id in testdata/bloomberg-detail.html's canonical link.
	bbFixtureID = "45870"

	// bbRobots is the live robots.txt of 2026-10-28 cut to its careers group and
	// the closing "Disallow: /" every group ends with: /careers is the longer
	// Allow.
	bbRobots = "User-agent: *\nAllow: /$\n# Disallowed portals are included as Allowed to use noindex strategy\nAllow: /careers\nDisallow: /careers/*qtvc=\n" +
		"Sitemap: https://bloomberg.avature.net/careers/sitemap_index.xml\nAllow: /timeslots\nDisallow: /timeslots/*qtvc=\nAllow: /_cms/15\nAllow: /favicon.ico\nAllow: /sitemap.xml\nDisallow: /\n"
)

func bbBoard() sources.Board {
	return sources.Board{ATS: posting.Avature, Token: "bloomberg", Company: "Bloomberg", Host: bbHost, URL: bbListBase}
}

// bbCard is one result article in the markup of the live Bloomberg page.
func bbCard(id, title, loc string) string {
	link := "https://" + bbHost + "/careers/JobDetail/Role-" + id + "/" + id
	return `<article class="article article--result" id="article--` + id + `">
<div class="article__header"><div class="article__header__text">
<h3 class="article__header__text__title title title--04">
<a class="link" href="` + link + `">
` + title + `
</a>
</h3>
<div class="article__header__text__subtitle">
<span class="list-item-location">` + loc + `</span>
</div></div></div>
<div class="article__footer">
<a class="button button--primary" href="` + link + `" tabindex="0">Apply </a>
<a class="button button--secondary" href="https://` + bbHost + `/careers/SaveJob?jobId=` + id + `" tabindex="0">Save </a>
</div>
</article>`
}

// bbSite is a fake Bloomberg portal of n postings, twelve to a page by
// jobOffset whatever jobRecordsPerPage it is sent (as the live site does), the
// real "No jobs found" page past the end, a detail page per id made from the
// real one, and the robots.txt.
type bbSite struct {
	t      *testing.T
	n      int
	robots string

	mu   sync.Mutex
	reqs []string
}

func bbID(i int) string { return strconv.Itoa(40000 + i) }

func (s *bbSite) handle(r srctest.Request) (srctest.Reply, bool) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != bbHost || r.Method != "GET" {
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return srctest.Reply{}, false
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, u.RequestURI())
	s.mu.Unlock()
	switch {
	case u.Path == "/robots.txt":
		return ok(s.robots), true
	case u.Path == "/careers/SearchJobs/":
		off, err := strconv.Atoi(u.Query().Get("jobOffset"))
		if err != nil {
			s.t.Errorf("jobOffset %q", u.Query().Get("jobOffset"))
			return srctest.Reply{}, false
		}
		if off >= s.n {
			return ok(readFile(s.t, "bloomberg-list-empty.html")), true
		}
		var cards []string
		for i := off; i < min(off+bbPageSize, s.n); i++ {
			cards = append(cards, bbCard(bbID(i), "Role "+bbID(i), "London, United Kingdom"))
		}
		return ok(listPage(cards...)), true
	case strings.HasPrefix(u.Path, "/careers/JobDetail/"):
		parts := strings.Split(u.Path, "/")
		return ok(bbDetail(s.t, parts[len(parts)-1])), true
	}
	s.t.Errorf("unexpected path %s", u.Path)
	return srctest.Reply{}, false
}

// bbDetail is the real detail page with its id swapped for id.
func bbDetail(t *testing.T, id string) string {
	return strings.ReplaceAll(readFile(t, "bloomberg-detail.html"), "/"+bbFixtureID, "/"+id)
}

func (s *bbSite) list() ([]posting.Posting, error) {
	src := &source{d: srctest.NewFunc(s.handle), now: func() time.Time { return fixedNow }, wait: noWait}
	return src.List(context.Background(), bbBoard())
}

func (s *bbSite) count(prefix string) int {
	n := 0
	for _, r := range s.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func newBBSite(t *testing.T, n int) *bbSite { return &bbSite{t: t, n: n, robots: bbRobots} }

// The real list page: three cards whose location is a list-item-location span,
// then the real end marker page.
func TestBloombergListPageFixture(t *testing.T) {
	st := newSite(bbHost, ListPage{Portal: "careers", Path: "/careers/SearchJobs"}, nil)
	rows, end, err := st.parseListing(readFile(t, "bloomberg-list-page1.html"))
	if err != nil || end || len(rows) != 3 {
		t.Fatalf("rows %d end %v err %v", len(rows), end, err)
	}
	for i, want := range []row{
		{id: "45870", title: "Senior Data Management Professional - Data Automation Engineer - People Data", location: "New York, New York, United States of America"},
		{id: "45860", title: "Senior Data Management Professional - Data Engineering - OTC Composites & Instruments", location: "Dublin, Ireland"},
		{id: "45857", title: "2027 - Analytics & Sales - London/South Africa Rotational Programme", location: "London, United Kingdom"},
	} {
		if rows[i].id != want.id || rows[i].title != want.title || rows[i].location != want.location {
			t.Errorf("row %d: %+v, want id %s title %q location %q", i, rows[i], want.id, want.title, want.location)
		}
	}
	rows, end, err = st.parseListing(readFile(t, "bloomberg-list-empty.html"))
	if err != nil || !end || len(rows) != 0 {
		t.Errorf("empty page: rows %d end %v err %v", len(rows), end, err)
	}
}

// One rule reads a card's location: the first span carrying either portal's
// class. Two Sigma's markup still reads, a card with neither has no list
// location (the detail's Location field stands in), and a look-alike class is
// not the location.
func TestCardLocationReadsEitherPortalsSpan(t *testing.T) {
	st := newSite(host, ListPage{Portal: "careers", Path: "/careers/OpenRoles"}, nil)
	link := "https://" + host + "/careers/JobDetail/Role-1/1"
	article := func(spans string) string {
		return `<a class="link" href="` + link + `">Role</a>` + spans
	}
	for name, tc := range map[string]struct{ spans, want string }{
		"two sigma":        {`<span class="paragraph_inner-span">United States - NY New York</span><span class="paragraph_inner-span">Engineering</span>`, "United States - NY New York"},
		"bloomberg":        {`<div class="article__header__text__subtitle"><span class="list-item-location">Dublin, Ireland</span></div>`, "Dublin, Ireland"},
		"class among many": {`<span class="x list-item-location y">Singapore, Singapore</span>`, "Singapore, Singapore"},
		"first one wins":   {`<span class="list-item-location">London</span><span class="paragraph_inner-span">Engineering</span>`, "London"},
		"neither class":    {`<span class="list-item-department">Data</span>`, ""},
		"no span":          {``, ""},
	} {
		r, ok, err := st.card(article(tc.spans))
		if err != nil || !ok || r.location != tc.want {
			t.Errorf("%s: location %q (ok %v, err %v), want %q", name, r.location, ok, err, tc.want)
		}
	}
}

// The real detail page, listed through the real card: the list's location and
// the detail's Business Area, a description that is the posting's text and not
// the title and field labels around it, and nothing from outside the section.
func TestBloombergPostingFromLiveFixtures(t *testing.T) {
	s := newBBSite(t, 0)
	src := &source{d: srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		switch {
		case u.Path == "/careers/SearchJobs/" && u.Query().Get("jobOffset") == "0":
			return ok(readFile(t, "bloomberg-list-page1.html")), true
		case u.Path == "/careers/SearchJobs/":
			return ok(readFile(t, "bloomberg-list-empty.html")), true
		}
		return s.handle(r)
	}), now: func() time.Time { return fixedNow }, wait: noWait}
	ps, err := src.List(context.Background(), bbBoard())
	if err != nil || len(ps) != 3 {
		t.Fatalf("%d postings, %v", len(ps), err)
	}
	p := ps[0]
	if p.Key != "av:bloomberg:45870" || p.Company != "Bloomberg" ||
		p.Title != "Senior Data Management Professional - Data Automation Engineer - People Data" ||
		p.URL != "https://bloomberg.avature.net/careers/JobDetail/Senior-Data-Management-Professional-Data-Automation-Engineer-People-Data/45870" {
		t.Errorf("identity %+v", p)
	}
	if p.Location != "New York, New York, United States of America" {
		t.Errorf("location %q, want the list card's (the detail field says only %q)", p.Location, "New York")
	}
	if p.Department != "Data" {
		t.Errorf("department %q, want the Business Area field", p.Department)
	}
	if !p.PostedAt.IsZero() {
		t.Errorf("posted %v: the page states no date", p.PostedAt)
	}
	d := p.Description
	for _, want := range []string{"Bloomberg runs on data.", "The People Data team is looking for", "Strong Python skills", "Salary Range = 110,000", "good faith belief"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q: %.300q", want, d)
		}
	}
	for _, bad := range []string{"Business Area", "Ref #", "10054759", "Back to Job Search", "Apply Now"} {
		if strings.Contains(d, bad) {
			t.Errorf("description holds page chrome or a field label %q: %.300q", bad, d)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(d), "Senior Data Management Professional") {
		t.Errorf("description opens by repeating the title: %.200q", d)
	}
	if strings.ContainsAny(d, "<>") || strings.Contains(d, "youtube.com") {
		t.Errorf("markup or a link in the description: %.200q", d)
	}
	if ps[1].Location != "Dublin, Ireland" || ps[2].Location != "London, United Kingdom" || ps[2].Department != "Data" {
		t.Errorf("other cards: %q %q %q", ps[1].Location, ps[2].Location, ps[2].Department)
	}
}

// The whole live board: 341 postings, twelve to a page, so thirty list pages
// and the end marker at offset 341 (not a multiple of twelve), one detail
// request each. The old cap of 200 postings refused this board.
func TestBloombergFullBoard(t *testing.T) {
	s := newBBSite(t, 341)
	ps, err := s.list()
	if err != nil || len(ps) != 341 {
		t.Fatalf("%d postings, %v; want 341", len(ps), err)
	}
	if got := s.count("/careers/SearchJobs/"); got != 30 {
		t.Errorf("%d list requests, want 29 pages and the marker", got)
	}
	if got := s.count("/careers/JobDetail/"); got != 341 {
		t.Errorf("%d detail requests, want 341", got)
	}
	var offsets []string
	for _, r := range s.reqs {
		if strings.HasPrefix(r, "/careers/SearchJobs/") {
			u, _ := url.Parse(r)
			offsets = append(offsets, u.Query().Get("jobOffset"))
		}
	}
	if offsets[0] != "0" || offsets[1] != "12" || offsets[len(offsets)-2] != "336" || offsets[len(offsets)-1] != "341" {
		t.Errorf("offsets %v: each steps by the 12 cards held, the last by the 5", offsets)
	}
	seen := map[posting.Key]bool{}
	for _, p := range ps {
		if seen[p.Key] || p.Location != "London, United Kingdom" {
			t.Errorf("key %s twice or location %q", p.Key, p.Location)
		}
		seen[p.Key] = true
	}
}

// The cap still holds above the Bloomberg board: a listing over MaxPostings is
// an error before any detail is fetched, never a truncated board.
func TestBloombergBoardOverTheCapFails(t *testing.T) {
	s := newBBSite(t, MaxPostings+1)
	if _, err := s.list(); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings", err)
	}
	if s.count("/careers/JobDetail/") != 0 {
		t.Errorf("details fetched for a board over the cap")
	}
	if MaxPostings < 341 {
		t.Errorf("MaxPostings %d is under the 341 postings Bloomberg lists", MaxPostings)
	}
}

// robots.txt of the portal: "Allow: /careers" beats the closing "Disallow: /",
// and "Disallow: /careers/*qtvc=" is a query this source never sends. A host
// whose robots.txt disallows the listing path fails the board.
func TestBloombergRobots(t *testing.T) {
	s := newBBSite(t, 3)
	if ps, err := s.list(); err != nil || len(ps) != 3 {
		t.Fatalf("%d postings, %v", len(ps), err)
	}
	for _, r := range s.reqs {
		if strings.Contains(r, "qtvc") {
			t.Errorf("a request carries the disallowed qtvc parameter: %s", r)
		}
	}
	s = newBBSite(t, 3)
	s.robots = "User-agent: *\nAllow: /$\nDisallow: /careers\n"
	if _, err := s.list(); err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("err %v, want a robots.txt refusal", err)
	}
	if s.count("/careers/") != 0 {
		t.Errorf("a disallowed portal was read")
	}
}

// The board URL is the listing page without a trailing slash, as the owner
// pastes it; the request goes to the same path with one.
func TestBloombergBoardURL(t *testing.T) {
	for _, raw := range []string{bbListBase, bbListBase + "/"} {
		lp, err := ParseListURL(raw, bbHost)
		if err != nil || lp.Portal != "careers" || lp.Path != "/careers/SearchJobs" {
			t.Errorf("%s: %+v, %v", raw, lp, err)
		}
	}
	st := newSite(bbHost, ListPage{Portal: "careers", Path: "/careers/SearchJobs"}, nil)
	if got := st.pageURL(24); got != "https://bloomberg.avature.net/careers/SearchJobs/?jobOffset=24&jobRecordsPerPage=10" {
		t.Errorf("page url %q", got)
	}
	for _, bad := range []string{
		"https://bloomberg.avature.net/careers/SearchJobs?jobOffset=12",
		"https://bloomberg.avature.net/careers/SearchJobs/extra/segment",
		"http://bloomberg.avature.net/careers/SearchJobs",
		"https://careers.twosigma.com/careers/SearchJobs",
	} {
		if _, err := ParseListURL(bad, bbHost); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// A main section holding nothing but labelled fields, or only a repeat of the
// posting's title, has no description: a layout this source does not know, not
// a posting with an empty text.
func TestDetailWithNoDescriptionIsAShapeError(t *testing.T) {
	fields := `<article class="article article--details "><div class="article__content__view"><div class="article__content__view__field ">` +
		`<div class="article__content__view__field__label">Location</div><div class="article__content__view__field__value">New York</div></div></div></article>`
	title := `<article class="article article--details "><div class="article__content__view"><div class="article__content__view__field ">` +
		`<div class="article__content__view__field__value">Role 40000</div></div></div></article>`
	for name, body := range map[string]string{"fields only": fields, "fields and the title": title + fields, "title only": title} {
		s := newBBSite(t, 1)
		src := &source{d: srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			if u, _ := url.Parse(r.URL); strings.HasPrefix(u.Path, "/careers/JobDetail/") {
				return ok(`<div class="grid__item grid__item--main"><section class="section">` + body + `</section></div>`), true
			}
			return s.handle(r)
		}), now: func() time.Time { return fixedNow }, wait: noWait}
		if _, err := src.List(context.Background(), bbBoard()); !errors.Is(err, ErrShape) {
			t.Errorf("%s: err %v, want ErrShape", name, err)
		}
	}
}
