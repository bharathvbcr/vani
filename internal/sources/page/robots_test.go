package page

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

func TestParseCrawlDelay(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       float64
	}{
		{"empty", "", 0},
		{"star", "User-agent: *\nCrawl-delay: 5\n", 5},
		{"case, spacing, comment, CRLF", "user-agent: *\r\ncrawl-DELAY : 2.5 # be gentle\r\n", 2.5},
		{"byte order mark", "\xef\xbb\xbfUser-agent: *\nCrawl-delay: 3", 3},
		{"before any group", "Crawl-delay: 7\nUser-agent: *\nDisallow: /", 0},
		{"named beats star", "User-agent: *\nCrawl-delay: 8\n\nUser-agent: Vani\nCrawl-delay: 2\n", 2},
		{"named group without a delay still beats star", "User-agent: *\nCrawl-delay: 8\nUser-agent: Vani/0.1\nDisallow: /x\n", 0},
		{"agent lines share a group", "User-agent: googlebot\nUser-agent: VANI\nCrawl-delay: 4\nUser-agent: *\nCrawl-delay: 1", 4},
		{"agent after a rule starts a group", "User-agent: vani\nDisallow: /\nUser-agent: other\nCrawl-delay: 9\n", 0},
		{"groups and lines merge to the longest", "User-agent: vani\nCrawl-delay: 1\nCrawl-delay: 3\nUser-agent: x\nCrawl-delay: 50\nUser-agent: vani\nCrawl-delay: 2", 3},
		{"sitemap does not split a group", "User-agent: vani\nSitemap: https://x.example/s.xml\nUser-agent: *\nCrawl-delay: 9", 9},
		{"unknown key does not split a group", "User-agent: vani\nHost: x.example\nUser-agent: *\nCrawl-delay: 6", 6},
		{"other product token", "User-agent: Vanilla\nCrawl-delay: 9\nUser-agent: *\nCrawl-delay: 1", 1},
		{"leading dot", "User-agent: *\nCrawl-delay: .5", 0.5},
		{"trailing dot", "User-agent: *\nCrawl-delay: 5.", 5},
		{"malformed in a group not ours", "User-agent: other\nCrawl-delay: abc", 0},
		{"malformed in a superseded star group", "User-agent: *\nCrawl-delay: abc\nUser-agent: vani\nCrawl-delay: 1", 1},
		{"no colons", "User-agent *\nCrawl-delay 4", 0},
		{"binary and html", "\x00\xff\xfe:::\n<html><body>Request blocked</body></html>", 0},
	} {
		got, err := parseCrawlDelay([]byte(c.body), robotsAgent)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

// A Crawl-delay that applies to Vani and is not decimal seconds is an error,
// never read as zero.
func TestParseCrawlDelayMalformed(t *testing.T) {
	for _, v := range []string{"abc", "-1", "NaN", "Inf", "1e3", "5s", "", "1,5", "0x10", "1\x00", "+2", strings.Repeat("9", maxDelayText+1)} {
		for _, agent := range []string{"*", "vani"} {
			if got, err := parseCrawlDelay([]byte("User-agent: "+agent+"\nCrawl-delay: "+v+"\n"), robotsAgent); err == nil {
				t.Errorf("agent %s, Crawl-delay %q: got %v, want an error", agent, v, got)
			}
		}
	}
	_, err := parseCrawlDelay([]byte("User-agent: *\nCrawl-delay: \x1b[31m"+strings.Repeat("a", 4000)), robotsAgent)
	if err == nil {
		t.Fatal("long value accepted")
	}
	if len(err.Error()) > 2*maxDelayText+len("its Crawl-delay is not a number of seconds") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("error does not bound or escape the value: %q", err)
	}
}

func TestDelayDuration(t *testing.T) {
	for secs, want := range map[float64]time.Duration{0: 0, 0.0005: time.Millisecond, 2.5: 2500 * time.Millisecond, 10: 10 * time.Second} {
		if got := delayDuration(secs); got != want {
			t.Errorf("%v s: got %s, want %s", secs, got, want)
		}
	}
}

// robotsDoer answers robots.txt requests with handle and records them.
type robotsDoer struct {
	handle func(ctx context.Context, req *http.Request) (*http.Response, error)

	mu    sync.Mutex
	reqs  []string
	uas   []string
	spans []span // each request, sent to answered
}

func (d *robotsDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := d.handle(ctx, req)
	d.mu.Lock()
	d.reqs = append(d.reqs, req.Method+" "+req.URL.String())
	d.uas = append(d.uas, req.Header.Get("User-Agent"))
	d.spans = append(d.spans, span{url: req.URL.String(), start: start, end: time.Now()})
	d.mu.Unlock()
	return resp, err
}

func (d *robotsDoer) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.reqs...)
}

func (d *robotsDoer) recorded() []span {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]span(nil), d.spans...)
}

func robotsReply(status int, body string) *robotsDoer {
	return &robotsDoer{handle: func(_ context.Context, req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}}
}

// blockingDoer never answers until its request's context ends.
func blockingDoer() *robotsDoer {
	return &robotsDoer{handle: func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
}

func mustRobotsPaced(t *testing.T, next Runner, d egress.Doer, floor time.Duration) *Paced {
	t.Helper()
	p, err := NewRobotsPaced(next, d, floor)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noRuns(t *testing.T, inner *timingRunner) {
	t.Helper()
	if spans, _ := inner.recorded(); len(spans) != 0 {
		t.Fatalf("%d runs made, want none", len(spans))
	}
}

func TestNewRobotsPacedBounds(t *testing.T) {
	inner, d := &timingRunner{}, robotsReply(404, "")
	for _, f := range []time.Duration{-time.Nanosecond, MaxInterval + time.Nanosecond} {
		if _, err := NewRobotsPaced(inner, d, f); err == nil {
			t.Errorf("floor %s accepted", f)
		}
	}
	if _, err := NewRobotsPaced(nil, d, 0); err == nil {
		t.Error("nil runner accepted")
	}
	if _, err := NewRobotsPaced(inner, nil, 0); err == nil {
		t.Error("nil doer accepted")
	}
	for _, f := range []time.Duration{0, MaxInterval} {
		if _, err := NewRobotsPaced(inner, d, f); err != nil {
			t.Errorf("floor %s refused: %v", f, err)
		}
	}
}

// A 4xx is no robots.txt: the run goes ahead at the floor. Any other status
// leaves the delay unknown, so the run is not made.
func TestRobotsStatus(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 410, 429, 451} {
		inner := &timingRunner{}
		p := mustRobotsPaced(t, inner, robotsReply(status, "User-agent: *\nCrawl-delay: 60\n"), 0)
		if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
	}
	for _, status := range []int{100, 301, 302, 304, 500, 502, 503, 599} {
		inner := &timingRunner{}
		p := mustRobotsPaced(t, inner, robotsReply(status, ""), 0)
		if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Errorf("status %d: %v", status, err)
		}
		noRuns(t, inner)
	}
}

// One read per host per run, at https://<host>/robots.txt, even after the
// host's slot was dropped between runs.
func TestRobotsReadOncePerHost(t *testing.T) {
	inner := &timingRunner{}
	d := robotsReply(200, "User-agent: *\nCrawl-delay: 0\n")
	p := mustRobotsPaced(t, inner, d, 0)
	for _, u := range []string{"https://a.example.com/1", "https://A.example.com/2", "https://b.example.com/1", "https://a.example.com/3"} {
		if _, err := p.Run(context.Background(), fetchArgs(u)); err != nil {
			t.Fatal(err)
		}
	}
	got := d.requests()
	want := []string{"GET https://a.example.com/robots.txt", "GET https://b.example.com/robots.txt"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("robots requests %q, want %q", got, want)
	}
	if spans, _ := inner.recorded(); len(spans) != 4 {
		t.Fatalf("%d runs, want 4", len(spans))
	}
}

// A failed read is not remembered: the next run to the host reads again.
func TestRobotsFailureNotCached(t *testing.T) {
	inner := &timingRunner{}
	status := 503
	d := &robotsDoer{handle: func(_ context.Context, req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}}
	p := mustRobotsPaced(t, inner, d, 0)
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err == nil {
		t.Fatal("503 robots.txt ran")
	}
	status = 404
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err != nil {
		t.Fatal(err)
	}
	if n := len(d.requests()); n != 2 {
		t.Fatalf("%d robots requests, want 2", n)
	}
}

// The wait is the longer of the floor and the Crawl-delay, whichever it is.
func TestRobotsDelayAndFloor(t *testing.T) {
	for _, c := range []struct {
		floor time.Duration
		delay string
	}{{150 * time.Millisecond, "0.03"}, {30 * time.Millisecond, "0.15"}} {
		inner := &timingRunner{hold: 5 * time.Millisecond}
		d := robotsReply(200, "User-agent: Vani\nCrawl-delay: "+c.delay+"\n")
		p := mustRobotsPaced(t, inner, d, c.floor)
		for _, u := range []string{"https://careers.example.com/a", "https://careers.example.com/b", "https://careers.example.com/c"} {
			if _, err := p.Run(context.Background(), fetchArgs(u)); err != nil {
				t.Fatal(err)
			}
		}
		spans, _ := inner.recorded()
		if len(spans) != 3 {
			t.Fatalf("floor %s, delay %s: %d runs", c.floor, c.delay, len(spans))
		}
		checkGaps(t, append(d.recorded(), spans...), 150*time.Millisecond)
	}
}

// Vani's own robots.txt read is a visit: the first run rests after it, and so
// does the read after a failed one.
func TestRobotsReadIsAVisit(t *testing.T) {
	const floor = 150 * time.Millisecond
	inner := &timingRunner{}
	status := 503
	d := &robotsDoer{handle: func(_ context.Context, req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}}
	p := mustRobotsPaced(t, inner, d, floor)
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err == nil {
		t.Fatal("503 robots.txt ran")
	}
	status = 404
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err != nil {
		t.Fatal(err)
	}
	spans, _ := inner.recorded()
	all := append(d.recorded(), spans...)
	if len(all) != 3 {
		t.Fatalf("%d visits, want 2 robots reads and 1 run", len(all))
	}
	checkGaps(t, all, floor)
}

// Every robots.txt read and every lightpanda run identifies as UserAgent,
// whose product token is the group Crawl-delay is read from.
func TestRobotsUserAgent(t *testing.T) {
	if got := productToken(UserAgent); got != robotsAgent {
		t.Fatalf("UserAgent's product token is %q, robots group is %q", got, robotsAgent)
	}
	if strings.Contains(UserAgent, "Mozilla") {
		t.Fatal("lightpanda refuses a User-Agent containing Mozilla")
	}
	d := robotsReply(404, "")
	if _, err := mustRobotsPaced(t, &timingRunner{}, d, 0).Run(context.Background(), fetchArgs("https://careers.example.com/a")); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.uas) != 1 || d.uas[0] != UserAgent {
		t.Fatalf("robots.txt User-Agent %q, want %q", d.uas, UserAgent)
	}
}

// A Crawl-delay above the cap is refused, not paced at the cap.
func TestRobotsDelayAboveCap(t *testing.T) {
	for _, v := range []string{"10.001", "11", "3600", "99999999999999999999"} {
		inner := &timingRunner{}
		p := mustRobotsPaced(t, inner, robotsReply(200, "User-agent: *\nCrawl-delay: "+v+"\n"), time.Second)
		_, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a"))
		if !errors.Is(err, ErrCrawlDelayTooLong) || !strings.Contains(err.Error(), "careers.example.com") {
			t.Errorf("Crawl-delay %s: %v", v, err)
		}
		noRuns(t, inner)
	}
	// At the cap is accepted (read directly: a Run would rest 10 s first).
	p := mustRobotsPaced(t, &timingRunner{}, robotsReply(200, "User-agent: *\nCrawl-delay: 10\n"), 0)
	if iv, err := p.intervalFor(context.Background(), &hostSlot{}, "careers.example.com"); err != nil || iv != MaxInterval {
		t.Fatalf("Crawl-delay at the cap: %s, %v", iv, err)
	}
}

func TestRobotsMalformedDelay(t *testing.T) {
	inner := &timingRunner{}
	p := mustRobotsPaced(t, inner, robotsReply(200, "User-agent: *\nCrawl-delay: soon\n"), 0)
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err == nil || !strings.Contains(err.Error(), `"soon"`) {
		t.Fatalf("malformed Crawl-delay: %v", err)
	}
	noRuns(t, inner)
}

// A robots.txt that is not one (a challenge page, binary) sets no delay.
func TestRobotsGarbageBody(t *testing.T) {
	inner := &timingRunner{}
	p := mustRobotsPaced(t, inner, robotsReply(200, "<html>\x00\xff<p>Checking your browser</p></html>"), 0)
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); err != nil {
		t.Fatal(err)
	}
}

// A robots.txt over the limit is an error, never read in part; one at the
// limit is read whole.
func TestRobotsSizeLimit(t *testing.T) {
	head := "User-agent: *\nCrawl-delay: 0.01\n"
	atLimit := head + "#" + strings.Repeat("x", maxRobotsBytes-len(head)-1)
	inner := &timingRunner{}
	if _, err := mustRobotsPaced(t, inner, robotsReply(200, atLimit), 0).Run(context.Background(), fetchArgs("https://careers.example.com/a")); err != nil {
		t.Fatalf("robots.txt at the limit: %v", err)
	}
	inner = &timingRunner{}
	_, err := mustRobotsPaced(t, inner, robotsReply(200, atLimit+"\n"), 0).Run(context.Background(), fetchArgs("https://careers.example.com/a"))
	if !errors.Is(err, ErrRobotsTooLarge) {
		t.Fatalf("robots.txt over the limit: %v", err)
	}
	noRuns(t, inner)
}

// A robots.txt request that does not answer in time fails the run quickly.
func TestRobotsTimeout(t *testing.T) {
	inner := &timingRunner{}
	p := mustRobotsPaced(t, inner, blockingDoer(), 0)
	p.robotsTimeout = 50 * time.Millisecond
	begin := time.Now()
	_, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a"))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want a robots.txt timeout", err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("timed out after %s", took)
	}
	noRuns(t, inner)
}

// A caller cancelled during the robots.txt read gets its context's error.
func TestRobotsCancelled(t *testing.T) {
	inner := &timingRunner{}
	p := mustRobotsPaced(t, inner, blockingDoer(), 0)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	if _, err := p.Run(ctx, fetchArgs("https://careers.example.com/a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	noRuns(t, inner)
}

// robots.txt goes through the egress policy: a host the allowlist does not
// name is refused before any socket opens, and the run is not made.
func TestRobotsEgressRefusesHost(t *testing.T) {
	c, err := egress.New(egress.Config{AllowedHosts: []string{"other.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	inner := &timingRunner{}
	p := mustRobotsPaced(t, inner, c, 0)
	if _, err := p.Run(context.Background(), fetchArgs("https://careers.example.com/a")); !errors.Is(err, egress.ErrNotAllowed) {
		t.Fatalf("got %v, want egress.ErrNotAllowed", err)
	}
	noRuns(t, inner)
}

// Through the source: a host whose Crawl-delay is above the cap fails the
// board loudly, with no page fetched.
func TestListRefusesBoardOverCrawlDelayCap(t *testing.T) {
	inner := &timingRunner{next: &fakeRunner{pages: map[string]string{listURL: listing("/jobs/eng-1")}}}
	p := mustRobotsPaced(t, inner, robotsReply(200, "User-agent: *\nCrawl-delay: 30\n"), 0)
	_, err := newSrc(p).List(context.Background(), board())
	if err == nil || !strings.Contains(err.Error(), "Crawl-delay") {
		t.Fatalf("List: %v", err)
	}
	noRuns(t, inner)
}
