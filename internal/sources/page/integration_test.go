package page

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// localBrowser is TEST-ONLY. The httptest server lives on 127.0.0.1 with a
// self-signed certificate, which production flags forbid by design, so this
// browser runs the production Flags minus the proxy and private-network ones
// and trusts the test certificate instead. Production code never builds a
// browser like this: it is defined in a _test file on purpose.
func localBrowser(t *testing.T, bin, caPath string) *MCPBrowser {
	t.Helper()
	flags, err := Flags(ProxyInfo{Addr: "127.0.0.1:1", Token: testTok})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{"--ca-cert", caPath}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--block-private-networks":
		case "--http-proxy", "--proxy-bearer-token", "--block-cidrs":
			i++ // drop the flag and its value
		default:
			out = append(out, flags[i])
		}
	}
	b, err := NewMCPBrowser(bin, out, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return b
}

// TestLightpandaIntegration runs the real binary against a local HTTPS server.
// It is skipped unless VANI_LIGHTPANDA_IT=1; VANI_LIGHTPANDA_BIN overrides the
// default /opt/homebrew/bin/lightpanda.
func TestLightpandaIntegration(t *testing.T) {
	if os.Getenv("VANI_LIGHTPANDA_IT") != "1" {
		t.Skip("set VANI_LIGHTPANDA_IT=1 to run the real lightpanda binary")
	}
	bin := os.Getenv("VANI_LIGHTPANDA_BIN")
	if bin == "" {
		bin = "/opt/homebrew/bin/lightpanda"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("VANI_LIGHTPANDA_IT=1 but no binary: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/jobs/p1">Platform Engineer</a><a href="/jobs/p2">Plain Page</a>
			<a href="/jobs/p1#apply">again</a><a href="mailto:x@example.com">mail</a><a href="/about">About</a></body></html>`))
	})
	mux.HandleFunc("/jobs/p1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Platform Engineer - Testco</title>
			<script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":"Platform Engineer",
			"description":"<p>Run the <b>platform</b>.</p>","datePosted":"2026-10-01","validThrough":"2099-01-01",
			"hiringOrganization":{"@type":"Organization","name":"Testco Ltd"},"jobLocationType":"TELECOMMUTE",
			"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"Lisbon","addressCountry":"PT"}}}</script>
			</head><body><h1>Platform Engineer</h1></body></html>`))
	})
	mux.HandleFunc("/jobs/p2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Plain Page Role</title></head><body><p>No structured data here.</p></body></html>`))
	})
	srv := httptest.NewTLSServer(mux) // unknown paths (robots.txt) answer 404
	defer srv.Close()

	ca := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	src := New(localBrowser(t, bin, ca), ProxyInfo{Addr: "127.0.0.1:1", Token: testTok})
	b := sources.Board{ATS: posting.Page, Token: "testco", Company: "Fallback Co", URL: srv.URL + "/jobs", LinkPattern: `/jobs/p\d$`}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	got, err := src.List(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d postings, want 2: %+v", len(got), got)
	}
	p1, p2 := got[0], got[1]
	if p1.Title != "Platform Engineer" || p1.Company != "Testco Ltd" || p1.Location != "Lisbon, PT" || !p1.Remote ||
		!strings.Contains(p1.Description, "Run the platform.") {
		t.Errorf("JSON-LD posting: %+v", p1)
	}
	if p2.Title != "Plain Page Role" || p2.Company != "Fallback Co" || !strings.Contains(p2.Description, "No structured data here.") {
		t.Errorf("fallback posting: %+v", p2)
	}
	for _, p := range got {
		if !strings.HasPrefix(string(p.Key), b.KeyPrefix()) {
			t.Errorf("key %q lacks prefix %q", p.Key, b.KeyPrefix())
		}
	}

	// A page that fails to load fails the board.
	// (/about is linked from the listing but the server answers 404 for it.)
	b.LinkPattern = `/jobs/p1$|/about$`
	if got, err := src.List(ctx, b); !errors.Is(err, ErrHTTPStatus) {
		t.Fatalf("a 404 posting page must fail the whole board: %d postings, err %v", len(got), err)
	}
}

// loopbackDoer is TEST-ONLY: it sends every request to addr, trusting the
// test server's certificate. egress forbids loopback and ports other than
// 443 by design, so Paced's robots.txt read cannot reach httptest through it.
type loopbackDoer struct {
	c    *http.Client
	addr string
}

func (d loopbackDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	r := req.Clone(ctx)
	r.URL.Host = d.addr
	return d.c.Do(r)
}

// TestLightpandaRobotsIntegration runs the real binary under Paced against a
// local HTTPS server and checks what Vani relies on: lightpanda identifies as
// UserAgent and obeys that robots.txt group (not its default Lightpanda one),
// including for subresources; runs are spaced by the group's Crawl-delay,
// starting after Vani's own robots.txt read. Skipped unless
// VANI_LIGHTPANDA_IT=1, like TestLightpandaIntegration.
func TestLightpandaRobotsIntegration(t *testing.T) {
	if os.Getenv("VANI_LIGHTPANDA_IT") != "1" {
		t.Skip("set VANI_LIGHTPANDA_IT=1 to run the real lightpanda binary")
	}
	bin := os.Getenv("VANI_LIGHTPANDA_BIN")
	if bin == "" {
		bin = "/opt/homebrew/bin/lightpanda"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("VANI_LIGHTPANDA_IT=1 but no binary: %v", err)
	}
	const delay = 400 * time.Millisecond

	type hit struct {
		path, ua string
		at       time.Time
	}
	var mu sync.Mutex
	var hits []hit
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, r.UserAgent(), time.Now()})
		mu.Unlock()
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: Lightpanda\nDisallow: /\n\nUser-agent: Vani\nCrawl-delay: 0.4\nDisallow: /jobs/blocked\nDisallow: /js/blocked.js\n"))
		case "/jobs":
			_, _ = w.Write([]byte(`<html><head><script src="/js/blocked.js"></script></head><body>
				<a href="/jobs/p1">One</a><a href="/jobs/p2">Two</a><a href="/jobs/blocked">Blocked</a></body></html>`))
		case "/jobs/p1", "/jobs/p2", "/jobs/blocked":
			_, _ = w.Write([]byte(`<html><head><title>Role ` + r.URL.Path + `</title></head><body><p>Role.</p></body></html>`))
		case "/js/blocked.js":
			_, _ = w.Write([]byte(`document.title = "script ran"`))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	paced, err := NewRobotsPaced(localBrowser(t, bin, ca), loopbackDoer{c: srv.Client(), addr: srv.Listener.Addr().String()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	src := New(paced, ProxyInfo{Addr: "127.0.0.1:1", Token: testTok})
	b := sources.Board{ATS: posting.Page, Token: "testco", Company: "Testco", URL: srv.URL + "/jobs", LinkPattern: `/jobs/p\d$`}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	got, err := src.List(ctx, b)
	if err != nil {
		t.Fatalf("List (a Lightpanda Disallow: / must not apply to Vani): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d postings, want 2", len(got))
	}

	mu.Lock()
	seen := slices.Clone(hits)
	mu.Unlock()
	var docs []time.Time // each run's page request, in order
	robots := 0
	for _, h := range seen {
		if h.ua != UserAgent {
			t.Errorf("%s came with User-Agent %q, want %q", h.path, h.ua, UserAgent)
		}
		switch {
		case h.path == "/js/blocked.js":
			t.Error("lightpanda loaded a script the Vani group disallows")
		case h.path == "/robots.txt":
			robots++
			if robots == 1 && len(docs) > 0 {
				t.Error("a page was fetched before Vani read robots.txt")
			}
		case strings.HasPrefix(h.path, "/jobs"):
			docs = append(docs, h.at)
		}
	}
	if len(docs) != 3 {
		t.Fatalf("%d page requests, want the listing and 2 postings: %v", len(docs), seen)
	}
	// Vani's read, then lightpanda's own once: one process serves the whole
	// board, so its RobotStore is shared by every page.
	if robots != 2 {
		t.Errorf("%d robots.txt requests for %d pages, want 2 (Vani's and lightpanda's)", robots, len(docs))
	}
	first := seen[0]
	if first.path != "/robots.txt" {
		t.Fatalf("first request %s, want Vani's robots.txt read", first.path)
	}
	prev := first.at
	for i, at := range docs {
		if gap := at.Sub(prev); gap < delay {
			t.Errorf("page request %d came %s after the previous visit, want at least the %s Crawl-delay", i, gap, delay)
		}
		prev = at
	}

	// The next board gets a fresh process, which reads robots.txt once more;
	// Vani's own Crawl-delay read is kept for the run.
	if _, err := src.List(ctx, b); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	robots = 0
	for _, h := range hits {
		if h.path == "/robots.txt" {
			robots++
		}
	}
	mu.Unlock()
	if robots != 3 {
		t.Errorf("%d robots.txt requests after two boards, want 3 (Vani's once, lightpanda's once per board)", robots)
	}

	// A posting the Vani group disallows fails the board: lightpanda refuses it.
	b.LinkPattern = `/jobs/(p1|blocked)$`
	if got, err := src.List(ctx, b); err == nil || !strings.Contains(err.Error(), "RobotsBlocked") {
		t.Fatalf("disallowed posting: %d postings, err %v", len(got), err)
	}
}
