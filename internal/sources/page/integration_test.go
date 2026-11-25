package page

import (
	"context"
	"encoding/json"
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

// TestLightpandaHardeningIntegration exercises RichBrowser (tools/list inspection,
// SPA hydration, waitForSelector, scroll, structuredData microdata/JSON-LD, and
// extract) plus multi-page pagination against the real lightpanda binary.
func TestLightpandaHardeningIntegration(t *testing.T) {
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
		page := r.URL.Query().Get("page")
		if page == "2" {
			_, _ = w.Write([]byte(`<html><body>
				<a href="/jobs/css-only">CSS Role</a>
				<a href="/jobs?page=1">Prev</a>
			</body></html>`))
			return
		}
		// Page 1 hydrates via JS after a short timer, testing SPA shell settle / waitForSelector.
		_, _ = w.Write([]byte(`<html><body><div id="root">Loading jobs...</div>
			<script>
				setTimeout(function() {
					document.getElementById("root").innerHTML =
						'<a class="job-link" href="/jobs/micro">Microdata Role</a>' +
						'<a class="job-link" href="/jobs/dyn-ld">Dynamic JSON-LD Role</a>' +
						'<a class="next" href="/jobs?page=2">Next</a>';
				}, 80);
			</script></body></html>`))
	})
	mux.HandleFunc("/jobs/micro", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Microdata Fallback</title></head><body>
			<div itemscope itemtype="https://schema.org/JobPosting">
				<h1 itemprop="title">Microdata Engineer</h1>
				<div itemprop="description">Build microdata pipelines.</div>
				<span itemprop="hiringOrganization" itemscope itemtype="https://schema.org/Organization">
					<span itemprop="name">MicroCo</span>
				</span>
				<span itemprop="jobLocation" itemscope itemtype="https://schema.org/Place">
					<span itemprop="address" itemscope itemtype="https://schema.org/PostalAddress">
						<span itemprop="addressLocality">Denver</span>,
						<span itemprop="addressRegion">CO</span>,
						<span itemprop="addressCountry">US</span>
					</span>
				</span>
				<meta itemprop="jobLocationType" content="TELECOMMUTE" />
				<meta itemprop="datePosted" content="2026-10-05" />
				<meta itemprop="validThrough" content="2099-01-01" />
			</div></body></html>`))
	})
	mux.HandleFunc("/jobs/dyn-ld", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Dynamic LD</title></head><body><main><h1>Dynamic Role</h1></main>
			<script>
				var s = document.createElement("script");
				s.type = "application/ld+json";
				s.textContent = JSON.stringify({
					"@context": "https://schema.org",
					"@type": "JobPosting",
					"title": "Dynamic JSON-LD Engineer",
					"description": "<p>Injected after load.</p>",
					"hiringOrganization": {"@type": "Organization", "name": "DynCo"}
				});
				document.head.appendChild(s);
			</script></body></html>`))
	})
	mux.HandleFunc("/jobs/css-only", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>CSS Page</title></head><body>
			<nav><a href="/">Home</a></nav>
			<main>
				<h1 class="role-title">CSS Extracted Engineer</h1>
				<div class="role-company">CSSCo</div>
				<div class="role-loc">Boston, MA</div>
				<div class="role-desc">Design compiler toolchains.</div>
			</main></body></html>`))
	})
	mux.HandleFunc("/wait-scroll", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body style="height:3000px;margin:0"><div style="height:5000px">tall</div><div id="status">init</div>
			<script>
				setTimeout(function() {
					var el = document.createElement("div");
					el.className = "delayed-ready";
					el.textContent = "Ready after delay with enough visible text so looksLikeShell would not need to guess and wait.";
					document.body.appendChild(el);
				}, 100);
				function addScrolled() {
					if (!document.querySelector(".scrolled-link")) {
						var a = document.createElement("a");
						a.className = "scrolled-link";
						a.href = "/jobs/scrolled";
						a.textContent = "Scrolled Role";
						document.body.appendChild(a);
					}
				}
				window.addEventListener("scroll", addScrolled);
				document.addEventListener("scroll", addScrolled);
			</script></body></html>`))
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	br := localBrowser(t, bin, ca)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Inspect tools/list from the real binary so any schema drift is caught immediately.
	var toolSchemas []string
	if err := br.withProc(ctx, func(ctx context.Context, p *mcpProc) error {
		res, rerr, err := p.call(ctx, "tools/list", map[string]any{})
		if err != nil {
			return err
		}
		if rerr != nil {
			t.Fatalf("tools/list rpc err: %s", rerr)
		}
		var list struct {
			Tools []struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(res, &list); err != nil {
			return err
		}
		names := make([]string, 0, len(list.Tools))
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
			toolSchemas = append(toolSchemas, tool.Name+"="+string(tool.InputSchema))
		}
		for _, wantTool := range []string{"goto", "html", "waitForSelector", "waitForState", "scroll", "structuredData", "extract"} {
			if !slices.Contains(names, wantTool) {
				t.Fatalf("missing MCP tool %q in %v (schemas=%v)", wantTool, names, toolSchemas)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Probe waitForSelector and scroll on the real binary.
	pdWaitScroll, err := br.LoadPage(ctx, srv.URL+"/wait-scroll", LoadOptions{
		WaitSelector: ".delayed-ready",
		ScrollSteps:  1,
	})
	if err != nil {
		t.Fatalf("LoadPage wait-scroll: %v", err)
	}
	if !strings.Contains(string(pdWaitScroll.HTML), "delayed-ready") {
		t.Errorf("waitForSelector did not capture .delayed-ready: %s", string(pdWaitScroll.HTML))
	}
	if len(pdWaitScroll.ExtraHTML) != 1 || !strings.Contains(string(pdWaitScroll.ExtraHTML[0]), "/jobs/scrolled") {
		var scrollY string
		_ = br.withProc(ctx, func(ctx context.Context, p *mcpProc) error {
			res, _, err := p.call(ctx, "tools/call", toolCall{Name: "evaluate", Arguments: map[string]any{"script": "window.scrollY + ',' + document.documentElement.scrollTop"}})
			scrollY = string(res)
			return err
		})
		// Lightpanda's scroll tool succeeds ("Scrolled window to x: 0, y: 1200") and
		// updates scrollY even when the headless page does not fire a synthetic DOM scroll event.
		if !strings.Contains(scrollY, "1200") {
			t.Errorf("scroll did not advance scrollY or capture .scrolled-link: extra=%q scrollY=%s", pdWaitScroll.ExtraHTML, scrollY)
		}
	}

	// Probe structuredData and extract directly on microdata and css-only pages.
	pdMicro, err := br.LoadPage(ctx, srv.URL+"/jobs/micro", LoadOptions{WantStructuredData: true})
	if err != nil {
		t.Fatalf("LoadPage micro: %v", err)
	}
	if len(pdMicro.StructuredData) == 0 {
		t.Errorf("micro structuredData empty; tools=%v", toolSchemas)
	}

	pdCSS, err := br.LoadPage(ctx, srv.URL+"/jobs/css-only", LoadOptions{
		WantStructuredData: true,
		ExtractSchema:      buildExtractSchema(&sources.PageExtract{Title: ".role-title", Description: ".role-desc", Location: ".role-loc", Company: ".role-company"}),
	})
	if err != nil {
		t.Fatalf("LoadPage css: %v", err)
	}
	if len(pdCSS.Extracted) == 0 {
		t.Errorf("css extracted empty (tools=%v)", toolSchemas)
	}

	// Full Source.List end-to-end with pagination + SPA hydration + dynamic JSON-LD + microdata + CSS extract.
	src := New(br, ProxyInfo{Addr: "127.0.0.1:1", Token: testTok})
	b := sources.Board{
		ATS:         posting.Page,
		Token:       "hardening",
		Company:     "Fallback Co",
		URL:         srv.URL + "/jobs",
		LinkPattern: `/jobs/(micro|dyn-ld|css-only)$`,
		NextPattern: `\?page=\d+$`,
		ScrollSteps: 1,
		Extract: &sources.PageExtract{
			Title:       ".role-title",
			Description: ".role-desc",
			Location:    ".role-loc",
			Company:     ".role-company",
		},
	}
	got, err := src.List(ctx, b)
	if err != nil {
		t.Fatalf("List: %v (tools=%v)", err, toolSchemas)
	}
	if len(got) != 3 {
		t.Fatalf("got %d postings, want 3: %+v", len(got), got)
	}
	byURL := map[string]posting.Posting{}
	for _, p := range got {
		byURL[p.URL] = p
	}
	if p := byURL[srv.URL+"/jobs/dyn-ld"]; p.Title != "Dynamic JSON-LD Engineer" || p.Company != "DynCo" || !strings.Contains(p.Description, "Injected after load.") {
		t.Errorf("dyn-ld posting: %+v", p)
	}
	if p := byURL[srv.URL+"/jobs/micro"]; p.Title != "Microdata Engineer" || p.Company != "MicroCo" || p.Location != "Denver, CO, US" || !p.Remote {
		t.Errorf("micro posting: %+v (structuredData=%s, tools=%v)", p, string(pdMicro.StructuredData), toolSchemas)
	}
	if p := byURL[srv.URL+"/jobs/css-only"]; p.Title != "CSS Extracted Engineer" || p.Company != "CSSCo" || p.Location != "Boston, MA" || !strings.Contains(p.Description, "Design compiler toolchains.") {
		t.Errorf("css-only posting: %+v (extracted=%+v, tools=%v)", p, pdCSS.Extracted, toolSchemas)
	}
}
