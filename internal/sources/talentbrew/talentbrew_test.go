package talentbrew

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var (
	fixedNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	board    = sources.Board{ATS: posting.TalentBrew, Token: "intuit", Company: "Intuit", Host: "jobs.intuit.com"}
	citi     = sources.Board{ATS: posting.TalentBrew, Token: "citi", Host: "jobs.citi.com"}
)

const (
	sitemapURL = "https://jobs.intuit.com/sitemap.xml"
	robotsURL  = "https://jobs.intuit.com/robots.txt"
	// robotsTxt is both live robots.txt files (jobs.intuit.com and jobs.citi.com,
	// 2026-10-28), byte for byte.
	robotsTxt = "User-agent: *\r\nDisallow:/search-jobs/\r\n"
	// upfront is the requests made before the first job page: robots.txt for
	// /sitemap.xml, robots.txt for /job/, and the sitemap.
	upfront = 3

	// The two Intuit jobs of testdata/intuit-sitemap.xml, as the sitemap writes them.
	mlURL     = "https://jobs.intuit.com/job/mountain-view/staff-machine-learning-engineer/27595/87369450000"
	remoteURL = "https://jobs.intuit.com/job/sacramento/central-planning-sr-strategy-and-ops-manager-emerging-mid-market/27595/97197172688"
	puneURL   = "https://jobs.citi.com/job/pune/java-angular-full-stack-technical-lead/287/102161084880"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

// newDoer answers replies, and the live robots.txt of both sites unless replies
// names one.
func newDoer(replies map[string]srctest.Reply) *srctest.Doer {
	for _, u := range []string{robotsURL, "https://jobs.citi.com/robots.txt"} {
		if _, set := replies[u]; !set {
			replies[u] = ok(robotsTxt)
		}
	}
	return srctest.New(replies)
}

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// newSource is a source with no bounded step (New), whose waits cost nothing.
func newSource(d *srctest.Doer) *source {
	return &source{d: d, now: func() time.Time { return fixedNow }, wait: noWait}
}

// newBounded is a source from NewWith. Its waits are recorded, not slept.
func newBounded(t *testing.T, d *srctest.Doer, o Options) (*source, *[]Stats, *[]time.Duration) {
	t.Helper()
	var reps []Stats
	var waits []time.Duration
	o.Report = func(_ sources.Board, st Stats) { reps = append(reps, st) }
	o.Wait = func(ctx context.Context, w time.Duration) error { waits = append(waits, w); return ctx.Err() }
	s, err := NewWith(d, o)
	if err != nil {
		t.Fatal(err)
	}
	src := s.(*source)
	src.now = func() time.Time { return fixedNow }
	return src, &reps, &waits
}

// jobAt is the address of the i-th synthetic job of a site.
func jobAt(host, company string, i int) string {
	return fmt.Sprintf("https://%s/job/mountain-view/role-%d/%s/%d", host, i, company, 102000000000+i)
}

func idOf(i int) string { return fmt.Sprint(102000000000 + i) }

// sitemapOf is a sitemap of the given job addresses among pages that are not
// jobs, with the byte-order mark and the layout the live ones have.
func sitemapOf(locs ...string) string {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\"?><urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">")
	b.WriteString("<url><loc>https://jobs.intuit.com</loc></url>")
	b.WriteString("<url><loc>https://jobs.intuit.com/category/data-jobs/27595/68338/1</loc><lastmod>2026-10-28T05:34:52.6997394Z</lastmod><changefreq>weekly</changefreq><priority>0.6</priority></url>")
	for _, l := range locs {
		fmt.Fprintf(&b, "<url><loc>%s</loc><lastmod>2026-10-28T05:34:54.4918809Z</lastmod></url>", l)
	}
	b.WriteString("</urlset>")
	return b.String()
}

// ldPage is a job page carrying one JobPosting for the job at addr.
func ldPage(addr, title string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html><head><title>x</title><script src="//cdn.example/a.js"></script>`+
		`<script type="application/ld+json">{"@context":"http://schema.org","@type":"JobPosting","datePosted":"2026-10-27",`+
		`"description":"<p>Build &amp; ship</p>","title":%q,"url":%q,"hiringOrganization":{"name":"Intuit"},`+
		`"jobLocation":[{"@type":"Place","address":{"addressLocality":"Mountain View","addressRegion":"California","addressCountry":"United States"}}]}`+
		`</script></head><body></body></html>`, title, addr)
}

// siteDoer answers the sitemap with locs and every job page with a good page,
// except the addresses in bad.
func siteDoer(locs []string, bad map[string]srctest.Reply) *srctest.Doer {
	site := sitemapOf(locs...)
	set := map[string]bool{}
	for _, l := range locs {
		set[l] = true
	}
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case r.URL == robotsURL:
			return ok(robotsTxt), true
		case r.URL == sitemapURL:
			return ok(site), true
		case set[r.URL]:
			if rp, hit := bad[r.URL]; hit {
				return rp, true
			}
			return ok(ldPage(r.URL, "Role of "+r.URL[strings.LastIndex(r.URL, "/")+1:])), true
		}
		return srctest.Reply{}, false
	})
}

func locsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = jobAt("jobs.intuit.com", "27595", i)
	}
	return out
}

func notFound() srctest.Reply {
	return srctest.Reply{Status: http.StatusNotFound, Body: []byte("<html>not found</html>")}
}

// ---- the live shapes ----

func TestFixturesStartWithAByteOrderMark(t *testing.T) {
	// Both live sitemaps start with U+FEFF; the parser tests below only mean
	// something while the fixtures keep it.
	for _, f := range []string{"intuit-sitemap.xml", "citi-sitemap.xml"} {
		if !bytes.HasPrefix(fixture(t, f), []byte("\xef\xbb\xbf")) {
			t.Errorf("%s lost its byte-order mark", f)
		}
	}
}

func TestListReadsTheIntuitSitemapAndJobPages(t *testing.T) {
	d := newDoer(map[string]srctest.Reply{
		sitemapURL: {Status: 200, Body: fixture(t, "intuit-sitemap.xml")},
		mlURL:      {Status: 200, Body: fixture(t, "intuit-job.html")},
		remoteURL:  {Status: 200, Body: fixture(t, "intuit-job-remote.html")},
	})
	got, err := newSource(d).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2 (the sitemap's other pages are not jobs): %+v", len(got), got)
	}
	ml, rm := got[0], got[1]
	if ml.Key != "tb:intuit:87369450000" || ml.ATS != posting.TalentBrew || ml.Company != "Intuit" ||
		ml.Title != "Staff Machine Learning Engineer" || ml.URL != mlURL {
		t.Errorf("ml identity: %+v", ml)
	}
	if ml.Location != "Mountain View, California, United States" || ml.Remote {
		t.Errorf("ml location %q remote %v", ml.Location, ml.Remote)
	}
	if want := time.Date(2024, 7, 10, 0, 0, 0, 0, time.UTC); !ml.PostedAt.Equal(want) {
		t.Errorf("ml posted %v, want the date-only datePosted as %v", ml.PostedAt, want)
	}
	if !ml.SeenAt.Equal(fixedNow) {
		t.Errorf("seen %v", ml.SeenAt)
	}
	if !strings.HasPrefix(ml.Description, "Role: full-time\n\n") || !strings.Contains(ml.Description, "Come join Intuit as a Staff Machine Learning Engineer!") {
		t.Errorf("ml description %q", ml.Description)
	}
	if strings.Contains(ml.Description, "Pay range") || strings.Contains(ml.Description, "<p") || strings.Contains(ml.Description, "&nbsp;") {
		t.Errorf("ml description has a pay line from the empty baseSalary template, or markup: %q", ml.Description)
	}
	if rm.Key != "tb:intuit:97197172688" || rm.Title != "Central Planning, Sr Strategy & Ops Manager — Emerging Mid-Market" {
		t.Errorf("remote identity: %+v", rm)
	}
	if rm.Location != "Sacramento, California, United States; San Francisco, California, United States" || !rm.Remote {
		t.Errorf("remote location %q remote %v: the Remote marker is no place, and sets Remote", rm.Location, rm.Remote)
	}
	if !strings.HasPrefix(rm.Description, "Role: full-time, remote\n\n") {
		t.Errorf("remote description %q", rm.Description)
	}
	// The page, not the identifier, names the job: 3497 and 22585 are Intuit's
	// requisition numbers.
	for _, p := range got {
		if strings.Contains(string(p.Key), "3497") || strings.Contains(string(p.Key), "22585") {
			t.Errorf("key %s uses the requisition number", p.Key)
		}
	}
	urls := d.URLs()
	if len(urls) != upfront+2 || urls[0] != robotsURL || urls[upfront-1] != sitemapURL {
		t.Fatalf("requests %v: want robots.txt, robots.txt, the sitemap, then each job page", urls)
	}
	for _, u := range urls {
		if strings.Contains(u, "/search-jobs") || !strings.HasPrefix(u, "https://jobs.intuit.com/") {
			t.Errorf("request %s is outside the pages robots.txt allows", u)
		}
	}
}

func TestListReadsCitiThroughTheSameSource(t *testing.T) {
	d := newDoer(map[string]srctest.Reply{
		"https://jobs.citi.com/sitemap.xml": {Status: 200, Body: fixture(t, "citi-sitemap.xml")},
		puneURL:                             {Status: 200, Body: fixture(t, "citi-job.html")},
	})
	var judged []posting.Posting
	s, _, _ := newBounded(t, d, Options{
		Stored: func(sources.Board) []posting.Posting { return nil },
		DropListed: func(p posting.Posting) bool {
			judged = append(judged, p)
			return strings.Contains(p.Location, "Chiyoda")
		},
	})
	got, err := s.List(context.Background(), citi)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d postings, want Pune only (the Chiyoda job was dropped before its page): %+v", len(got), got)
	}
	p := got[0]
	if p.Key != "tb:citi:102161084880" || p.Title != "Java Angular Full Stack Technical Lead" || p.Company != "CITI" ||
		p.Location != "Pune, MH, IN" || p.URL != puneURL || p.Remote {
		t.Errorf("citi posting %+v", p)
	}
	if !p.PostedAt.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("posted %v", p.PostedAt)
	}
	if strings.HasPrefix(p.Description, "Role:") || !strings.HasPrefix(p.Description, "The Applications Development Technology Lead Analyst") {
		t.Errorf("citi has no employmentType, so no role line: %q", p.Description)
	}
	// The Japanese slug is percent-encoded in the sitemap; DropListed sees it decoded.
	var jp posting.Posting
	for _, j := range judged {
		if j.Location == "Chiyoda" {
			jp = j
		}
	}
	if jp.Title != "2028卒向け オヘレーション部門オンラインセミナー" || jp.Company != "citi" || jp.ATS != posting.TalentBrew || jp.Description != "" {
		t.Errorf("listed Chiyoda posting %+v", jp)
	}
	for _, u := range d.URLs() {
		if strings.Contains(u, "chiyoda") {
			t.Errorf("a dropped job was fetched: %s", u)
		}
	}
}

// ---- the sitemap, adversarially ----

func TestSitemapIsTheWholeListingOrAnError(t *testing.T) {
	jobs2 := sitemapOf(locsN(2)...)
	var bigEntries strings.Builder
	bigEntries.WriteString("<?xml version=\"1.0\"?><urlset>")
	for i := 0; i <= maxEntries; i++ {
		fmt.Fprintf(&bigEntries, "<url><loc>https://jobs.intuit.com/blog-%d</loc></url>", i)
	}
	bigEntries.WriteString("</urlset>")
	for name, tc := range map[string]struct {
		reply srctest.Reply
		want  string
	}{
		"sitemap index":          {ok(`<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://jobs.intuit.com/sitemap-1.xml</loc></sitemap></sitemapindex>`), "sitemap index"},
		"gzip":                   {srctest.Reply{Status: 200, Body: []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\x03abc")}, "gzip"},
		"truncated":              {ok(jobs2[:len(jobs2)/2]), "sitemap"},
		"cut before the end tag": {ok(strings.TrimSuffix(jobs2, "</urlset>")), "sitemap"},
		"not xml":                {ok("<html><body>maintenance</body></html>"), "root element"},
		"json":                   {ok(`{"urls":[]}`), "sitemap"},
		"empty":                  {ok(""), "sitemap"},
		"whitespace":             {ok("\n  \n"), "sitemap"},
		"no jobs":                {ok(sitemapOf()), "no job page"},
		"text after":             {ok(jobs2 + "trailing"), "after the closing"},
		"element after":          {ok(jobs2 + "<urlset></urlset>"), "after the closing"},
		"too many entries":       {ok(bigEntries.String()), "more than"},
		"oversized":              {srctest.Reply{Status: 200, Body: bytes.Repeat([]byte("x"), maxSitemapBody+1)}, "too large"},
		"500":                    {srctest.Reply{Status: 500, Body: []byte("oops")}, "500"},
		"403":                    {srctest.Reply{Status: 403, Body: []byte("denied")}, "403"},
		"404":                    {notFound(), "no sitemap"},
		"entity":                 {ok(`<?xml version="1.0"?><!DOCTYPE urlset [<!ENTITY x SYSTEM "file:///etc/passwd">]><urlset><url><loc>&x;</loc></url></urlset>`), "sitemap"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDoer(map[string]srctest.Reply{sitemapURL: tc.reply})
			got, err := newSource(d).List(context.Background(), board)
			if err == nil {
				t.Fatalf("got %d postings and no error, want the board to fail", len(got))
			}
			if got != nil {
				t.Errorf("a failed board returned %d postings", len(got))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if n := len(d.URLs()); n != upfront {
				t.Errorf("%d requests after a bad sitemap, want %d: %v", n, upfront, d.URLs())
			}
		})
	}
}

func TestSitemapMissingIsBoardNotFound(t *testing.T) {
	d := newDoer(map[string]srctest.Reply{sitemapURL: notFound()})
	_, err := newSource(d).List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("err %v, want ErrBoardNotFound", err)
	}
}

func TestSitemapTimeoutIsAnErrorNotAnEmptyList(t *testing.T) {
	s := &source{d: &toggleDoer{inner: siteDoer(locsN(2), nil), failURL: sitemapURL}, now: time.Now, wait: noWait}
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %d postings, err %v; want a deadline error", len(got), err)
	}
}

// ---- robots.txt ----

func TestRobotsDisallowingOurPathsFailsTheBoardBeforeTheSitemap(t *testing.T) {
	for name, txt := range map[string]string{
		"job pages":    "User-agent: *\nDisallow: /job/\n",
		"the sitemap":  "User-agent: *\nDisallow: /sitemap.xml\n",
		"everything":   "User-agent: *\nDisallow: /\n",
		"our own name": "User-agent: Vani\nDisallow: /\n\nUser-agent: *\nAllow: /\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := newDoer(map[string]srctest.Reply{robotsURL: ok(txt)})
			got, err := newSource(d).List(context.Background(), board)
			if err == nil || got != nil || !strings.Contains(err.Error(), "disallows") {
				t.Fatalf("got %d postings, err %v; want a refusal", len(got), err)
			}
			for _, u := range d.URLs() {
				if u == sitemapURL {
					t.Fatalf("the sitemap was requested after robots.txt disallowed it: %v", d.URLs())
				}
			}
		})
	}
}

// What the live files say is allowed: the sitemap and /job/, with /search-jobs/
// off limits, which this source never asks for.
func TestLiveRobotsAllowsTheSitemapAndJobPages(t *testing.T) {
	for _, txt := range []string{robotsTxt, "", "User-agent: *\nDisallow:\n", "User-agent: *\nDisallow: /search-jobs/\nAllow: /job/\n"} {
		d := siteDoer(locsN(1), nil)
		d.Handler = wrapRobots(d.Handler, txt)
		if got, err := newSource(d).List(context.Background(), board); err != nil || len(got) != 1 {
			t.Fatalf("robots %q: %d postings, %v", txt, len(got), err)
		}
		for _, u := range d.URLs() {
			if strings.Contains(u, "/search-jobs") {
				t.Fatalf("asked for %s", u)
			}
		}
	}
}

func wrapRobots(h func(srctest.Request) (srctest.Reply, bool), txt string) func(srctest.Request) (srctest.Reply, bool) {
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == robotsURL {
			return ok(txt), true
		}
		return h(r)
	}
}

func TestRobotsThatCannotBeReadFailsTheBoard(t *testing.T) {
	for name, rp := range map[string]srctest.Reply{
		"500":       {Status: 500, Body: []byte("oops")},
		"503":       {Status: 503, Body: []byte("later")},
		"oversized": {Status: 200, Body: []byte(strings.Repeat("#", 600<<10))},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDoer(map[string]srctest.Reply{robotsURL: rp})
			got, err := newSource(d).List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %d postings, err %v; a robots.txt that could not be read must not pass as allowed", len(got), err)
			}
			for _, u := range d.URLs() {
				if u == sitemapURL {
					t.Fatalf("the sitemap was requested: %v", d.URLs())
				}
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		s := &source{d: &toggleDoer{inner: siteDoer(locsN(1), nil), failURL: robotsURL}, now: time.Now, wait: noWait}
		if got, err := s.List(context.Background(), board); err == nil || got != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%d postings, %v", len(got), err)
		}
	})
	t.Run("a 404 is no robots.txt", func(t *testing.T) {
		d := siteDoer(locsN(1), nil)
		d.Handler = wrapRobots(d.Handler, "")
		inner := d.Handler
		d.Handler = func(r srctest.Request) (srctest.Reply, bool) {
			if r.URL == robotsURL {
				return notFound(), true
			}
			return inner(r)
		}
		if got, err := newSource(d).List(context.Background(), board); err != nil || len(got) != 1 {
			t.Fatalf("%d postings, %v", len(got), err)
		}
	})
}

// Citi's sitemap on 2026-11-02 had one job whose percent-encoded title is 341
// bytes. The whole address is 392 bytes, under the sitemap protocol's limit,
// and it is a job page: city, title, company, id.
func TestCitiJapaneseTitlePast300EncodedBytes(t *testing.T) {
	const loc = "https://jobs.citi.com/job/chiyoda/%e5%9b%bd%e5%86%85%e6%96%b0%e5%8d%92%e6%8e%a1%e7%94%a8%e6%9c%ac%e9%81%b8%e8%80%83-%e3%82%aa%e3%83%98%e3%83%ac%e3%83%bc%e3%82%b7%e3%83%a7%e3%83%b3%e9%83%a8%e9%96%80-%e6%9d%b1%e4%ba%ac-%e3%83%95%e3%83%ab%e3%82%bf%e3%82%a4%e3%83%a0-%e3%82%a2%e3%83%8a%e3%83%aa%e3%82%b9%e3%83%88-28%e5%8d%92-operations-full-time-analyst-tokyo-japan-class-of-2028/287/102713775456"
	r, company, isJob, err := jobRow(loc, "jobs.citi.com")
	if err != nil || !isJob || company != "287" || r.id != "102713775456" || r.city != "chiyoda" {
		t.Fatalf("row %+v company %s job %v err %v", r, company, isJob, err)
	}
	if title := slugText(r.slug); !strings.Contains(title, "Operations") || !strings.Contains(title, "国内新卒採用本選考") {
		t.Errorf("title from the slug: %q", title)
	}
	if len(loc) > maxLocLen {
		t.Errorf("the live address is %d bytes, over the %d cap", len(loc), maxLocLen)
	}
}

// A segment of 1000 encoded bytes is the longest the pattern can name. One
// more must fail the pattern while the address is still under maxLocLen, so
// the two caps stay independent.
func TestJobPathSegmentBoundIs1000(t *testing.T) {
	host := "jobs.citi.com"
	at := func(n int) string {
		return "https://" + host + "/job/" + strings.Repeat("a", n) + "/role/287/1"
	}
	okLoc := at(1000)
	if len(okLoc) > maxLocLen {
		t.Fatalf("a 1000-byte segment is %d bytes, over maxLocLen", len(okLoc))
	}
	r, company, isJob, err := jobRow(okLoc, host)
	if err != nil || !isJob || company != "287" || r.city != strings.Repeat("a", 1000) || r.slug != "role" {
		t.Fatalf("row %+v company %s job %v err %v", r, company, isJob, err)
	}
	over := at(1001)
	if len(over) > maxLocLen {
		t.Fatalf("a 1001-byte segment is %d bytes; this case must fail the pattern, not the length cap", len(over))
	}
	if _, _, _, err := jobRow(over, host); !errors.Is(err, ErrShape) {
		t.Fatalf("err %v, want ErrShape", err)
	}
}

func TestJobLocsThisSourceCannotReadFailTheBoard(t *testing.T) {
	good := jobAt("jobs.intuit.com", "27595", 1)
	for name, bad := range map[string]string{
		"http scheme":     "http://jobs.intuit.com/job/mountain-view/role/27595/102000000009",
		"userinfo":        "https://evil.test@jobs.intuit.com/job/mountain-view/role/27595/102000000009",
		"port":            "https://jobs.intuit.com:8443/job/mountain-view/role/27595/102000000009",
		"query":           "https://jobs.intuit.com/job/mountain-view/role/27595/102000000009?x=1",
		"empty query":     "https://jobs.intuit.com/job/mountain-view/role/27595/102000000009?",
		"fragment":        "https://jobs.intuit.com/job/mountain-view/role/27595/102000000009#x",
		"trailing slash":  "https://jobs.intuit.com/job/mountain-view/role/27595/102000000009/",
		"non-numeric id":  "https://jobs.intuit.com/job/mountain-view/role/27595/abc",
		"missing company": "https://jobs.intuit.com/job/mountain-view/role/102000000009",
		"extra segment":   "https://jobs.intuit.com/job/mountain-view/x/role/27595/102000000009",
		"dot dot":         "https://jobs.intuit.com/job/../role/27595/102000000009",
		"dot title":       "https://jobs.intuit.com/job/mountain-view/./27595/102000000009",
		"too long":        "https://jobs.intuit.com/job/" + strings.Repeat("a", 1800) + "/" + strings.Repeat("b", 300) + "/27595/102000000009",
		"id too long":     "https://jobs.intuit.com/job/mountain-view/role/27595/" + strings.Repeat("9", 21),
	} {
		t.Run(name, func(t *testing.T) {
			d := newDoer(map[string]srctest.Reply{sitemapURL: ok(sitemapOf(good, bad))})
			got, err := newSource(d).List(context.Background(), board)
			if err == nil || got != nil || !errors.Is(err, ErrShape) {
				t.Fatalf("got %d postings, err %v; want ErrShape, never a list that leaves a job out", len(got), err)
			}
			if len(d.URLs()) != upfront {
				t.Errorf("requests %v", d.URLs())
			}
		})
	}
}

func TestSitemapEntriesThatAreNotThisBoardsJobsAreSkipped(t *testing.T) {
	locs := []string{
		jobAt("jobs.intuit.com", "27595", 1),
		"https://evil.test/job/mountain-view/role/27595/102000000008", // another host
		"https://jobs.intuit.com.evil.test/job/mountain-view/role/27595/102000000007",
		"https://other.intuit.com/job/mountain-view/role/27595/102000000006",
	}
	d := siteDoer(locs[:1], nil)
	d.Handler = wrapHandler(d.Handler, sitemapOf(locs...))
	got, err := newSource(d).List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d postings, %v", len(got), err)
	}
	for _, u := range d.URLs() {
		if !strings.HasPrefix(u, "https://jobs.intuit.com/") {
			t.Errorf("followed a loc off the board's host: %s", u)
		}
	}
	// A case-different host is the same host.
	up := "https://JOBS.Intuit.com/job/mountain-view/role-1/27595/102000000001"
	d2 := siteDoer(locs[:1], nil)
	d2.Handler = wrapHandler(d2.Handler, sitemapOf(up))
	got, err = newSource(d2).List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].URL != locs[0] {
		t.Fatalf("upper-case host: %d postings, %v, %+v", len(got), err, got)
	}
}

// wrapHandler answers the sitemap with site instead and everything else as h.
func wrapHandler(h func(srctest.Request) (srctest.Reply, bool), site string) func(srctest.Request) (srctest.Reply, bool) {
	return func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == sitemapURL {
			return ok(site), true
		}
		return h(r)
	}
}

func TestDuplicateAndMixedTenantRows(t *testing.T) {
	a := jobAt("jobs.intuit.com", "27595", 1)
	t.Run("the same address twice is one job", func(t *testing.T) {
		d := siteDoer([]string{a}, nil)
		d.Handler = wrapHandler(d.Handler, sitemapOf(a, a))
		got, err := newSource(d).List(context.Background(), board)
		if err != nil || len(got) != 1 || len(d.URLs()) != upfront+1 {
			t.Fatalf("%d postings, %v, requests %v", len(got), err, d.URLs())
		}
	})
	t.Run("one id at two addresses fails", func(t *testing.T) {
		other := "https://jobs.intuit.com/job/oakland/another-slug/27595/" + idOf(1)
		d := newDoer(map[string]srctest.Reply{sitemapURL: ok(sitemapOf(a, other))})
		if got, err := newSource(d).List(context.Background(), board); err == nil || got != nil || !strings.Contains(err.Error(), "two addresses") {
			t.Fatalf("%d postings, %v", len(got), err)
		}
	})
	t.Run("two company ids fail", func(t *testing.T) {
		other := "https://jobs.intuit.com/job/oakland/role/27596/102000000002"
		d := newDoer(map[string]srctest.Reply{sitemapURL: ok(sitemapOf(a, other))})
		if got, err := newSource(d).List(context.Background(), board); err == nil || got != nil || !strings.Contains(err.Error(), "company ids") {
			t.Fatalf("%d postings, %v", len(got), err)
		}
	})
}

func TestBoardOverTheListingLimitFailsBeforeAnyDetail(t *testing.T) {
	d := newDoer(map[string]srctest.Reply{sitemapURL: ok(sitemapOf(locsN(srcutil.MaxPostings + 1)...))})
	got, err := newSource(d).List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != upfront {
		t.Fatalf("%d postings, %v, requests %d", len(got), err, len(d.URLs()))
	}
}

// ---- the board ----

func TestHostileBoardsAreRefusedBeforeAnyRequest(t *testing.T) {
	cases := map[string]sources.Board{
		"wrong ats":        {ATS: posting.Lever, Token: "intuit", Host: "jobs.intuit.com"},
		"empty token":      {ATS: posting.TalentBrew, Token: "", Host: "jobs.intuit.com"},
		"dot dot":          {ATS: posting.TalentBrew, Token: "..", Host: "jobs.intuit.com"},
		"slash":            {ATS: posting.TalentBrew, Token: "in/tuit", Host: "jobs.intuit.com"},
		"colon":            {ATS: posting.TalentBrew, Token: "in:tuit", Host: "jobs.intuit.com"},
		"space":            {ATS: posting.TalentBrew, Token: "in tuit", Host: "jobs.intuit.com"},
		"upper case token": {ATS: posting.TalentBrew, Token: "Intuit", Host: "jobs.intuit.com"},
		"no host":          {ATS: posting.TalentBrew, Token: "intuit"},
		"path in host":     {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com/x"},
		"port in host":     {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com:8443"},
		"userinfo in host": {ATS: posting.TalentBrew, Token: "intuit", Host: "evil.test@jobs.intuit.com"},
		"ip host":          {ATS: posting.TalentBrew, Token: "intuit", Host: "10.0.0.1"},
		"one label host":   {ATS: posting.TalentBrew, Token: "intuit", Host: "localhost"},
		"scheme in host":   {ATS: posting.TalentBrew, Token: "intuit", Host: "https://jobs.intuit.com"},
		"search":           {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com", Search: "engineer"},
		"filters":          {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com", Filters: map[string][]string{"country": {"us"}}},
		"region":           {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com", Region: "eu"},
		"max postings":     {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com", MaxPostings: 5},
		"url":              {ATS: posting.TalentBrew, Token: "intuit", Host: "jobs.intuit.com", URL: "https://jobs.intuit.com/search-jobs"},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			d := srctest.New(nil)
			got, err := newSource(d).List(context.Background(), b)
			if err == nil || got != nil || len(d.URLs()) != 0 {
				t.Fatalf("got %d postings, %v, requests %v", len(got), err, d.URLs())
			}
		})
	}
	// The host is read as config.BoardHost reads it: lower-cased and trimmed.
	d := siteDoer(locsN(1), nil)
	b := board
	b.Host = "  JOBS.Intuit.COM "
	if got, err := newSource(d).List(context.Background(), b); err != nil || len(got) != 1 {
		t.Fatalf("mixed-case host: %d postings, %v", len(got), err)
	}
}

func TestATSAndConstructors(t *testing.T) {
	if New(srctest.New(nil)).ATS() != posting.TalentBrew {
		t.Fatal("ATS")
	}
	if _, err := NewWith(srctest.New(nil), Options{}); err == nil {
		t.Fatal("NewWith without Stored must be refused")
	}
	if _, err := NewWith(srctest.New(nil), Options{Stored: func(sources.Board) []posting.Posting { return nil }}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := srctest.New(nil)
	if got, err := newSource(d).List(ctx, board); !errors.Is(err, context.Canceled) || got != nil || len(d.URLs()) != 0 {
		t.Fatalf("%d, %v, %v", len(got), err, d.URLs())
	}
}
