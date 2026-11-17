package meta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// Fixtures, from www.metacareers.com on 2026-11-17:
//   - robots.txt is the live file, byte for byte.
//   - job.html is job 1826062781882324's JSON-LD block, as served, inside the
//     page's head with its og:title; the rest of the 560 KB page (scripts,
//     styles) is left out.
//   - shell.html is the shape a made-up id answered with (200, 555 KB): the
//     same head with no og:title and no JSON-LD.
//   - ogonly is the shape sitemap job 1426965155339653 answered with: og:title
//     and og:description of the job family, no JSON-LD and no job text.
const ogonly = `<!DOCTYPE html><html lang="en" id="meta-careers"><head><meta charset="utf-8" /><title>Meta Careers</title>` +
	`<meta property="og:title" content="Product Manager" /><meta property="og:description" content="Meta Product Managers work with cross-functional teams" />` +
	`<script>requireLazy(["TimeSliceImpl"],function(){});</script></head><body><div id="root"></div></body></html>`

var (
	fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	board    = sources.Board{ATS: posting.Meta, Token: "meta", Company: "Meta"}
)

const (
	robotsURL  = "https://www.metacareers.com/robots.txt"
	sitemapURL = "https://www.metacareers.com/jobsearch/sitemap.xml"
	fixtureID  = "1826062781882324"
	// upfront is the requests before the first job page: robots.txt for the
	// sitemap, robots.txt for the job pages, and the sitemap.
	upfront = 3
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func jobURL(id string) string { return "https://www.metacareers.com/profile/job_details/" + id + "/" }

func idOf(i int) string { return fmt.Sprint(1000000000000000 + i) }

func idsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = idOf(i)
	}
	return out
}

// sitemapOf is a sitemap of the given job ids, laid out as the live one is
// (every lastmod the generation time).
func sitemapOf(ids ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns:xhtml="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, id := range ids {
		fmt.Fprintf(&b, "<url><loc>%s</loc><lastmod>2026-11-17T11:24:36-08:00</lastmod></url>", jobURL(id))
	}
	b.WriteString("</urlset>")
	return b.String()
}

// pageFor is the fixture job page retitled for job id.
func pageFor(t *testing.T, id string) string {
	return strings.ReplaceAll(fixture(t, "job.html"), "Hardware Engineer, PCB Layout", "Role "+id)
}

// site answers robots.txt, a sitemap of ids and each id's page, except the
// replies in bad (by id).
func site(t *testing.T, ids []string, bad map[string]srctest.Reply) *srctest.Doer {
	t.Helper()
	robots := fixture(t, "robots.txt")
	sm := sitemapOf(ids...)
	pages := map[string]string{}
	for _, id := range ids {
		pages[jobURL(id)] = id
	}
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case robotsURL:
			return ok(robots), true
		case sitemapURL:
			return ok(sm), true
		}
		id, hit := pages[r.URL]
		if !hit {
			return srctest.Reply{}, false
		}
		if rp, isBad := bad[id]; isBad {
			return rp, true
		}
		return ok(pageFor(t, id)), true
	})
}

func newBounded(t *testing.T, d interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}, stored []posting.Posting) (*source, *[]Stats) {
	t.Helper()
	var reps []Stats
	s, err := NewWith(d, Options{
		Stored: func(sources.Board) []posting.Posting { return stored },
		Report: func(_ sources.Board, st Stats) { reps = append(reps, st) },
		Wait:   noWait,
	})
	if err != nil {
		t.Fatal(err)
	}
	src := s.(*source)
	src.now = func() time.Time { return fixedNow }
	return src, &reps
}

func TestListReadsTheSitemapAndAJobPage(t *testing.T) {
	d := site(t, []string{fixtureID}, map[string]srctest.Reply{fixtureID: ok(fixture(t, "job.html"))})
	s, reps := newBounded(t, d, nil)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	p := got[0]
	if p.Key != "mt:meta:"+fixtureID || p.ATS != posting.Meta || p.Company != "Meta" ||
		p.Title != "Hardware Engineer, PCB Layout" || p.URL != jobURL(fixtureID) {
		t.Fatalf("posting %+v", p)
	}
	if p.Location != "Austin, Texas, US; Menlo Park, California, US; Remote, US" || !p.Remote {
		t.Fatalf("location %q remote %v", p.Location, p.Remote)
	}
	if !p.PostedAt.Equal(time.Date(2026, 10, 2, 0, 10, 9, 0, time.UTC)) || !p.SeenAt.Equal(fixedNow) {
		t.Fatalf("posted %v seen %v", p.PostedAt, p.SeenAt)
	}
	for _, want := range []string{"Role: full-time, remote", "Responsibilities", "Qualifications",
		"Define PCB layout architecture and floorplanning strategies"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, p.Description)
		}
	}
	if strings.Contains(p.Description, "&nbsp;") || strings.Contains(p.Description, "<li>") {
		t.Errorf("description keeps markup: %s", p.Description)
	}
	// Each responsibility is its own line.
	if !strings.Contains(p.Description, "\nDefine PCB layout architecture") {
		t.Errorf("responsibilities not split into lines:\n%s", p.Description)
	}
	urls := d.URLs()
	if len(urls) != upfront+1 || urls[0] != robotsURL || urls[1] != robotsURL || urls[2] != sitemapURL || urls[3] != jobURL(fixtureID) {
		t.Fatalf("requests %v", urls)
	}
	if len(*reps) != 1 || (*reps)[0] != (Stats{Listed: 1, Fetched: 1}) {
		t.Fatalf("reports %+v", *reps)
	}
}

func TestOnlyTelecommuteIsRemote(t *testing.T) {
	page := strings.Replace(fixture(t, "job.html"), `"jobLocationType":"TELECOMMUTE",`, "", 1)
	if page == fixture(t, "job.html") {
		t.Fatal("fixture has no jobLocationType to remove")
	}
	d := site(t, []string{fixtureID}, map[string]srctest.Reply{fixtureID: ok(page)})
	s, _ := newBounded(t, d, nil)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Remote || strings.Contains(got[0].Description, "remote\n") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRobotsDisallowingOurPathsFailsBeforeTheSitemap(t *testing.T) {
	for name, robots := range map[string]string{
		"all":      "User-agent: *\nDisallow: /\n",
		"sitemap":  "User-agent: *\nDisallow: /jobsearch/\n",
		"job page": "User-agent: *\nDisallow: /profile/\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.New(map[string]srctest.Reply{robotsURL: ok(robots)})
			s, _ := newBounded(t, d, nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil || !strings.Contains(err.Error(), "disallows") {
				t.Fatalf("%v %v", got, err)
			}
			for _, u := range d.URLs() {
				if u != robotsURL {
					t.Fatalf("asked %s after robots disallowed it", u)
				}
			}
		})
	}
}

func TestRobotsThatCannotBeReadFailsTheBoard(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{robotsURL: {Status: 500, Body: []byte("x")}})
	s, _ := newBounded(t, d, nil)
	if got, err := s.List(context.Background(), board); err == nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
}

// A closed job answers 404, or a page with no JSON-LD block (the bare shell,
// or a page that kept only its og: tags): gone, within srcutil.MaxGone.
func TestGoneJobsAreBoundedByMaxGone(t *testing.T) {
	for name, gone := range map[string]func(t *testing.T) srctest.Reply{
		"404":    func(*testing.T) srctest.Reply { return srctest.Reply{Status: 404, Body: []byte("x")} },
		"shell":  func(t *testing.T) srctest.Reply { return ok(fixture(t, "shell.html")) },
		"ogonly": func(*testing.T) srctest.Reply { return ok(ogonly) },
	} {
		for _, k := range []int{1, 2, 3, 20} {
			t.Run(fmt.Sprintf("%s/%d", name, k), func(t *testing.T) {
				ids := idsN(20)
				bad := map[string]srctest.Reply{}
				for i := 0; i < k; i++ {
					bad[ids[i]] = gone(t)
				}
				s, reps := newBounded(t, site(t, ids, bad), nil)
				got, err := s.List(context.Background(), board)
				bound := max(1, 20/10)
				if k <= bound {
					if err != nil || len(got) != 20-k || (*reps)[0].Gone != k {
						t.Fatalf("%d postings, %v, %+v", len(got), err, *reps)
					}
					return
				}
				if err == nil || got != nil || !strings.Contains(err.Error(), "are gone") || errors.Is(err, srcutil.ErrBoardNotFound) {
					t.Fatalf("%d postings, %v", len(got), err)
				}
			})
		}
	}
}

// A page with JSON-LD but no JobPosting this source can read is a layout it
// does not know: the board fails, nothing is counted gone.
func TestPagesThatAreNotGoneFailTheBoard(t *testing.T) {
	head := `<html><head><meta property="og:title" content="Role" />`
	cases := map[string]srctest.Reply{
		"broken JSON-LD":   ok(head + `<script type="application/ld+json">{"@type":"JobPosting",</script></head></html>`),
		"not a JobPosting": ok(head + `<script type="application/ld+json">{"@type":"Organization","name":"Meta"}</script></head></html>`),
		"empty title":      ok(strings.Replace(fixture(t, "job.html"), `"title":"Hardware Engineer, PCB Layout"`, `"title":"  "`, 1)),
		"bad date":         ok(strings.Replace(fixture(t, "job.html"), `"datePosted":"2026-10-01T17:10:09-07:00"`, `"datePosted":"last week"`, 1)),
		"500":              {Status: 500, Body: []byte("x")},
		"410":              {Status: 410, Body: []byte("x")},
		"oversized":        {Status: 200, Body: []byte(strings.Repeat("x", maxDetailBody+1))},
	}
	for name, rp := range cases {
		t.Run(name, func(t *testing.T) {
			ids := idsN(20)
			s, reps := newBounded(t, site(t, ids, map[string]srctest.Reply{ids[5]: rp}), nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("%d postings, %v; want the board to fail", len(got), err)
			}
			if strings.Contains(err.Error(), "are gone") || errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Errorf("read as gone: %v", err)
			}
			if len(*reps) != 1 || (*reps)[0].Gone != 0 {
				t.Errorf("reports %+v", *reps)
			}
		})
	}
}

// followed answers like d, except that a request for a URL in to is answered
// as the egress client answers a redirect it followed: the response's Request
// is at the landing page.
type followed struct {
	d  *srctest.Doer
	to map[string]string
}

func (f followed) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := f.d.Do(ctx, req)
	if dest, hit := f.to[req.URL.String()]; hit && resp != nil {
		u, perr := url.Parse(dest)
		if perr != nil {
			return nil, perr
		}
		resp.Request = &http.Request{Method: req.Method, URL: u}
	}
	return resp, err
}

// The JSON-LD names no url, so a page is only this job's when it came from
// this job's address: a redirect anywhere else fails the board, even when the
// landing page carries a JobPosting.
func TestAPageFromAnotherAddressFailsTheBoard(t *testing.T) {
	for name, dest := range map[string]string{
		"login":           "https://www.metacareers.com/login/?next=x",
		"another job":     jobURL("999"),
		"home":            "https://www.metacareers.com/",
		"another host":    "https://www.facebook.com/careers/",
		"http":            "http://www.metacareers.com/profile/job_details/" + idOf(5) + "/",
		"look-alike host": "https://www.metacareers.com.evil.test/profile/job_details/" + idOf(5) + "/",
	} {
		t.Run(name, func(t *testing.T) {
			ids := idsN(20)
			fd := followed{site(t, ids, nil), map[string]string{jobURL(ids[5]): dest}}
			s, reps := newBounded(t, fd, nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil || !strings.Contains(err.Error(), "not its own address") {
				t.Fatalf("%d postings, %v", len(got), err)
			}
			if (*reps)[0].Gone != 0 {
				t.Errorf("counted gone: %+v", *reps)
			}
		})
	}
}

func TestARedirectBackToTheSameJobReadsIt(t *testing.T) {
	ids := idsN(3)
	fd := followed{site(t, ids, nil), map[string]string{jobURL(ids[1]): "https://WWW.metacareers.com/profile/job_details/" + ids[1]}}
	s, _ := newBounded(t, fd, nil)
	if got, err := s.List(context.Background(), board); err != nil || len(got) != 3 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
}

func TestFirstRunFetchesMaxDetailsAndDefersTheRest(t *testing.T) {
	ids := idsN(MaxDetails + 7)
	d := site(t, ids, nil)
	s, reps := newBounded(t, d, nil)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != MaxDetails {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if st := (*reps)[0]; st.Listed != MaxDetails+7 || st.Fetched != MaxDetails || st.Deferred != 7 {
		t.Fatalf("%+v", st)
	}
	if n := len(d.URLs()); n != upfront+MaxDetails {
		t.Fatalf("%d requests", n)
	}
}

func TestStoredJobsAreReusedWithoutARequest(t *testing.T) {
	ids := idsN(4)
	var stored []posting.Posting
	for _, id := range ids[:3] {
		k, _ := posting.MetaKey("meta", id)
		stored = append(stored, posting.Posting{Key: k, ATS: posting.Meta, Company: "Meta", Title: "Stored " + id, URL: jobURL(id)})
	}
	d := site(t, ids, nil)
	s, reps := newBounded(t, d, stored)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 4 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if st := (*reps)[0]; st.Reused+st.Refreshed != 3 || st.Fetched != 1 {
		t.Fatalf("%+v", st)
	}
	if st := (*reps)[0]; st.Reused == 3 && len(d.URLs()) != upfront+1 {
		t.Fatalf("requests %v", d.URLs())
	}
}

func TestNewFailsABoardOverMaxDetails(t *testing.T) {
	s := &source{d: site(t, idsN(MaxDetails+1), nil), now: func() time.Time { return fixedNow }, wait: noWait}
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestSitemapsThisSourceCannotAccountFor(t *testing.T) {
	gz := string([]byte{0x1f, 0x8b, 0x08, 0x00})
	cases := map[string]string{
		"gzip":            gz,
		"index":           `<?xml version="1.0"?><sitemapindex><sitemap><loc>https://x/</loc></sitemap></sitemapindex>`,
		"rss":             `<rss></rss>`,
		"cut":             sitemapOf(idOf(1))[:200],
		"no jobs":         sitemapOf(),
		"bad job path":    strings.Replace(sitemapOf(idOf(1)), "/job_details/"+idOf(1)+"/", "/job_details/abc/", 1),
		"query on a job":  strings.Replace(sitemapOf(idOf(1)), idOf(1)+"/<", idOf(1)+"/?x=1<", 1),
		"http job":        strings.Replace(sitemapOf(idOf(1)), "https://", "http://", 1),
		"text after root": sitemapOf(idOf(1)) + "junk",
		"two addresses": strings.Replace(sitemapOf(idOf(1), idOf(1)), "</urlset>",
			"<url><loc>https://www.metacareers.com/profile/job_details/"+idOf(1)+"</loc></url></urlset>", 1),
	}
	for name, sm := range cases {
		t.Run(name, func(t *testing.T) {
			robots := fixture(t, "robots.txt")
			d := srctest.New(map[string]srctest.Reply{robotsURL: ok(robots), sitemapURL: ok(sm)})
			s, _ := newBounded(t, d, nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}

func TestSitemapSkipsPagesThatAreNotJobsAndRepeatsOfAJob(t *testing.T) {
	sm := strings.Replace(sitemapOf(idOf(1), idOf(2), idOf(1)), "</urlset>",
		"<url><loc>https://www.metacareers.com/jobs/</loc></url><url><loc>https://www.example.com/profile/job_details/5/</loc></url></urlset>", 1)
	rows, err := parseSitemap([]byte(sm))
	if err != nil || len(rows) != 2 || rows[0].id != idOf(1) || rows[1].id != idOf(2) {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestSitemapMissingIsBoardNotFound(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{robotsURL: ok(fixture(t, "robots.txt")), sitemapURL: {Status: 404, Body: []byte("x")}})
	s, _ := newBounded(t, d, nil)
	if _, err := s.List(context.Background(), board); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestBoardsThisSourceRefusesBeforeAnyRequest(t *testing.T) {
	for name, b := range map[string]sources.Board{
		"wrong ats":   {ATS: posting.DEShaw, Token: "meta"},
		"empty token": {ATS: posting.Meta, Token: ""},
		"upper token": {ATS: posting.Meta, Token: "Meta"},
		"slash token": {ATS: posting.Meta, Token: "me/ta"},
		"host":        {ATS: posting.Meta, Token: "meta", Host: "www.metacareers.com"},
		"search":      {ATS: posting.Meta, Token: "meta", Search: "engineer"},
		"filters":     {ATS: posting.Meta, Token: "meta", Filters: map[string][]string{"location": {"x"}}},
		"url":         {ATS: posting.Meta, Token: "meta", URL: "https://www.metacareers.com/"},
		"board name":  {ATS: posting.Meta, Token: "meta", BoardName: "x"},
		"max":         {ATS: posting.Meta, Token: "meta", MaxPostings: 5},
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.New(map[string]srctest.Reply{})
			s, _ := newBounded(t, d, nil)
			if got, err := s.List(context.Background(), b); err == nil || got != nil {
				t.Fatalf("%v %v", got, err)
			}
			if n := len(d.URLs()); n != 0 {
				t.Fatalf("%d requests", n)
			}
		})
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := site(t, idsN(2), nil)
	s, _ := newBounded(t, d, nil)
	if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("%v %v", err, d.URLs())
	}
}

func TestBulletsAndPlacesAreBounded(t *testing.T) {
	if got := bullets("a&nbsp;&nbsp; b &nbsp;"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("%q", got)
	}
	if got := bullets(strings.Repeat("x&nbsp;", maxBullets+5)); len(got) != maxBullets {
		t.Fatalf("%d bullets", len(got))
	}
	if got := bullets(""); got != nil {
		t.Fatalf("%q", got)
	}
}

func TestATSAndConstructor(t *testing.T) {
	if New(nil).ATS() != posting.Meta {
		t.Fatal("ATS")
	}
	if _, err := NewWith(nil, Options{}); err == nil {
		t.Fatal("NewWith without Stored")
	}
}
