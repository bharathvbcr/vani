package eightfold

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Every board reads its host's robots.txt once, before the first request of
// either API, and requests only a path it permits. The four known hosts
// (Microsoft, Qualcomm, Netflix, Millennium) Allow both APIs; these tests are
// for the host that does not.

const (
	// robotsNoPCSX is a host that allows /api/apply and /careers but not pcsx.
	robotsNoPCSX = "User-agent: *\nDisallow: /\nAllow: /$\nAllow: /careers\nAllow: /api/apply\n"
	// robotsNoV2 allows pcsx but not v2.
	robotsNoV2 = "User-agent: *\nDisallow: /\nAllow: /careers\nAllow: /api/pcsx\n"
	// robotsNeither allows neither API.
	robotsNeither = "User-agent: *\nDisallow: /\nAllow: /careers\n"
)

func robotsReply(body string) *srctest.Reply { return &srctest.Reply{Status: 200, Body: []byte(body)} }

// pathsOf is the request paths a Doer saw, in order.
func pathsOf(d *srctest.Doer) []string {
	var out []string
	for _, r := range d.Requests() {
		u, _ := url.Parse(r.URL)
		out = append(out, u.Path)
	}
	return out
}

func countPrefix(paths []string, prefix string) int {
	n := 0
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

// The failing case of the old code: a host whose robots.txt disallows pcsx
// was sent a pcsx search first, and only then read for v2. Now it goes straight
// to v2 without one pcsx request, however pcsx would have answered.
func TestPCSXDisallowedGoesStraightToV2(t *testing.T) {
	f := fixtureV2(t)
	f.replies = map[string]srctest.Reply{"/robots.txt": *robotsReply(robotsNoPCSX)}
	// pcsx answers 403 if asked, as on Netflix's site; the point is that it is
	// not asked.
	var stats []Stats
	s := f.source()
	s.opt = &Options{Report: func(_ sources.Board, st Stats) { stats = append(stats, st) }}
	got, err := s.List(context.Background(), nfBoard("machine learning"))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d postings, err %v", len(got), err)
	}
	if n := f.seen("/api/pcsx/"); n != 0 {
		t.Errorf("%d requests to /api/pcsx/ on a host whose robots.txt disallows it: %v", n, f.reqs)
	}
	want := []string{"/robots.txt?", "/api/apply/v2/jobs?0", "/api/apply/v2/jobs/790316208720?", "/api/apply/v2/jobs/790317580317?"}
	if strings.Join(f.reqs, " ") != strings.Join(want, " ") {
		t.Errorf("requests %v, want %v", f.reqs, want)
	}
	if len(stats) != 1 || stats[0] != (Stats{Listed: 2, V2: true}) {
		t.Errorf("reports %+v, want one read through v2", stats)
	}
}

// Disallowing only pcsx's detail path makes the pcsx API unusable too: a board
// is read through one API end to end, so it never starts on one it could not
// finish.
func TestPCSXDetailDisallowedGoesStraightToV2(t *testing.T) {
	f := fixtureV2(t)
	f.replies = map[string]srctest.Reply{"/robots.txt": *robotsReply("User-agent: *\nDisallow: /api/pcsx/position_details\n")}
	got, err := f.source().List(context.Background(), nfBoard("x"))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d postings, err %v", len(got), err)
	}
	if n := f.seen("/api/pcsx/"); n != 0 {
		t.Errorf("%d pcsx requests: %v", n, f.reqs)
	}
}

// A host that allows pcsx stays on pcsx: one robots.txt read first, then pcsx
// only; v2 is never touched.
func TestPCSXAllowedKeepsPCSX(t *testing.T) {
	for name, robots := range map[string]*srctest.Reply{
		"allows both":  nil,
		"allows pcsx":  robotsReply(robotsNoV2),
		"no robots":    {Status: 404, Body: []byte("nope")},
		"403 robots":   {Status: 403, Body: []byte("denied")},
		"vani allowed": robotsReply("User-agent: vani\nAllow: /\n\nUser-agent: *\nDisallow: /\n"),
	} {
		f := fixtureSite(t)
		f.robots = robots
		s, d := newSource(f)
		got, err := s.List(context.Background(), board("LLM inference"))
		if err != nil || len(got) != 2 {
			t.Errorf("%s: %d postings, err %v", name, len(got), err)
			continue
		}
		paths := pathsOf(d)
		if paths[0] != "/robots.txt" || countPrefix(paths, "/robots.txt") != 1 {
			t.Errorf("%s: requests %v, want robots.txt first and once", name, paths)
		}
		if countPrefix(paths, "/api/pcsx/") != len(paths)-1 {
			t.Errorf("%s: requests %v, want robots.txt then pcsx only", name, paths)
		}
	}
}

// A host that allows neither API fails the board with an error that names both
// paths, before any API request: nothing is listed.
func TestRobotsDisallowingBothFailsTheBoard(t *testing.T) {
	f := fixtureSite(t)
	f.robots = robotsReply(robotsNeither)
	s, d := newSource(f)
	got, err := s.List(context.Background(), board("LLM inference"))
	if err == nil || got != nil {
		t.Fatalf("got %d postings, err %v; want an error", len(got), err)
	}
	for _, want := range []string{"robots.txt", host, "/api/pcsx/", "/api/apply/v2/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Errorf("a refusal is not a missing board: %v", err)
	}
	if paths := pathsOf(d); len(paths) != 1 || paths[0] != "/robots.txt" {
		t.Errorf("requests %v, want the robots.txt read alone", paths)
	}
	// The same through the v2 site, and the report is not called for a board
	// that listed nothing.
	g := fixtureV2(t)
	g.replies = map[string]srctest.Reply{"/robots.txt": *robotsReply(robotsNeither)}
	reported := false
	gs := g.source()
	gs.opt = &Options{Report: func(sources.Board, Stats) { reported = true }}
	if _, err := gs.List(context.Background(), nfBoard("x")); err == nil || len(g.reqs) != 1 || reported {
		t.Errorf("v2 site: err %v, requests %v, reported %v", err, g.reqs, reported)
	}
}

// Before the old code, pcsx answering 403 on a host that disallows v2 reported
// both; it still does, and now from the one robots.txt read of the board.
func TestPCSXForbiddenWithV2DisallowedReportsBoth(t *testing.T) {
	f := fixtureV2(t)
	f.replies = map[string]srctest.Reply{"/robots.txt": *robotsReply(robotsNoV2)}
	_, err := f.source().List(context.Background(), nfBoard("x"))
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "disallows") {
		t.Fatalf("err %v, want the pcsx 403 and the v2 robots refusal", err)
	}
	if f.seen("/api/apply/") != 0 || f.seen("/robots.txt") != 1 || f.seen("/api/pcsx/search") != 1 {
		t.Errorf("requests %v, want one robots.txt, one pcsx probe, no v2", f.reqs)
	}
}

// An unreadable robots.txt is not permission: a 5xx, an oversized file, a
// transport failure and a timeout fail the board before any API request.
func TestRobotsUnreadableFailsTheBoard(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"500":       {Status: 500, Body: []byte("oops")},
		"503":       {Status: 503, Body: []byte("")},
		"oversized": {Status: 200, Body: []byte(strings.Repeat("#", 600<<10))},
	} {
		f := fixtureSite(t)
		f.robots = &rep
		s, d := newSource(f)
		got, err := s.List(context.Background(), board("LLM inference"))
		if err == nil || got != nil || !strings.Contains(err.Error(), "robots.txt") {
			t.Errorf("%s: %d postings, err %v; want a robots.txt error", name, len(got), err)
		}
		if paths := pathsOf(d); len(paths) != 1 {
			t.Errorf("%s: requests %v, want the robots.txt read alone", name, paths)
		}
	}
	// A transport failure.
	s := &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false }), now: time.Now, wait: noWait}
	if _, err := s.List(context.Background(), board("x")); err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("transport failure: %v", err)
	}
}

// blockRobots is a Doer whose robots.txt read never answers.
type blockRobots struct{ inner *srctest.Doer }

func (b blockRobots) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/robots.txt" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.inner.Do(ctx, r)
}

func TestRobotsTimeoutFailsTheBoard(t *testing.T) {
	f := fixtureSite(t)
	_, inner := newSource(f)
	s := &source{d: blockRobots{inner}, now: time.Now, wait: noWait}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got, err := s.List(ctx, board("LLM inference"))
	if !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Errorf("got %d postings, err %v; want the deadline", len(got), err)
	}
	if n := len(inner.Requests()); n != 0 {
		t.Errorf("%d API requests while robots.txt was unanswered", n)
	}
}

// The board reads robots.txt once, not once per search term, per page or per
// API: three terms through pcsx, and a v2 board whose pcsx probe is refused.
func TestOneRobotsReadPerBoard(t *testing.T) {
	f := fixtureSite(t)
	f.positions = map[string][]string{"a": {"5000000001"}, "b": {"5000000002"}, "c": {"5000000003"}}
	s, d := newSource(f)
	if got, err := s.List(context.Background(), board("a", "b", "c")); err != nil || len(got) != 3 {
		t.Fatalf("pcsx: %d postings, err %v", len(got), err)
	}
	if n := countPrefix(pathsOf(d), "/robots.txt"); n != 1 {
		t.Errorf("pcsx board: %d robots.txt reads, want 1", n)
	}
	g := synthV2(t, 12)
	if got, err := g.source().List(context.Background(), nfBoard("a", "b")); err != nil || len(got) != 12 {
		t.Fatalf("v2: %d postings, err %v", len(got), err)
	}
	if n := g.seen("/robots.txt"); n != 1 {
		t.Errorf("v2 board: %d robots.txt reads, want 1", n)
	}
}

// A cancelled context sends nothing, the robots.txt read included. (Its pacing
// is TestPacing: every request after the first waits.)
func TestRobotsReadStopsOnCancel(t *testing.T) {
	f := fixtureSite(t)
	s, d := newSource(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx, board("LLM inference")); err == nil {
		t.Error("cancelled context accepted")
	}
	if n := len(d.Requests()); n != 0 {
		t.Errorf("%d requests on a cancelled context", n)
	}
}
