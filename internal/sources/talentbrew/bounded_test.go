package talentbrew

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func storedNone(sources.Board) []posting.Posting { return nil }

func storedPosting(t *testing.T, i int) posting.Posting {
	t.Helper()
	key, err := posting.TalentBrewKey(board.Token, idOf(i))
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: key, ATS: posting.TalentBrew, Company: "Intuit", Title: fmt.Sprintf("Stored %d", i),
		URL: jobAt(board.Host, "27595", i), Description: "stored text", SeenAt: fixedNow.Add(-24 * time.Hour)}
}

// indexes returns n job numbers whose refresh turn is (due) or is not fixedNow's day.
func indexes(t *testing.T, n int, due bool) []int {
	t.Helper()
	day := srcutil.Day(fixedNow)
	var out []int
	for i := 0; len(out) < n && i < 100000; i++ {
		if srcutil.RefreshTurn(idOf(i), day) == due {
			out = append(out, i)
		}
	}
	if len(out) < n {
		t.Fatalf("found %d of %d jobs", len(out), n)
	}
	return out
}

func locsOf(idx []int) []string {
	out := make([]string, len(idx))
	for k, i := range idx {
		out[k] = jobAt(board.Host, "27595", i)
	}
	return out
}

func pageRequests(d *srctest.Doer) int { return len(d.URLs()) - upfront }

// ---- the bound ----

// Before the bound, every listed job cost a 600 KB request every run.
func TestNewFailsABoardOverMaxDetailsBeforeAnyDetail(t *testing.T) {
	d := siteDoer(locsN(MaxDetails+1), nil)
	got, err := newSource(d).List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != upfront {
		t.Fatalf("%d postings, %v, requests %d", len(got), err, len(d.URLs()))
	}
}

func TestNewReadsABoardAtMaxDetails(t *testing.T) {
	d := siteDoer(locsN(MaxDetails), nil)
	got, err := newSource(d).List(context.Background(), board)
	if err != nil || len(got) != MaxDetails || pageRequests(d) != MaxDetails {
		t.Fatalf("%d postings, %v, page requests %d", len(got), err, pageRequests(d))
	}
}

// The first run of a big site: new rows are fetched up to MaxDetails and the
// rest wait for the next run. Intuit has 602 jobs.
func TestFirstRunFetchesMaxDetailsAndDefersTheRest(t *testing.T) {
	d := siteDoer(locsN(MaxDetails+7), nil)
	s, reps, waits := newBounded(t, d, Options{Stored: storedNone})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDetails || pageRequests(d) != MaxDetails {
		t.Fatalf("%d postings, %d page requests, want %d of each", len(got), pageRequests(d), MaxDetails)
	}
	if want := (Stats{Listed: MaxDetails + 7, Fetched: MaxDetails, Deferred: 7}); len(*reps) != 1 || (*reps)[0] != want {
		t.Fatalf("reports %+v, want %+v", *reps, want)
	}
	// In sitemap order: the first MaxDetails.
	for i, p := range got {
		if p.Key != posting.Key("tb:intuit:"+idOf(i)) {
			t.Fatalf("posting %d is %s", i, p.Key)
		}
	}
	// One wait before every request but the first: the second robots.txt read,
	// the sitemap and each detail.
	if len(*waits) != upfront-1+MaxDetails {
		t.Fatalf("%d waits for %d requests", len(*waits), upfront+MaxDetails)
	}
	for _, w := range *waits {
		if w != paceEvery || w < 2*time.Second {
			t.Fatalf("wait %v, want %v (at least two seconds a page)", w, paceEvery)
		}
	}
	if d.URLs()[0] != robotsURL || d.URLs()[upfront-1] != sitemapURL {
		t.Fatalf("requests begin %v", d.URLs()[:upfront])
	}
}

func TestPaceBudgetCoversAFullRun(t *testing.T) {
	if need := time.Duration(MaxDetails+upfront) * paceEvery; need >= paceBudget {
		t.Fatalf("%d requests at %v is %v, not under the %v pace budget", MaxDetails+upfront, paceEvery, need, paceBudget)
	}
	if paceEvery < 2*time.Second {
		t.Fatalf("paceEvery %v", paceEvery)
	}
}

func TestPaceBudgetEndsARunThatWouldWaitTooLong(t *testing.T) {
	d := siteDoer(locsN(3), nil)
	s, _, _ := newBounded(t, d, Options{Stored: storedNone})
	// A pacer with room for one wait only: the third request refuses without waiting.
	pace := &srcutil.Pacer{Every: time.Second, Budget: time.Second, Sleep: noWait}
	rows, company, err := parseSitemap([]byte(sitemapOf(locsN(3)...)), board.Host)
	if err != nil {
		t.Fatal(err)
	}
	var st Stats
	_, err = s.details(context.Background(), pace, board, board.Host, company, rows, &st)
	if !errors.Is(err, srcutil.ErrPaceBudget) {
		t.Fatalf("err %v, want ErrPaceBudget", err)
	}
}

// ---- stored postings ----

func TestStoredJobsAreReusedWithoutARequest(t *testing.T) {
	old := indexes(t, 3, false)
	fresh := 9000
	all := append(append([]int{}, old...), fresh)
	d := siteDoer(locsOf(all), nil)
	stored := []posting.Posting{storedPosting(t, old[0]), storedPosting(t, old[1]), storedPosting(t, old[2])}
	s, reps, _ := newBounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if pageRequests(d) != 1 || d.URLs()[upfront] != jobAt(board.Host, "27595", fresh) {
		t.Fatalf("want one page request, for the new job: %v", d.URLs())
	}
	if len(got) != 4 {
		t.Fatalf("%d postings", len(got))
	}
	for i, p := range got[:3] {
		if p.Title != fmt.Sprintf("Stored %d", old[i]) || p.Description != "stored text" || !p.SeenAt.Equal(fixedNow) {
			t.Fatalf("posting %d not reused as stored with SeenAt now: %+v", i, p)
		}
	}
	if got[3].Key != posting.Key("tb:intuit:"+idOf(fresh)) || got[3].Description != "Build & ship" {
		t.Fatalf("new posting %+v", got[3])
	}
	if want := (Stats{Listed: 4, Reused: 3, Fetched: 1}); (*reps)[0] != want {
		t.Fatalf("report %+v, want %+v", (*reps)[0], want)
	}
}

func TestStoredJobOnItsTurnIsReread(t *testing.T) {
	due := indexes(t, 1, true)[0]
	d := siteDoer(locsOf([]int{due}), nil)
	s, reps, _ := newBounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return []posting.Posting{storedPosting(t, due)} }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if got[0].Description != "Build & ship" || pageRequests(d) != 1 {
		t.Fatalf("not re-read: %+v, requests %v", got[0], d.URLs())
	}
	if want := (Stats{Listed: 1, Refreshed: 1}); (*reps)[0] != want {
		t.Fatalf("report %+v", (*reps)[0])
	}
}

// A stored job whose turn came but whose re-read does not fit is reused, not dropped.
func TestStoredRereadsThatDoNotFitAreReused(t *testing.T) {
	newIdx := make([]int, MaxDetails)
	for i := range newIdx {
		newIdx[i] = 5000 + i
	}
	due := indexes(t, 2, true)
	all := append(append([]int{}, newIdx...), due...)
	d := siteDoer(locsOf(all), nil)
	stored := []posting.Posting{storedPosting(t, due[0]), storedPosting(t, due[1])}
	s, reps, _ := newBounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDetails+2 || pageRequests(d) != MaxDetails {
		t.Fatalf("%d postings, %d page requests", len(got), pageRequests(d))
	}
	if want := (Stats{Listed: MaxDetails + 2, Reused: 2, Fetched: MaxDetails}); (*reps)[0] != want {
		t.Fatalf("report %+v, want %+v", (*reps)[0], want)
	}
}

func TestOtherBoardsStoredPostingsDoNotMatch(t *testing.T) {
	i := indexes(t, 1, false)[0]
	other, _ := posting.TalentBrewKey("citi", idOf(i))
	p := storedPosting(t, i)
	p.Key = other
	d := siteDoer(locsOf([]int{i}), nil)
	s, _, _ := newBounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return []posting.Posting{p} }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Description != "Build & ship" || pageRequests(d) != 1 {
		t.Fatalf("%d postings, %v, %v", len(got), err, d.URLs())
	}
}

// ---- DropListed ----

func TestDropListedSkipsNewJobsBeforeTheirPageAndNeverJudgesStoredOnes(t *testing.T) {
	old := indexes(t, 1, false)[0]
	locs := []string{
		jobAt(board.Host, "27595", old),
		"https://jobs.intuit.com/job/new-york/staff-software-engineer/27595/102000000501",
		"https://jobs.intuit.com/job/san-diego/summer-intern-marketing/27595/102000000502",
	}
	d := siteDoer(locs, nil)
	var judged []posting.Posting
	s, reps, _ := newBounded(t, d, Options{
		Stored: func(sources.Board) []posting.Posting { return []posting.Posting{storedPosting(t, old)} },
		DropListed: func(p posting.Posting) bool {
			judged = append(judged, p)
			return strings.Contains(strings.ToLower(p.Title), "intern")
		},
	})
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || pageRequests(d) != 1 || d.URLs()[upfront] != locs[1] {
		t.Fatalf("%d postings, requests %v", len(got), d.URLs())
	}
	if len(judged) != 2 {
		t.Fatalf("DropListed saw %d rows, want the two new ones: %+v", len(judged), judged)
	}
	want := posting.Posting{ATS: posting.TalentBrew, Company: "Intuit", Title: "Staff Software Engineer", Location: "New York"}
	if judged[0] != want {
		t.Fatalf("listed posting %+v, want %+v", judged[0], want)
	}
	if judged[1].Title != "Summer Intern Marketing" || judged[1].Location != "San Diego" {
		t.Fatalf("listed posting %+v", judged[1])
	}
	if want := (Stats{Listed: 3, Reused: 1, Fetched: 1, Prefiltered: 1}); (*reps)[0] != want {
		t.Fatalf("report %+v, want %+v", (*reps)[0], want)
	}
}

func TestDroppedRowsDoNotCountTowardTheDetailCap(t *testing.T) {
	d := siteDoer(locsN(MaxDetails+20), nil)
	keep := map[string]bool{}
	for i := 0; i < 5; i++ {
		keep[fmt.Sprintf("Role %d", i)] = true
	}
	s, reps, _ := newBounded(t, d, Options{Stored: storedNone, DropListed: func(p posting.Posting) bool { return !keep[p.Title] }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 5 || pageRequests(d) != 5 {
		t.Fatalf("%d postings, %v, page requests %d", len(got), err, pageRequests(d))
	}
	if (*reps)[0].Prefiltered != MaxDetails+15 || (*reps)[0].Deferred != 0 {
		t.Fatalf("report %+v", (*reps)[0])
	}
}

// ---- gone ----

// goneBoard is a board of n jobs where the first k pages answer 404.
func goneBoard(n, k int) *srctest.Doer {
	locs := locsN(n)
	bad := map[string]srctest.Reply{}
	for i := 0; i < k; i++ {
		bad[locs[i]] = notFound()
	}
	return siteDoer(locs, bad)
}

func TestGoneJobsAreBoundedByMaxGone(t *testing.T) {
	for _, n := range []int{1, 2, 9, 10, 11, 19, 20, 21, 100} {
		bound := max(1, n/10)
		seenK := map[int]bool{}
		for _, k := range []int{1, bound, bound + 1, n} {
			if k > n || k < 1 || seenK[k] {
				continue
			}
			seenK[k] = true
			t.Run(fmt.Sprintf("n=%d/gone=%d", n, k), func(t *testing.T) {
				d := goneBoard(n, k)
				s, reps, _ := newBounded(t, d, Options{Stored: storedNone})
				got, err := s.List(context.Background(), board)
				if k > bound || k == n {
					if err == nil || got != nil {
						t.Fatalf("got %d postings, err %v; want the board to fail", len(got), err)
					}
					if errors.Is(err, srcutil.ErrBoardNotFound) {
						t.Fatalf("detail 404s read as board-not-found: %v", err)
					}
					return
				}
				if err != nil || len(got) != n-k {
					t.Fatalf("got %d postings, err %v; want %d, nil", len(got), err, n-k)
				}
				if (*reps)[0].Gone != k {
					t.Fatalf("report %+v, want gone %d", (*reps)[0], k)
				}
			})
		}
	}
}

// With every other job reused, one new job gone is only that job.
func TestOneNewJobGoneAmongStoredIsNotABoardFailure(t *testing.T) {
	old := indexes(t, 3, false)
	locs := append(locsOf(old), jobAt(board.Host, "27595", 9001))
	d := siteDoer(locs, map[string]srctest.Reply{locs[3]: notFound()})
	stored := []posting.Posting{storedPosting(t, old[0]), storedPosting(t, old[1]), storedPosting(t, old[2])}
	s, reps, _ := newBounded(t, d, Options{Stored: func(sources.Board) []posting.Posting { return stored }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 3 || (*reps)[0].Gone != 1 {
		t.Fatalf("%d postings, %v, %+v", len(got), err, *reps)
	}
}

// Only a 404 means closed. A 410, a server error, a page that is not a job
// page and a body over the cap are failures that fail the board, never gone
// (goneparity pins the same for every adapter).
func TestOnlyA404IsGone(t *testing.T) {
	cases := map[string]srctest.Reply{
		"410":                {Status: 410, Body: []byte("gone")},
		"500":                {Status: 500, Body: []byte("oops")},
		"403":                {Status: 403, Body: []byte("denied")},
		"429":                {Status: 429, Header: http.Header{"Retry-After": {"0"}}},
		"no JobPosting":      ok("<html><body>404 Page Not Found</body></html>"),
		"empty body":         ok(""),
		"oversized":          {Status: 200, Body: []byte(strings.Repeat("x", maxDetailBody+1))},
		"another job's page": ok(ldPage(jobAt(board.Host, "27595", 99), "Other")),
		"another host's url": ok(ldPage("https://evil.test/job/x/y/27595/"+idOf(5), "Other")),
		"another company id": ok(ldPage("https://jobs.intuit.com/job/x/y/27596/"+idOf(5), "Other")),
		"http url":           ok(ldPage("http://jobs.intuit.com/job/x/y/27595/"+idOf(5), "Other")),
		"empty title":        ok(ldPage(jobAt(board.Host, "27595", 5), "  ")),
		"bad date":           ok(strings.Replace(ldPage(jobAt(board.Host, "27595", 5), "T"), `"2026-10-27"`, `"yesterday"`, 1)),
		"not a JobPosting":   ok(strings.Replace(ldPage(jobAt(board.Host, "27595", 5), "T"), `"JobPosting"`, `"Organization"`, 1)),
		"broken json":        ok(`<script type="application/ld+json">{"@type":"JobPosting",</script>`),
	}
	for name, rp := range cases {
		t.Run(name, func(t *testing.T) {
			locs := locsN(20)
			d := siteDoer(locs, map[string]srctest.Reply{locs[5]: rp})
			s, reps, _ := newBounded(t, d, Options{Stored: storedNone})
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

func TestDetailTimeoutFailsTheBoardAndReportsIt(t *testing.T) {
	locs := locsN(3)
	d := siteDoer(locs, nil)
	d2 := &toggleDoer{inner: d, failURL: locs[1]}
	s, err := NewWith(d2, Options{Stored: storedNone, Wait: noWait})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%d postings, %v; want a deadline error and no partial list", len(got), err)
	}
}

type toggleDoer struct {
	inner   *srctest.Doer
	failURL string
}

func (d *toggleDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req.URL.String() == d.failURL {
		return nil, context.DeadlineExceeded
	}
	return d.inner.Do(ctx, req)
}

func TestCancellationBetweenDetailsStopsTheRun(t *testing.T) {
	d := siteDoer(locsN(5), nil)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	s, err := NewWith(d, Options{Stored: storedNone, Wait: func(ctx context.Context, _ time.Duration) error {
		if n++; n == upfront+1 { // the second detail's wait
			cancel()
		}
		return ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if pageRequests(d) != 1 {
		t.Fatalf("requests after the cancel: %v", d.URLs())
	}
}

// A hostile page cannot name another job: a decoy block for a different job
// is passed over for the page's own.
func TestPageWithADecoyBlockReadsOwnJob(t *testing.T) {
	locs := locsN(1)
	decoy := `<script type="application/ld+json">{"@type":"JobPosting","title":"DECOY","datePosted":"2026-01-01","url":"` + jobAt(board.Host, "27595", 77) + `"}</script>`
	page := strings.Replace(ldPage(locs[0], "Real"), "<head>", "<head>"+decoy, 1)
	d := siteDoer(locs, map[string]srctest.Reply{locs[0]: ok(page)})
	got, err := newSource(d).List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Title != "Real" {
		t.Fatalf("%+v, %v", got, err)
	}
}
