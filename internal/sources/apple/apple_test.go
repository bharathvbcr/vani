package apple

import (
	"context"
	"encoding/json"
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

var fixedNow = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

func board(terms ...string) sources.Board {
	return sources.Board{ATS: posting.Apple, Token: "apple", Company: "Apple", Searches: terms}
}

// searchPage is a search page shaped like the live one: the count, then each
// row's link twice (title link and accordion link).
func searchPage(count string, rows ...[3]string) string {
	var b strings.Builder
	b.WriteString(`<html><body><p role="status" class="t-eyebrow-reduced" id="search-result-count">` + count + ` Result(s)</p><ul>`)
	for _, r := range rows {
		link := fmt.Sprintf(`/en-us/details/%s-%s/%s?team=MLAI`, r[0], r[1], r[2])
		fmt.Fprintf(&b, `<li><h3><a class="link-inline" href="%s">T</a></h3><a aria-label="x" href="%s">more</a></li>`, link, link)
	}
	return b.String() + `</ul></body></html>`
}

// detailsPage embeds jd the way jobs.apple.com does: an object serialised
// into a JSON string literal passed to JSON.parse.
func detailsPage(t *testing.T, jd map[string]any) string {
	t.Helper()
	doc, err := json.Marshal(map[string]any{"loaderData": map[string]any{"root": map[string]any{"locale": "en-us"}, "jobDetails": map[string]any{"jobsData": jd}}})
	if err != nil {
		t.Fatal(err)
	}
	lit, _ := json.Marshal(string(doc))
	return `<html><head><script>window.__staticRouterHydrationData = JSON.parse(` + string(lit) + `);</script></head><body>x</body></html>`
}

func fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/jobsdata.json")
	if err != nil {
		t.Fatal(err)
	}
	var jd map[string]any
	if err := json.Unmarshal(raw, &jd); err != nil {
		t.Fatal(err)
	}
	return jd
}

func with(jd map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range jd {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func detailURL(id, suffix, slug string) string {
	return "https://jobs.apple.com/en-us/details/" + id + "-" + suffix + "/" + slug
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
	jd := fixture(t)
	slug := jd["transformedPostingTitle"].(string)
	pages := map[string]string{
		pageURL("LLM inference", nil, 1):     searchPage("1", [3]string{"200677962", "3760", slug}),
		detailURL("200677962", "3760", slug): detailsPage(t, jd),
	}
	s, _ := newSource(pages)
	got, err := s.List(context.Background(), board("LLM inference"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d postings", len(got))
	}
	p := got[0]
	if p.Key != "ap:apple:200677962" || p.ATS != posting.Apple || p.Company != "Apple" ||
		p.Title != "Sr. Machine Learning Engineer, Foundation Models Inference - Cloud OS & Inference" ||
		p.Department != "Machine Learning and AI" || p.URL != detailURL("200677962", "3760", slug) ||
		!p.PostedAt.Equal(time.Date(2026, 8, 13, 22, 45, 49, 595000000, time.UTC)) || p.Remote ||
		!strings.Contains(p.Location, "Santa Clara, California, United States") {
		t.Errorf("posting: %+v", p)
	}
	for _, want := range []string{"Level: Machine Learning ICT4 to Machine Learning ICT5", "Minimum Qualifications", "LLM inf", "Pay & Benefits", "$"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q:\n%.600s", want, p.Description)
		}
	}
	if !strings.HasPrefix(p.Description, "Level: ") {
		t.Errorf("level must lead the description: %.80s", p.Description)
	}
}

// Rows across pages and terms: each job number read once, the listing ends
// when the count is reached, and a position two terms find is fetched once.
func TestPagingAndUnion(t *testing.T) {
	jd := fixture(t)
	pages := map[string]string{}
	var rows [][3]string
	for i := 0; i < 25; i++ {
		id := fmt.Sprint(300000000 + i)
		rows = append(rows, [3]string{id, "0001", "role-" + id})
		pages[detailURL(id, "0001", "role-"+id)] = detailsPage(t, with(jd, "positionId", id, "jobNumber", id+"-0001", "postingTitle", "Role "+id))
	}
	pages[pageURL("a", nil, 1)] = searchPage("25", rows[:20]...)
	pages[pageURL("a", nil, 2)] = searchPage("25", rows[20:]...)
	pages[pageURL("b", nil, 1)] = searchPage("2", rows[0], rows[24])
	s, d := newSource(pages)
	got, err := s.List(context.Background(), board("a", "b"))
	if err != nil || len(got) != 25 {
		t.Fatalf("got %d, err %v; want 25 distinct postings", len(got), err)
	}
	details := 0
	for _, u := range d.URLs() {
		if strings.Contains(u, "/details/") {
			details++
		}
	}
	if details != 25 {
		t.Errorf("%d details requests, want 25 (one per position)", details)
	}
}

func TestRefusals(t *testing.T) {
	jd := fixture(t)
	u1 := pageURL("x", nil, 1)
	row := [3]string{"200677962", "3760", "r"}
	durl := detailURL("200677962", "3760", "r")
	for name, tc := range map[string]struct {
		pages map[string]string
		want  error
	}{
		"600+ results":        {map[string]string{u1: searchPage("600+", row)}, srcutil.ErrTooManyPostings},
		"count over the cap":  {map[string]string{u1: searchPage("201", row)}, srcutil.ErrTooManyPostings},
		"no count":            {map[string]string{u1: `<html><body>Something went wrong</body></html>`}, ErrShape},
		"rows short of count": {map[string]string{u1: searchPage("2", row), pageURL("x", nil, 2): searchPage("2")}, ErrShape},
		"no hydration":        {map[string]string{u1: searchPage("1", row), durl: `<html>x</html>`}, ErrShape},
		"hydration not a string": {map[string]string{u1: searchPage("1", row),
			durl: `<script>window.__staticRouterHydrationData = JSON.parse({"loaderData":{}});</script>`}, ErrShape},
		"no jobsData": {map[string]string{u1: searchPage("1", row),
			durl: `<script>window.__staticRouterHydrationData = JSON.parse("{\"loaderData\":{}}");</script>`}, ErrShape},
		"position mismatch": {map[string]string{u1: searchPage("1", row), durl: detailsPage(t, with(jd, "positionId", "999"))}, ErrShape},
		"empty title":       {map[string]string{u1: searchPage("1", row), durl: detailsPage(t, with(jd, "postingTitle", " "))}, ErrShape},
	} {
		s, _ := newSource(tc.pages)
		got, err := s.List(context.Background(), board("x"))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	// Zero results is an honest empty term.
	s, _ := newSource(map[string]string{u1: searchPage("0")})
	if got, err := s.List(context.Background(), board("x")); err != nil || len(got) != 0 {
		t.Errorf("zero results: %d %v", len(got), err)
	}
	// A bad date fails the posting, so the board.
	s, _ = newSource(map[string]string{u1: searchPage("1", row), durl: detailsPage(t, with(jd, "postDateInGMT", "yesterday"))})
	if _, err := s.List(context.Background(), board("x")); err == nil {
		t.Error("bad postDateInGMT accepted")
	}
	// No search, unverified filter, cancelled context.
	s, _ = newSource(nil)
	if _, err := s.List(context.Background(), board()); err == nil {
		t.Error("no search accepted")
	}
	b := board("x")
	b.Filters = map[string][]string{"team": {"MLAI"}}
	if _, err := s.List(context.Background(), b); err == nil {
		t.Error("unverified team filter accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _ = newSource(map[string]string{u1: searchPage("1", row), durl: detailsPage(t, jd)})
	if _, err := s.List(ctx, board("x")); err == nil {
		t.Error("cancelled context accepted")
	}
}

func TestPageURL(t *testing.T) {
	got := pageURL(`"LLM inference"`, map[string][]string{"location": {"united-states-USA"}}, 1)
	want := "https://jobs.apple.com/en-us/search?location=united-states-USA&search=%22LLM+inference%22&sort=relevance"
	if got != want {
		t.Errorf("page 1: %s\nwant      %s", got, want)
	}
	if got := pageURL("x", nil, 3); !strings.HasSuffix(got, "&page=3&search=x&sort=relevance") && !strings.Contains(got, "page=3") {
		t.Errorf("page 3: %s", got)
	}
}

// An unquoted multi-word phrase is Apple's loose search ("GPU kernel" and
// "AI agent" match 600+ roles). List must ask for the quoted phrase once,
// including when the board already stored the quotes.
func TestListQuotesALoosePhrase(t *testing.T) {
	jd := fixture(t)
	slug := jd["transformedPostingTitle"].(string)
	const (
		quoted = "https://jobs.apple.com/en-us/search?location=united-states-USA&search=%22GPU+kernel%22&sort=relevance"
		loose  = "https://jobs.apple.com/en-us/search?location=united-states-USA&search=GPU+kernel&sort=relevance"
	)
	pages := map[string]string{
		quoted:                               searchPage("1", [3]string{"200677962", "3760", slug}),
		detailURL("200677962", "3760", slug): detailsPage(t, jd),
	}
	for _, term := range []string{"GPU kernel", `"GPU kernel"`, "  GPU kernel  "} {
		s, d := newSource(pages)
		b := board(term)
		b.Filters = map[string][]string{"location": {"united-states-USA"}}
		got, err := s.List(context.Background(), b)
		if err != nil || len(got) != 1 || got[0].Key != "ap:apple:200677962" {
			t.Errorf("%q: got %d err %v", term, len(got), err)
		}
		if urls := d.URLs(); len(urls) == 0 || urls[0] != quoted {
			t.Errorf("%q: first request %v, want %s (loose is %s)", term, urls, quoted, loose)
		}
	}
	if got := pageURL("CUDA", nil, 1); strings.Contains(got, "%22") {
		t.Errorf("a single word was quoted: %s", got)
	}
	if got := pageURL(`"CUDA"`, nil, 1); !strings.Contains(got, "search=%22CUDA%22") {
		t.Errorf("quotes already on a single word were dropped or doubled: %s", got)
	}
}

// Live "human factors" (2026-10-28) displays 30 results and repeats one job
// number on the next page, so the distinct job numbers stay at 29. That
// repeat is the whole gap. List keeps both roles.
func TestRepeatedJobOnTheNextPage(t *testing.T) {
	jd := fixture(t)
	mk := func(id, title string) string {
		return detailsPage(t, with(jd, "positionId", id, "jobNumber", id+"-0001", "postingTitle", title))
	}
	a := [3]string{"200000001", "0001", "role-a"}
	b := [3]string{"200000002", "0001", "role-b"}
	pages := map[string]string{
		pageURL("human factors", nil, 1): searchPage("3", a, b),
		pageURL("human factors", nil, 2): searchPage("3", b),
		detailURL(a[0], a[1], a[2]):      mk(a[0], "Role A"),
		detailURL(b[0], b[1], b[2]):      mk(b[0], "Role B"),
	}
	s, _ := newSource(pages)
	got, err := s.List(context.Background(), board("human factors"))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d postings, err %v; want both roles", len(got), err)
	}
}

// Live "MLX" (2026-10-28) counts a details link with no team suffix
// (/en-us/details/{position}/{slug}) beside the suffixed link for the same
// role. The count includes it. One position is fetched once, at the suffixed URL.
func TestUnsuffixedDetailLink(t *testing.T) {
	jd := fixture(t)
	const pos, bare = "200667449", "200650849"
	slugS, slugB := "senior-role", "bare-role"
	link := func(href string) string {
		return fmt.Sprintf(`<li><a href="%s">t</a><a href="%s">m</a></li>`, href, href)
	}
	suffixed := fmt.Sprintf(`/en-us/details/%s-3401/%s?team=MLAI`, pos, slugS)
	bareHref := fmt.Sprintf(`/en-us/details/%s/%s?team=MLAI`, bare, slugB)
	aliasHref := fmt.Sprintf(`/en-us/details/%s/%s?team=MLAI`, pos, slugS)
	page := func(rows string, n string) string {
		return `<html><body><p id="search-result-count">` + n + ` Result(s)</p><ul>` + rows + `</ul></body></html>`
	}
	s, d := newSource(map[string]string{
		pageURL("MLX", nil, 1):                                       page(link(suffixed)+link(bareHref), "2"),
		detailURL(pos, "3401", slugS):                                detailsPage(t, with(jd, "positionId", pos, "jobNumber", pos+"-3401", "postingTitle", "Suffixed")),
		"https://jobs.apple.com/en-us/details/" + bare + "/" + slugB: detailsPage(t, with(jd, "positionId", bare, "jobNumber", bare, "postingTitle", "Bare")),
	})
	got, err := s.List(context.Background(), board("MLX"))
	if err != nil || len(got) != 2 {
		t.Fatalf("two positions: got %d err %v", len(got), err)
	}
	s, d = newSource(map[string]string{
		pageURL("MLX", nil, 1):        page(link(suffixed)+link(aliasHref), "2"),
		detailURL(pos, "3401", slugS): detailsPage(t, with(jd, "positionId", pos, "jobNumber", pos+"-3401", "postingTitle", "Suffixed")),
	})
	got, err = s.List(context.Background(), board("MLX"))
	if err != nil || len(got) != 1 || got[0].Key != "ap:apple:"+pos {
		t.Fatalf("alias: got %d err %v", len(got), err)
	}
	for _, u := range d.URLs() {
		if strings.Contains(u, "/details/"+pos+"/") && !strings.Contains(u, pos+"-3401") {
			t.Errorf("fetched the unsuffixed alias %s", u)
		}
	}
}

func TestLevelLine(t *testing.T) {
	for _, tc := range [][3]string{
		{"", "", ""}, {"ICT4", "ICT4", "Level: ICT4"}, {"", "ICT5", "Level: ICT5"}, {"ICT3", "", "Level: ICT3"}, {"ICT3", "ICT4", "Level: ICT3 to ICT4"},
	} {
		if got := levelLine(tc[0], tc[1]); got != tc[2] {
			t.Errorf("levelLine(%q,%q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}
