package page

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestParseAllowed(t *testing.T) {
	const apiPath = "/api/apply/v2/jobs"
	for _, c := range []struct {
		name, body, path string
		want             bool
	}{
		{"empty file", "", apiPath, true},
		{"no applicable group", "User-agent: googlebot\nDisallow: /\n", apiPath, true},
		{"disallow all", "User-agent: *\nDisallow: /\n", apiPath, false},
		{"empty disallow allows", "User-agent: *\nDisallow:\n", apiPath, true},
		{"empty allow adds nothing", "User-agent: *\nDisallow: /\nAllow:\n", apiPath, false},
		{"netflix: allow beats disallow all", "User-agent: *\nDisallow: /\nAllow: /$\nAllow: /careers\nAllow: /api/apply\nAllow: /api/pcsx\n", apiPath, true},
		{"netflix: other path stays disallowed", "User-agent: *\nDisallow: /\nAllow: /careers\nAllow: /api/apply\n", "/api/other", false},
		{"netflix: root only", "User-agent: *\nDisallow: /\nAllow: /$\n", "/", true},
		{"netflix: root anchor does not allow deeper", "User-agent: *\nDisallow: /\nAllow: /$\n", "/careers", false},
		{"longest wins: allow longer", "User-agent: *\nDisallow: /api\nAllow: /api/apply\n", apiPath, true},
		{"longest wins: disallow longer", "User-agent: *\nAllow: /api\nDisallow: /api/apply\n", apiPath, false},
		{"tie goes to allow", "User-agent: *\nDisallow: /api/apply\nAllow: /api/apply\n", apiPath, true},
		{"wildcard", "User-agent: *\nDisallow: /api/*/v2/\n", apiPath, false},
		{"wildcard no match", "User-agent: *\nDisallow: /api/*/v3/\n", apiPath, true},
		{"end anchor", "User-agent: *\nDisallow: /api/apply/v2/jobs$\n", apiPath, false},
		{"end anchor does not match longer", "User-agent: *\nDisallow: /api/apply/v2/jobs$\n", apiPath + "/1", true},
		{"regexp metacharacters are literal", "User-agent: *\nDisallow: /api.apply\n", apiPath, true},
		{"named beats star", "User-agent: *\nDisallow: /\n\nUser-agent: Vani\nAllow: /\n", apiPath, true},
		{"named group disallow", "User-agent: *\nAllow: /\n\nUser-agent: vani\nDisallow: /api\n", apiPath, false},
		{"agent lines share a group", "User-agent: googlebot\nUser-agent: VANI\nDisallow: /\n", apiPath, false},
		{"other product token", "User-agent: Vanilla\nDisallow: /\n", apiPath, true},
		{"rules before any group", "Disallow: /\nUser-agent: *\nAllow: /\n", apiPath, true},
		{"byte order mark, CRLF, comments", "\xef\xbb\xbfUser-agent: *\r\nDisallow: /api # no\r\n", apiPath, false},
		{"sitemap does not split a group", "User-agent: *\nSitemap: https://x.example/s.xml\nDisallow: /api\n", apiPath, false},
		{"crawl-delay does not merge groups", "User-agent: googlebot\nCrawl-delay: 2\nUser-agent: *\nDisallow: /\n", apiPath, false},
		{"binary and html", "\x00\xff\xfe:::\n<html><body>Request blocked</body></html>", apiPath, true},
		{"unbalanced wildcards compile", "User-agent: *\nDisallow: /a*b*c*d*e*f*g*h\n", apiPath, true},
	} {
		if got := parseAllowed([]byte(c.body), robotsAgent, c.path); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRobotsAllows(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		d       *robotsDoer
		want    bool
		wantErr bool
	}{
		{"allowed", robotsReply(200, "User-agent: *\nAllow: /api/apply\nDisallow: /\n"), true, false},
		{"disallowed", robotsReply(200, "User-agent: *\nDisallow: /\n"), false, false},
		{"404 is no robots.txt", robotsReply(404, "not found"), true, false},
		{"403 is no robots.txt too (RFC 9309 4xx)", robotsReply(403, "denied"), true, false},
		{"500 is unknown", robotsReply(500, "oops"), false, true},
		{"503 is unknown", robotsReply(503, ""), false, true},
		{"oversized is unknown", robotsReply(200, strings.Repeat("#", maxRobotsBytes+1)), false, true},
	} {
		got, err := RobotsAllows(ctx, c.d, "x.example.com", "/api/apply/v2/jobs")
		if (err != nil) != c.wantErr || (err == nil && got != c.want) {
			t.Errorf("%s: got %v, %v; want %v, error %v", c.name, got, err, c.want, c.wantErr)
		}
		if err != nil && got {
			t.Errorf("%s: an unreadable robots.txt must not report allowed", c.name)
		}
	}
}

// The read is of the host's own /robots.txt, with Vani's user agent.
func TestRobotsAllowsRequest(t *testing.T) {
	d := robotsReply(200, "")
	if _, err := RobotsAllows(context.Background(), d, "x.example.com", "/p"); err != nil {
		t.Fatal(err)
	}
	if reqs := d.requests(); len(reqs) != 1 || !strings.HasSuffix(reqs[0], "https://x.example.com/robots.txt") {
		t.Errorf("requests %v", reqs)
	}
	if len(d.uas) != 1 || d.uas[0] != UserAgent {
		t.Errorf("user agents %v", d.uas)
	}
}

// Several paths are answered from one read of robots.txt, each by its own
// rules; an unreadable robots.txt answers none of them.
func TestRobotsAllowsPathsOneRead(t *testing.T) {
	d := robotsReply(200, "User-agent: *\nDisallow: /\nAllow: /api/apply\n")
	got, err := RobotsAllowsPaths(context.Background(), d, "x.example.com", "/api/pcsx/search", "/api/apply/v2/jobs", "/api/apply/v2/jobs/")
	if err != nil || len(got) != 3 || got[0] || !got[1] || !got[2] {
		t.Errorf("got %v, %v; want [false true true]", got, err)
	}
	if reqs := d.requests(); len(reqs) != 1 {
		t.Errorf("%d robots.txt reads for three paths: %v", len(reqs), reqs)
	}
	got, err = RobotsAllowsPaths(context.Background(), robotsReply(404, ""), "x.example.com", "/a", "/b")
	if err != nil || len(got) != 2 || !got[0] || !got[1] {
		t.Errorf("404: got %v, %v; want both allowed", got, err)
	}
	got, err = RobotsAllowsPaths(context.Background(), robotsReply(503, ""), "x.example.com", "/a", "/b")
	if err == nil || got != nil {
		t.Errorf("503: got %v, %v; want an error and no answers", got, err)
	}
}

// A host the run's throttle has blocked is asked for nothing, robots.txt
// included, and the refusal is an error, never "no robots.txt, so allowed".
func TestRobotsReadHonoursTheHostThrottle(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	th := srcutil.NewHostThrottle(map[string]srcutil.HostCooldown{
		"x.example.com": {Runs: 3, Last: now.Add(-time.Minute)}, // inside its backoff window
	}, func() time.Time { return now })
	ctx := srcutil.WithHostThrottle(context.Background(), th)
	d := robotsReply(200, "")
	got, err := RobotsAllows(ctx, d, "x.example.com", "/p")
	if got || !errors.Is(err, srcutil.ErrRateLimited) {
		t.Errorf("got %v, %v; want a rate-limit refusal", got, err)
	}
	if _, err := RobotsAllowsPaths(ctx, d, "x.example.com", "/p", "/q"); !errors.Is(err, srcutil.ErrRateLimited) {
		t.Errorf("paths: %v", err)
	}
	if _, err := crawlDelay(ctx, d, "x.example.com", robotsTimeout); !errors.Is(err, srcutil.ErrRateLimited) {
		t.Errorf("crawl-delay: %v", err)
	}
	if reqs := d.requests(); len(reqs) != 0 {
		t.Errorf("requests to a blocked host: %v", reqs)
	}
	// Another host is read as usual.
	if got, err := RobotsAllows(ctx, d, "y.example.com", "/p"); err != nil || !got {
		t.Errorf("other host: %v, %v", got, err)
	}
}

func TestRobotsAllowsTimeoutAndCancel(t *testing.T) {
	got, err := robotsAllows(context.Background(), blockingDoer(), "x.example.com", "/p", 50*time.Millisecond)
	if got || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: got %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	got, err = RobotsAllows(ctx, blockingDoer(), "x.example.com", "/p")
	if got || !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: got %v, %v", got, err)
	}
}
