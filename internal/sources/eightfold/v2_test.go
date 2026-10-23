package eightfold

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Netflix's Eightfold site answers 403 "PCSX is not enabled for this user." on
// every /api/pcsx call and serves the same positions through the older
// /api/apply/v2/jobs API (verified 2026-10-22). Microsoft and Qualcomm answer
// the reverse: v2 is 403 "Not authorized for PCSX". The fixtures are real
// responses trimmed to the fields this source reads, with count set to the rows
// kept.

const (
	nfHost   = "explore.jobs.netflix.net"
	nfDomain = "netflix.com"

	// nfRobots is the explore.jobs.netflix.net robots.txt as read 2026-10-22.
	nfRobots = "User-agent: *\nDisallow: /\nAllow: /$\nAllow: /careers\nAllow: /api/apply\nAllow: /api/pcsx\nAllow: /static/gen\n\nSitemap: https://explore.jobs.netflix.net/careers/sitemap_index.xml\n"

	pcsxOff = `{"message": "PCSX is not enabled for this user."}`
)

func nfBoard(terms ...string) sources.Board {
	return sources.Board{ATS: posting.Eightfold, Token: nfDomain, Company: "Netflix", Host: nfHost, Searches: terms}
}

// v2Site is a fake Netflix careers site.
type v2Site struct {
	t       *testing.T
	rows    []map[string]any // search rows, in listing order
	count   *int             // the reported count; default len(rows)
	details map[string]map[string]any
	// replies replace an answer by request path ("/api/pcsx/search", "/robots.txt",
	// "/api/apply/v2/jobs") or by "/api/apply/v2/jobs/{id}".
	replies map[string]srctest.Reply
	// pcsx answers /api/pcsx/search; nil is 403 pcsxOff.
	pcsx func(start int) srctest.Reply

	mu   sync.Mutex
	reqs []string // "path?start" per request, in order
}

func (f *v2Site) seen(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func ok(v any) srctest.Reply {
	raw, _ := json.Marshal(v)
	return srctest.Reply{Status: http.StatusOK, Body: raw}
}

func (f *v2Site) handle(r srctest.Request) (srctest.Reply, bool) {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != nfHost || r.Method != "GET" {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return srctest.Reply{}, false
	}
	q := u.Query()
	f.mu.Lock()
	f.reqs = append(f.reqs, u.Path+"?"+q.Get("start"))
	f.mu.Unlock()
	if rep, has := f.replies[u.Path]; has {
		return rep, true
	}
	start, _ := strconv.Atoi(q.Get("start"))
	switch {
	case u.Path == "/robots.txt":
		return srctest.Reply{Status: 200, Body: []byte(nfRobots)}, true
	case u.Path == "/api/pcsx/search":
		if f.pcsx != nil {
			return f.pcsx(start), true
		}
		return srctest.Reply{Status: http.StatusForbidden, Body: []byte(pcsxOff)}, true
	case strings.HasPrefix(u.Path, "/api/pcsx/"):
		return srctest.Reply{Status: http.StatusForbidden, Body: []byte(pcsxOff)}, true
	case u.Path == "/api/apply/v2/jobs":
		if q.Get("domain") != nfDomain {
			f.t.Errorf("domain %q", q.Get("domain"))
		}
		count := len(f.rows)
		if f.count != nil {
			count = *f.count
		}
		page := []map[string]any{}
		for i := start; i < len(f.rows) && i < start+10; i++ {
			page = append(page, f.rows[i])
		}
		return ok(map[string]any{"domain": nfDomain, "count": count, "positions": page, "branding": map[string]any{"junk": "x"}}), true
	case strings.HasPrefix(u.Path, "/api/apply/v2/jobs/"):
		if q.Get("domain") != nfDomain {
			f.t.Errorf("domain %q", q.Get("domain"))
		}
		id := strings.TrimPrefix(u.Path, "/api/apply/v2/jobs/")
		if rep, has := f.replies[u.Path]; has {
			return rep, true
		}
		d, has := f.details[id]
		if !has {
			return srctest.Reply{Status: 404, Body: []byte(`{"message": "Job with ID ` + id + ` not found"}`)}, true
		}
		return ok(d), true
	}
	f.t.Errorf("unexpected path %s", u.Path)
	return srctest.Reply{}, false
}

func (f *v2Site) source() *source {
	return &source{d: srctest.NewFunc(f.handle), now: func() time.Time { return fixedNow }, wait: noWait}
}

// fixtureV2 is the real two-position Netflix site.
func fixtureV2(t *testing.T) *v2Site {
	t.Helper()
	f := &v2Site{t: t, details: map[string]map[string]any{}}
	for _, p := range readJSON(t, "v2_search.json")["positions"].([]any) {
		f.rows = append(f.rows, p.(map[string]any))
	}
	f.details[numID(f.rows[0])] = readJSON(t, "v2_detail.json")
	f.details[numID(f.rows[1])] = readJSON(t, "v2_detail_remote.json")
	return f
}

func numID(m map[string]any) string { return string(m["id"].(json.Number)) }

// synthV2 is n positions cloned from the fixture, ids 800000000000+i.
func synthV2(t *testing.T, n int) *v2Site {
	t.Helper()
	fx := fixtureV2(t)
	f := &v2Site{t: t, details: map[string]map[string]any{}}
	for i := 0; i < n; i++ {
		id := fmt.Sprint(800000000000 + i)
		row := map[string]any{}
		for k, v := range fx.rows[0] {
			row[k] = v
		}
		row["id"], row["name"] = json.Number(id), "Role "+id
		row["canonicalPositionUrl"] = "https://" + nfHost + "/careers/job/" + id
		f.rows = append(f.rows, row)
		d := map[string]any{}
		for k, v := range fx.details[numID(fx.rows[0])] {
			d[k] = v
		}
		d["id"], d["name"] = json.Number(id), "Role "+id
		d["canonicalPositionUrl"] = "https://" + nfHost + "/careers/job/" + id
		f.details[id] = d
	}
	return f
}

// The failing case: every pcsx call is 403, so the board must be read through
// /api/apply/v2 and produce the same posting shape.
func TestV2FallbackFixture(t *testing.T) {
	f := fixtureV2(t)
	got, err := f.source().List(context.Background(), nfBoard("machine learning"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings", len(got))
	}
	p := got[0]
	if p.Key != "ef:netflix.com:790316208720" || p.ATS != posting.Eightfold || p.Company != "Netflix" ||
		p.Title != "Machine Learning Scientist 5 - Ads Demand Science" || p.Department != "Data & Insights" ||
		p.Location != "Remote, United States" || p.Remote ||
		p.URL != "https://explore.jobs.netflix.net/careers/job/790316208720" ||
		!p.PostedAt.Equal(time.Unix(1790553600, 0).UTC()) || !p.SeenAt.Equal(fixedNow) {
		t.Errorf("posting: %+v", p)
	}
	if !strings.HasPrefix(p.Description, "At Netflix, our mission is to entertain the world") || strings.Contains(p.Description, "<p>") ||
		strings.Contains(p.Description, "&#39;") {
		t.Errorf("description: %.200s", p.Description)
	}
	// remote_local on the search row is carried over: the detail says null.
	if q := got[1]; q.Key != "ef:netflix.com:790317580317" || !q.Remote || q.Location != "Los Gatos,California,United States of America" {
		t.Errorf("second posting: %+v", q)
	}
	// One pcsx probe, the robots.txt read, one v2 search, one v2 detail per
	// position; nothing else.
	want := []string{"/api/pcsx/search?0", "/robots.txt?", "/api/apply/v2/jobs?0",
		"/api/apply/v2/jobs/790316208720?", "/api/apply/v2/jobs/790317580317?"}
	if strings.Join(f.reqs, " ") != strings.Join(want, " ") {
		t.Errorf("requests %v, want %v", f.reqs, want)
	}
}

// Paging by start in tens, the union of terms, filters on every v2 search, and
// the pcsx probe once per List, not per term or page.
func TestV2PagingUnionAndFilters(t *testing.T) {
	f := synthV2(t, 26)
	// Term "a" lists the first 25; term "b" is served by the same rows, so
	// every id repeats and the union is still 26 postings.
	count := 26
	f.count = &count
	var filtered, searches int
	s := f.source()
	inner := s.d.(*srctest.Doer).Handler
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if u, _ := url.Parse(r.URL); u.Path == "/api/apply/v2/jobs" {
			searches++
			if u.Query().Get("location") == "United States" && u.Query().Get("num") == "10" {
				filtered++
			}
		}
		return inner(r)
	})
	b := nfBoard("a", "b")
	b.Filters = map[string][]string{"location": {"United States"}}
	got, err := s.List(context.Background(), b)
	if err != nil || len(got) != 26 {
		t.Fatalf("got %d, err %v; want 26", len(got), err)
	}
	if searches != 6 || filtered != 6 {
		t.Errorf("%d v2 searches, %d with the filter, want 6 and 6", searches, filtered)
	}
	if n := f.seen("/api/pcsx/search"); n != 1 {
		t.Errorf("%d pcsx searches, want the one probe", n)
	}
	if n := f.seen("/api/apply/v2/jobs/"); n != 26 {
		t.Errorf("%d v2 detail requests, want 26", n)
	}
}

// Microsoft and Qualcomm answer pcsx: a board that works must never touch v2
// or robots.txt.
func TestPCSXBoardNeverUsesV2(t *testing.T) {
	f := fixtureSite(t)
	var mu sync.Mutex
	var paths []string
	s, d := newSource(f)
	inner := d.Handler
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		mu.Lock()
		paths = append(paths, u.Path)
		mu.Unlock()
		return inner(r)
	})
	if _, err := s.List(context.Background(), board("LLM inference")); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/api/pcsx/") {
			t.Errorf("request to %s on a pcsx board", p)
		}
	}
}

// Both APIs refusing is reported with both errors, never as one of them.
func TestBothAPIs403(t *testing.T) {
	f := fixtureV2(t)
	f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": {Status: http.StatusForbidden, Body: []byte(`{"message": "Not authorized for PCSX"}`)}}
	_, err := f.source().List(context.Background(), nfBoard("x"))
	if err == nil {
		t.Fatal("board with both APIs forbidden succeeded")
	}
	for _, want := range []string{"pcsx", "v2", "403"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if n := strings.Count(err.Error(), "403"); n != 2 {
		t.Errorf("error %q names %d 403s, want both", err, n)
	}
	if f.seen("/api/apply/v2/jobs/") != 0 {
		t.Error("detail requests after the search failed")
	}
}

// v2 404 on the search under a pcsx 403 is reported with both, and is still
// recognisable as a board that is not there.
func TestV2Search404(t *testing.T) {
	f := fixtureV2(t)
	f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": {Status: 404, Body: []byte(`{"message": "x"}`)}}
	_, err := f.source().List(context.Background(), nfBoard("x"))
	if !errors.Is(err, srcutil.ErrBoardNotFound) || !strings.Contains(err.Error(), "403") {
		t.Errorf("err %v, want board-not-found and the pcsx 403", err)
	}
}

// A 403 after pcsx already answered is a real outage, not a reason to switch
// APIs mid-board: ids from two APIs are never mixed.
func TestPCSX403AfterFirstPageDoesNotSwitch(t *testing.T) {
	f := synthV2(t, 25)
	f.pcsx = func(start int) srctest.Reply {
		if start == 0 {
			var page []map[string]any
			for _, r := range f.rows[:10] {
				page = append(page, map[string]any{"id": r["id"], "name": r["name"]})
			}
			return ok(map[string]any{"status": 200, "data": map[string]any{"count": 25, "positions": page}})
		}
		return srctest.Reply{Status: http.StatusForbidden, Body: []byte(pcsxOff)}
	}
	_, err := f.source().List(context.Background(), nfBoard("x"))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err %v, want the pcsx 403", err)
	}
	if n := f.seen("/api/apply/"); n != 0 || f.seen("/robots.txt") != 0 {
		t.Errorf("switched to v2 mid-board (%d v2 requests)", n)
	}
}

// A 403 on a later term, after term 0 answered over pcsx, does not switch.
func TestPCSX403OnLaterTermDoesNotSwitch(t *testing.T) {
	f := synthV2(t, 2)
	f.pcsx = func(start int) srctest.Reply {
		return srctest.Reply{Status: http.StatusForbidden, Body: []byte(pcsxOff)}
	}
	// Term "a" answers over pcsx, term "b" is refused.
	s := f.source()
	inner := s.d.(*srctest.Doer).Handler
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if u, _ := url.Parse(r.URL); u.Path == "/api/pcsx/search" && u.Query().Get("query") == "a" {
			f.mu.Lock()
			f.reqs = append(f.reqs, u.Path+"?"+u.Query().Get("start"))
			f.mu.Unlock()
			return ok(map[string]any{"status": 200, "data": map[string]any{"count": 0, "positions": []any{}}}), true
		}
		return inner(r)
	})
	_, err := s.List(context.Background(), nfBoard("a", "b"))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err %v, want the pcsx 403 on term b", err)
	}
	if f.seen("/api/apply/") != 0 {
		t.Error("switched to v2 on a later term")
	}
}

// robots.txt is the permission to read /api/apply. A host that disallows it,
// or whose robots.txt cannot be read, is not read through v2.
func TestV2RequiresRobotsPermission(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"disallowed":        {Status: 200, Body: []byte("User-agent: *\nDisallow: /\nAllow: /careers\n")},
		"disallowed by api": {Status: 200, Body: []byte("User-agent: *\nDisallow: /api/\n")},
		"vani disallowed":   {Status: 200, Body: []byte("User-agent: vani\nDisallow: /\n\nUser-agent: *\nAllow: /\n")},
		"robots 500":        {Status: 500, Body: []byte("oops")},
		"oversized robots":  {Status: 200, Body: []byte(strings.Repeat("#", 600<<10))},
	} {
		f := fixtureV2(t)
		f.replies = map[string]srctest.Reply{"/robots.txt": rep}
		_, err := f.source().List(context.Background(), nfBoard("x"))
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "robots") {
			t.Errorf("%s: err %v, want the pcsx 403 and a robots refusal", name, err)
		}
		if n := f.seen("/api/apply/"); n != 0 {
			t.Errorf("%s: %d v2 requests despite robots.txt", name, n)
		}
	}
	// No robots.txt (404) and a more specific Allow are permission.
	for name, rep := range map[string]srctest.Reply{
		"no robots.txt":     {Status: 404, Body: []byte("nope")},
		"allow beats short": {Status: 200, Body: []byte("User-agent: *\nDisallow: /api\nAllow: /api/apply\n")},
		"vani allowed":      {Status: 200, Body: []byte("User-agent: vani\nAllow: /\n\nUser-agent: *\nDisallow: /\n")},
	} {
		f := fixtureV2(t)
		f.replies = map[string]srctest.Reply{"/robots.txt": rep}
		if got, err := f.source().List(context.Background(), nfBoard("x")); err != nil || len(got) != 2 {
			t.Errorf("%s: %d postings, err %v", name, len(got), err)
		}
	}
}

// Cancelling after the pcsx 403 stops before robots.txt and v2.
func TestCancelBetweenPCSX403AndV2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := fixtureV2(t)
	s := f.source()
	inner := s.d.(*srctest.Doer).Handler
	s.d = srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		rep, ok := inner(r)
		if strings.Contains(r.URL, "/api/pcsx/search") {
			cancel()
		}
		return rep, ok
	})
	_, err := s.List(ctx, nfBoard("x"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want context.Canceled", err)
	}
	if f.seen("/robots.txt") != 0 || f.seen("/api/apply/") != 0 {
		t.Errorf("requests after the cancel: %v", f.reqs)
	}
}

func TestV2Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixtureV2(t).source().List(ctx, nfBoard("x")); err == nil {
		t.Error("cancelled context accepted")
	}
}

// Zero results is an honest empty term, not an error.
func TestV2EmptyPositions(t *testing.T) {
	f := &v2Site{t: t}
	got, err := f.source().List(context.Background(), nfBoard("nothing"))
	if err != nil || len(got) != 0 {
		t.Errorf("zero results: %d %v", len(got), err)
	}
}

func TestV2Refusals(t *testing.T) {
	over := MaxPostings + 1
	short := 30
	for name, tc := range map[string]struct {
		site func() *v2Site
		want error
	}{
		"over the cap":   {func() *v2Site { f := synthV2(t, 12); f.count = &over; return f }, srcutil.ErrTooManyPostings},
		"short of count": {func() *v2Site { f := synthV2(t, 12); f.count = &short; return f }, ErrShape},
		"non-numeric id": {func() *v2Site {
			f := synthV2(t, 2)
			f.rows[1]["id"] = "x1y"
			return f
		}, ErrShape},
		"id with sign": {func() *v2Site {
			f := synthV2(t, 2)
			f.rows[0]["id"] = json.Number("-5")
			return f
		}, ErrShape},
		"details for other": {func() *v2Site {
			f := synthV2(t, 2)
			f.details["800000000001"]["id"] = json.Number("1")
			return f
		}, ErrShape},
		"offsite canonical url": {func() *v2Site {
			f := synthV2(t, 2)
			f.details["800000000000"]["canonicalPositionUrl"] = "https://evil.example/careers/job/800000000000"
			return f
		}, ErrShape},
		"offsite canonical url with query": {func() *v2Site {
			f := synthV2(t, 2)
			f.details["800000000000"]["canonicalPositionUrl"] = "https://evil.example/careers/job/800000000000?microsite=netflix.com"
			return f
		}, ErrShape},
		"other position canonical url": {func() *v2Site {
			f := synthV2(t, 2)
			f.details["800000000000"]["canonicalPositionUrl"] = "https://" + nfHost + "/careers/job/800000000001?microsite=netflix.com"
			return f
		}, ErrShape},
		"empty name": {func() *v2Site {
			f := synthV2(t, 2)
			f.details["800000000000"]["name"] = " "
			return f
		}, ErrShape},
		"malformed search": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": {Status: 200, Body: []byte("{not json")}}
			return f
		}, ErrShape},
		"html search": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": {Status: 200, Body: []byte("<html>blocked</html>")}}
			return f
		}, ErrShape},
		"null search": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": {Status: 200, Body: []byte("null")}}
			return f
		}, ErrShape},
		"no count": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": ok(map[string]any{"positions": []any{}})}
			return f
		}, ErrShape},
		"oversized search": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs": {Status: 200, Body: []byte(strings.Repeat("x", 5<<20))}}
			return f
		}, srcutil.ErrBodyTooLarge},
		"malformed detail": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs/800000000001": {Status: 200, Body: []byte("{not json")}}
			return f
		}, ErrShape},
		"oversized detail": {func() *v2Site {
			f := synthV2(t, 2)
			f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs/800000000001": {Status: 200, Body: []byte(strings.Repeat("x", 3<<20))}}
			return f
		}, srcutil.ErrBodyTooLarge},
	} {
		got, err := tc.site().source().List(context.Background(), nfBoard("x"))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %d postings, err %v; want %v", name, len(got), err, tc.want)
		}
	}
}

// A search that never adds a new id stops at once; one that keeps adding
// stops at the page bound.
func TestV2PagingIsBounded(t *testing.T) {
	// Ten positions against a count of forty: the second page is empty.
	f := synthV2(t, 10)
	count := 40
	f.count = &count
	_, err := f.source().List(context.Background(), nfBoard("x"))
	if !errors.Is(err, ErrShape) || !strings.Contains(err.Error(), "added nothing") {
		t.Errorf("short page: err %v, want ErrShape: added nothing", err)
	}
	if n := f.seen("/api/apply/v2/jobs?"); n != 2 {
		t.Errorf("%d searches, want 2", n)
	}
	// Each page repeats nine ids and adds one new one against a count that
	// takes more pages than the bound: the listing stops at maxPages.
	var searches int
	slow := &source{d: srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		u, _ := url.Parse(r.URL)
		switch u.Path {
		case "/api/pcsx/search":
			return srctest.Reply{Status: 403, Body: []byte(pcsxOff)}, true
		case "/robots.txt":
			return srctest.Reply{Status: 200, Body: []byte(nfRobots)}, true
		case "/api/apply/v2/jobs":
			searches++
			start, _ := strconv.Atoi(u.Query().Get("start"))
			var page []map[string]any
			for i := 0; i < 9; i++ {
				page = append(page, map[string]any{"id": json.Number(fmt.Sprint(900000000000 + i)), "name": "n"})
			}
			page = append(page, map[string]any{"id": json.Number(fmt.Sprint(900000001000 + start)), "name": "n"})
			return ok(map[string]any{"count": MaxPostings, "positions": page}), true
		}
		t.Errorf("unexpected request %s", u.Path)
		return srctest.Reply{}, false
	}), now: time.Now, wait: noWait}
	_, err = slow.List(context.Background(), nfBoard("x"))
	if !errors.Is(err, ErrShape) || !strings.Contains(err.Error(), "did not end within") {
		t.Errorf("slow listing: err %v, want ErrShape: did not end within", err)
	}
	if searches != maxPages {
		t.Errorf("%d searches, want the page bound %d", searches, maxPages)
	}
}

// A detail that answers 404 under a v2 search that answered is gone, bounded
// like the pcsx path.
func TestV2Detail404IsBoundedLikeWorkday(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		gone    []int
		want    int
		wantErr bool
	}{
		{"one 404 among twelve", 12, []int{3}, 11, false},
		{"two 404 among twelve exceed a tenth", 12, []int{3, 4}, 0, true},
		{"two 404 among twenty are within a tenth", 20, []int{0, 19}, 18, false},
		{"all 404", 3, []int{0, 1, 2}, 0, true},
	} {
		f := synthV2(t, tc.n)
		for _, i := range tc.gone {
			delete(f.details, fmt.Sprint(800000000000+i))
		}
		ps, err := f.source().List(context.Background(), nfBoard("x"))
		if !tc.wantErr {
			if err != nil || len(ps) != tc.want {
				t.Errorf("%s: got %d postings, err %v; want %d", tc.name, len(ps), err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: got %d postings, want an error", tc.name, len(ps))
		} else if errors.Is(err, srcutil.ErrBoardNotFound) || !strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: want a detail error, not board-not-found: %v", tc.name, err)
		}
	}
}

// The board report says which API read the board.
func TestStatsNameTheAPI(t *testing.T) {
	var got []Stats
	opt := &Options{Report: func(_ sources.Board, st Stats) { got = append(got, st) }}
	s := fixtureV2(t).source()
	s.opt = opt
	if _, err := s.List(context.Background(), nfBoard("x")); err != nil {
		t.Fatal(err)
	}
	m, _ := newSource(fixtureSite(t))
	m.opt = opt
	if _, err := m.List(context.Background(), board("LLM inference")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Stats{Listed: 2, V2: true}) || got[1] != (Stats{Listed: 2}) {
		t.Errorf("reports %+v, want v2 for Netflix and pcsx for Microsoft", got)
	}
}

// remote_local is remote, onsite and hybrid are not; pcsx's "remote" stands.
func TestIsRemote(t *testing.T) {
	for opt, want := range map[string]bool{"remote": true, "Remote": true, " remote_local ": true, "remote_any": true,
		"onsite": false, "hybrid": false, "": false, "remotely": false, "not_remote": false} {
		if got := isRemote(opt); got != want {
			t.Errorf("%q: %v, want %v", opt, got, want)
		}
	}
}

// A detail that could not be read is not a gone posting.
func TestV2DetailDependencyDownIsNotGone(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"410": {Status: http.StatusGone, Body: []byte("{}")},
		"403": {Status: http.StatusForbidden, Body: []byte("{}")},
		"500": {Status: http.StatusInternalServerError, Body: []byte("{}")},
	} {
		f := synthV2(t, 12)
		f.replies = map[string]srctest.Reply{"/api/apply/v2/jobs/800000000005": rep}
		ps, err := f.source().List(context.Background(), nfBoard("x"))
		if err == nil {
			t.Errorf("%s: got %d postings, want the board to fail", name, len(ps))
		} else if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: must not read as gone: %v", name, err)
		}
	}
}
