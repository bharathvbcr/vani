package page

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const (
	listURL  = "https://careers.example.com/jobs"
	testAddr = "127.0.0.1:43210"
	testTok  = "0123456789abcdef0123456789abcdef"
)

var fixedNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// fakeBrowser serves canned pages by URL, records every URL loaded, and
// counts Resets.
type fakeBrowser struct {
	mu       sync.Mutex
	pages    map[string]string
	errs     map[string]error
	calls    []string
	resets   int
	resetErr error                                 // returned by Reset
	hook     func(ctx context.Context, url string) // optional, runs before answering
}

func (f *fakeBrowser) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets++
	return f.resetErr
}

func (f *fakeBrowser) resetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resets
}

func (f *fakeBrowser) Load(ctx context.Context, u string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, u)
	f.mu.Unlock()
	if f.hook != nil {
		f.hook(ctx, u)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.errs[u]; err != nil {
		return nil, err
	}
	body, ok := f.pages[u]
	if !ok {
		return nil, fmt.Errorf("fake: no page for %s", u)
	}
	return []byte(body), nil
}

func board() sources.Board {
	return sources.Board{ATS: posting.Page, Token: "Example", Company: "Example Co", URL: listURL, LinkPattern: `^https://careers\.example\.com/jobs/[a-z0-9-]+$`}
}

func newSrc(r Browser) *source {
	return New(r, ProxyInfo{Addr: testAddr, Token: testTok}).(*source).withNow(fixedNow)
}

func (s *source) withNow(t time.Time) *source { s.now = func() time.Time { return t }; return s }

func ldPage(title, ld string) string {
	return `<html><head><title>` + title + `</title><script type="application/ld+json">` + ld + `</script></head><body>x</body></html>`
}

func listing(hrefs ...string) string {
	var b strings.Builder
	b.WriteString("<html><body>")
	for _, h := range hrefs {
		b.WriteString(`<a href="` + h + `">link</a>`)
	}
	return b.String() + "</body></html>"
}

func TestListHappyPath(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{
		listURL: listing("/jobs/eng-1", "/jobs/ops-2#apply", "https://careers.example.com/jobs/eng-1/?utm_source=x", "mailto:hr@example.com", "javascript:void(0)", "/about", "https://other.example.org/jobs/zzz"),
		"https://careers.example.com/jobs/eng-1": ldPage("Eng page", `{"@context":"https://schema.org","@type":"JobPosting","title":"Staff Engineer",
			"description":"&lt;p&gt;Build things&lt;/p&gt;","datePosted":"2026-10-01","validThrough":"2026-12-31",
			"hiringOrganization":{"@type":"Organization","name":"Example Corp"},
			"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"London","addressCountry":"GB"}}}`),
		"https://careers.example.com/jobs/ops-2": ldPage("Ops page", `{"@type":"JobPosting","title":"Ops","jobLocationType":"TELECOMMUTE"}`),
	}}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2: %+v", len(got), got)
	}
	a, b := got[0], got[1]
	if a.Title != "Staff Engineer" || a.Company != "Example Corp" || a.Location != "London, GB" || a.Remote {
		t.Errorf("posting 0: %+v", a)
	}
	if a.Description != "Build things" {
		t.Errorf("description %q: double-escaped HTML must be reduced to text", a.Description)
	}
	if !a.PostedAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PostedAt %v", a.PostedAt)
	}
	if b.Title != "Ops" || !b.Remote || b.Company != "Example Co" {
		t.Errorf("posting 1: %+v", b)
	}
	for _, p := range got {
		want, _ := posting.PageKey("Example", p.URL)
		if p.Key != want || !strings.HasPrefix(string(p.Key), board().KeyPrefix()) || p.ATS != posting.Page || !p.SeenAt.Equal(fixedNow) {
			t.Errorf("bad identity: %+v", p)
		}
	}
	if a.URL != "https://careers.example.com/jobs/eng-1" {
		t.Errorf("URL %q is not canonical", a.URL)
	}
	// Listing page plus two postings: the duplicate, mailto, javascript, other
	// host and non-matching links must not cost a fetch.
	if len(r.calls) != 3 {
		t.Errorf("%d fetches, want 3", len(r.calls))
	}
}

func TestLinkExtractionAndPattern(t *testing.T) {
	re := mustPattern(t, `/jobs/\d+$`)
	links := scanHTML(`<!-- <a href="/jobs/9"> --><script>var s = '<a href="/jobs/8">';</script>
		<A HREF=/jobs/1>one</A> <a class=x href='/jobs/2'>two</a> <a href="/jobs/1#frag">dup</a>
		<a href="/jobs/3?utm_campaign=a&amp;utm_medium=b">three</a> <a name="x">noref</a>
		<a href="tel:123">tel</a><a href="data:text/html,hi">d</a><a href="//cdn.example.net/jobs/5">proto-relative</a>`).links
	got, err := postingURLs(links, listURL, listURL, re)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, g := range got {
		urls = append(urls, g.canon)
	}
	want := []string{
		"https://careers.example.com/jobs/1",
		"https://careers.example.com/jobs/2",
		"https://careers.example.com/jobs/3",
		"https://cdn.example.net/jobs/5",
	}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v, want %v", urls, want)
	}
	if got[0].text != "one" {
		t.Errorf("anchor text %q", got[0].text)
	}
}

// A link pattern derived from the scraped postings (vani jobs ingest) covers
// the listing's own directory, so it also matches the listing's pagination. A
// link on the listing's own host and path is the listing again, never a posting.
func TestListingOwnPathIsNeverAPosting(t *testing.T) {
	const search = "https://careers.example.com/jobs/search"
	b := board()
	b.URL = search + "?q=engineer"
	b.LinkPattern = `^https://careers\.example\.com/jobs/`
	r := &fakeBrowser{pages: map[string]string{
		search + "?q=engineer": listing("/jobs/search?q=engineer&page=2", "/jobs/search?page=3", "/jobs/search/",
			"HTTPS://CAREERS.EXAMPLE.COM/jobs/search#top", "/jobs/eng-1", "/jobs/ops-2"),
		"https://careers.example.com/jobs/eng-1": ldPage("t", `{"@type":"JobPosting","title":"Eng"}`),
		"https://careers.example.com/jobs/ops-2": ldPage("t", `{"@type":"JobPosting","title":"Ops"}`),
	}}
	got, err := newSrc(r).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "Eng" || got[1].Title != "Ops" {
		t.Fatalf("got %+v, want only the two postings", got)
	}
	if len(r.calls) != 3 {
		t.Errorf("%d fetches (%v), want the listing and two postings", len(r.calls), r.calls)
	}

	// A listing whose links are all its own pages reads as an empty board, not
	// as postings.
	only := &fakeBrowser{pages: map[string]string{search + "?q=engineer": listing("/jobs/search?page=2", "/jobs/search?page=3")}}
	if got, err := newSrc(only).List(context.Background(), b); err == nil {
		t.Fatalf("pagination-only listing returned %+v, want an error", got)
	}
}

// Relative links resolve against the page's first <base href>, as in a
// browser. Google's careers results set one and link postings as
// "jobs/results/<id>-<slug>"; against the listing URL they would resolve one
// directory too deep and the board would read as empty.
func TestBaseHrefResolvesRelativeLinks(t *testing.T) {
	const list = "https://careers.example.com/about/careers/applications/jobs/results?q=llm"
	baseBoard := func() sources.Board {
		b := board()
		b.URL = list
		b.LinkPattern = `^https://careers\.example\.com/about/careers/applications/jobs/results/[0-9]+-[a-z-]+(\?q=llm)?$`
		return b
	}
	post := "https://careers.example.com/about/careers/applications/jobs/results/12-ml-engineer?q=llm"
	pages := map[string]string{
		list: `<html><head><base href="https://careers.example.com/about/careers/applications/"><base href="https://evil.example.org/"></head>
			<body><a href="jobs/results/12-ml-engineer?q=llm">ML Engineer</a></body></html>`,
		post: ldPage("t", `{"@type":"JobPosting","title":"ML Engineer"}`),
	}
	got, err := newSrc(&fakeBrowser{pages: pages}).List(context.Background(), baseBoard())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != post || got[0].Title != "ML Engineer" {
		t.Fatalf("got %+v, want the one posting at %s", got, post)
	}

	// Only the first <base> with an href counts; later ones and bases that
	// are not absolute http(s) URLs leave the listing URL in charge.
	for name, tc := range map[string]struct {
		head string
		want string
	}{
		"no base":            {``, list},
		"first wins":         {`<base target="_top"><base href="/a/"><base href="/b/">`, "https://careers.example.com/a/"},
		"relative base":      {`<base href="../x/">`, "https://careers.example.com/about/careers/applications/x/"},
		"javascript base":    {`<base href="javascript:alert(1)">`, list},
		"empty-host base":    {`<base href="https:">`, list},
		"entity in href":     {`<base href="https://careers.example.com/a/?x=1&amp;y=2">`, "https://careers.example.com/a/?x=1&y=2"},
		"base inside script": {`<script>"<base href='/s/'>"</script>`, list},
	} {
		got, err := documentBase(list, scanHTML(`<html><head>`+tc.head+`</head><body></body></html>`))
		if err != nil || got != tc.want {
			t.Errorf("%s: base %q, %v; want %q", name, got, err, tc.want)
		}
	}

	// A hostile base cannot widen what is fetched: links it redirects to
	// another host still have to match the operator's pattern.
	pages[list] = `<html><head><base href="https://evil.example.org/"></head><body><a href="jobs/results/12-ml-engineer?q=llm">x</a></body></html>`
	if got, err := newSrc(&fakeBrowser{pages: pages}).List(context.Background(), baseBoard()); err == nil {
		t.Fatalf("a base on another host produced postings: %+v", got)
	}
}

// A board URL written with a trailing "/" is a directory: "job-1" on it is
// ".../eng/job-1". The canonical form drops the "/", and resolving against it
// moved every relative link up a directory, so the board read as empty.
func TestRelativeLinksResolveAgainstTheBoardURLAsWritten(t *testing.T) {
	b := board()
	b.URL = "https://careers.example.com/teams/eng/"
	b.LinkPattern = `^https://careers\.example\.com/teams/eng/job-[0-9]+$`
	post := "https://careers.example.com/teams/eng/job-1"
	r := &fakeBrowser{pages: map[string]string{
		"https://careers.example.com/teams/eng": listing("job-1", "./job-1"),
		post:                                    ldPage("t", `{"@type":"JobPosting","title":"Eng"}`),
	}}
	got, err := newSrc(r).List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != post {
		t.Fatalf("got %+v, want the one posting at %s", got, post)
	}
}

// Posting links written as http are fetched as https on the default port.
// They used to fail the board (unfetchable) or, under an https pattern, not
// match at all, so the board read as empty.
func TestHTTPLinksAreUpgraded(t *testing.T) {
	re := mustPattern(t, `^https://careers\.example\.com/jobs/[a-z0-9-]+$`)
	links := scanHTML(listing("http://careers.example.com/jobs/a-1", "HTTP://careers.example.com:80/jobs/b-2",
		"https://careers.example.com/jobs/a-1")).links
	got, err := postingURLs(links, listURL, listURL, re)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, g := range got {
		urls = append(urls, g.canon)
	}
	if want := "https://careers.example.com/jobs/a-1 https://careers.example.com/jobs/b-2"; strings.Join(urls, " ") != want {
		t.Fatalf("got %v, want %s (the https duplicate folded in)", urls, want)
	}

	// The whole board, through List: an http-only listing still yields postings.
	r := &fakeBrowser{pages: map[string]string{
		listURL:                                listing("http://careers.example.com/jobs/a-1"),
		"https://careers.example.com/jobs/a-1": ldPage("t", `{"@type":"JobPosting","title":"A"}`),
	}}
	if got, err := newSrc(r).List(context.Background(), board()); err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v; want one posting", got, err)
	}

	// On another port the https form is a guess: the link is left as http and
	// a pattern that selects it fails the board instead of fetching elsewhere.
	links = scanHTML(listing("http://careers.example.com:8080/jobs/c-3")).links
	if got, err := postingURLs(links, listURL, listURL, mustPattern(t, `/jobs/c-3$`)); err == nil {
		t.Fatalf("an http link on port 8080 was accepted: %+v", got)
	}
}

func TestOverCapIsAnErrorNotATruncation(t *testing.T) {
	var hrefs []string
	for i := 0; i <= MaxLinks; i++ {
		hrefs = append(hrefs, "/jobs/j"+strconv.Itoa(i))
	}
	r := &fakeBrowser{pages: map[string]string{listURL: listing(hrefs...)}}
	_, err := newSrc(r).List(context.Background(), board())
	if err == nil || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("err %v, want a narrower-pattern error", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("%d fetches; no posting page may be fetched when over the cap", len(r.calls))
	}
	// Exactly at the cap is fine.
	pages := map[string]string{listURL: listing(hrefs[:MaxLinks]...)}
	for _, h := range hrefs[:MaxLinks] {
		pages["https://careers.example.com"+h] = ldPage("t", `{"@type":"JobPosting","title":"T"}`)
	}
	got, err := newSrc(&fakeBrowser{pages: pages}).List(context.Background(), board())
	if err != nil || len(got) != MaxLinks {
		t.Fatalf("at the cap: %d postings, err %v", len(got), err)
	}
}

func TestNoMatchingLinksIsAnError(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{listURL: listing("/about", "/contact")}}
	if _, err := newSrc(r).List(context.Background(), board()); err == nil {
		t.Fatal("an unreadable or empty listing must not look like an empty board")
	}
}

func TestJSONLDShapes(t *testing.T) {
	cases := []struct {
		name, ld string
		want     jobPosting
	}{
		{"graph", `{"@context":"https://schema.org","@graph":[{"@type":"WebSite","name":"x"},{"@type":"JobPosting","title":"G","hiringOrganization":"Acme"}]}`,
			jobPosting{title: "G", company: "Acme"}},
		{"top-level array", `[{"@type":"Organization"},{"@type":["Thing","JobPosting"],"title":"A"}]`, jobPosting{title: "A"}},
		{"multiple locations", `{"@type":"JobPosting","title":"M","jobLocation":[
			{"address":{"addressLocality":"Berlin","addressRegion":"BE","addressCountry":{"@type":"Country","name":"Germany"}}},
			{"address":{"addressLocality":"London, United Kingdom","addressRegion":null,"addressCountry":null}},
			{"address":{"addressLocality":"Berlin","addressRegion":"BE","addressCountry":"Germany"}}]}`,
			jobPosting{title: "M", location: "Berlin, BE, Germany; London, United Kingdom"}},
		{"telecommute list", `{"@type":"JobPosting","title":"R","jobLocationType":["TELECOMMUTE"]}`, jobPosting{title: "R", remote: true}},
		{"nulls and missing", `{"@type":"JobPosting","title":null,"description":null,"hiringOrganization":null,"jobLocation":null,"datePosted":null}`, jobPosting{}},
		{"string address", `{"@type":"JobPosting","title":"S","jobLocation":{"@type":"Place","address":"Paris, France"}}`, jobPosting{title: "S", location: "Paris, France"}},
		{"bad dates ignored", `{"@type":"JobPosting","title":"D","datePosted":"yesterday","validThrough":"soon"}`, jobPosting{title: "D"}},
		// Full type URIs and compact IRIs name the same type as the bare word.
		{"https type uri", `{"@type":"https://schema.org/JobPosting","title":"U"}`, jobPosting{title: "U"}},
		{"http type uri in list", `{"@type":["http://schema.org/Thing","HTTP://SCHEMA.ORG/JobPosting"],"title":"L"}`, jobPosting{title: "L"}},
		{"compact iri in graph", `{"@graph":[{"@type":"schema:JobPosting","title":"C"}]}`, jobPosting{title: "C"}},
		// A PostalAddress given directly as the jobLocation, with no Place around it.
		{"postal address location", `{"@type":"JobPosting","title":"P","jobLocation":{"@type":"PostalAddress","addressLocality":"Austin","addressRegion":"TX","addressCountry":"US"}}`,
			jobPosting{title: "P", location: "Austin, TX, US"}},
		{"postal address list", `{"@type":"JobPosting","title":"Q","jobLocation":[{"@type":"PostalAddress","addressLocality":"Austin"},{"address":{"addressLocality":"Austin"}}]}`,
			jobPosting{title: "Q", location: "Austin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := findJobPosting("", []string{tc.ld}, fixedNow)
			if !ok {
				t.Fatal("no JobPosting found")
			}
			if got != tc.want {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
	for name, ld := range map[string]string{
		"not json":      `{"@type":"JobPosting",`,
		"other type":    `{"@type":"Organization","name":"x"}`,
		"empty":         ``,
		"scalar":        `42`,
		"too deep":      strings.Repeat(`{"@graph":`, 20) + `{"@type":"JobPosting"}` + strings.Repeat(`}`, 20),
		"type in value": `{"name":"JobPosting"}`,
		"other vocab":   `{"@type":"https://example.org/JobPosting"}`,
		"prefix only":   `{"@type":"https://schema.org/"}`,
		"other suffix":  `{"@type":"https://schema.org/JobPostingX"}`,
		"trailing junk": `{"@type":"JobPosting","title":"X"} true`,
	} {
		if _, ok := findJobPosting("", []string{ld}, fixedNow); ok {
			t.Errorf("%s: found a JobPosting", name)
		}
	}
	// A bad block does not hide a later good one.
	if jp, ok := findJobPosting("", []string{`{bad`, `{"@type":"JobPosting","title":"late"}`}, fixedNow); !ok || jp.title != "late" {
		t.Errorf("later block ignored: %+v %v", jp, ok)
	}
}

// TestJSONLDPageURL is the page-URL selection the first-node walk got wrong:
// an expired sibling must not drop this page, and a JobPosting is used only
// when it names this page or it is the only one and names no page.
func TestJSONLDPageURL(t *testing.T) {
	const (
		dead = "2020-01-01"
		live = "2027-06-01"
	)
	sibling := `{"@type":"JobPosting","title":"Expired Sibling","description":"old body","url":"https://careers.example.com/jobs/other-9","validThrough":"` + dead + `"}`
	pageOf := func(title, body, ld string) string {
		return `<html><head><title>` + title + `</title><script type="application/ld+json">` + ld + `</script></head><body>` + body + `</body></html>`
	}
	twoBlocks := func(title, body, a, b string) string {
		return `<html><head><title>` + title + `</title>` +
			`<script type="application/ld+json">` + a + `</script>` +
			`<script type="application/ld+json">` + b + `</script>` +
			`</head><body>` + body + `</body></html>`
	}
	wrap := func(levels int, leaf string) string {
		s := leaf
		for i := 0; i < levels; i++ {
			s = `{"nest":` + s + `}`
		}
		return s
	}
	cases := []struct {
		name, path, html string
		omit             bool
		title, desc      string
	}{
		{
			name:  "expired sibling then live url",
			path:  "/jobs/live-1",
			html:  pageOf("HTML Title", "html body", `[`+sibling+`,{"@type":"JobPosting","title":"Live Role","description":"new work","url":"https://careers.example.com/jobs/live-1","validThrough":"`+live+`"}]`),
			title: "Live Role", desc: "new work",
		},
		{
			name:  "mainEntity",
			path:  "/jobs/nested-1",
			html:  pageOf("HTML Title", "html body", `{"@type":"WebPage","mainEntity":{"@type":"JobPosting","title":"Nested Role","description":"under main"}}`),
			title: "Nested Role", desc: "under main",
		},
		{
			name: "single expired url omitted",
			path: "/jobs/gone-1",
			html: pageOf("HTML Title", "html body", `{"@type":"JobPosting","title":"Gone","description":"dead text","url":"https://careers.example.com/jobs/gone-1","validThrough":"`+dead+`"}`),
			omit: true,
		},
		{
			name:  "different url does not expire or title the page",
			path:  "/jobs/keep-1",
			html:  pageOf("HTML Title", "html body", `{"@type":"JobPosting","title":"Other Role","description":"other desc","url":"https://careers.example.com/jobs/other-9","validThrough":"`+dead+`"}`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "live different url is not copied",
			path:  "/jobs/keep-2",
			html:  pageOf("HTML Title", "html body", `{"@type":"JobPosting","title":"Other Role","description":"other desc","url":"https://careers.example.com/jobs/other-9","validThrough":"`+live+`"}`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "sameAs list",
			path:  "/jobs/alias-1",
			html:  pageOf("HTML Title", "html body", `[`+sibling+`,{"@type":"JobPosting","title":"Alias Role","description":"via same","sameAs":["https://example.org/elsewhere","http://careers.example.com/jobs/alias-1#apply"]}]`),
			title: "Alias Role", desc: "via same",
		},
		{
			name:  "sameAs trailing slash",
			path:  "/jobs/slash-1",
			html:  pageOf("HTML Title", "html body", `[`+sibling+`,{"@type":"JobPosting","title":"Slash Role","description":"slash body","sameAs":"https://careers.example.com/jobs/slash-1/"}]`),
			title: "Slash Role", desc: "slash body",
		},
		{
			name:  "url list",
			path:  "/jobs/urls-1",
			html:  pageOf("HTML Title", "html body", `[`+sibling+`,{"@type":"JobPosting","title":"List Role","description":"list body","url":["https://example.org/x","https://careers.example.com/jobs/urls-1/"]}]`),
			title: "List Role", desc: "list body",
		},
		{
			name:  "canonical scheme host slash utm fragment",
			path:  "/jobs/canon-1",
			html:  pageOf("HTML Title", "html body", `[`+sibling+`,{"@type":"JobPosting","title":"Canon Role","description":"canon body","url":"http://Careers.Example.com/jobs/canon-1/?utm_campaign=a#section"}]`),
			title: "Canon Role", desc: "canon body",
		},
		{
			name:  "path prefix does not match",
			path:  "/jobs/prefix-1",
			html:  pageOf("HTML Title", "html body", `{"@type":"JobPosting","title":"Prefix Role","description":"prefix desc","url":"https://careers.example.com/jobs/prefix"}`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "longer path does not match",
			path:  "/jobs/prefix-2",
			html:  pageOf("HTML Title", "html body", `{"@type":"JobPosting","title":"Extra Role","description":"extra desc","url":"https://careers.example.com/jobs/prefix-2/extra"}`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "non-utm query does not match",
			path:  "/jobs/query-1",
			html:  pageOf("HTML Title", "html body", `{"@type":"JobPosting","title":"Query Role","description":"query desc","url":"https://careers.example.com/jobs/query-1?ref=1"}`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "nested at walk cap",
			path:  "/jobs/atcap-1",
			html:  pageOf("HTML Title", "html body", wrap(maxJSONLDDepth, `{"@type":"JobPosting","title":"At Cap","description":"cap body","url":"https://careers.example.com/jobs/atcap-1"}`)),
			title: "At Cap", desc: "cap body",
		},
		{
			name:  "nested over walk cap",
			path:  "/jobs/over-1",
			html:  pageOf("HTML Title", "html body", wrap(maxJSONLDDepth+1, `{"@type":"JobPosting","title":"Over Cap","description":"over body","url":"https://careers.example.com/jobs/over-1"}`)),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "malformed falls back",
			path:  "/jobs/bad-1",
			html:  pageOf("HTML Title", "html body", `{"@type":"JobPosting",`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "several without url fall back",
			path:  "/jobs/multi-1",
			html:  pageOf("HTML Title", "html body", `[{"@type":"JobPosting","title":"One","description":"aaa"},{"@type":"JobPosting","title":"Two","description":"bbb"}]`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "bare plus a different url falls back",
			path:  "/jobs/multi-2",
			html:  pageOf("HTML Title", "html body", `[{"@type":"JobPosting","title":"Bare","description":"bare desc"},{"@type":"JobPosting","title":"Other","description":"other desc","url":"https://careers.example.com/jobs/other-9"}]`),
			title: "HTML Title", desc: "html body",
		},
		{
			name:  "first of two live matches",
			path:  "/jobs/dup-1",
			html:  pageOf("HTML Title", "html body", `[{"@type":"JobPosting","title":"First Live","description":"first body","url":"https://careers.example.com/jobs/dup-1"},{"@type":"JobPosting","title":"Second Live","description":"second body","url":"https://careers.example.com/jobs/dup-1/"}]`),
			title: "First Live", desc: "first body",
		},
		{
			name:  "expired match then live match",
			path:  "/jobs/both-1",
			html:  pageOf("HTML Title", "html body", `[{"@type":"JobPosting","title":"Dead Match","description":"dead body","url":"https://careers.example.com/jobs/both-1","validThrough":"`+dead+`"},{"@type":"JobPosting","title":"Live Match","description":"live body","url":"http://careers.example.com/jobs/both-1#a"}]`),
			title: "Live Match", desc: "live body",
		},
		{
			name: "every match expired",
			path: "/jobs/alldead-1",
			html: pageOf("HTML Title", "html body", `[{"@type":"JobPosting","title":"Dead Match","description":"dead body","url":"https://careers.example.com/jobs/alldead-1","validThrough":"`+dead+`"},{"@type":"JobPosting","title":"Other Live","description":"other body","url":"https://careers.example.com/jobs/other-9","validThrough":"`+live+`"}]`),
			omit: true,
		},
		{
			name:  "context is not a posting",
			path:  "/jobs/ctx-1",
			html:  pageOf("HTML Title", "html body", `{"@context":{"@type":"JobPosting","title":"Context Role","description":"ctx","url":"https://careers.example.com/jobs/ctx-1"},"mainEntity":{"@type":"JobPosting","title":"Nested Role","description":"under main"}}`),
			title: "Nested Role", desc: "under main",
		},
		{
			name:  "second block after expired sibling",
			path:  "/jobs/block-1",
			html:  twoBlocks("HTML Title", "html body", sibling, `{"@type":"JobPosting","title":"Second Block","description":"block body","url":"https://careers.example.com/jobs/block-1"}`),
			title: "Second Block", desc: "block body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := "https://careers.example.com" + tc.path
			r := &fakeBrowser{pages: map[string]string{listURL: listing(tc.path), u: tc.html}}
			got, err := newSrc(r).List(context.Background(), board())
			if err != nil {
				t.Fatal(err)
			}
			if tc.omit {
				if len(got) != 0 {
					t.Fatalf("kept %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d postings: %+v", len(got), got)
			}
			if got[0].Title != tc.title || got[0].Description != tc.desc || got[0].URL != u {
				t.Fatalf("got title %q desc %q url %q", got[0].Title, got[0].Description, got[0].URL)
			}
		})
	}
}

func TestFallbackWithoutJobPosting(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{
		listURL: listing("/jobs/plain-1", "/jobs/badld-2"),
		"https://careers.example.com/jobs/plain-1": `<html><head><title>Designer &amp; Maker | Example</title><style>p{}</style></head>
			<body><nav>menu</nav><p>We need a <b>designer</b>.</p><script>evil()</script></body></html>`,
		"https://careers.example.com/jobs/badld-2": ldPage("Broken JSON-LD", `{"@type":"JobPosting",`),
	}}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want both listed", len(got))
	}
	if got[0].Title != "Designer & Maker | Example" || got[0].Company != "Example Co" {
		t.Errorf("fallback posting: %+v", got[0])
	}
	if !strings.Contains(got[0].Description, "We need a designer.") || strings.Contains(got[0].Description, "evil") || strings.Contains(got[0].Description, "<") {
		t.Errorf("description %q", got[0].Description)
	}
	if got[1].Title != "Broken JSON-LD" {
		t.Errorf("malformed JSON-LD must fall back to the title: %+v", got[1])
	}
}

func TestFallbackTitleChain(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{
		listURL:                                `<a href="/jobs/a-1">Anchor Title</a><a href="/jobs/b-2"></a>`,
		"https://careers.example.com/jobs/a-1": `<html><body>no title here</body></html>`,
		"https://careers.example.com/jobs/b-2": `<html><body>nor here</body></html>`,
	}}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Title != "Anchor Title" || got[1].Title != "https://careers.example.com/jobs/b-2" {
		t.Errorf("titles %q %q", got[0].Title, got[1].Title)
	}
}

func TestExpiredPostingsAreSkipped(t *testing.T) {
	mk := func(vt string) string {
		return ldPage("t", `{"@type":"JobPosting","title":"T","validThrough":"`+vt+`"}`)
	}
	r := &fakeBrowser{pages: map[string]string{
		listURL: listing("/jobs/past-1", "/jobs/today-2", "/jobs/future-3", "/jobs/pastts-4", "/jobs/nots-5"),
		"https://careers.example.com/jobs/past-1":   mk("2026-10-17"),
		"https://careers.example.com/jobs/today-2":  mk("2026-10-18"), // valid through the end of the day
		"https://careers.example.com/jobs/future-3": mk("2027-01-01T00:00:00Z"),
		"https://careers.example.com/jobs/pastts-4": mk("2026-10-18T11:59:59Z"),
		"https://careers.example.com/jobs/nots-5":   mk("whenever"),
	}}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, p := range got {
		kept = append(kept, p.URL[strings.LastIndex(p.URL, "/")+1:])
	}
	if strings.Join(kept, ",") != "today-2,future-3,nots-5" {
		t.Errorf("kept %v", kept)
	}
}

func TestOneFailedPageFailsTheBoard(t *testing.T) {
	good := ldPage("t", `{"@type":"JobPosting","title":"T"}`)
	for name, r := range map[string]*fakeBrowser{
		"runner error": {
			pages: map[string]string{listURL: listing("/jobs/a-1", "/jobs/b-2", "/jobs/c-3"), "https://careers.example.com/jobs/a-1": good, "https://careers.example.com/jobs/c-3": good},
			errs:  map[string]error{"https://careers.example.com/jobs/b-2": errors.New("exit status 22")},
		},
		"listing fails": {errs: map[string]error{listURL: errors.New("timeout")}},
	} {
		got, err := newSrc(r).List(context.Background(), board())
		if err == nil || got != nil {
			t.Errorf("%s: got %d postings, err %v; want nil and an error", name, len(got), err)
		}
	}
}

func TestErrorsNeverCarryTheToken(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{listURL: listing("/jobs/a-1")},
		errs: map[string]error{"https://careers.example.com/jobs/a-1": errors.New("proxy http://vani:" + testTok + "@127.0.0.1 refused")}}
	_, err := newSrc(r).List(context.Background(), board())
	if err == nil || strings.Contains(err.Error(), testTok) {
		t.Fatalf("err %v", err)
	}
}

// Hostile page content reaches the browser only as validated https URLs of
// the board's own pattern; it never touches the process flags.
func TestPageContentOnlyReachesTheBrowserAsURLs(t *testing.T) {
	evil := `--dump-selector=x" ; rm -rf / ; "`
	r := &fakeBrowser{pages: map[string]string{
		listURL:                                `<a href="/jobs/a-1">` + evil + `</a><a href="/jobs/b-2">$(touch pwned)</a><a href="` + evil + `">x</a>`,
		"https://careers.example.com/jobs/a-1": ldPage(evil, `{"@type":"JobPosting","title":"`+evil+`","description":"--insecure-disable-tls-host-verification"}`),
		"https://careers.example.com/jobs/b-2": ldPage("`id`", `{"@type":"JobPosting","title":"T"}`),
	}}
	if _, err := newSrc(r).List(context.Background(), board()); err != nil {
		t.Fatal(err)
	}
	want := []string{listURL, "https://careers.example.com/jobs/a-1", "https://careers.example.com/jobs/b-2"}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("loaded %q, want %q", r.calls, want)
	}
}

// Flags is the whole process command line apart from "mcp": a fixed set,
// with the proxy forced and every safety flag present.
func TestFlags(t *testing.T) {
	flags, err := Flags(ProxyInfo{Addr: testAddr, Token: testTok})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--obey-robots", "--block-private-networks",
		"--http-timeout", strconv.Itoa(httpTimeoutMs), "--http-max-host-open", "1",
		"--http-max-response-size", strconv.Itoa(MaxStdout), "--watchdog-ms", strconv.Itoa(watchdogMs),
		// lightpanda obeys the robots.txt group of its own User-Agent's product
		// token; it must be the one Crawl-delay is read for.
		"--user-agent", UserAgent,
		"--block-cidrs", "-127.0.0.1/32",
		"--http-proxy", "http://vani:" + testTok + "@" + testAddr,
		"--proxy-bearer-token", testTok,
	}
	if !slices.Equal(flags, want) {
		t.Fatalf("flags\n %q\nwant\n %q", flags, want)
	}
	// An IPv6 loopback proxy is exempted as a /128.
	v6, err := Flags(ProxyInfo{Addr: "[::1]:9", Token: testTok})
	if err != nil || !slices.Contains(v6, "-::1/128") || !slices.Contains(v6, "http://vani:"+testTok+"@[::1]:9") {
		t.Fatalf("ipv6 proxy: %q, %v", v6, err)
	}
	for name, p := range map[string]ProxyInfo{
		"not loopback": {Addr: "10.0.0.1:80", Token: testTok},
		"hostname":     {Addr: "localhost:80", Token: testTok},
		"no port":      {Addr: "127.0.0.1", Token: testTok},
		"port zero":    {Addr: "127.0.0.1:0", Token: testTok},
		"empty addr":   {Token: testTok},
		"empty token":  {Addr: testAddr},
		"odd token":    {Addr: testAddr, Token: "short"},
		"token space":  {Addr: testAddr, Token: testTok + " --x"},
		"token at":     {Addr: testAddr, Token: testTok + "@evil"},
	} {
		if f, err := Flags(p); err == nil {
			t.Errorf("proxy %s: accepted, flags %q", name, f)
		}
	}
}

// Every board ends by resetting the browser, whether it was read, failed or
// was cancelled, so the next board starts a fresh process. A board rejected
// before any page is loaded has nothing to reset.
func TestListResetsTheBrowserOncePerBoard(t *testing.T) {
	good := ldPage("t", `{"@type":"JobPosting","title":"T"}`)
	pages := func() map[string]string {
		return map[string]string{listURL: listing("/jobs/a-1"), "https://careers.example.com/jobs/a-1": good}
	}
	read := &fakeBrowser{pages: pages()}
	if _, err := newSrc(read).List(context.Background(), board()); err != nil || read.resetCount() != 1 {
		t.Fatalf("read board: err %v, %d resets", err, read.resetCount())
	}
	failed := &fakeBrowser{pages: pages(), errs: map[string]error{"https://careers.example.com/jobs/a-1": ErrHTTPStatus}}
	if _, err := newSrc(failed).List(context.Background(), board()); !errors.Is(err, ErrHTTPStatus) || failed.resetCount() != 1 {
		t.Fatalf("failed board: err %v, %d resets", err, failed.resetCount())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := &fakeBrowser{pages: pages(), hook: func(context.Context, string) { cancel() }}
	if _, err := newSrc(cancelled).List(ctx, board()); !errors.Is(err, context.Canceled) || cancelled.resetCount() != 1 {
		t.Fatalf("cancelled board: err %v, %d resets", err, cancelled.resetCount())
	}
	rejected := &fakeBrowser{pages: pages()}
	b := board()
	b.LinkPattern = "("
	if _, err := newSrc(rejected).List(context.Background(), b); err == nil || rejected.resetCount() != 0 || len(rejected.calls) != 0 {
		t.Fatalf("rejected board: err %v, %d resets, %d loads", err, rejected.resetCount(), len(rejected.calls))
	}

	// One source, two boards: one Reset each, in order.
	twice := &fakeBrowser{pages: pages()}
	s := newSrc(twice)
	for i := 1; i <= 2; i++ {
		if _, err := s.List(context.Background(), board()); err != nil || twice.resetCount() != i {
			t.Fatalf("board %d: err %v, %d resets", i, err, twice.resetCount())
		}
	}
}

// A browser that cannot be ended fails a board that was otherwise read: the
// postings are not returned, so the reconciler leaves the board unchanged.
// A board that already failed keeps its own error.
func TestResetFailureFailsTheBoard(t *testing.T) {
	boom := errors.New("lightpanda (pid 1) did not exit after a kill")
	r := &fakeBrowser{resetErr: boom, pages: map[string]string{
		listURL: listing("/jobs/a-1"), "https://careers.example.com/jobs/a-1": ldPage("t", `{"@type":"JobPosting","title":"T"}`)}}
	got, err := newSrc(r).List(context.Background(), board())
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("got %d postings, err %v; want none and the reset error", len(got), err)
	}
	r.errs = map[string]error{listURL: ErrHTTPStatus}
	if _, err := newSrc(r).List(context.Background(), board()); !errors.Is(err, ErrHTTPStatus) || errors.Is(err, boom) {
		t.Fatalf("err %v; want the page's own error", err)
	}
}

// A browser error keeps its sentinel through the token redaction, so an
// empty page is still retried once, and the message never carries the token.
func TestBrowserErrorsKeepTheirSentinel(t *testing.T) {
	noEmptyWait(t)
	const post = "https://careers.example.com/jobs/a-1"
	r := &fakeBrowser{pages: map[string]string{listURL: listing("/jobs/a-1")},
		errs: map[string]error{post: fmt.Errorf("via http://vani:%s@127.0.0.1: %w", testTok, ErrEmptyDocument)}}
	_, err := newSrc(r).List(context.Background(), board())
	if !errors.Is(err, ErrEmptyDocument) || strings.Contains(err.Error(), testTok) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("err %v", err)
	}
	if n := countRuns(r, post); n != 2 {
		t.Errorf("empty page loaded %d times, want 2", n)
	}
}

func TestBoardAndProxyValidation(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{}}
	good := board()
	mod := func(f func(*sources.Board)) sources.Board { b := good; f(&b); return b }
	for name, b := range map[string]sources.Board{
		"wrong ats":     mod(func(b *sources.Board) { b.ATS = posting.Lever }),
		"no token":      mod(func(b *sources.Board) { b.Token = "" }),
		"http url":      mod(func(b *sources.Board) { b.URL = "http://careers.example.com/jobs" }),
		"flag-like url": mod(func(b *sources.Board) { b.URL = "--dump" }),
		"url creds":     mod(func(b *sources.Board) { b.URL = "https://u:p@careers.example.com/" }),
		"no pattern":    mod(func(b *sources.Board) { b.LinkPattern = "" }),
		"bad pattern":   mod(func(b *sources.Board) { b.LinkPattern = "(" }),
		"huge pattern":  mod(func(b *sources.Board) { b.LinkPattern = strings.Repeat("a", maxPatternLen+1) }),
		"token colon":   mod(func(b *sources.Board) { b.Token = "a:b" }),
		"empty url":     mod(func(b *sources.Board) { b.URL = "" }),
		"file url":      mod(func(b *sources.Board) { b.URL = "file:///etc/passwd" }),
	} {
		if _, err := newSrc(r).List(context.Background(), b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("a rejected board still loaded pages: %v", r.calls)
	}
	if _, err := New(nil, ProxyInfo{Addr: testAddr, Token: testTok}).List(context.Background(), good); err == nil {
		t.Error("nil browser accepted")
	}
	if New(r, ProxyInfo{}).ATS() != posting.Page {
		t.Error("wrong ATS")
	}
}

func TestStdoutOverCapFailsTheBoard(t *testing.T) {
	big := &bigBrowser{n: MaxStdout + 1}
	if _, err := newSrc(big).List(context.Background(), board()); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("err %v, want ErrOutputTooLarge", err)
	}
}

// Lightpanda exits 0 with a bare doctype when the first response misses its
// --wait-ms (reproduced by holding the proxy CONNECT past 5 s). Such a dump,
// on the listing or on any posting page, fails the board: it must never
// become an empty posting or an empty listing.
func TestEmptyDocumentFailsTheBoard(t *testing.T) {
	noEmptyWait(t)
	const post = "https://careers.example.com/jobs/a-1"
	for name, dump := range map[string]string{
		"lightpanda's empty dump": "<!DOCTYPE html>",
		"with newline":            "<!DOCTYPE html>\n",
		"lower case":              "  <!doctype html>  ",
		"nothing":                 "",
		"whitespace":              " \n\t ",
		"unterminated doctype":    "<!DOCTYPE html",
		"text only":               "Navigation failed",
	} {
		for _, where := range []string{"listing", "posting"} {
			pages := map[string]string{listURL: listing("/jobs/a-1"), post: ldPage("t", `{"@type":"JobPosting","title":"T"}`)}
			if where == "listing" {
				pages[listURL] = dump
			} else {
				pages[post] = dump
			}
			got, err := newSrc(&fakeBrowser{pages: pages}).List(context.Background(), board())
			if !errors.Is(err, ErrEmptyDocument) {
				t.Errorf("%s on the %s: got %d postings, err %v; want ErrEmptyDocument", name, where, len(got), err)
			}
		}
	}
	// An empty dump is asked again exactly once, never more.
	r := &fakeBrowser{pages: map[string]string{listURL: listing("/jobs/a-1"), post: "<!DOCTYPE html>"}}
	if _, err := newSrc(r).List(context.Background(), board()); !errors.Is(err, ErrEmptyDocument) {
		t.Fatalf("err %v", err)
	}
	if n := countRuns(r, post); n != 2 {
		t.Errorf("empty posting page fetched %d times, want 2 (one retry)", n)
	}

	// The smallest real document still passes.
	for _, doc := range []string{"<!DOCTYPE html><html><head></head><body></body></html>", "<html></html>", "<p>x</p>"} {
		if !hasElement(doc) {
			t.Errorf("hasElement(%q) = false", doc)
		}
	}
}

func noEmptyWait(t *testing.T) {
	t.Helper()
	old := emptyRetryWait
	emptyRetryWait = 0
	t.Cleanup(func() { emptyRetryWait = old })
}

func countRuns(r *fakeBrowser, u string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == u {
			n++
		}
	}
	return n
}

// A page whose first load dumped an empty document but whose retry loads is
// read normally: one slow response must not fail a whole board.
func TestEmptyDocumentRetrySucceeds(t *testing.T) {
	noEmptyWait(t)
	const post = "https://careers.example.com/jobs/a-1"
	r := &fakeBrowser{pages: map[string]string{listURL: listing("/jobs/a-1"), post: "<!DOCTYPE html>"}}
	r.hook = func(_ context.Context, u string) {
		if u == post && countRuns(r, post) == 2 {
			r.mu.Lock()
			r.pages[post] = ldPage("t", `{"@type":"JobPosting","title":"Second Try"}`)
			r.mu.Unlock()
		}
	}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil || len(got) != 1 || got[0].Title != "Second Try" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	// A failed run (a bot challenge, a 404) is never retried.
	r2 := &fakeBrowser{pages: map[string]string{listURL: listing("/jobs/a-1")}, errs: map[string]error{post: errors.New("err=BotChallenge")}}
	if _, err := newSrc(r2).List(context.Background(), board()); err == nil || countRuns(r2, post) != 1 {
		t.Fatalf("a failed run was retried: runs=%d err=%v", countRuns(r2, post), err)
	}
}

// Cancelling during the retry pause returns promptly with the context error.
func TestEmptyDocumentRetryHonoursContext(t *testing.T) {
	old := emptyRetryWait
	emptyRetryWait = time.Minute
	t.Cleanup(func() { emptyRetryWait = old })
	r := &fakeBrowser{pages: map[string]string{listURL: "<!DOCTYPE html>"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := newSrc(r).List(ctx, board()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second || countRuns(r, listURL) != 1 {
		t.Fatalf("took %v with %d runs", time.Since(start), countRuns(r, listURL))
	}
}

type bigBrowser struct{ n int }

func (b *bigBrowser) Load(context.Context, string) ([]byte, error) { return make([]byte, b.n), nil }
func (b *bigBrowser) Reset() error                                 { return nil }

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeBrowser{
		pages: map[string]string{
			listURL:                                listing("/jobs/a-1", "/jobs/b-2"),
			"https://careers.example.com/jobs/a-1": ldPage("t", `{"@type":"JobPosting","title":"T"}`),
			"https://careers.example.com/jobs/b-2": ldPage("t", `{"@type":"JobPosting","title":"T"}`),
		},
		hook: func(_ context.Context, u string) {
			if strings.HasSuffix(u, "a-1") {
				cancel()
			}
		},
	}
	got, err := newSrc(r).List(ctx, board())
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("got %d postings, err %v; want nil and context.Canceled", len(got), err)
	}
	if len(r.calls) != 2 {
		t.Errorf("%d fetches after cancel, want listing + the in-flight page only", len(r.calls))
	}
}

func TestScannerSurvivesHostileHTML(t *testing.T) {
	for name, doc := range map[string]string{
		"unterminated comment": `<a href="/jobs/1">x</a><!-- <a href="/jobs/2">`,
		"unterminated quote":   `<a href="/jobs/1">x</a><a href="/jobs/2`,
		"unterminated tag":     `<a href="/jobs/1">x</a><a `,
		"unterminated script":  `<a href="/jobs/1">x</a><script>`,
		"lone lt":              `< < <a href="/jobs/1">x</a> <<<`,
		"empty":                ``,
		"binary":               "\x00\xff<a\x00href=\"/jobs/1\">",
		"deep":                 strings.Repeat("<a href=", 50000),
	} {
		sc := scanHTML(doc) // must terminate without panicking
		if name == "unterminated comment" || name == "unterminated quote" || name == "unterminated tag" || name == "unterminated script" || name == "lone lt" {
			if len(sc.links) < 1 || sc.links[0].href != "/jobs/1" {
				t.Errorf("%s: lost the well-formed link before the damage: %+v", name, sc.links)
			}
		}
	}
}

func TestScanTitleAndLD(t *testing.T) {
	sc := scanHTML(`<HTML><HEAD><TITLE> A &amp; B </TITLE><SCRIPT TYPE="Application/LD+JSON">{"a":1}</SCRIPT>
		<script type="text/javascript">{"b":2}</script></HEAD><BODY class=x><a href=/x>t</a></BODY>`)
	if sc.title != "A & B" || len(sc.ldJSON) != 1 || sc.ldJSON[0] != `{"a":1}` || sc.bodyStart == 0 {
		t.Errorf("%+v", sc)
	}
}

func mustPattern(t *testing.T, p string) *regexp.Regexp {
	t.Helper()
	re, err := compilePattern(p)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
