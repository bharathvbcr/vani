package google

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

func board(terms ...string) sources.Board {
	return sources.Board{ATS: posting.Google, Token: "google", Company: "Google", Searches: terms}
}

// wrap puts a ds:1 data value inside a page shaped like the live one: other
// callbacks before it, script text after it.
func wrap(data string) string {
	return `<!doctype html><html><head><base href="https://www.google.com/about/careers/applications/"></head><body>` +
		`<script>AF_initDataCallback({key: 'ds:0', hash: '1', data:[[1,2]], sideChannel: {}});</script>` +
		`<script nonce="x">AF_initDataCallback({key: 'ds:1', hash: '2', data:` + data + `, sideChannel: {}});</script>` +
		`<a href="jobs/results/1-x">x</a></body></html>`
}

func fixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/ds1.json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func newSource(pages map[string]string) (*source, *srctest.Doer) {
	replies := map[string]srctest.Reply{}
	for u, body := range pages {
		replies[u] = srctest.Reply{Status: 200, Body: []byte(body)}
	}
	d := srctest.New(replies)
	return &source{d: d, now: func() time.Time { return fixedNow }}, d
}

func TestFixture(t *testing.T) {
	u := searchURL("LLM inference", nil)
	s, _ := newSource(map[string]string{u: wrap(fixture(t))})
	got, err := s.List(context.Background(), board("LLM inference"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3", len(got))
	}
	p := got[0]
	if p.Key != "gg:google:124910793232655046" || p.ATS != posting.Google || p.Company != "Google" ||
		p.Title != "Software Engineer, Model Inference, DeepMind" || p.Department != "DeepMind" ||
		p.URL != "https://www.google.com/about/careers/applications/jobs/results/124910793232655046" ||
		!strings.HasPrefix(p.Location, "London, UK; Mountain View, CA, USA") || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("posting 0: %+v", p)
	}
	for _, want := range []string{"Google DeepMind", "Minimum qualifications", "Bachelor"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q: %.300s", want, p.Description)
		}
	}
	for _, bad := range []string{"<li>", "<ul>", "signin?jobId"} {
		if i := strings.Index(p.Description, bad); i >= 0 {
			t.Errorf("description kept %q: ...%s...", bad, p.Description[max(0, i-80):min(len(p.Description), i+80)])
		}
	}
}

func TestSearchURL(t *testing.T) {
	u := searchURL("LLM inference", map[string][]string{"target_level": {"EARLY", "MID"}, "location": {"United States"}})
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("q") != "LLM inference" || strings.Join(q["target_level"], ",") != "EARLY,MID" || q.Get("location") != "United States" {
		t.Errorf("query %v", q)
	}
	// robots.txt disallows every paginated variant: no page parameter, ever.
	if q.Has("page") || strings.Contains(u, "page=") {
		t.Errorf("url carries a page parameter: %s", u)
	}
	if !strings.HasPrefix(u, "https://www.google.com/about/careers/applications/jobs/results?") {
		t.Errorf("url %s", u)
	}
}

// Every term is page 1 of its own search; a posting two terms find is
// reported once; filters ride on every term.
func TestTermsUnionAndFilters(t *testing.T) {
	f := map[string][]string{"target_level": {"EARLY"}}
	a, b := searchURL("LLM", f), searchURL("inference", f)
	s, d := newSource(map[string]string{a: wrap(fixture(t)), b: wrap(fixture(t))})
	brd := board("LLM", "inference", "llm")
	brd.Filters = f
	got, err := s.List(context.Background(), brd)
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d, err %v; want the 3 fixture postings once", len(got), err)
	}
	if urls := d.URLs(); len(urls) != 2 || urls[0] != a || urls[1] != b {
		t.Errorf("requests %v", urls)
	}
}

func TestRefusals(t *testing.T) {
	u := searchURL("x", nil)
	for name, tc := range map[string]struct {
		page string
		want error
	}{
		"no ds:1 block (consent or error page)": {`<html><body>Before you continue to Google</body></html>`, ErrNoResults},
		"truncated: more jobs than page 1":      {wrap(`[[` + one("1", "A") + `], null, 28, 20]`), ErrTruncated},
		"data not a list":                       {wrap(`{"a":1}`), ErrShape},
		"short data":                            {wrap(`[[]]`), ErrShape},
		"total not a number":                    {wrap(`[[], null, "many", 20]`), ErrShape},
		"id not numeric":                        {wrap(`[[` + one("abc", "A") + `], null, 1, 20]`), ErrShape},
		"empty title":                           {wrap(`[[` + one("1", " ") + `], null, 1, 20]`), ErrShape},
		"too few fields":                        {wrap(`[[["1","A"]], null, 1, 20]`), ErrShape},
		"locations wrong type":                  {wrap(`[[` + strings.Replace(one("1", "A"), `[["Mountain View, CA, USA"]]`, `"MV"`, 1) + `], null, 1, 20]`), ErrShape},
		"unterminated data":                     {`<script>AF_initDataCallback({key: 'ds:1', hash: '2', data:[[["1","A"`, ErrShape},
	} {
		s, _ := newSource(map[string]string{u: tc.page})
		got, err := s.List(context.Background(), board("x"))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	// An honest zero is a valid empty term, not an error.
	s, _ := newSource(map[string]string{u: wrap(`[null, null, 0, 20]`)})
	if got, err := s.List(context.Background(), board("x")); err != nil || len(got) != 0 {
		t.Errorf("zero results: %d, %v", len(got), err)
	}
	// No search, unknown filter, wrong ATS.
	s, _ = newSource(nil)
	for name, b := range map[string]sources.Board{
		"no search":      board(),
		"unknown filter": func() sources.Board { b := board("x"); b.Filters = map[string][]string{"degree": {"X"}}; return b }(),
		"wrong ats":      {ATS: posting.Amazon, Token: "google", Search: "x"},
	} {
		if _, err := s.List(context.Background(), b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Google HTML-escapes text fields; titles, teams and locations come back plain.
func TestUnescapesText(t *testing.T) {
	u := searchURL("x", nil)
	row := strings.Replace(one("7", "Revenue Insights Analyst, Ads Finance P&amp;L"), `"Google","en-US"`, `"Research &amp; Ads","en-US"`, 1)
	row = strings.Replace(row, `[["Mountain View, CA, USA"]]`, `[["S&#227;o Paulo, Brazil"]]`, 1)
	s, _ := newSource(map[string]string{u: wrap(`[[` + row + `], null, 1, 20]`)})
	got, err := s.List(context.Background(), board("x"))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if p := got[0]; p.Title != "Revenue Insights Analyst, Ads Finance P&L" || p.Department != "Research & Ads" || p.Location != "São Paulo, Brazil" {
		t.Errorf("not unescaped: %q / %q / %q", p.Title, p.Department, p.Location)
	}
}

// one is a minimal well-formed result with id and title.
func one(id, title string) string {
	return fmt.Sprintf(`[%q,%q,null,[null,"<p>Do things.</p>"],[null,"<ul><li>BS</li></ul>"],null,null,"Google","en-US",[["Mountain View, CA, USA"]],[null,"About."]]`, id, title)
}

func TestStatusAndContext(t *testing.T) {
	u := searchURL("x", nil)
	d := srctest.New(map[string]srctest.Reply{u: {Status: 500}})
	s := &source{d: d, now: time.Now}
	if _, err := s.List(context.Background(), board("x")); err == nil {
		t.Fatal("HTTP 500 accepted")
	}
	d = srctest.New(map[string]srctest.Reply{u: {Status: 404}})
	s = &source{d: d, now: time.Now}
	if _, err := s.List(context.Background(), board("x")); !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Fatalf("404: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _ = newSource(map[string]string{u: wrap(fixture(t))})
	if _, err := s.List(ctx, board("x")); err == nil {
		t.Fatal("cancelled context accepted")
	}
}
