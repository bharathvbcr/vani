package talentbrew

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// homePage is the body a followed redirect lands on: a site's home page,
// carrying no JobPosting.
const homePage = `<!DOCTYPE html><html><head><title>Careers</title></head><body>Search jobs</body></html>`

// followed answers like the srctest.Doer it wraps, except that a request for a
// URL in to is answered as the egress client answers a redirect it followed:
// the 200 body of the page it landed on, with the response's Request at that
// page's address.
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

// sentBoard is a 20-job Intuit board whose jobs at the given indexes are
// redirected to dest, landing on body.
func sentBoard(t *testing.T, idx []int, dest, body string) (sources.Source, *[]Stats) {
	t.Helper()
	locs := locsN(20)
	bad := map[string]srctest.Reply{}
	to := map[string]string{}
	for _, i := range idx {
		bad[locs[i]] = ok(body)
		to[locs[i]] = dest
	}
	var reps []Stats
	s, err := NewWith(followed{siteDoer(locs, bad), to}, Options{
		Stored: storedNone,
		Report: func(_ sources.Board, st Stats) { reps = append(reps, st) },
		Wait:   noWait,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, &reps
}

// Synopsys answers a closed job's page 302 "Location: /" (2026-11-17). The
// client follows it to the home page: that job is gone, not a board failure.
func TestJobSentToItsSitesOwnPageIsGone(t *testing.T) {
	for name, dest := range map[string]string{
		"home":            "https://jobs.intuit.com/",
		"home, no slash":  "https://jobs.intuit.com",
		"upper-case host": "https://JOBS.intuit.com/",
		"home with query": "https://jobs.intuit.com/?closed=1",
	} {
		t.Run(name, func(t *testing.T) {
			s, reps := sentBoard(t, []int{5}, dest, homePage)
			got, err := s.List(context.Background(), board)
			if err != nil || len(got) != 19 {
				t.Fatalf("got %d postings, err %v; want 19, nil", len(got), err)
			}
			for _, p := range got {
				if strings.HasSuffix(p.URL, "/"+idOf(5)) {
					t.Fatalf("the closed job was returned: %s", p.URL)
				}
			}
			if len(*reps) != 1 || (*reps)[0].Gone != 1 {
				t.Fatalf("reports %+v, want gone 1", *reps)
			}
		})
	}
}

// Jobs sent home count toward srcutil.MaxGone with the 404s: a site that sends
// every job page home is not a site whose jobs all closed.
func TestJobsSentHomeAreBoundedByMaxGone(t *testing.T) {
	bound := max(1, 20/10)
	for _, k := range []int{bound, bound + 1, 20} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			idx := make([]int, k)
			for i := range idx {
				idx[i] = i
			}
			s, reps := sentBoard(t, idx, "https://jobs.intuit.com/", homePage)
			got, err := s.List(context.Background(), board)
			if k <= bound {
				if err != nil || len(got) != 20-k || (*reps)[0].Gone != k {
					t.Fatalf("got %d postings, err %v, reports %+v; want %d, nil, gone %d", len(got), err, *reps, 20-k, k)
				}
				return
			}
			if err == nil || got != nil || !strings.Contains(err.Error(), "are gone") {
				t.Fatalf("got %d postings, err %v; want the board to fail as gone past the bound", len(got), err)
			}
			if errors.Is(err, srcutil.ErrBoardNotFound) {
				t.Fatalf("jobs sent home read as board-not-found: %v", err)
			}
		})
	}
}

// Only the site's own home page means closed. Any other page it lands on
// (search, category, error or maintenance pages, another job, another host)
// fails the board as before.
func TestRedirectsThatAreNotHomeAreNotGone(t *testing.T) {
	for name, tc := range map[string]struct{ dest, body string }{
		"another host":              {"https://evil.test/", homePage},
		"a look-alike host":         {"https://jobs.intuit.com.evil.test/", homePage},
		"search page":               {"https://jobs.intuit.com/search-jobs/", homePage},
		"jobs, not /job/":           {"https://jobs.intuit.com/jobs/12", homePage},
		"category listing":          {"https://jobs.intuit.com/category/data-jobs/27595/68338/1", homePage},
		"an error page":             {"https://jobs.intuit.com/error?code=500", homePage},
		"a path of only slashes":    {"https://jobs.intuit.com//", homePage},
		"a rewritten job address":   {"https://jobs.intuit.com/job/mountain-view/renamed/27595/" + idOf(5), homePage},
		"another job's page":        {jobAt(board.Host, "27595", 99), ldPage(jobAt(board.Host, "27595", 99), "Other")},
		"the job index under /job/": {"https://jobs.intuit.com/job/", homePage},
	} {
		t.Run(name, func(t *testing.T) {
			s, reps := sentBoard(t, []int{5}, tc.dest, tc.body)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %d postings, err %v; want the board to fail", len(got), err)
			}
			if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") {
				t.Errorf("read as gone: %v", err)
			}
			if len(*reps) != 1 || (*reps)[0].Gone != 0 {
				t.Errorf("reports %+v: counted gone, or did not report the failed board", *reps)
			}
		})
	}
}

// A redirect that lands back on the job's own page reads it as usual.
func TestRedirectBackToTheJobPageReadsIt(t *testing.T) {
	locs := locsN(20)
	s, reps := sentBoard(t, []int{5}, locs[5], ldPage(locs[5], "Role"))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 20 || (*reps)[0].Gone != 0 {
		t.Fatalf("got %d postings, err %v, reports %+v; want 20, nil, gone 0", len(got), err, *reps)
	}
}
