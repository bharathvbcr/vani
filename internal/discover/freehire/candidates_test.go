package freehire

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

func fixtureJobs(t *testing.T) []Job {
	t.Helper()
	d := byOffset(map[string]srctest.Reply{"0": ok(fixture(t))})
	res, err := noWait(d, 1, nil).Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return res.Jobs
}

func find(c Candidates, ats posting.ATS, token string) (Candidate, bool) {
	for _, b := range c.Boards {
		if b.Board.ATS == ats && strings.EqualFold(b.Board.Token, token) {
			return b, true
		}
	}
	return Candidate{}, false
}

func unreadableCount(c Candidates, source, reason string) int {
	n := 0
	for _, u := range c.Unreadable {
		if u.Source == source && (reason == "" || u.Reason == reason) {
			n += u.Count
		}
	}
	return n
}

// The live fixture: every result is on exactly one board or in exactly one
// unreadable group, so nothing is dropped between the two.
func TestBoardsFromTheFixture(t *testing.T) {
	jobs := fixtureJobs(t)
	c := Boards(jobs, "ml engineer")
	total := 0
	for _, b := range c.Boards {
		total += b.Postings
		if err := config.ValidBoard(b.Board); err != nil {
			t.Errorf("%s/%s is not a board `watchlist add` takes: %v", b.Board.ATS, b.Board.Token, err)
		}
		if b.Board.Company == "" {
			t.Errorf("%s/%s has no company", b.Board.ATS, b.Board.Token)
		}
	}
	for _, u := range c.Unreadable {
		total += u.Count
	}
	if total != len(jobs) {
		t.Fatalf("boards and unreadable hold %d results of %d", total, len(jobs))
	}

	for _, tc := range []struct {
		ats      posting.ATS
		token    string
		postings int
		host     string
		boardNm  string
		company  string
	}{
		{posting.Greenhouse, "noetik", 1, "", "", "Noetik"},
		{posting.Greenhouse, "defcon", 1, "", "", "DEFCON AI"},
		// A careers page on the company's own domain: the board is the id's.
		{posting.Greenhouse, "nebius", 1, "", "", "Nebius"},
		// An EU-hosted board (job-boards.eu.greenhouse.io): also named by the id.
		{posting.Greenhouse, "prolificacademicltd", 21, "", "", "Prolific Academic Ltd"},
		{posting.Lever, "entefy", 2, "", "", "Entefy"},
		{posting.Lever, "makpar", 1, "", "", "Makpar"},
		{posting.Ashby, "pika", 1, "", "", "Pika"},
		// A board name with a space is a slug token and the literal name.
		{posting.Ashby, "vibrant-planet", 1, "", "vibrant planet", "Vibrant Planet"},
		// A dotted board name is a valid token.
		{posting.Ashby, "careers.azx.io", 2, "", "", "AZX"},
		{posting.Workday, "r1rcm/R1RCM", 2, "r1rcm.wd1.myworkdayjobs.com", "", "r1rcm"},
		{posting.Workday, "netflix/Netflix", 1, "netflix.wd108.myworkdayjobs.com", "", "netflix"},
		{posting.Workday, "ebay/apply", 1, "ebay.wd5.myworkdayjobs.com", "", "eBay"},
		// A Workable shortlink (/j/{id}) names the job only: the id names the account.
		{posting.Workable, "workana-premium", 1, "", "", "Workana Premium"},
	} {
		b, ok := find(c, tc.ats, tc.token)
		if !ok {
			t.Errorf("no board %s/%s", tc.ats, tc.token)
			continue
		}
		if b.Postings != tc.postings || b.Board.Host != tc.host || b.Board.BoardName != tc.boardNm || b.Board.Company != tc.company || b.Board.Token != tc.token {
			t.Errorf("%s/%s = %+v (postings %d), want postings %d host %q board name %q company %q",
				tc.ats, tc.token, b.Board, b.Postings, tc.postings, tc.host, tc.boardNm, tc.company)
		}
	}

	// Workday boards take the search that found them; the others do not.
	for _, b := range c.Boards {
		if want := b.Board.ATS == posting.Workday; (b.Board.Search == "ml engineer") != want || (!want && b.Board.Search != "") {
			t.Errorf("%s/%s search %q", b.Board.ATS, b.Board.Token, b.Board.Search)
		}
	}

	// Sources Vani has no adapter for are named, with counts.
	for source, n := range map[string]int{"aijobs": 6, "jazzhr": 6, "himalayas": 4, "radancy": 3, "loxo": 3, "icims": 2, "adp": 1} {
		if got := unreadableCount(c, source, ReasonNoAdapter); got != n {
			t.Errorf("unreadable %s = %d, want %d", source, got, n)
		}
	}

	// Ranked by postings, most first, then by ATS and token.
	for i := 1; i < len(c.Boards); i++ {
		a, b := c.Boards[i-1], c.Boards[i]
		if a.Postings < b.Postings {
			t.Fatalf("board %d (%d) ranks above %d (%d)", i-1, a.Postings, i, b.Postings)
		}
	}
	for i := 1; i < len(c.Unreadable); i++ {
		if c.Unreadable[i-1].Count < c.Unreadable[i].Count {
			t.Fatalf("unreadable %d is out of order", i)
		}
	}
	if again := Boards(slices.Clone(jobs), "ml engineer"); !slices.EqualFunc(again.Boards, c.Boards, func(a, b Candidate) bool {
		return a.Postings == b.Postings && a.Board.ATS == b.Board.ATS && a.Board.Token == b.Board.Token
	}) {
		t.Error("the same results gave a different order")
	}
}

// IDBoard is only as sound as the id's shape: in the live fixture the part of
// a Greenhouse or Lever external_id before the first ':' is the board the URL
// names, whenever the URL names one. Most of the fixture's Greenhouse results
// are on job-boards.eu.greenhouse.io, which sources.BoardFromURL does not read
// (so the id names their board): there the URL's first path segment is the board.
func TestFixtureExternalIDMatchesURLBoard(t *testing.T) {
	checked := 0
	for _, j := range fixtureJobs(t) {
		if j.Source != "greenhouse" && j.Source != "lever" {
			continue
		}
		u, err := url.Parse(j.URL)
		if err != nil {
			t.Fatal(err)
		}
		urlToken := ""
		if b, known, err := sources.BoardFromURL(u); known && err == nil {
			urlToken = b.Token
		} else if strings.HasSuffix(u.Hostname(), ".greenhouse.io") {
			urlToken, _, _ = strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		}
		if urlToken == "" {
			continue
		}
		checked++
		if token, _, _ := strings.Cut(j.ExternalID, ":"); !strings.EqualFold(token, urlToken) {
			t.Errorf("%s: external id %q names %q, the URL names %q", j.Source, j.ExternalID, token, urlToken)
		}
	}
	if checked < 38 {
		t.Errorf("only %d Greenhouse and Lever results compared", checked)
	}
}

func job(source, id, rawURL, company string) Job {
	return Job{Source: source, ExternalID: id, URL: rawURL, Company: company}
}

func TestBoardsReasons(t *testing.T) {
	for name, tc := range map[string]struct {
		job    Job
		reason string // "" = a board
		ats    posting.ATS
		token  string
	}{
		"unknown source":                {job("jazzhr", "x:1", "https://x.applytojob.com/apply/1", "X"), ReasonNoAdapter, "", ""},
		"an aggregator":                 {job("himalayas", ":https://himalayas.app/c/a", "https://himalayas.app/c/a", "X"), ReasonNoAdapter, "", ""},
		"source names are exact":        {job("greenhouse-eu", "x:1", "https://boards.greenhouse.io/x/jobs/1", "X"), ReasonNoAdapter, "", ""},
		"lever":                         {job("lever", "x:1", "https://jobs.lever.co/x/uuid", "X"), "", posting.Lever, "x"},
		"lever eu":                      {job("lever", "x:1", "https://jobs.eu.lever.co/x/uuid", "X"), "", posting.Lever, "x"},
		"smartrecruiters":               {job("smartrecruiters", "x:1", "https://jobs.smartrecruiters.com/Acme/123-engineer", "X"), "", posting.SmartRecruiters, "Acme"},
		"smartrecruiters app page":      {job("smartrecruiters", "x:1", "https://jobs.smartrecruiters.com/oneclick-ui/company/Acme/publication/1", "X"), ReasonNoBoard, "", ""},
		"gem":                           {job("gem", "x:1", "https://jobs.gem.com/acme/abc", "X"), "", posting.Gem, "acme"},
		"rippling with a locale":        {job("rippling", "x:1", "https://ats.rippling.com/en-GB/acme/jobs/uuid", "X"), "", posting.Rippling, "acme"},
		"rippling without a board":      {job("rippling", "x:1", "https://ats.rippling.com/jobs/uuid", "X"), ReasonNoBoard, "", ""},
		"workable account":              {job("workable", "x:1", "https://apply.workable.com/acme/j/ABC/", "X"), "", posting.Workable, "acme"},
		"workable shortlink":            {job("workable", "acme:ABC", "https://apply.workable.com/j/ABC", "X"), "", posting.Workable, "acme"},
		"workable shortlink, no id":     {job("workable", "", "https://apply.workable.com/j/ABC", "X"), ReasonBadBoardID, "", ""},
		"oracle":                        {job("oracle", "x:1", "https://eeho.fa.us2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/job/42", "X"), "", posting.OracleHCM, "eeho/CX_1"},
		"workday on the shared host":    {job("workday", "x:1", "https://wd3.myworkdaysite.com/en-US/recruiting/acme/Careers/job/1", "X"), "", posting.Workday, "acme/Careers"},
		"greenhouse custom domain":      {job("greenhouse", "nebius:4864833101", "https://careers.nebius.com/?gh_jid=4864833101", "X"), "", posting.Greenhouse, "nebius"},
		"greenhouse, empty board path":  {job("greenhouse", "acme:1", "https://boards.greenhouse.io/", "X"), "", posting.Greenhouse, "acme"},
		"greenhouse custom, no id":      {job("greenhouse", "", "https://careers.acme.com/?gh_jid=1", "X"), ReasonBadBoardID, "", ""},
		"greenhouse custom, empty slug": {job("greenhouse", ":1", "https://careers.acme.com/?gh_jid=1", "X"), ReasonBadBoardID, "", ""},
		"greenhouse custom, path id":    {job("greenhouse", "../etc:1", "https://careers.acme.com/?gh_jid=1", "X"), ReasonBadBoardID, "", ""},
		"greenhouse custom, spaces":     {job("greenhouse", "a b:1", "https://careers.acme.com/?gh_jid=1", "X"), ReasonBadBoardID, "", ""},
		"greenhouse custom, long":       {job("greenhouse", strings.Repeat("a", 101)+":1", "https://careers.acme.com/?gh_jid=1", "X"), ReasonBadBoardID, "", ""},
		"lever custom domain":           {job("lever", "acme:1", "https://careers.acme.com/job/1", "X"), ReasonNoBoard, "", ""},
		"ashby custom domain":           {job("ashby", "acme:1", "https://careers.acme.com/job/1", "X"), ReasonNoBoard, "", ""},
		"source says lever, URL is gh":  {job("lever", "x:1", "https://boards.greenhouse.io/x/jobs/1", "X"), ReasonOtherATS, "", ""},
		"source says greenhouse, lever": {job("greenhouse", "x:1", "https://jobs.lever.co/x/uuid", "X"), ReasonOtherATS, "", ""},
		"http URL":                      {job("lever", "x:1", "http://jobs.lever.co/x/uuid", "X"), ReasonBadURL, "", ""},
		"userinfo":                      {job("lever", "x:1", "https://jobs.lever.co@evil.test/x/uuid", "X"), ReasonBadURL, "", ""},
		"no host":                       {job("lever", "x:1", "https:///x", "X"), ReasonBadURL, "", ""},
		"ftp":                           {job("lever", "x:1", "ftp://jobs.lever.co/x", "X"), ReasonBadURL, "", ""},
		"unparsable":                    {job("lever", "x:1", "https://jobs.lever.co/%zz", "X"), ReasonBadURL, "", ""},
		"lever host with no path":       {job("lever", "x:1", "https://jobs.lever.co/", "X"), ReasonNoBoard, "", ""},
		"token with a colon":            {job("lever", "x:1", "https://jobs.lever.co/a:b/uuid", "X"), reasonRefused, "", ""},
		"token with an escape":          {job("lever", "x:1", "https://jobs.lever.co/acme%1b%5b2J/uuid", "X"), ReasonBadBoardID, "", ""},
		"token with a bidi override":    {job("ashby", "x:1", "https://jobs.ashbyhq.com/acme%E2%80%AEx/uuid", "X"), ReasonBadBoardID, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := Boards([]Job{tc.job}, "ml engineer")
			if tc.reason == "" {
				if len(c.Boards) != 1 || len(c.Unreadable) != 0 {
					t.Fatalf("got %+v, want one board", c)
				}
				if b := c.Boards[0].Board; b.ATS != tc.ats || b.Token != tc.token {
					t.Errorf("board %s/%s, want %s/%s", b.ATS, b.Token, tc.ats, tc.token)
				}
				return
			}
			if len(c.Boards) != 0 || len(c.Unreadable) != 1 || c.Unreadable[0].Count != 1 || !strings.HasPrefix(c.Unreadable[0].Reason, tc.reason) {
				t.Errorf("got %+v, want one unreadable %q", c, tc.reason)
			}
		})
	}
}

func TestBoardsMergesDuplicateBoards(t *testing.T) {
	c := Boards([]Job{
		job("greenhouse", "noetik:1", "https://job-boards.greenhouse.io/noetik/jobs/1", "Noetik"),
		job("greenhouse", "noetik:2", "https://boards.greenhouse.io/Noetik/jobs/2", "NOETIK Inc"),
		job("greenhouse", "noetik:3", "https://careers.noetik.com/?gh_jid=3", "Noetik"),
		job("greenhouse", "noetik:4", "https://boards.greenhouse.io/noetik/jobs/4", ""),
		job("lever", "noetik:5", "https://jobs.lever.co/noetik/5", "Noetik Lever"),
	}, "x")
	if len(c.Boards) != 2 {
		t.Fatalf("%d boards, want the Greenhouse one once and the Lever one: %+v", len(c.Boards), c.Boards)
	}
	gh, _ := find(c, posting.Greenhouse, "noetik")
	if gh.Postings != 4 || gh.Board.Company != "Noetik" {
		t.Errorf("greenhouse board %+v postings %d: want 4 postings, the most common name", gh.Board, gh.Postings)
	}
	if lv, _ := find(c, posting.Lever, "noetik"); lv.Postings != 1 || c.Boards[0].Board.ATS != posting.Greenhouse {
		t.Errorf("lever %+v, first %+v", lv, c.Boards[0])
	}
}

func TestBoardsCompanyFallsBackToTheToken(t *testing.T) {
	c := Boards([]Job{job("lever", "acme:1", "https://jobs.lever.co/acme/1", "")}, "x")
	if len(c.Boards) != 1 || c.Boards[0].Board.Company != "acme" {
		t.Fatalf("%+v", c)
	}
}

func TestBoardsOfNothing(t *testing.T) {
	if c := Boards(nil, "x"); len(c.Boards) != 0 || len(c.Unreadable) != 0 {
		t.Errorf("%+v", c)
	}
}

func TestBoardsGroupUnreadableBySourceAndReason(t *testing.T) {
	c := Boards([]Job{
		job("jazzhr", "a:1", "https://a.applytojob.com/apply/1", "A"),
		job("jazzhr", "a:2", "https://a.applytojob.com/apply/2", "A"),
		job("icims", "b:1", "https://careers-b.icims.com/jobs/1", "B"),
		job("lever", "c:1", "https://careers.c.com/1", "C"),
		job("lever", "c:2", "https://boards.greenhouse.io/c/jobs/2", "C"),
	}, "x")
	want := []Unreadable{
		{"jazzhr", ReasonNoAdapter, 2},
		{"icims", ReasonNoAdapter, 1},
		{"lever", ReasonOtherATS, 1},
		{"lever", ReasonNoBoard, 1},
	}
	if !slices.Equal(c.Unreadable, want) {
		t.Errorf("unreadable %+v, want %+v", c.Unreadable, want)
	}
}

// The table is the one place a source is tied to an ATS: one row per source,
// and a board of each ATS in it is one Vani accepts.
func TestSourceTable(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range sourceATS {
		if seen[r.Source] || !sourceRE.MatchString(r.Source) {
			t.Errorf("source %q is repeated or not a freehire source name", r.Source)
		}
		seen[r.Source] = true
	}
	for source, ats := range map[string]posting.ATS{
		"greenhouse": posting.Greenhouse, "lever": posting.Lever, "ashby": posting.Ashby, "workday": posting.Workday,
		"gem": posting.Gem, "smartrecruiters": posting.SmartRecruiters, "workable": posting.Workable,
		"rippling": posting.Rippling, "oracle": posting.OracleHCM,
	} {
		i := slices.IndexFunc(sourceATS, func(r sourceRow) bool { return r.Source == source })
		if i < 0 || sourceATS[i].ATS != ats {
			t.Errorf("source %q does not map to %s", source, ats)
		}
	}
	if len(sourceATS) != 9 {
		t.Errorf("%d rows: a new row needs a case here and a host in sources.BoardFromURL", len(sourceATS))
	}
}
