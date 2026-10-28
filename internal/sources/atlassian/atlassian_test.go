package atlassian

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
	return sources.Board{ATS: posting.Atlassian, Token: "atlassian", Company: "Atlassian"}
}

// fixture is testdata/listings.json: two rows as atlassian.com served them on
// 2026-10-27, 26954 (Seattle and remote, no employment type) and 26762
// (Full-Time, six locations).
func fixture(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/listings.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// clone deep-copies a row.
func clone(t *testing.T, row map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(row)
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// withID is a copy of row for job id, its portal and apply URLs following.
func withID(t *testing.T, row map[string]any, id int) map[string]any {
	t.Helper()
	c := clone(t, row)
	c["id"] = id
	pj := c["portalJobPost"].(map[string]any)
	pj["id"] = id
	pj["portalUrl"] = fmt.Sprintf("https://careers-americas.icims.com/jobs/%d/role/job", id)
	c["applyUrl"] = fmt.Sprintf("https://careers-americas.icims.com/jobs/%d/role/job?mode=apply", id)
	return c
}

func body(t *testing.T, rows any) []byte {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func serve(b []byte, status int) *srctest.Doer {
	return srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
		return srctest.Reply{Status: status, Body: b}, true
	})
}

func list(d egress.Doer, b sources.Board) ([]posting.Posting, error) {
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	return s.List(context.Background(), b)
}

func TestFixture(t *testing.T) {
	d := serve(body(t, fixture(t)), 200)
	got, err := list(d, board())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2", len(got))
	}
	p := got[0]
	if p.Key != "at:atlassian:26954" || p.ATS != posting.Atlassian || p.Company != "Atlassian" ||
		p.Title != "Senior Machine Learning Engineer" || p.Department != "Engineering" ||
		p.Location != "Seattle, Washington; Remote" || !p.Remote ||
		p.URL != "https://careers-americas.icims.com/jobs/26954/senior-machine-learning-engineer/job" ||
		p.ApplyURL != "https://careers-americas.icims.com/jobs/26954/senior-machine-learning-engineer/job?mode=apply" ||
		!p.PostedAt.IsZero() || !p.UpdatedAt.IsZero() || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("posting: %+v", p)
	}
	// 26954 has no employment type, so no role line; 26762 is full-time.
	if strings.HasPrefix(p.Description, "Role:") || !strings.HasPrefix(p.Description, "Overview\n") {
		t.Errorf("description opens %.80q", p.Description)
	}
	for _, want := range []string{"Overview", "Responsibilities", "Qualifications"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("description lacks %q", want)
		}
	}
	if strings.Contains(p.Description, "<p>") || strings.Contains(p.Description, "&amp;") {
		t.Errorf("markup kept: %.300s", p.Description)
	}
	q := got[1]
	wantLoc := "Remote, Americas; Remote; San Francisco, California; Austin, Texas; Mountain View, California; New York, New York"
	if q.Key != "at:atlassian:26762" || q.Location != wantLoc || !q.Remote || !strings.HasPrefix(q.Description, "Role: full-time\n\nOverview") {
		t.Errorf("26762: %q %q %.80q", q.Key, q.Location, q.Description)
	}
	reqs := d.Requests()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].URL != "https://www.atlassian.com/endpoint/careers/listings" {
		t.Errorf("requests: %+v", reqs)
	}
}

func TestDuplicates(t *testing.T) {
	rows := fixture(t)
	// A job listed three times, identically, is one posting.
	rows = append(rows, clone(t, rows[0]), clone(t, rows[0]), clone(t, rows[1]))
	got, err := list(serve(body(t, rows), 200), board())
	if err != nil || len(got) != 2 {
		t.Fatalf("identical repeats: %d %v", len(got), err)
	}
	// Two rows that disagree about one job are a broken listing, not a choice.
	rows = fixture(t)
	other := clone(t, rows[0])
	other["title"] = "A different title"
	rows = append(rows, other)
	if got, err := list(serve(body(t, rows), 200), board()); !errors.Is(err, ErrShape) || got != nil {
		t.Errorf("conflicting repeat: %d %v", len(got), err)
	}
}

// An empty, null or non-array answer is an error, not a board with no jobs:
// the reconciler would age out every live posting.
func TestEmptyOrWrongShapeIsAnError(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"empty array":          {`[]`, ErrShape},
		"null":                 {`null`, ErrShape},
		"object":               {`{"jobs":[]}`, nil},
		"string":               {`"none"`, nil},
		"truncated":            {`[{"id":1,"title":`, nil},
		"html":                 {`<html>blocked</html>`, nil},
		"empty body":           {``, nil},
		"rows are not objects": {`[1,2]`, nil},
	} {
		got, err := list(serve([]byte(tc.body), 200), board())
		if err == nil || got != nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
}

func TestBadRowsFailTheBoard(t *testing.T) {
	const ok = "https://careers-americas.icims.com/jobs/26954/senior-machine-learning-engineer/job"
	set := func(k string, v any) func(r map[string]any) { return func(r map[string]any) { r[k] = v } }
	portal := func(v any) func(r map[string]any) {
		return func(r map[string]any) { r["portalJobPost"].(map[string]any)["portalUrl"] = v }
	}
	for name, edit := range map[string]func(r map[string]any){
		"no id":                func(r map[string]any) { delete(r, "id") },
		"null id":              set("id", nil),
		"zero id":              set("id", 0),
		"negative id":          set("id", -1),
		"string id":            set("id", "26954"),
		"fractional id":        set("id", 26954.5),
		"portal id differs":    func(r map[string]any) { r["portalJobPost"].(map[string]any)["id"] = 1 },
		"empty title":          set("title", "  "),
		"no portal url":        portal(""),
		"portal url missing":   func(r map[string]any) { delete(r["portalJobPost"].(map[string]any), "portalUrl") },
		"portal not a string":  portal(5),
		"apply not a string":   set("applyUrl", 5),
		"http portal":          portal(strings.Replace(ok, "https", "http", 1)),
		"offsite portal":       portal("https://evil.example/jobs/26954/x/job"),
		"look-alike host":      portal("https://careers-americas.icims.com.evil.example/jobs/26954/x/job"),
		"userinfo":             portal("https://icims.com@evil.example/jobs/26954/x/job"),
		"port":                 portal("https://careers-americas.icims.com:8443/jobs/26954/x/job"),
		"portal names another": portal("https://careers-americas.icims.com/jobs/26955/x/job"),
		"portal query":         portal(ok + "?x=1"),
		"portal fragment":      portal(ok + "#x"),
		"portal other path":    portal("https://careers-americas.icims.com/login"),
		"portal climbs":        portal("https://careers-americas.icims.com/jobs/26954/../../admin/job"),
		"portal javascript":    portal("javascript:alert(1)"),
		"portal relative":      portal("/jobs/26954/x/job"),
		"portal has control":   portal("https://careers-americas.icims.com/jobs/26954/x\n/job"),
		"portal job id alpha":  portal("https://careers-americas.icims.com/jobs/abc/x/job"),
		"portal slug too long": portal("https://careers-americas.icims.com/jobs/26954/" + strings.Repeat("a", 301) + "/job"),
	} {
		rows := fixture(t)
		edit(rows[0])
		got, err := list(serve(body(t, rows), 200), board())
		if err == nil || got != nil {
			t.Errorf("%s: accepted, %d postings", name, len(got))
		}
	}
}

// The apply link is kept only when it is the portal page with ?mode=apply.
func TestApplyURLIsCheckedNotFollowed(t *testing.T) {
	const portal = "https://careers-americas.icims.com/jobs/26954/senior-machine-learning-engineer/job"
	for name, tc := range map[string]struct {
		apply any
		want  string
	}{
		"good":        {portal + "?mode=apply", portal + "?mode=apply"},
		"missing":     {nil, ""},
		"empty":       {"", ""},
		"javascript":  {"javascript:alert(1)", ""},
		"other host":  {"https://evil.example/jobs/26954/senior-machine-learning-engineer/job?mode=apply", ""},
		"other path":  {"https://careers-americas.icims.com/jobs/1/x/job?mode=apply", ""},
		"other query": {portal + "?mode=apply&x=1", ""},
		"no query":    {portal, ""},
		"http":        {strings.Replace(portal, "https", "http", 1) + "?mode=apply", ""},
		"userinfo":    {"https://x@careers-americas.icims.com/jobs/26954/senior-machine-learning-engineer/job?mode=apply", ""},
		"fragment":    {portal + "?mode=apply#x", ""},
	} {
		rows := fixture(t)
		rows[0]["applyUrl"] = tc.apply
		got, err := list(serve(body(t, rows), 200), board())
		if err != nil || len(got) != 2 {
			t.Errorf("%s: %d %v (a bad apply link does not fail the posting)", name, len(got), err)
			continue
		}
		if got[0].ApplyURL != tc.want {
			t.Errorf("%s: apply %q, want %q", name, got[0].ApplyURL, tc.want)
		}
	}
}

func TestSparseRow(t *testing.T) {
	rows := fixture(t)
	r := withID(t, rows[0], 5)
	for _, k := range []string{"locations", "overview", "responsibilities", "qualifications", "category", "type", "applyUrl"} {
		delete(r, k)
	}
	delete(r["portalJobPost"].(map[string]any), "id")
	got, err := list(serve(body(t, []any{r}), 200), board())
	if err != nil || len(got) != 1 {
		t.Fatalf("%d %v", len(got), err)
	}
	if p := got[0]; p.Location != "" || p.Remote || p.Department != "" || p.Description != "" || p.ApplyURL != "" {
		t.Errorf("%+v", p)
	}
}

func TestCountCap(t *testing.T) {
	rows := fixture(t)
	small := clone(t, rows[0])
	for _, k := range []string{"overview", "responsibilities", "qualifications", "locations"} {
		delete(small, k)
	}
	var many []any
	for i := 1; i <= MaxPostings; i++ {
		many = append(many, withID(t, small, i))
	}
	if got, err := list(serve(body(t, many), 200), board()); err != nil || len(got) != MaxPostings {
		t.Fatalf("at the cap: %d %v", len(got), err)
	}
	many = append(many, withID(t, small, MaxPostings+1))
	if got, err := list(serve(body(t, many), 200), board()); !errors.Is(err, srcutil.ErrTooManyPostings) || got != nil {
		t.Errorf("over the cap: %d %v", len(got), err)
	}
}

func TestDependencyDown(t *testing.T) {
	ok := body(t, fixture(t))
	for name, tc := range map[string]struct {
		d    egress.Doer
		want error
	}{
		"404":       {serve([]byte("not found"), 404), srcutil.ErrBoardNotFound},
		"500":       {serve([]byte("boom"), 500), nil},
		"403":       {serve([]byte("denied"), 403), nil},
		"302":       {serve(ok, 302), nil},
		"transport": {srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false }), nil},
		"oversized": {serve(append([]byte(`[`), []byte(strings.Repeat(" ", maxBody+1))...), 200), srcutil.ErrBodyTooLarge},
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
	d := serve(body(t, fixture(t)), 200)
	for name, b := range map[string]sources.Board{
		"wrong ats":   {ATS: posting.Lever, Token: "atlassian"},
		"empty token": {ATS: posting.Atlassian, Token: ""},
		"colon token": {ATS: posting.Atlassian, Token: "at:lassian"},
		"search":      {ATS: posting.Atlassian, Token: "atlassian", Search: "engineer"},
		"searches":    {ATS: posting.Atlassian, Token: "atlassian", Searches: []string{"engineer"}},
		"filters":     {ATS: posting.Atlassian, Token: "atlassian", Filters: map[string][]string{"category": {"Engineering"}}},
		"board name":  {ATS: posting.Atlassian, Token: "atlassian", BoardName: "x"},
	} {
		if got, err := list(d, b); err == nil {
			t.Errorf("%s: accepted, %d postings", name, len(got))
		}
	}
	if n := len(d.Requests()); n != 0 {
		t.Errorf("a refused board sent %d requests", n)
	}
	// Odd but legal tokens never reach the URL: the host and path are fixed.
	for _, tok := range []string{"..", "a/b", "a b", "x?y#z"} {
		b := board()
		b.Token = tok
		_, _ = list(d, b)
	}
	for _, r := range d.Requests() {
		if r.URL != "https://www.atlassian.com/endpoint/careers/listings" {
			t.Errorf("token reached the request: %s", r.URL)
		}
	}
}

func TestLocationAndRemote(t *testing.T) {
	for _, tc := range []struct {
		in     []string
		want   string
		remote bool
	}{
		{nil, "", false},
		{[]string{}, "", false},
		{[]string{"", "  "}, "", false},
		{[]string{"Austin - United States -   Austin, Texas 78702 United States"}, "Austin, Texas", false},
		{[]string{"Seattle - United States -   Seattle, Washington  United States"}, "Seattle, Washington", false},
		{[]string{"Washington DC - United States -   Washington DC, Washington, DC  United States"}, "Washington DC, Washington, DC", false},
		{[]string{"Mountain View - United States or Remote -   Mountain View, California 94041 United States"}, "Mountain View, California", true},
		{[]string{"Sydney - Australia -   Sydney,  2000 Australia"}, "Sydney, Australia", false},
		{[]string{"Seoul - South Korea -     Korea, South"}, "Seoul, South Korea", false},
		{[]string{"Singapore - Singapore -   Central Singapore  Singapore"}, "Singapore", false},
		{[]string{"Canada -     Canada"}, "Canada", false},
		{[]string{"Remote - Remote"}, "Remote", true},
		{[]string{"Remote - India - Remote"}, "Remote, India", true},
		{[]string{"Remote - France -     France"}, "Remote, France", true},
		{[]string{"Manila  - Remote"}, "Manila, Remote", true},
		{[]string{"Remote - Remote", "Remote - remote", "Remote - Remote"}, "Remote", true},
		{[]string{"Austin - United States -   Austin, Texas 78702 United States", "Austin - United States -   Austin, Texas 78702 United States"}, "Austin, Texas", false},
		// A US address that is only a ZIP and country falls back to the fields.
		{[]string{"X - United States -   78702 United States"}, "X, United States", false},
		// "Remote" inside a longer word is not remote.
		{[]string{"Remotely - Elsewhere"}, "Remotely, Elsewhere", false},
	} {
		if got := location(tc.in); got != tc.want {
			t.Errorf("location(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := remote(tc.in); got != tc.remote {
			t.Errorf("remote(%q) = %v, want %v", tc.in, got, tc.remote)
		}
	}
}

func TestATSAndNew(t *testing.T) {
	if s := New(serve(nil, 200)); s.ATS() != posting.Atlassian {
		t.Errorf("ATS %q", s.ATS())
	}
}
