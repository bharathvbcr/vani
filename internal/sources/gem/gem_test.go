package gem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

const groqURL = "https://api.gem.com/job_board/v0/groq/job_posts/"

var board = sources.Board{ATS: posting.Gem, Token: "groq", Company: "Groq"}

func newSource(replies map[string]srctest.Reply) (*source, *srctest.Doer) {
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func reply(status int, body string) map[string]srctest.Reply {
	return map[string]srctest.Reply{groqURL: {Status: status, Body: []byte(body)}}
}

func list(t *testing.T, body string) []posting.Posting {
	t.Helper()
	s, _ := newSource(reply(200, body))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	got := list(t, string(raw))
	want := []struct {
		p      posting.Posting
		prefix string
	}{
		{posting.Posting{
			Key: "gm:groq:am9icg9zddqauz7g7jlglhqawwv7sec2", ATS: posting.Gem, Company: "Groq",
			Title: "Sr./Staff Forward Deployed Engineer", Location: "Dallas, United States", Department: "Commercial Sales",
			URL:       "https://jobs.gem.com/groq/am9icG9zdDqAUz7G7JlGLHQawWv7Sec2",
			PostedAt:  time.Date(2026, 9, 22, 1, 2, 11, 972e6, time.UTC),
			UpdatedAt: time.Date(2026, 10, 26, 23, 29, 16, 989e6, time.UTC), SeenAt: fixedNow,
		}, "Role: full-time, on-site\n\nAbout Groq\nInference is the engine"},
		{posting.Posting{
			Key: "gm:groq:am9icg9zddop2lf966r_owznxina1zw3", ATS: posting.Gem, Company: "Groq",
			Title: "Commercial Counsel", Location: "United States - Remote", Department: "Legal", Remote: true,
			URL:       "https://jobs.gem.com/retool/am9icG9zdDop2lf966R_oWZnXINA1Zw3",
			PostedAt:  time.Date(2026, 10, 27, 22, 54, 52, 299e6, time.UTC),
			UpdatedAt: time.Date(2026, 10, 27, 22, 54, 52, 299e6, time.UTC), SeenAt: fixedNow,
		}, "Role: full-time, remote\n\nABOUT RETOOL\nNearly every company"},
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
			t.Errorf("posting %d description starts %.80q, want prefix %q", i, desc, w.prefix)
		}
		if strings.ContainsAny(desc, "<>") || len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description carries markup or is %d bytes", i, len(desc))
		}
	}
}

// TestPayStaysInTheText: Gem has no pay field, so the range the board writes
// into its text must survive into the description and be read as pay.
func TestPayStaysInTheText(t *testing.T) {
	raw, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	got := list(t, string(raw))
	for _, r := range []string{"$270,400 - $365,800", "$341,400 - $401,600"} {
		if !strings.Contains(got[0].Description, r) {
			t.Fatalf("description lost the range %q", r)
		}
	}
	// The parser's own reading of the two-level list is its business; the
	// adapter's is that some USD range in the six figures comes out.
	pays := jobinsight.PostingPay(got[0])
	if len(pays) == 0 || pays[0].Currency != "USD" || pays[0].Min < 270000 || pays[0].Max > 402000 {
		t.Fatalf("pay read from the Groq description: %+v", pays)
	}
	if p := jobinsight.PostingPay(got[1]); len(p) != 0 {
		t.Fatalf("a post that states no pay read as %+v", p)
	}
}

func TestDescriptionFallsBackToHTML(t *testing.T) {
	const post = `{"id":"p1","title":"T","absolute_url":"https://jobs.gem.com/groq/p1"`
	for name, tc := range map[string]struct{ extra, want string }{
		"plain wins":      {`,"content_plain":"Plain text.","content":"<p>Html text.</p>"`, "Plain text."},
		"blank plain":     {`,"content_plain":" \n ","content":"<div>Html &amp; <b>text</b>.</div><div>Two</div>"`, "Html & text.\nTwo"},
		"no plain field":  {`,"content":"<p>Only html.</p>"`, "Only html."},
		"neither":         {``, ""},
		"html is escaped": {`,"content_plain":"","content":"&lt;script&gt;x&lt;/script&gt;"`, "<script>x</script>"},
	} {
		got := list(t, `[`+post+tc.extra+`}]`)
		if got[0].Description != tc.want {
			t.Errorf("%s: description %q, want %q", name, got[0].Description, tc.want)
		}
	}
}

func TestRoleLine(t *testing.T) {
	const post = `{"id":"p1","title":"T","absolute_url":"https://jobs.gem.com/groq/p1","content_plain":"Body."`
	for name, tc := range map[string]struct {
		extra, want string
		remote      bool
	}{
		"in office":    {`,"employment_type":"full_time","location_type":"in_office"`, "Role: full-time, on-site\n\nBody.", false},
		"hybrid":       {`,"employment_type":"part_time","location_type":"hybrid"`, "Role: part-time, hybrid\n\nBody.", false},
		"remote":       {`,"employment_type":"contract","location_type":"remote"`, "Role: contract, remote\n\nBody.", true},
		"unknown site": {`,"employment_type":"full_time","location_type":"moon"`, "Role: full-time\n\nBody.", false},
		"neither":      {``, "Body.", false},
		"Remote case":  {`,"location_type":"Remote"`, "Body.", false},
	} {
		got := list(t, `[`+post+tc.extra+`}]`)
		if got[0].Description != tc.want || got[0].Remote != tc.remote {
			t.Errorf("%s: description %q remote %v, want %q %v", name, got[0].Description, got[0].Remote, tc.want, tc.remote)
		}
	}
}

// TestDescriptionCapKeepsTheRoleLine: a huge description is cut, the role line
// ahead of it is not.
func TestDescriptionCapKeepsTheRoleLine(t *testing.T) {
	plain := strings.Repeat("lorem ", 100<<10/6)
	body := fmt.Sprintf(`[{"id":"p","title":"T","absolute_url":"https://jobs.gem.com/groq/p","employment_type":"full_time","content_plain":%q}]`, plain)
	got := list(t, body)
	if n := len(got[0].Description); n == 0 || n > srcutil.MaxDescription {
		t.Fatalf("description %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
	if !strings.HasPrefix(got[0].Description, "Role: full-time\n\n") {
		t.Fatalf("role line cut: %.40q", got[0].Description)
	}
}

func TestOptionalFields(t *testing.T) {
	const post = `{"id":"p1","title":" T  ","absolute_url":"https://jobs.gem.com/groq/p1"`
	for name, extra := range map[string]string{
		"nothing":        ``,
		"null location":  `,"location":null,"departments":null`,
		"null name":      `,"location":{"name":null},"departments":[]`,
		"null timestamp": `,"first_published_at":null,"updated_at":null`,
	} {
		got := list(t, `[`+post+extra+`}]`)
		p := got[0]
		if p.Title != "T" || p.Location != "" || p.Department != "" || !p.PostedAt.IsZero() || !p.UpdatedAt.IsZero() {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

func TestEmptyBoardIsValid(t *testing.T) {
	s, _ := newSource(reply(200, `[]`))
	got, err := s.List(context.Background(), board)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v: want empty non-nil list and no error", got, err)
	}
}

func TestNotFound(t *testing.T) {
	// Verbatim from a live miss, 2026-10-27.
	s, _ := newSource(reply(404, `{"code":404,"errors":[{"message":"Job board with vanity URL path groq not found"}],"message":"Job board with vanity URL path groq not found","status":"Not Found"}`))
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) || got != nil {
		t.Fatalf("got %v, %v, want ErrBoardNotFound", got, err)
	}
}

func TestDependencyDownIsAnError(t *testing.T) {
	for _, status := range []int{500, 502, 504} {
		s, _ := newSource(reply(status, `[]`))
		got, err := s.List(context.Background(), board)
		if err == nil || errors.Is(err, ErrBoardNotFound) || got != nil {
			t.Errorf("status %d: got %v, %v", status, got, err)
		}
	}
	// A 200 with an HTML error page is not a listing either.
	s, _ := newSource(reply(200, `<html><body>Service Unavailable</body></html>`))
	if got, err := s.List(context.Background(), board); err == nil || got != nil {
		t.Fatalf("html 200: got %v, %v", got, err)
	}
}

// blockedDoer never answers: it returns when the request's context is done.
type blockedDoer struct{}

func (blockedDoer) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTimeoutIsAnError(t *testing.T) {
	s := &source{d: blockedDoer{}, now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got, err := s.List(ctx, board)
	if !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatalf("got %v, %v, want a deadline error and no postings", got, err)
	}
}

func TestMalformedPayloadsAreErrors(t *testing.T) {
	ok := `"title":"T","absolute_url":"https://jobs.gem.com/groq/1"`
	bad := map[string]string{
		"not json":              `<html>`,
		"truncated":             `[{"id":"1"`,
		"object not array":      `{"code":429,"message":"rate limited"}`,
		"null":                  `null`,
		"empty body":            ``,
		"wrong element type":    `[1]`,
		"missing id":            `[{` + ok + `}]`,
		"blank id":              `[{"id":"  ",` + ok + `}]`,
		"id with separator":     `[{"id":"a:b",` + ok + `}]`,
		"numeric id":            `[{"id":12,` + ok + `}]`,
		"missing title":         `[{"id":"1","absolute_url":"https://jobs.gem.com/groq/1"}]`,
		"blank title":           `[{"id":"1","title":" ","absolute_url":"https://jobs.gem.com/groq/1"}]`,
		"missing absolute_url":  `[{"id":"1","title":"T"}]`,
		"file url":              `[{"id":"1","title":"T","absolute_url":"file:///etc/passwd"}]`,
		"javascript url":        `[{"id":"1","title":"T","absolute_url":"javascript:alert(1)"}]`,
		"userinfo url":          `[{"id":"1","title":"T","absolute_url":"https://jobs.gem.com@evil.test/x"}]`,
		"bad first_published":   `[{"id":"1",` + ok + `,"first_published_at":"yesterday"}]`,
		"bad updated_at":        `[{"id":"1",` + ok + `,"updated_at":1790000000}]`,
		"departments not array": `[{"id":"1",` + ok + `,"departments":"x"}]`,
		"one bad among good":    `[{"id":"1",` + ok + `},{"id":"2","title":"T"}]`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(reply(200, body))
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want error, got %d postings", len(got))
			}
		})
	}
}

func TestDuplicateIDsAreAnError(t *testing.T) {
	post := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"title":"T","absolute_url":"https://jobs.gem.com/groq/%s"}`, id, id)
	}
	// The same id twice, and two ids that differ only in case: the key lowercases.
	for name, body := range map[string]string{
		"same id":   `[` + post("abc") + `,` + post("abc") + `]`,
		"case only": `[` + post("AbC") + `,` + post("aBc") + `]`,
	} {
		s, _ := newSource(reply(200, body))
		got, err := s.List(context.Background(), board)
		if err == nil || !strings.Contains(err.Error(), "share key") || got != nil {
			t.Errorf("%s: got %v, %v", name, got, err)
		}
	}
	// Posts of one job (same internal_job_id, different ids) are separate postings.
	body := `[{"id":"a","internal_job_id":"j","title":"T","absolute_url":"https://jobs.gem.com/groq/a"},` +
		`{"id":"b","internal_job_id":"j","title":"T","absolute_url":"https://jobs.gem.com/groq/b"}]`
	if got := list(t, body); len(got) != 2 || got[0].Key == got[1].Key {
		t.Fatalf("two posts of one job: %+v", got)
	}
}

func postsBody(n int) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":"p%d","title":"T","absolute_url":"https://jobs.gem.com/groq/p%d"}`, i, i)
	}
	sb.WriteByte(']')
	return sb.String()
}

// TestRoundNumberListingIsSuspect: a listing that is exactly a common page
// size may be a truncated one, and a truncated listing would age out live
// postings, so it is an error. The live boards (37, 27) and counts either
// side of a round number read fine.
func TestRoundNumberListingIsSuspect(t *testing.T) {
	for _, n := range []int{50, 100, 200, 250, 500, 1000} {
		s, _ := newSource(reply(200, postsBody(n)))
		got, err := s.List(context.Background(), board)
		if !errors.Is(err, ErrSuspectCap) || got != nil {
			t.Errorf("%d posts: got %d postings, %v, want ErrSuspectCap", n, len(got), err)
		}
	}
	for _, n := range []int{0, 1, 10, 20, 25, 27, 37, 49, 51, 99, 101, 999, 1001} {
		s, _ := newSource(reply(200, postsBody(n)))
		got, err := s.List(context.Background(), board)
		if err != nil || len(got) != n {
			t.Errorf("%d posts: got %d postings, %v", n, len(got), err)
		}
	}
}

func TestPostingCap(t *testing.T) {
	s, _ := newSource(reply(200, postsBody(srcutil.MaxPostings+1)))
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Fatalf("over the cap: %d postings, %v", len(got), err)
	}
}

func TestOversizedBodyIsAnError(t *testing.T) {
	// One byte over MaxBody; the body is the cheap kind (spaces after a valid
	// array), so the cap, not the parser, is what refuses it.
	body := make([]byte, srcutil.MaxBody+1)
	for i := range body {
		body[i] = ' '
	}
	copy(body, `[]`)
	s, _ := newSource(map[string]srctest.Reply{groqURL: {Status: 200, Body: body}})
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("got %v, %v, want ErrBodyTooLarge", got, err)
	}
}

func TestWrongATSAndBadTokensRefused(t *testing.T) {
	s, d := newSource(reply(200, `[]`))
	for _, b := range []sources.Board{
		{ATS: posting.Lever, Token: "groq"},
		{ATS: posting.Gem, Token: ""},
		{ATS: posting.Gem, Token: " \t"},
		{ATS: posting.Gem, Token: ".."},
		{ATS: posting.Gem, Token: "."},
		{ATS: posting.Gem, Token: "a:b"},
		{ATS: posting.Gem, Token: "groq", BoardName: "Groq"},
		{Token: "groq"},
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("board %+v accepted", b)
		}
	}
	if n := len(d.URLs()); n != 0 {
		t.Errorf("refused boards still made %d requests", n)
	}
}

// TestTokenIsEscaped: the shared token rule lets "/", "?", "#" and spaces
// through (config.ValidBoard refuses them earlier, on the adapter's behalf);
// each must still stay inside one path segment of the board URL.
func TestTokenIsEscaped(t *testing.T) {
	for token, want := range map[string]string{
		"a?x=1#y": "https://api.gem.com/job_board/v0/a%3Fx=1%23y/job_posts/",
		"a/b":     "https://api.gem.com/job_board/v0/a%2Fb/job_posts/",
		"a/../b":  "https://api.gem.com/job_board/v0/a%2F..%2Fb/job_posts/",
		"a b":     "https://api.gem.com/job_board/v0/a%20b/job_posts/",
	} {
		s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: []byte(`[]`)}})
		if _, err := s.List(context.Background(), sources.Board{ATS: posting.Gem, Token: token}); err != nil {
			t.Fatalf("%q: %v", token, err)
		}
		if u := d.URLs(); len(u) != 1 || u[0] != want {
			t.Fatalf("%q: requested %v, want %s", token, u, want)
		}
	}
}

func TestOneRequestPerList(t *testing.T) {
	s, d := newSource(reply(200, postsBody(3)))
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if u := d.URLs(); len(u) != 1 || u[0] != groqURL {
		t.Fatalf("requested %v, want exactly %s", u, groqURL)
	}
}

func TestNewAndATS(t *testing.T) {
	src := New(nil)
	if src.ATS() != posting.Gem {
		t.Fatal(src.ATS())
	}
	if _, err := src.List(context.Background(), board); err == nil {
		t.Fatal("nil doer must error, not panic")
	}
}
