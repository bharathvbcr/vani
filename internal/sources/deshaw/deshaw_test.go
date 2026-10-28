package deshaw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

var fixedNow = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func board() sources.Board {
	return sources.Board{ATS: posting.DEShaw, Token: "deshaw", Company: "D. E. Shaw"}
}

// fixture is testdata/nextdata.json decoded: two regular jobs (Software
// Developer in New York, and the general application "All Positions in
// Systems") and one internship, all as deshaw.com served them on 2026-10-27.
func fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/nextdata.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func pageProps(m map[string]any) map[string]any {
	return m["props"].(map[string]any)["pageProps"].(map[string]any)
}

func entries(m map[string]any, key string) []any { return pageProps(m)[key].([]any) }

// cloneEntry deep-copies one fixture entry and gives it a new job id and the
// matching jobUrl.
func cloneEntry(t *testing.T, e any, id int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(e)
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c["id"] = id
	d := c["data"].(map[string]any)
	d["id"] = id
	d["jobUrl"] = fmt.Sprintf("Role-%d", id)
	return c
}

// dupEntry deep-copies one fixture entry unchanged.
func dupEntry(t *testing.T, e any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(e)
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func htmlPage(block string) []byte {
	return []byte(`<!DOCTYPE html><html><head><title>Careers</title></head><body><div id="__next">x</div>` +
		block + `<script src="/app.js"></script></body></html>`)
}

func wrap(t *testing.T, m map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return htmlPage(`<script id="__NEXT_DATA__" type="application/json">` + string(raw) + `</script>`)
}

func serve(body []byte, status int) *srctest.Doer {
	return srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: status, Body: body}, true
	})
}

func list(d egress.Doer, b sources.Board) ([]posting.Posting, error) {
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	return s.List(context.Background(), b)
}

func TestFixture(t *testing.T) {
	d := serve(wrap(t, fixture(t)), 200)
	got, err := list(d, board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3", len(got))
	}
	p := got[0]
	if p.Key != "ds:deshaw:2646" || p.ATS != posting.DEShaw || p.Company != "D. E. Shaw" ||
		p.Title != "Software Developer" || p.Location != "New York" || p.Department != "Quantitative Strategies" ||
		p.URL != "https://www.deshaw.com/careers/Software-Developer-2646" || p.ApplyURL != "" ||
		!p.PostedAt.IsZero() || !p.SeenAt.Equal(fixedNow) || p.Remote {
		t.Errorf("posting: %+v", p)
	}
	for _, want := range []string{"Role: full-time\n\n", "Description", "D. E. Shaw group", "Responsibilities",
		"Specific responsibilities may include", "People we are looking for"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q: %.500s", want, p.Description)
		}
	}
	if strings.Contains(p.Description, "<p>") || strings.Contains(p.Description, "&nbsp;") {
		t.Errorf("markup kept: %.300s", p.Description)
	}
	if n := strings.Count(p.Description, "Specific responsibilities may include"); n != 1 {
		t.Errorf("responsibilities text repeated %d times (the plain and HTML forms are one section)", n)
	}
	// The exploratory entry is a general application, and says so.
	if g := got[1]; g.Key != "ds:deshaw:3480" || !strings.HasPrefix(g.Description, "Role: full-time, general application, no specific role\n\n") {
		t.Errorf("exploratory: %q %.200s", g.Key, g.Description)
	}
	// The internship comes from the second list, and is an internship.
	if i := got[2]; i.Key != "ds:deshaw:5731" || i.Title != "Proprietary Trading Intern (New York) – Summer 2027" ||
		!strings.HasPrefix(i.Description, "Role: internship\n\n") {
		t.Errorf("internship: %q %q %.120s", i.Key, i.Title, i.Description)
	}
	reqs := d.Requests()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].URL != "https://www.deshaw.com/careers" {
		t.Errorf("requests: %+v", reqs)
	}
}

// A page whose data block is missing, renamed, retyped, empty or cut off is an
// error, never an empty listing: the reconciler would age out every posting.
func TestBrokenPageIsAnErrorNotAnEmptyList(t *testing.T) {
	good := fixture(t)
	raw, _ := json.Marshal(good)
	block := func(open, body, closeTag string) []byte { return htmlPage(open + body + closeTag) }
	const open = `<script id="__NEXT_DATA__" type="application/json">`
	mutate := func(f func(pp map[string]any)) []byte {
		m := fixture(t)
		f(pageProps(m))
		return wrap(t, m)
	}
	big := strings.Repeat("x", maxJSON+1)
	for name, tc := range map[string]struct {
		page []byte
		want error
	}{
		"no script at all":     {htmlPage(""), ErrShape},
		"empty page":           {[]byte{}, ErrShape},
		"renamed id":           {block(`<script id="__NEXT_DATA_V2__" type="application/json">`, string(raw), `</script>`), ErrShape},
		"id lower case":        {block(`<script id="__next_data__" type="application/json">`, string(raw), `</script>`), ErrShape},
		"retyped":              {block(`<script id="__NEXT_DATA__" type="text/plain">`, string(raw), `</script>`), ErrShape},
		"empty block":          {block(open, "", `</script>`), ErrShape},
		"blank block":          {block(open, " \n\t ", `</script>`), ErrShape},
		"unterminated":         {[]byte("<html><body>" + open + string(raw)), ErrShape},
		"two blocks":           {block(open+string(raw)+`</script>`+open, string(raw), `</script>`), ErrShape},
		"not JSON":             {block(open, `{"props":`, `</script>`), ErrShape},
		"JSON array":           {block(open, `[]`, `</script>`), ErrShape},
		"null":                 {block(open, `null`, `</script>`), ErrShape},
		"no props":             {block(open, `{}`, `</script>`), ErrShape},
		"oversized block":      {block(open, big, `</script>`), srcutil.ErrBodyTooLarge},
		"oversized page":       {htmlPage(strings.Repeat("y", maxPage+1)), srcutil.ErrBodyTooLarge},
		"regularJobs missing":  {mutate(func(pp map[string]any) { delete(pp, "regularJobs") }), ErrShape},
		"regularJobs renamed":  {mutate(func(pp map[string]any) { pp["regularPositions"] = pp["regularJobs"]; delete(pp, "regularJobs") }), ErrShape},
		"regularJobs null":     {mutate(func(pp map[string]any) { pp["regularJobs"] = nil }), ErrShape},
		"regularJobs empty":    {mutate(func(pp map[string]any) { pp["regularJobs"] = []any{} }), ErrShape},
		"internships missing":  {mutate(func(pp map[string]any) { delete(pp, "internships") }), ErrShape},
		"fetching error":       {mutate(func(pp map[string]any) { pp["jobsFetchingError"] = true }), ErrShape},
		"regularJobs a string": {mutate(func(pp map[string]any) { pp["regularJobs"] = "none" }), ErrShape},
		"entries are not jobs": {mutate(func(pp map[string]any) { pp["regularJobs"] = []any{"a"} }), ErrShape},
		"every job inactive":   {mutate(func(pp map[string]any) { setInactive(pp) }), ErrShape},
		"only internal jobs": {mutate(func(pp map[string]any) {
			pp["internalJobs"] = pp["regularJobs"]
			pp["regularJobs"] = []any{}
			pp["internships"] = []any{}
		}), ErrShape},
		"runs into next script": {htmlPage(open + string(raw)), ErrShape},
	} {
		got, err := list(serve(tc.page, 200), board())
		if err == nil || !errors.Is(err, tc.want) || got != nil {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	// Control: the unmodified page lists.
	if got, err := list(serve(wrap(t, fixture(t)), 200), board()); err != nil || len(got) != 3 {
		t.Fatalf("control: %d %v", len(got), err)
	}
}

func setInactive(pp map[string]any) {
	for _, k := range []string{"regularJobs", "internships"} {
		for _, e := range pp[k].([]any) {
			e.(map[string]any)["data"].(map[string]any)["activeOnJobsListing"] = false
		}
	}
}

// internalJobs is not read: a page with internal jobs lists the same postings.
func TestInternalJobsAreNotListed(t *testing.T) {
	m := fixture(t)
	pageProps(m)["internalJobs"] = []any{cloneEntry(t, entries(m, "regularJobs")[0], 9001)}
	got, err := list(serve(wrap(t, m), 200), board())
	if err != nil || len(got) != 3 {
		t.Fatalf("%d %v", len(got), err)
	}
	for _, p := range got {
		if p.Key == "ds:deshaw:9001" {
			t.Error("an internal job was listed")
		}
	}
}

func TestInactiveSkippedAndDuplicates(t *testing.T) {
	// An entry the page itself marks not active is not a posting.
	m := fixture(t)
	entries(m, "regularJobs")[1].(map[string]any)["data"].(map[string]any)["activeOnJobsListing"] = false
	got, err := list(serve(wrap(t, m), 200), board())
	if err != nil || len(got) != 2 {
		t.Fatalf("inactive: %d %v", len(got), err)
	}
	for _, p := range got {
		if p.Key == "ds:deshaw:3480" {
			t.Error("inactive entry listed")
		}
	}
	// The same job twice, identical, is one posting (the same entry in both lists).
	m = fixture(t)
	pageProps(m)["internships"] = append(entries(m, "internships"), dupEntry(t, entries(m, "internships")[0]))
	got, err = list(serve(wrap(t, m), 200), board())
	if err != nil || len(got) != 3 {
		t.Fatalf("identical duplicate: %d %v", len(got), err)
	}
	// The same id with different content is a broken page, not a choice.
	m = fixture(t)
	other := cloneEntry(t, entries(m, "internships")[0], 5731)
	other["data"].(map[string]any)["displayName"] = "A different role"
	pageProps(m)["internships"] = append(entries(m, "internships"), other)
	if _, err := list(serve(wrap(t, m), 200), board()); !errors.Is(err, ErrShape) {
		t.Errorf("conflicting duplicate: %v", err)
	}
}

func TestBadEntriesFailTheBoard(t *testing.T) {
	for name, edit := range map[string]func(e map[string]any){
		"zero id":               func(e map[string]any) { e["id"] = 0; e["data"].(map[string]any)["id"] = 0 },
		"negative id":           func(e map[string]any) { e["id"] = -5; e["data"].(map[string]any)["id"] = -5 },
		"missing id":            func(e map[string]any) { delete(e, "id"); delete(e["data"].(map[string]any), "id") },
		"top and data id split": func(e map[string]any) { e["id"] = 7 },
		"jobUrl names another":  func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "Software-Developer-2647" },
		"jobUrl without id":     func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "Software-Developer" },
		"jobUrl climbs":         func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "../admin-2646" },
		"jobUrl is a path":      func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "x/y-2646" },
		"jobUrl is a host":      func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "//evil.example/x-2646" },
		"jobUrl has a query":    func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "x-2646?a=b" },
		"jobUrl empty":          func(e map[string]any) { e["data"].(map[string]any)["jobUrl"] = "" },
		"empty title":           func(e map[string]any) { e["data"].(map[string]any)["displayName"] = "   " },
		"id is a string":        func(e map[string]any) { e["data"].(map[string]any)["id"] = "2646" },
	} {
		m := fixture(t)
		edit(entries(m, "regularJobs")[0].(map[string]any))
		got, err := list(serve(wrap(t, m), 200), board())
		if err == nil {
			t.Errorf("%s: accepted, %d postings", name, len(got))
		}
	}
}

// A job the page lists without optional parts still lists.
func TestSparseEntry(t *testing.T) {
	m := fixture(t)
	e := cloneEntry(t, entries(m, "regularJobs")[0], 77)
	d := e["data"].(map[string]any)
	delete(d, "department")
	delete(d, "jobDescription")
	delete(d, "activeOnJobsListing")
	d["jobMetadata"] = map[string]any{"jobLocations": []any{
		map[string]any{"name": "New York"}, map[string]any{"name": "Denver"}, map[string]any{"name": " New  York "}, map[string]any{"name": ""}}}
	pageProps(m)["regularJobs"] = []any{e}
	pageProps(m)["internships"] = []any{}
	got, err := list(serve(wrap(t, m), 200), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("%d %v", len(got), err)
	}
	if p := got[0]; p.Location != "New York; Denver" || p.Department != "" || p.Description != "" {
		t.Errorf("%+v", p)
	}
}

func TestCountCap(t *testing.T) {
	m := fixture(t)
	base := entries(m, "regularJobs")[0]
	var many []any
	for i := 1; i <= MaxPostings; i++ {
		many = append(many, cloneEntry(t, base, i))
	}
	pageProps(m)["regularJobs"] = many
	pageProps(m)["internships"] = []any{}
	if got, err := list(serve(wrap(t, m), 200), board()); err != nil || len(got) != MaxPostings {
		t.Fatalf("at the cap: %d %v", len(got), err)
	}
	pageProps(m)["regularJobs"] = append(many, cloneEntry(t, base, MaxPostings+1))
	if got, err := list(serve(wrap(t, m), 200), board()); !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Errorf("over the cap: %d %v", len(got), err)
	}
}

func TestDependencyDown(t *testing.T) {
	ok := wrap(t, fixture(t))
	for name, tc := range map[string]struct {
		d    egress.Doer
		want error
	}{
		"404":           {serve([]byte("not found"), 404), srcutil.ErrBoardNotFound},
		"500":           {serve([]byte("boom"), 500), nil},
		"403":           {serve([]byte("denied"), 403), nil},
		"302":           {serve(ok, 302), nil},
		"empty body":    {serve(nil, 200), ErrShape},
		"json not html": {serve([]byte(`{"jobs":[]}`), 200), ErrShape},
		"transport":     {srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false }), nil},
	} {
		got, err := list(tc.d, board())
		if err == nil || got != nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &source{d: serve(ok, 200), now: func() time.Time { return fixedNow }}
	if _, err := s.List(ctx, board()); err == nil {
		t.Error("cancelled context accepted")
	}
}

func TestBoardRefusals(t *testing.T) {
	d := serve(wrap(t, fixture(t)), 200)
	for name, b := range map[string]sources.Board{
		"wrong ats":   {ATS: posting.Lever, Token: "deshaw"},
		"empty token": {ATS: posting.DEShaw, Token: ""},
		"colon token": {ATS: posting.DEShaw, Token: "de:shaw"},
		"search":      {ATS: posting.DEShaw, Token: "deshaw", Search: "quant"},
		"searches":    {ATS: posting.DEShaw, Token: "deshaw", Searches: []string{"quant"}},
		"filters":     {ATS: posting.DEShaw, Token: "deshaw", Filters: map[string][]string{"location": {"New York"}}},
		"board name":  {ATS: posting.DEShaw, Token: "deshaw", BoardName: "x"},
	} {
		if got, err := list(d, b); err == nil {
			t.Errorf("%s: accepted, %d postings", name, len(got))
		}
	}
	if n := len(d.Requests()); n != 0 {
		t.Errorf("a refused board sent %d requests", n)
	}
	// A token that is odd but legal never reaches the URL: the host and path
	// are fixed.
	for _, tok := range []string{"..", "a/b", "a b", "x?y#z"} {
		b := board()
		b.Token = tok
		_, _ = list(d, b)
	}
	for _, r := range d.Requests() {
		if r.URL != "https://www.deshaw.com/careers" {
			t.Errorf("token reached the request: %s", r.URL)
		}
	}
}

func TestATSAndNew(t *testing.T) {
	s := New(serve(nil, 200))
	if s.ATS() != posting.DEShaw {
		t.Errorf("ATS %q", s.ATS())
	}
}
