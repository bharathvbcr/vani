package jobvite

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

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Fixtures, from jobs.jobvite.com/nutanix on 2026-11-17:
//   - list.html is the first three rows of the list page's table, as served,
//     in a minimal page.
//   - job.html is job ohorAfwq's header, meta line and description block, as
//     served, in a minimal page.

var (
	fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	board    = sources.Board{ATS: posting.Jobvite, Token: "nutanix", Company: "Nutanix"}
)

const (
	robotsURL = "https://jobs.jobvite.com/robots.txt"
	listURL   = "https://jobs.jobvite.com/nutanix/jobs"
	// upfront is robots.txt twice (list path, job path) and the list.
	upfront = 3
)

var fixtureIDs = []string{"oUQKAfwO", "olJUAfwi", "ogcSAfwE"}

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

// listOf is a list page of n synthetic rows in the live layout.
func listOf(ids ...string) string {
	var b strings.Builder
	b.WriteString(`<html><body><table class="jv-job-list"><tbody>`)
	for _, id := range ids {
		fmt.Fprintf(&b, "<tr>\n<td class=\"jv-job-list-name\">\n<a href=\"/nutanix/job/%s\">Role %s</a>\n</td>\n<td class=\"jv-job-list-location\">\n San Jose,\n California\n</td>\n</tr>\n", id, id)
	}
	b.WriteString("</tbody></table></body></html>")
	return b.String()
}

// jobPage is the fixture job page retitled for id.
func jobPage(t *testing.T, id string) string {
	return strings.Replace(fixture(t, "job.html"), "Technical Support Engineer (SRE)", "Role "+id, 1)
}

func idsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("j%07d", i)
	}
	return out
}

// site answers robots.txt (404, as live), the list, and each job page, except
// the replies in bad (by id).
func site(t *testing.T, list string, ids []string, bad map[string]srctest.Reply) *srctest.Doer {
	t.Helper()
	pages := map[string]string{}
	for _, id := range ids {
		pages["https://jobs.jobvite.com/nutanix/job/"+id] = id
	}
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case robotsURL:
			return srctest.Reply{Status: 404, Body: []byte("not found")}, true
		case listURL:
			return ok(list), true
		}
		id, hit := pages[r.URL]
		if !hit {
			return srctest.Reply{}, false
		}
		if rp, isBad := bad[id]; isBad {
			return rp, true
		}
		return ok(jobPage(t, id)), true
	})
}

func newBounded(t *testing.T, d egress.Doer, stored []posting.Posting, drop func(posting.Posting) bool) (*source, *[]Stats) {
	t.Helper()
	var reps []Stats
	s, err := NewWith(d, Options{
		Stored:     func(sources.Board) []posting.Posting { return stored },
		DropListed: drop,
		Report:     func(_ sources.Board, st Stats) { reps = append(reps, st) },
		Wait:       noWait,
	})
	if err != nil {
		t.Fatal(err)
	}
	src := s.(*source)
	src.now = func() time.Time { return fixedNow }
	return src, &reps
}

func TestParseListReadsTheLiveRows(t *testing.T) {
	rows, err := parseList(fixture(t, "list.html"), "nutanix")
	if err != nil || len(rows) != 3 {
		t.Fatalf("%+v %v", rows, err)
	}
	want := []row{
		{"oUQKAfwO", "Finance Manager", "San Jose, California"},
		{"olJUAfwi", "Financial Analyst", "Bangalore, India"},
		{"ogcSAfwE", "IT Audit Project Manager", "San Jose, California"},
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestListReadsTheListAndAJobPage(t *testing.T) {
	d := site(t, fixture(t, "list.html"), fixtureIDs, map[string]srctest.Reply{fixtureIDs[0]: ok(fixture(t, "job.html"))})
	s, reps := newBounded(t, d, nil, nil)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 3 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	p := got[0]
	if p.Key != "jv:nutanix:o_u_q_k_afw_o" || p.ATS != posting.Jobvite || p.Company != "Nutanix" ||
		p.Title != "Technical Support Engineer (SRE)" || p.Location != "San Jose, California" ||
		p.URL != "https://jobs.jobvite.com/nutanix/job/oUQKAfwO" || !p.SeenAt.Equal(fixedNow) || !p.PostedAt.IsZero() {
		t.Fatalf("posting %+v", p)
	}
	// "Who are our employees?" is inside the page's <!-- removed ===== -->
	// comment, so it is not text.
	if !strings.HasPrefix(p.Description, "Team: Product Support\n") || !strings.Contains(p.Description, "The Opportunity") ||
		strings.Contains(p.Description, "Who are our employees?") ||
		strings.Contains(p.Description, "<p>") || strings.Contains(p.Description, "Apply") {
		t.Fatalf("description:\n%s", p.Description)
	}
	urls := d.URLs()
	if len(urls) != upfront+3 || urls[0] != robotsURL || urls[1] != robotsURL || urls[2] != listURL {
		t.Fatalf("requests %v", urls)
	}
	if (*reps)[0] != (Stats{Listed: 3, Fetched: 3}) {
		t.Fatalf("%+v", *reps)
	}
}

func TestDropListedSkipsAJobBeforeItsPage(t *testing.T) {
	d := site(t, fixture(t, "list.html"), fixtureIDs, nil)
	drop := func(p posting.Posting) bool {
		return strings.Contains(p.Title, "Audit") && p.Location == "San Jose, California"
	}
	s, reps := newBounded(t, d, nil, drop)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 2 || (*reps)[0].Prefiltered != 1 {
		t.Fatalf("%d, %v, %+v", len(got), err, *reps)
	}
	for _, u := range d.URLs() {
		if strings.HasSuffix(u, "/ogcSAfwE") {
			t.Fatal("the dropped job's page was asked for")
		}
	}
}

// A closed job answers 303 to /careers/{token}/jobs?error=404 (or a 404):
// gone, within srcutil.MaxGone.
func TestGoneJobsAreBoundedByMaxGone(t *testing.T) {
	for _, k := range []int{1, 2, 3, 20} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			ids := idsN(20)
			to := map[string]string{}
			bad := map[string]srctest.Reply{}
			for i := 0; i < k; i++ {
				if i%2 == 0 {
					to["https://jobs.jobvite.com/nutanix/job/"+ids[i]] = "https://jobs.jobvite.com/careers/nutanix/jobs?error=404"
					bad[ids[i]] = ok(listOf())
				} else {
					bad[ids[i]] = srctest.Reply{Status: 404, Body: []byte("x")}
				}
			}
			s, reps := newBounded(t, followed{site(t, listOf(ids...), ids, bad), to}, nil, nil)
			got, err := s.List(context.Background(), board)
			if k <= 2 {
				if err != nil || len(got) != 20-k || (*reps)[0].Gone != k {
					t.Fatalf("%d, %v, %+v", len(got), err, *reps)
				}
				return
			}
			if err == nil || got != nil || !strings.Contains(err.Error(), "are gone") {
				t.Fatalf("%d, %v", len(got), err)
			}
		})
	}
}

// followed answers like d, except that a request for a URL in to is answered
// as the egress client answers a redirect it followed.
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

func TestJobPagesThatAreNotGoneFailTheBoard(t *testing.T) {
	page := fixture(t, "job.html")
	for name, rp := range map[string]srctest.Reply{
		"no description":    ok(strings.Replace(page, `class="jv-job-detail-description"`, `class="x"`, 1)),
		"description cut":   ok(strings.Replace(page, `<div class="jv-job-detail-bottom-actions">`, ``, 1)),
		"empty description": ok(`<h2 class="jv-header">X</h2><div class="jv-job-detail-description"> <!-- x --> </div><div class="jv-job-detail-bottom-actions">`),
		"500":               {Status: 500, Body: []byte("x")},
		"oversized":         {Status: 200, Body: []byte(strings.Repeat("x", maxDetailBody+1))},
	} {
		t.Run(name, func(t *testing.T) {
			ids := idsN(20)
			s, reps := newBounded(t, site(t, listOf(ids...), ids, map[string]srctest.Reply{ids[5]: rp}), nil, nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil || strings.Contains(err.Error(), "are gone") || (*reps)[0].Gone != 0 {
				t.Fatalf("%d, %v, %+v", len(got), err, *reps)
			}
		})
	}
}

func TestAJobPageFromAnotherAddressFailsTheBoard(t *testing.T) {
	for name, dest := range map[string]string{
		"another job":           "https://jobs.jobvite.com/nutanix/job/zzzz",
		"another company":       "https://jobs.jobvite.com/other/job/j0000005",
		"list without an error": "https://jobs.jobvite.com/careers/nutanix/jobs",
		"another host":          "https://search.jobvite.com/",
	} {
		t.Run(name, func(t *testing.T) {
			ids := idsN(20)
			fd := followed{site(t, listOf(ids...), ids, nil), map[string]string{"https://jobs.jobvite.com/nutanix/job/" + ids[5]: dest}}
			s, reps := newBounded(t, fd, nil, nil)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil || !strings.Contains(err.Error(), "not its own address") || (*reps)[0].Gone != 0 {
				t.Fatalf("%d, %v, %+v", len(got), err, *reps)
			}
		})
	}
}

// A company Jobvite does not serve is redirected to search.jobvite.com.
func TestUnknownCompanyIsBoardNotFound(t *testing.T) {
	for name, do := range map[string]egress.Doer{
		"followed": followed{site(t, "<html>search</html>", nil, nil), map[string]string{listURL: "https://search.jobvite.com/?invalid=1"}},
		"refused hop": srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
			if r.URL == robotsURL {
				return srctest.Reply{Status: 404}, true
			}
			return srctest.Reply{Err: &url.Error{Op: "Get", URL: r.URL, Err: fmt.Errorf("%w: %q", egress.ErrNotAllowed, "search.jobvite.com")}}, true
		}),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newBounded(t, do, nil, nil)
			got, err := s.List(context.Background(), board)
			if !errors.Is(err, srcutil.ErrBoardNotFound) || got != nil {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}

func TestListsThisSourceCannotAccountFor(t *testing.T) {
	good := listOf("a1", "b2")
	for name, list := range map[string]string{
		"pagination":      strings.Replace(good, "</table>", `</table><div class="jv-pagination"><a href="?p=1">2</a></div>`, 1),
		"unreadable row":  strings.Replace(good, `<a href="/nutanix/job/b2">`, `<a class="x" title="y" data-z href="/nutanix/job/b2">`, 1) + `<td class="jv-job-list-name"></td>`,
		"another company": strings.Replace(good, "/nutanix/job/b2", "/other/job/b2", 1),
		"empty title":     strings.Replace(good, "Role b2", " ", 1),
		"no jobs":         listOf(),
	} {
		t.Run(name, func(t *testing.T) {
			if rows, err := parseList(list, "nutanix"); err == nil {
				t.Fatalf("%+v", rows)
			}
		})
	}
	rows, err := parseList(listOf("a1", "b2", "a1"), "nutanix")
	if err != nil || len(rows) != 2 {
		t.Fatalf("duplicate row: %+v %v", rows, err)
	}
}

func TestRobotsDisallowingOurPathsFailsBeforeTheList(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{robotsURL: ok("User-agent: *\nDisallow: /nutanix/\n")})
	s, _ := newBounded(t, d, nil, nil)
	if got, err := s.List(context.Background(), board); err == nil || got != nil || !strings.Contains(err.Error(), "disallows") {
		t.Fatalf("%v %v", got, err)
	}
	for _, u := range d.URLs() {
		if u != robotsURL {
			t.Fatalf("asked %s", u)
		}
	}
}

func TestFirstRunFetchesMaxDetailsAndDefersTheRest(t *testing.T) {
	ids := idsN(MaxDetails + 4)
	s, reps := newBounded(t, site(t, listOf(ids...), ids, nil), nil, nil)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != MaxDetails || (*reps)[0].Deferred != 4 {
		t.Fatalf("%d, %v, %+v", len(got), err, *reps)
	}
}

func TestStoredJobsAreReusedWithoutARequest(t *testing.T) {
	ids := idsN(3)
	var stored []posting.Posting
	for _, id := range ids[:2] {
		k, _ := posting.JobviteKey("nutanix", id)
		stored = append(stored, posting.Posting{Key: k, ATS: posting.Jobvite, Title: "Stored"})
	}
	d := site(t, listOf(ids...), ids, nil)
	s, reps := newBounded(t, d, stored, nil)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 3 || (*reps)[0].Fetched != 1 || (*reps)[0].Reused+(*reps)[0].Refreshed != 2 {
		t.Fatalf("%d, %v, %+v", len(got), err, *reps)
	}
}

func TestBoardsThisSourceRefusesBeforeAnyRequest(t *testing.T) {
	for name, b := range map[string]sources.Board{
		"wrong ats":   {ATS: posting.Meta, Token: "nutanix"},
		"empty token": {ATS: posting.Jobvite},
		"upper token": {ATS: posting.Jobvite, Token: "Nutanix"},
		"slash token": {ATS: posting.Jobvite, Token: "nu/tanix"},
		"dots":        {ATS: posting.Jobvite, Token: ".."},
		"host":        {ATS: posting.Jobvite, Token: "nutanix", Host: "evil.test"},
		"search":      {ATS: posting.Jobvite, Token: "nutanix", Search: "engineer"},
		"url":         {ATS: posting.Jobvite, Token: "nutanix", URL: "https://evil.test/"},
	} {
		t.Run(name, func(t *testing.T) {
			d := srctest.New(map[string]srctest.Reply{})
			s, _ := newBounded(t, d, nil, nil)
			if got, err := s.List(context.Background(), b); err == nil || got != nil || len(d.URLs()) != 0 {
				t.Fatalf("%v %v %v", got, err, d.URLs())
			}
		})
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := site(t, listOf("a1"), []string{"a1"}, nil)
	s, _ := newBounded(t, d, nil, nil)
	if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) || len(d.URLs()) != 0 {
		t.Fatalf("%v %v", err, d.URLs())
	}
}

func TestATSAndConstructor(t *testing.T) {
	if New(nil).ATS() != posting.Jobvite {
		t.Fatal("ATS")
	}
	if _, err := NewWith(nil, Options{}); err == nil {
		t.Fatal("NewWith without Stored")
	}
}
