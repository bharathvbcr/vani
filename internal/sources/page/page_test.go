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

// fakeRunner serves canned pages by the URL in the last argument and records
// every argv.
type fakeRunner struct {
	mu    sync.Mutex
	pages map[string]string
	errs  map[string]error
	calls [][]string
	hook  func(ctx context.Context, url string) // optional, runs before answering
}

func (f *fakeRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	u := args[len(args)-1]
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

func newSrc(r Runner) *source {
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
	r := &fakeRunner{pages: map[string]string{
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
	got, err := newSrc(&fakeRunner{pages: pages}).List(context.Background(), baseBoard())
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
	if got, err := newSrc(&fakeRunner{pages: pages}).List(context.Background(), baseBoard()); err == nil {
		t.Fatalf("a base on another host produced postings: %+v", got)
	}
}

func TestOverCapIsAnErrorNotATruncation(t *testing.T) {
	var hrefs []string
	for i := 0; i <= MaxLinks; i++ {
		hrefs = append(hrefs, "/jobs/j"+strconv.Itoa(i))
	}
	r := &fakeRunner{pages: map[string]string{listURL: listing(hrefs...)}}
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
	got, err := newSrc(&fakeRunner{pages: pages}).List(context.Background(), board())
	if err != nil || len(got) != MaxLinks {
		t.Fatalf("at the cap: %d postings, err %v", len(got), err)
	}
}

func TestNoMatchingLinksIsAnError(t *testing.T) {
	r := &fakeRunner{pages: map[string]string{listURL: listing("/about", "/contact")}}
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := findJobPosting([]string{tc.ld})
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
	} {
		if _, ok := findJobPosting([]string{ld}); ok {
			t.Errorf("%s: found a JobPosting", name)
		}
	}
	// A bad block does not hide a later good one.
	if jp, ok := findJobPosting([]string{`{bad`, `{"@type":"JobPosting","title":"late"}`}); !ok || jp.title != "late" {
		t.Errorf("later block ignored: %+v %v", jp, ok)
	}
}

func TestFallbackWithoutJobPosting(t *testing.T) {
	r := &fakeRunner{pages: map[string]string{
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
	r := &fakeRunner{pages: map[string]string{
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
	r := &fakeRunner{pages: map[string]string{
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
	for name, r := range map[string]*fakeRunner{
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
	r := &fakeRunner{pages: map[string]string{listURL: listing("/jobs/a-1")},
		errs: map[string]error{"https://careers.example.com/jobs/a-1": errors.New("proxy http://vani:" + testTok + "@127.0.0.1 refused")}}
	_, err := newSrc(r).List(context.Background(), board())
	if err == nil || strings.Contains(err.Error(), testTok) {
		t.Fatalf("err %v", err)
	}
}

func TestArgvSafety(t *testing.T) {
	evil := `--dump-selector=x" ; rm -rf / ; "`
	r := &fakeRunner{pages: map[string]string{
		listURL:                                `<a href="/jobs/a-1">` + evil + `</a><a href="/jobs/b-2">$(touch pwned)</a>`,
		"https://careers.example.com/jobs/a-1": ldPage(evil, `{"@type":"JobPosting","title":"`+evil+`","description":"--insecure-disable-tls-host-verification"}`),
		"https://careers.example.com/jobs/b-2": ldPage("`id`", `{"@type":"JobPosting","title":"T"}`),
	}}
	if _, err := newSrc(r).List(context.Background(), board()); err != nil {
		t.Fatal(err)
	}
	fixed := map[string]bool{
		"fetch": true, "--dump": true, "html": true, "--obey-robots": true, "--block-private-networks": true,
		"--fail-on-http-error": true, "--http-timeout": true, strconv.Itoa(httpTimeoutMs): true, "--http-max-host-open": true, "1": true,
		"--block-cidrs": true, "-127.0.0.1/32": true, "--http-proxy": true, "--proxy-bearer-token": true, testTok: true,
		"http://vani:" + testTok + "@" + testAddr: true,
		listURL: true, "https://careers.example.com/jobs/a-1": true, "https://careers.example.com/jobs/b-2": true,
	}
	for _, argv := range r.calls {
		have := map[string]bool{}
		for _, a := range argv {
			if !fixed[a] {
				t.Errorf("unexpected argument %q in %v", a, argv)
			}
			have[a] = true
		}
		for _, must := range []string{"--obey-robots", "--block-private-networks", "--http-proxy", "--proxy-bearer-token", "--fail-on-http-error"} {
			if !have[must] {
				t.Errorf("argv lacks %s: %v", must, argv)
			}
		}
		// One connection per host per run keeps the proxy's per-host pacing
		// from queueing several CONNECTs inside one page load.
		if i := slices.Index(argv, "--http-max-host-open"); i < 0 || i+1 >= len(argv) || argv[i+1] != "1" {
			t.Errorf("argv lacks --http-max-host-open 1: %v", argv)
		}
		if argv[len(argv)-1][:8] != "https://" {
			t.Errorf("last argument is not an https URL: %v", argv)
		}
	}
}

func TestBoardAndProxyValidation(t *testing.T) {
	r := &fakeRunner{pages: map[string]string{}}
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
	for name, p := range map[string]ProxyInfo{
		"not loopback": {Addr: "10.0.0.1:80", Token: testTok},
		"hostname":     {Addr: "localhost:80", Token: testTok},
		"no port":      {Addr: "127.0.0.1", Token: testTok},
		"empty token":  {Addr: testAddr},
		"odd token":    {Addr: testAddr, Token: "short"},
		"token space":  {Addr: testAddr, Token: testTok + " --x"},
	} {
		s := New(r, p).(*source)
		if _, err := s.List(context.Background(), good); err == nil {
			t.Errorf("proxy %s: accepted", name)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("a rejected board or proxy still ran lightpanda: %v", r.calls)
	}
	if _, err := New(nil, ProxyInfo{Addr: testAddr, Token: testTok}).List(context.Background(), good); err == nil {
		t.Error("nil runner accepted")
	}
	if New(r, ProxyInfo{}).ATS() != posting.Page {
		t.Error("wrong ATS")
	}
}

func TestStdoutOverCapFailsTheBoard(t *testing.T) {
	big := &bigRunner{n: MaxStdout + 1}
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
			got, err := newSrc(&fakeRunner{pages: pages}).List(context.Background(), board())
			if !errors.Is(err, ErrEmptyDocument) {
				t.Errorf("%s on the %s: got %d postings, err %v; want ErrEmptyDocument", name, where, len(got), err)
			}
		}
	}
	// An empty dump is asked again exactly once, never more.
	r := &fakeRunner{pages: map[string]string{listURL: listing("/jobs/a-1"), post: "<!DOCTYPE html>"}}
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

func countRuns(r *fakeRunner, u string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, argv := range r.calls {
		if argv[len(argv)-1] == u {
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
	r := &fakeRunner{pages: map[string]string{listURL: listing("/jobs/a-1"), post: "<!DOCTYPE html>"}}
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
	r2 := &fakeRunner{pages: map[string]string{listURL: listing("/jobs/a-1")}, errs: map[string]error{post: errors.New("err=BotChallenge")}}
	if _, err := newSrc(r2).List(context.Background(), board()); err == nil || countRuns(r2, post) != 1 {
		t.Fatalf("a failed run was retried: runs=%d err=%v", countRuns(r2, post), err)
	}
}

// Cancelling during the retry pause returns promptly with the context error.
func TestEmptyDocumentRetryHonoursContext(t *testing.T) {
	old := emptyRetryWait
	emptyRetryWait = time.Minute
	t.Cleanup(func() { emptyRetryWait = old })
	r := &fakeRunner{pages: map[string]string{listURL: "<!DOCTYPE html>"}}
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

type bigRunner struct{ n int }

func (b *bigRunner) Run(context.Context, []string) ([]byte, error) { return make([]byte, b.n), nil }

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &fakeRunner{
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
